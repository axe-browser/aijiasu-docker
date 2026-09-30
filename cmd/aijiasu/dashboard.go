package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// 启动内嵌轻量 Web 可视化控制台
func startDashboardServer(ctx context.Context, port int) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleDashboardHome)
	mux.HandleFunc("/api/status", handleAPIStatus)
	mux.HandleFunc("/api/nodes", handleAPINodes)
	mux.HandleFunc("/api/csv", handleAPICSV)
	mux.HandleFunc("/api/logs", handleAPILogs)
	mux.HandleFunc("/api/config/failover", handleAPIConfigFailover)
	mux.HandleFunc("/api/switch", handleAPISwitch)
	mux.HandleFunc("/api/disconnect", handleAPIDisconnect)
	mux.HandleFunc("/api/ping", handleAPIPing)

	server := &http.Server{
		Addr:         fmt.Sprintf("127.0.0.1:%d", port),
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	_ = server.ListenAndServe()
}

// 控制台主页 Handler
func handleDashboardHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	_, _ = w.Write([]byte(dashboardHTML))
}

// 状态接口 Handler
func handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	dispatcher.mu.Lock()
	activeNodeID := dispatcher.activeNodeID
	activeNodeName := dispatcher.activeNodeName
	activeCity := dispatcher.activeCity
	activePort := dispatcher.activePort
	activeExitIP := dispatcher.activeExitIP
	activeLatencyMs := dispatcher.activeLatencyMs
	backendPort := dispatcher.backendPort
	startTime := dispatcher.startTime
	lastSwitch := dispatcher.lastSwitchTime
	allowFailover := dispatcher.allowFailover
	dispatcher.mu.Unlock()

	activeClients := atomic.LoadInt64(&dispatcher.activeClients)
	conflicts := atomic.LoadInt64(&dispatcher.switchConflictCount)

	uptimeSeconds := int64(0)
	if !startTime.IsZero() {
		uptimeSeconds = int64(time.Since(startTime).Seconds())
	}

	totalNodes := 0
	if dispatcher.cfg != nil {
		totalNodes = dispatcher.cfg.TotalNodes
	}

	cred, _ := loadCredentialsFromFile()
	if cred == nil {
		cred, _ = migrateOldEnvIfPresent()
	}
	authConfigured := cred != nil && cred.Username != ""
	authUser := ""
	if authConfigured {
		authUser = maskUsername(cred.Username)
	}

	data := map[string]interface{}{
		"status":            "online",
		"auth_configured":   authConfigured,
		"auth_user":         authUser,
		"auth_algorithm":    "AES-256-GCM (硬件指纹绑定)",
		"total_nodes":       totalNodes,
		"active_node_id":    activeNodeID,
		"active_node_name":  activeNodeName,
		"active_city":       activeCity,
		"active_port":       activePort,
		"active_exit_ip":    activeExitIP,
		"active_latency_ms": activeLatencyMs,
		"active_clients":    activeClients,
		"switch_conflicts":  conflicts,
		"uptime_seconds":    uptimeSeconds,
		"backend_port":      backendPort,
		"web_port":          defaultWebPort,
		"last_switch_time":  lastSwitch.Format("2006-01-02 15:04:05"),
		"allow_failover":    allowFailover,
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(data)
}

// 主动触发即时延迟测速接口 Handler
func handleAPIPing(w http.ResponseWriter, r *http.Request) {
	latency := measureActiveNodeLatency()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success":    latency >= 0,
		"latency_ms": latency,
	})
}

// 动态修改故障转移开关接口 Handler
func handleAPIConfigFailover(w http.ResponseWriter, r *http.Request) {
	enableStr := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("enable")))
	enable := (enableStr == "true" || enableStr == "1" || enableStr == "on")
	cfg, err := setAllowFailover(enable)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success":        true,
		"allow_failover": cfg.AllowFailover,
	})
}

// 网页一键主动切换节点接口 Handler
func handleAPISwitch(w http.ResponseWriter, r *http.Request) {
	portStr := strings.TrimSpace(r.URL.Query().Get("port"))
	nodeID := strings.TrimSpace(r.URL.Query().Get("node_id"))

	dispatcher.mu.Lock()
	cfg := dispatcher.cfg
	dispatcher.mu.Unlock()

	if cfg == nil {
		cfg, _ = loadPortConfig()
	}

	var targetItem *PortItem
	if portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err == nil && cfg != nil {
			for i := range cfg.Mappings {
				if cfg.Mappings[i].Port == p {
					targetItem = &cfg.Mappings[i]
					break
				}
			}
		}
	} else if nodeID != "" && cfg != nil {
		for i := range cfg.Mappings {
			if cfg.Mappings[i].NodeID == nodeID {
				targetItem = &cfg.Mappings[i]
				break
			}
		}
	}

	if targetItem == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "未找到指定的端口或节点映射",
		})
		return
	}

	// 记录切换日志并输出
	fmt.Printf("\x1b[36m[%s]\x1b[0m 接收到 Web 控制台主动切换指令 ➜ 目标节点: %s (%s, 端口: %d, 城市: %s/%s)\n",
		time.Now().Format("15:04:05"), targetItem.NodeName, targetItem.NodeID, targetItem.Port, targetItem.Province, targetItem.City)

	startT := time.Now()
	node, err := switchNodeInternal(targetItem.NodeID)
	if err != nil {
		fmt.Printf("       \x1b[31m❌ Web 主动切换失败:\x1b[0m %v\n", err)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	elapsed := time.Since(startT)
	dispatcher.mu.Lock()
	dispatcher.activeNodeID = node.ID
	dispatcher.activeNodeName = node.Name
	dispatcher.activeCity = targetItem.City
	dispatcher.activePort = targetItem.Port
	dispatcher.lastSwitchTime = time.Now()
	dispatcher.mu.Unlock()

	fmt.Printf("       \x1b[32m✅ Web 主动切换成功！\x1b[0m 当前节点: %s (%s)，耗时: %v\n",
		node.Name, node.ID, elapsed.Round(time.Millisecond))

	// 异步后台探测出口公网 IP 与真实节点延迟
	go func(host string, p int) {
		ip := fetchExitIP(host, p, 3*time.Second)
		if ip != "" {
			dispatcher.mu.Lock()
			dispatcher.activeExitIP = ip
			dispatcher.mu.Unlock()
			fmt.Printf("       出口 IP: \x1b[32m%s\x1b[0m\n", ip)
		}
		lat := measureActiveNodeLatency()
		if lat > 0 {
			fmt.Printf("       通道延迟: \x1b[32m%d ms\x1b[0m\n", lat)
		}
	}(dispatcher.backendHost, dispatcher.backendPort)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success":   true,
		"port":      targetItem.Port,
		"node_id":   node.ID,
		"node_name": node.Name,
		"city":      targetItem.City,
		"elapsed":   elapsed.Round(time.Millisecond).String(),
	})
}

// 网页主动断开当前节点连接 Handler
func handleAPIDisconnect(w http.ResponseWriter, r *http.Request) {
	dispatcher.mu.Lock()
	oldNodeName := dispatcher.activeNodeName
	oldNodeID := dispatcher.activeNodeID
	oldPort := dispatcher.activePort
	dispatcher.activeNodeID = ""
	dispatcher.activeNodeName = ""
	dispatcher.activeCity = ""
	dispatcher.activePort = 0
	dispatcher.activeExitIP = ""
	dispatcher.activeLatencyMs = 0
	dispatcher.lastSwitchTime = time.Time{}
	dispatcher.mu.Unlock()

	// 底层断开爱加速连接
	_, _ = dockerExec("pkill", "-f", connectPattern)
	_, _ = dockerExec("ajiasu", "disconnect")

	msg := "已成功断开爱加速连接"
	if oldNodeName != "" {
		msg = fmt.Sprintf("已成功断开与 [%s (:%d)] 的直通连接", oldNodeName, oldPort)
	}

	fmt.Printf("\x1b[33m[%s]\x1b[0m 接收到 Web 控制台断开指令 ➜ %s，网关恢复待命状态\n",
		time.Now().Format("15:04:05"), msg)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success":       true,
		"message":       msg,
		"old_node_name": oldNodeName,
		"old_node_id":   oldNodeID,
		"old_port":      oldPort,
	})
}

// 节点映射列表接口 Handler
func handleAPINodes(w http.ResponseWriter, r *http.Request) {
	kw := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kw")))

	dispatcher.mu.Lock()
	activeNodeID := dispatcher.activeNodeID
	cfg := dispatcher.cfg
	dispatcher.mu.Unlock()

	if cfg == nil {
		cfg, _ = loadPortConfig()
	}

	type NodeViewItem struct {
		Port     int    `json:"port"`
		NodeID   string `json:"node_id"`
		NodeName string `json:"node_name"`
		Province string `json:"province"`
		City     string `json:"city"`
		IsActive bool   `json:"is_active"`
	}

	var results []NodeViewItem
	if cfg != nil {
		for _, m := range cfg.Mappings {
			if kw != "" {
				portStr := strconv.Itoa(m.Port)
				if !strings.Contains(portStr, kw) &&
					!strings.Contains(strings.ToLower(m.NodeName), kw) &&
					!strings.Contains(strings.ToLower(m.NodeID), kw) &&
					!strings.Contains(strings.ToLower(m.City), kw) &&
					!strings.Contains(strings.ToLower(m.Province), kw) {
					continue
				}
			}
			results = append(results, NodeViewItem{
				Port:     m.Port,
				NodeID:   m.NodeID,
				NodeName: m.NodeName,
				Province: m.Province,
				City:     m.City,
				IsActive: (activeNodeID != "" && m.NodeID == activeNodeID),
			})
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(results)
}

// CSV 导出下载接口 Handler
func handleAPICSV(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadPortConfig()
	if err != nil {
		http.Error(w, "无法读取端口映射配置", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"aijiasu_proxies.csv\"")

	// 写入 UTF-8 BOM，防止 Windows Excel 打开乱码
	_, _ = w.Write([]byte("\xEF\xBB\xBF"))
	_, _ = w.Write([]byte("端口,省份,城市,节点名称,节点ID,代理类型,代理主机,完整代理地址\r\n"))
	for _, item := range cfg.Mappings {
		line := fmt.Sprintf("%d,%s,%s,%s,%s,socks5,127.0.0.1,socks5://127.0.0.1:%d\r\n",
			item.Port, item.Province, item.City, item.NodeName, item.NodeID, item.Port)
		_, _ = w.Write([]byte(line))
	}
}

// 日志获取接口 Handler (获取尾部 50 行)
func handleAPILogs(w http.ResponseWriter, r *http.Request) {
	logPath := getDispatcherLogPath()
	data, err := os.ReadFile(logPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"lines": []string{"(暂无日志记录)"}})
		return
	}

	// 截取最后最多 64KB
	if len(data) > 65536 {
		data = data[len(data)-65536:]
	}

	lines := strings.Split(string(data), "\n")
	var nonEmpties []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			nonEmpties = append(nonEmpties, trimmed)
		}
	}

	maxLines := 120
	if len(nonEmpties) > maxLines {
		nonEmpties = nonEmpties[len(nonEmpties)-maxLines:]
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"lines": nonEmpties})
}

// 内嵌单页面 HTML/CSS/JS (纯原生无外部依赖，浅蓝科技风 · 单屏双栏工作台 · 节点与日志首屏同览)
const dashboardHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>爱加速 · 多端口智能路由分发控制台</title>
  <style>
    :root {
      --bg: #f0f7ff;
      --card-bg: #ffffff;
      --card-border: #dbeafe;
      --card-border-hover: #93c5fd;
      --text-main: #0f172a;
      --text-sub: #475569;
      --text-muted: #94a3b8;
      --primary: #2563eb;
      --primary-hover: #1d4ed8;
      --primary-light: #eff6ff;
      --accent: #0284c7;
      --success: #059669;
      --success-light: #ecfdf5;
      --warning: #d97706;
      --warning-light: #fffbeb;
      --danger: #dc2626;
      --shadow-sm: 0 1px 3px rgba(37, 99, 235, 0.05);
      --shadow-md: 0 3px 12px -2px rgba(37, 99, 235, 0.07);
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    html, body {
      height: 100vh;
      max-height: 100vh;
      overflow: hidden;
      background: radial-gradient(circle at 10% 20%, rgba(219, 234, 254, 0.5) 0%, rgba(240, 246, 255, 1) 90%);
      background-attachment: fixed;
      color: var(--text-main);
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
      font-size: 15px;
      line-height: 1.6;
      padding: 14px 20px;
      display: flex;
      flex-direction: column;
    }
    .container {
      width: 100%;
      max-width: 1800px;
      margin: 0 auto;
      height: 100%;
      display: flex;
      flex-direction: column;
      min-height: 0;
      flex: 1;
    }
    
    /* 顶部 Header */
    header {
      flex-shrink: 0;
      background: var(--card-bg);
      border: 1px solid var(--card-border);
      border-radius: 12px;
      padding: 12px 20px;
      margin-bottom: 12px;
      display: flex;
      justify-content: space-between;
      align-items: center;
      box-shadow: var(--shadow-sm);
      gap: 12px;
      flex-wrap: wrap;
    }
    .logo-area { display: flex; align-items: center; gap: 10px; }
    .logo-badge {
      background: linear-gradient(135deg, #2563eb 0%, #06b6d4 100%);
      color: #fff;
      font-size: 20px;
      font-weight: bold;
      width: 40px;
      height: 40px;
      border-radius: 9px;
      display: flex;
      align-items: center;
      justify-content: center;
      box-shadow: 0 2px 8px rgba(37, 99, 235, 0.25);
    }
    h1 { font-size: 19px; font-weight: 700; color: var(--text-main); letter-spacing: -0.3px; }
    .subtitle { font-size: 14px; color: var(--text-muted); margin-left: 6px; }

    /* 顶部中间：整合型容灾策略小开关 */
    .header-strategy {
      display: flex;
      align-items: center;
      gap: 10px;
      background: #f8faff;
      border: 1px solid #dbeafe;
      padding: 6px 14px;
      border-radius: 20px;
      font-size: 14px;
    }
    .strategy-badge {
      font-size: 13px;
      padding: 3px 10px;
      border-radius: 5px;
      font-weight: 600;
    }
    .strategy-badge.strict {
      background: var(--warning-light);
      color: var(--warning);
      border: 1px solid rgba(245, 158, 11, 0.3);
    }
    .strategy-badge.active {
      background: var(--success-light);
      color: var(--success);
      border: 1px solid rgba(16, 185, 129, 0.3);
    }
    .switch {
      position: relative;
      display: inline-block;
      width: 38px;
      height: 22px;
    }
    .switch input { opacity: 0; width: 0; height: 0; }
    .slider {
      position: absolute;
      cursor: pointer;
      top: 0; left: 0; right: 0; bottom: 0;
      background-color: #cbd5e1;
      transition: .3s;
      border-radius: 20px;
    }
    .slider:before {
      position: absolute;
      content: "";
      height: 16px;
      width: 16px;
      left: 3px;
      bottom: 3px;
      background-color: white;
      transition: .3s;
      border-radius: 50%;
      box-shadow: 0 1px 3px rgba(0,0,0,0.2);
    }
    input:checked + .slider { background-color: var(--primary); }
    input:checked + .slider:before { transform: translateX(16px); }

    .header-actions { display: flex; gap: 10px; align-items: center; }
    .btn {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      padding: 7px 16px;
      border-radius: 8px;
      font-size: 13.5px;
      font-weight: 600;
      text-decoration: none;
      cursor: pointer;
      border: none;
      transition: all 0.2s ease;
    }
    .btn-primary {
      background: linear-gradient(135deg, #2563eb 0%, #1d4ed8 100%);
      color: #fff;
      box-shadow: 0 2px 6px rgba(37, 99, 235, 0.2);
    }
    .btn-primary:hover {
      background: linear-gradient(135deg, #1d4ed8 0%, #1e40af 100%);
      transform: translateY(-1px);
    }
    .status-badge {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      padding: 6px 14px;
      border-radius: 9999px;
      font-size: 13px;
      font-weight: 600;
      background: var(--success-light);
      color: var(--success);
      border: 1px solid rgba(16, 185, 129, 0.25);
    }
    .status-dot {
      width: 7px;
      height: 7px;
      border-radius: 50%;
      background-color: var(--success);
      box-shadow: 0 0 6px var(--success);
      animation: pulse 2s infinite;
    }
    @keyframes pulse {
      0% { opacity: 1; transform: scale(1); }
      50% { opacity: 0.4; transform: scale(0.9); }
      100% { opacity: 1; transform: scale(1); }
    }

    .auth-badge {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      padding: 5px 12px;
      background: #eff6ff;
      border: 1px solid #bfdbfe;
      border-radius: 9999px;
      font-size: 13px;
      font-weight: 600;
      color: #1d4ed8;
      box-shadow: 0 1px 2px rgba(37,99,235,0.05);
    }

    /* 核心指标横条 */
    .stats-bar {
      flex-shrink: 0;
      display: grid;
      grid-template-columns: repeat(6, 1fr);
      gap: 12px;
      margin-bottom: 12px;
    }
    .stat-mini-card {
      background: var(--card-bg);
      border: 1px solid var(--card-border);
      border-radius: 10px;
      padding: 9px 15px;
      box-shadow: var(--shadow-sm);
      display: flex;
      flex-direction: column;
      justify-content: center;
    }
    .stat-mini-label { font-size: 13px; color: var(--text-sub); font-weight: 500; }
    .stat-mini-value { font-size: 17px; font-weight: 800; color: var(--text-main); margin-top: 3px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .stat-mini-card.highlight {
      border-color: #93c5fd;
      background: linear-gradient(180deg, #eff6ff 0%, #ffffff 100%);
    }
    .stat-mini-card.highlight-latency {
      border-color: #a7f3d0;
      background: linear-gradient(180deg, #f0fdf4 0%, #ffffff 100%);
      transition: all 0.2s ease;
    }
    .stat-mini-card.highlight-latency:hover {
      border-color: #34d399;
      box-shadow: 0 2px 8px rgba(16, 185, 129, 0.15);
      transform: translateY(-1px);
    }

    /* 首屏核心双栏布局 (左侧节点工作台 · 右侧加宽实时日志) */
    .main-workspace {
      flex: 1;
      min-height: 0;
      height: 100%;
      display: grid;
      grid-template-columns: 1fr 640px;
      gap: 14px;
      align-items: stretch;
    }

    /* 左侧节点卡片 */
    .workbench-card {
      background: var(--card-bg);
      border: 1px solid var(--card-border);
      border-radius: 12px;
      padding: 14px 18px;
      box-shadow: var(--shadow-sm);
      display: flex;
      flex-direction: column;
      height: 100%;
      min-height: 0;
      overflow: hidden;
    }

    /* 工具栏 (搜索 + 城市快速筛选 + 每页条数) */
    .workbench-toolbar {
      display: flex;
      justify-content: space-between;
      align-items: center;
      gap: 10px;
      margin-bottom: 10px;
      flex-wrap: wrap;
    }
    .search-input {
      padding: 7px 14px;
      background-color: #f8fafc;
      border: 1px solid var(--card-border);
      border-radius: 8px;
      color: var(--text-main);
      font-size: 14px;
      outline: none;
      width: 260px;
      transition: all 0.2s;
    }
    .search-input:focus {
      background: #fff;
      border-color: var(--primary);
      box-shadow: 0 0 0 2px rgba(37, 99, 235, 0.15);
    }
    
    .quick-cities {
      display: flex;
      gap: 6px;
      align-items: center;
      flex-wrap: wrap;
    }
    .city-pill {
      padding: 5px 12px;
      background: #f8fafc;
      border: 1px solid #e2e8f0;
      border-radius: 6px;
      font-size: 13px;
      color: var(--text-sub);
      cursor: pointer;
      transition: all 0.15s;
    }
    .city-pill:hover { border-color: #93c5fd; color: var(--primary); background: #eff6ff; }
    .city-pill.active { background: var(--primary); color: #fff; border-color: var(--primary); font-weight: 600; }

    .page-size-select {
      padding: 6px 10px;
      border-radius: 6px;
      border: 1px solid var(--card-border);
      background: #f8fafc;
      color: var(--text-main);
      font-size: 13px;
      outline: none;
      cursor: pointer;
    }

    /* 表格容器 (内部滚动，表头固定) */
    .table-container {
      flex: 1;
      min-height: 0;
      overflow-y: auto;
      border: 1px solid #e2e8f0;
      border-radius: 8px;
      background: #fff;
    }
    table { width: 100%; border-collapse: collapse; text-align: left; font-size: 14.5px; }
    thead th {
      position: sticky;
      top: 0;
      background-color: #f8fafc;
      color: var(--text-sub);
      padding: 10px 14px;
      font-weight: 700;
      font-size: 14px;
      border-bottom: 1px solid #e2e8f0;
      white-space: nowrap;
      z-index: 1;
    }
    td { padding: 8px 14px; border-bottom: 1px solid #f1f5f9; white-space: nowrap; font-size: 14px; }
    tr:hover td { background-color: #f8faff; }
    tr.active-row td {
      background-color: #f0fdf4 !important;
      border-bottom-color: #bbf7d0;
    }

    .port-pill {
      font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
      font-weight: 700;
      color: #1d4ed8;
      background: #eff6ff;
      border: 1px solid #bfdbfe;
      padding: 3px 9px;
      border-radius: 6px;
      display: inline-block;
      font-size: 14px;
    }
    .badge-active {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      color: var(--success);
      font-weight: 600;
      font-size: 13.5px;
    }
    .badge-idle { color: var(--text-muted); font-size: 13.5px; }
    /* 极致规整的一体化分段操作胶囊组 (Segmented Action Bar) */
    .action-group {
      display: inline-flex;
      align-items: center;
      background: #ffffff;
      border: 1px solid #cbd5e1;
      border-radius: 7px;
      overflow: hidden;
      box-shadow: 0 1px 2px rgba(0, 0, 0, 0.04);
      height: 28px;
      vertical-align: middle;
    }
    .action-btn-item {
      height: 100%;
      border: none;
      background: #ffffff;
      padding: 0 6px;
      font-size: 12px;
      font-weight: 600;
      color: #334155;
      cursor: pointer;
      display: inline-flex;
      align-items: center;
      justify-content: center;
      gap: 3px;
      white-space: nowrap;
      transition: all 0.15s ease;
      border-right: 1px solid #e2e8f0;
      box-sizing: border-box;
    }
    .action-btn-item:last-child {
      border-right: none;
    }
    .action-btn-item:hover:not(:disabled) {
      background: #eff6ff;
      color: var(--primary);
    }
    
    /* 切换按钮 (严格定宽 74px) */
    .action-btn-item.btn-switch-item {
      width: 74px;
      color: var(--primary);
      background: #f8faff;
    }
    .action-btn-item.btn-switch-item:hover:not(:disabled) {
      background: var(--primary);
      color: #fff;
    }
    .action-btn-item.btn-switch-item.is-active {
      width: 74px;
      background: #ecfdf5;
      color: var(--success);
      cursor: pointer;
      font-weight: 700;
    }
    .action-btn-item.btn-switch-item.is-active:hover {
      background: #fee2e2;
      color: #dc2626;
    }
    .action-btn-item.btn-switch-item:disabled {
      opacity: 0.6;
      cursor: wait;
    }

    /* 代理复制按钮 (严格定宽 66px，显式高对比 Hover 反馈) */
    .action-btn-item.btn-proxy-item {
      width: 66px;
    }
    .action-btn-item.btn-proxy-item:hover {
      background: #0284c7;
      color: #ffffff;
    }
    
    /* CMD测试命令复制按钮 (严格定宽 76px，终端科技黑底 Hover 反馈) */
    .action-btn-item.btn-cmd-item {
      width: 76px;
      color: #0369a1;
    }
    .action-btn-item.btn-cmd-item:hover {
      background: #0f172a;
      color: #38bdf8;
    }

    /* 紧凑分页导航条 */
    .pagination-bar {
      flex-shrink: 0;
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding-top: 10px;
      font-size: 14px;
      gap: 8px;
      flex-wrap: wrap;
    }
    .pagination-info { color: var(--text-sub); font-size: 13.5px; }
    .pagination-controls {
      display: flex;
      align-items: center;
      gap: 5px;
    }
    .page-btn {
      min-width: 32px;
      height: 32px;
      padding: 0 8px;
      display: inline-flex;
      align-items: center;
      justify-content: center;
      background: #ffffff;
      border: 1px solid #cbd5e1;
      border-radius: 6px;
      color: var(--text-main);
      font-size: 13px;
      cursor: pointer;
      transition: all 0.15s;
    }
    .page-btn:hover:not(:disabled):not(.active) {
      background: #eff6ff;
      border-color: #93c5fd;
      color: var(--primary);
    }
    .page-btn.active {
      background: var(--primary);
      color: #fff;
      border-color: var(--primary);
      font-weight: 700;
    }
    .page-btn:disabled { opacity: 0.35; cursor: not-allowed; }
    .page-ellipsis { padding: 0 4px; color: var(--text-muted); font-size: 13px; }
    .page-input {
      width: 48px;
      height: 32px;
      text-align: center;
      border: 1px solid #cbd5e1;
      border-radius: 6px;
      outline: none;
      font-size: 13px;
    }

    /* 右侧实时日志终端卡片 (与左侧高度完全等高，首屏并排直现) */
    .log-card {
      background: var(--card-bg);
      border: 1px solid var(--card-border);
      border-radius: 12px;
      padding: 14px 16px;
      box-shadow: var(--shadow-sm);
      display: flex;
      flex-direction: column;
      height: 100%;
      min-height: 0;
      overflow: hidden;
    }
    .log-card-header {
      flex-shrink: 0;
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: 10px;
    }
    .log-card-title {
      font-size: 16px;
      font-weight: 700;
      color: var(--text-main);
      display: flex;
      align-items: center;
      gap: 6px;
    }
    .log-tools {
      display: flex;
      align-items: center;
      gap: 8px;
      font-size: 13px;
      color: var(--text-muted);
    }
    .log-tool-btn {
      background: #f1f5f9;
      border: 1px solid #e2e8f0;
      padding: 4px 10px;
      border-radius: 5px;
      cursor: pointer;
      font-size: 13px;
      color: var(--text-sub);
    }
    .log-tool-btn:hover { background: #e2e8f0; }
    
    .log-terminal {
      flex: 1;
      min-height: 0;
      background-color: #0b1120;
      border: 1px solid #1e293b;
      border-radius: 8px;
      padding: 12px 14px;
      overflow-y: auto;
      font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
      font-size: 13.5px;
      line-height: 1.6;
      color: #94a3b8;
    }
    .log-line { margin-bottom: 4px; word-break: break-all; }
    .log-line.highlight { color: #38bdf8; font-weight: 600; }
    .log-line.success { color: #34d399; }
    .log-line.warn { color: #fbbf24; }

    /* Toast 浮层提示 */
    .toast {
      position: fixed;
      bottom: 24px;
      right: 24px;
      background: #0f172a;
      color: #fff;
      padding: 10px 20px;
      border-radius: 8px;
      box-shadow: 0 4px 16px rgba(0, 0, 0, 0.25);
      font-size: 14px;
      font-weight: 600;
      opacity: 0;
      transform: translateY(10px);
      transition: all 0.25s ease;
      pointer-events: none;
      z-index: 9999;
      border-left: 3px solid var(--primary);
    }
    .toast.show { opacity: 1; transform: translateY(0); }

    /* 响应式回退：中屏平滑过渡，小屏或低高度视口自动堆叠并允许纵向滚动 */
    @media (max-width: 1400px) {
      .main-workspace { grid-template-columns: 1fr 520px; }
    }
    @media (max-width: 1080px), (max-height: 640px) {
      html, body {
        height: auto;
        max-height: none;
        overflow-y: auto;
      }
      .container { height: auto; }
      .stats-bar { grid-template-columns: repeat(3, 1fr); }
      .main-workspace { height: auto; grid-template-columns: 1fr; }
      .workbench-card, .log-card { height: 500px; }
    }
  </style>
</head>
<body>
  <div class="container">
    <!-- 紧凑 Header -->
    <header>
      <div class="logo-area">
        <div class="logo-badge">⚡</div>
        <div>
          <h1>爱加速多端口调度网关 <span class="subtitle">全量 2810 节点 1:1 专属端口 · 浅蓝科技工作台</span></h1>
        </div>
      </div>

      <!-- 容灾策略小开关 -->
      <div class="header-strategy">
        <span style="font-size: 13px;">🛡️ 容错策略:</span>
        <label class="switch">
          <input type="checkbox" id="failover-toggle" onchange="toggleFailover(this.checked)">
          <span class="slider"></span>
        </label>
        <span class="strategy-badge strict" id="strategy-badge">严格锁定模式 (默认)</span>
      </div>

      <div class="header-actions">
        <div class="auth-badge" id="auth-badge" title="凭据已通过 AES-256-GCM 硬件指纹加密保护">
          <span>🔒</span> <span id="auth-user-text">AES-256 保护</span>
        </div>
        <div class="status-badge">
          <span class="status-dot"></span>
          <span>网关运行中</span>
        </div>
        <a href="/api/csv" class="btn btn-primary" download="aijiasu_proxies.csv">
          <span>📥 导出全量 CSV</span>
        </a>
      </div>
    </header>

    <!-- 紧凑单行指标条 -->
    <div class="stats-bar">
      <div class="stat-mini-card highlight">
        <div class="stat-mini-label">当前活跃节点</div>
        <div class="stat-mini-value" id="val-active-node" style="color: var(--primary);">--</div>
      </div>
      <div class="stat-mini-card">
        <div class="stat-mini-label">当前出口公网 IP</div>
        <div class="stat-mini-value" id="val-exit-ip" style="font-family: monospace;">--</div>
      </div>
      <div class="stat-mini-card">
        <div class="stat-mini-label">专属端口总覆盖</div>
        <div class="stat-mini-value" id="val-total-ports">--</div>
      </div>
      <div class="stat-mini-card">
        <div class="stat-mini-label">活跃长连接数</div>
        <div class="stat-mini-value" id="val-active-clients" style="color: var(--success);">0</div>
      </div>
      <div class="stat-mini-card highlight-latency" onclick="recheckLatency()" style="cursor: pointer;" title="点击可立即刷新当前节点往返延迟">
        <div class="stat-mini-label" style="display: flex; justify-content: space-between; align-items: center;">
          <span>当前节点延迟</span>
          <span style="font-size: 11px; color: #059669; font-weight: 600;">↻ 测速</span>
        </div>
        <div class="stat-mini-value" id="val-latency">--</div>
      </div>
      <div class="stat-mini-card">
        <div class="stat-mini-label">服务运行时长</div>
        <div class="stat-mini-value" id="val-uptime">--</div>
      </div>
    </div>

    <!-- 首屏核心双栏工作台 (左: 节点映射表格 | 右: 实时分发日志) -->
    <div class="main-workspace">
      <!-- 左栏：节点映射工作台 -->
      <div class="workbench-card">
        <div class="workbench-toolbar">
          <div style="display: flex; gap: 6px; align-items: center;">
            <input type="text" id="search-input" class="search-input" placeholder="🔍 搜索端口 (如 1080)、城市、省份..." oninput="handleSearch(this.value)">
            <select id="page-size-select" class="page-size-select" onchange="handlePageSizeChange(this.value)">
              <option value="15">15 条/页</option>
              <option value="20" selected>20 条/页 (默认)</option>
              <option value="30">30 条/页</option>
              <option value="50">50 条/页</option>
              <option value="100">100 条/页</option>
            </select>
          </div>

          <!-- 精选热门城市胶囊 -->
          <div class="quick-cities" id="tag-container">
            <span class="city-pill active" onclick="setCityFilter('')">全部</span>
            <span class="city-pill" onclick="setCityFilter('北京')">北京</span>
            <span class="city-pill" onclick="setCityFilter('上海')">上海</span>
            <span class="city-pill" onclick="setCityFilter('广州')">广州</span>
            <span class="city-pill" onclick="setCityFilter('深圳')">深圳</span>
            <span class="city-pill" onclick="setCityFilter('杭州')">杭州</span>
            <span class="city-pill" onclick="setCityFilter('成都')">成都</span>
          </div>
        </div>

        <!-- 节点表格 (内部滚动) -->
        <div class="table-container">
          <table>
            <thead>
              <tr>
                <th>端口</th>
                <th>节点名称</th>
                <th>省份</th>
                <th>城市</th>
                <th>节点 ID</th>
                <th>状态</th>
                <th>快捷操作 & 调试</th>
              </tr>
            </thead>
            <tbody id="node-tbody">
              <tr><td colspan="7" style="text-align: center; color: var(--text-muted); padding: 40px;">正在加载节点数据...</td></tr>
            </tbody>
          </table>
        </div>

        <!-- 紧凑分页导航条 -->
        <div class="pagination-bar" id="pagination-bar" style="display: none;">
          <div class="pagination-info" id="pagination-info">显示 1 - 10 条，共 2,810 节点</div>
          <div class="pagination-controls">
            <button class="page-btn" id="btn-first" onclick="goToPage(1)" title="首页">«</button>
            <button class="page-btn" id="btn-prev" onclick="goToPage(currentPage - 1)" title="上一页">‹</button>
            <div id="page-numbers" style="display: flex; gap: 3px; align-items: center;"></div>
            <button class="page-btn" id="btn-next" onclick="goToPage(currentPage + 1)" title="下一页">›</button>
            <button class="page-btn" id="btn-last" onclick="goToPage(totalPages)" title="末页">»</button>
            <div style="display: flex; align-items: center; gap: 4px; margin-left: 4px;">
              <input type="number" id="jump-page-input" class="page-input" min="1" onkeydown="if(event.key==='Enter') jumpToPage()">
              <button class="page-btn" onclick="jumpToPage()" style="padding: 0 6px;">跳</button>
            </div>
          </div>
        </div>
      </div>

      <!-- 右栏：实时分发与调度日志视窗 (并排展示，首屏直达) -->
      <div class="log-card">
        <div class="log-card-header">
          <div class="log-card-title">
            <span>📋</span>
            <span>实时分发与热切换日志</span>
          </div>
          <div class="log-tools">
            <span id="log-status">● 自动更新中</span>
            <button class="log-tool-btn" onclick="clearLogView()">清屏</button>
            <button class="log-tool-btn" onclick="updateLogs()">刷新</button>
          </div>
        </div>
        <div class="log-terminal" id="log-box">
          <div class="log-line">正在载入实时调度日志流...</div>
        </div>
      </div>
    </div>
  </div>

  <div id="toast" class="toast">已成功复制代理地址</div>

  <script>
    let allNodes = [];
    let currentFilter = '';
    let currentSearch = '';
    let currentPage = 1;
    let pageSize = 20; // 默认 20 条，充分利用 100vh 垂直空间并提供单屏多行视野
    let totalPages = 1;
    let filteredNodes = [];
    let currentActiveLatency = 0;

    function showToast(text) {
      const t = document.getElementById('toast');
      t.innerText = text;
      t.classList.add('show');
      setTimeout(() => t.classList.remove('show'), 2000);
    }

    function copyRawText(val) {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        return navigator.clipboard.writeText(val);
      }
      const input = document.createElement('input');
      input.value = val;
      document.body.appendChild(input);
      input.select();
      document.execCommand('copy');
      document.body.removeChild(input);
      return Promise.resolve();
    }

    function copyProxyAddr(val) {
      copyRawText(val).then(() => {
        showToast('📋 已复制代理地址: ' + val);
      });
    }

    function copyCurlCmd(port) {
      const cmd = 'curl -x socks5h://127.0.0.1:' + port + ' https://myip.ipip.net';
      copyRawText(cmd).then(() => {
        showToast('💻 已复制 CMD 测试命令: ' + cmd);
      });
    }

    async function switchTargetNode(port, nodeName, btn) {
      if (!btn) return;
      const originalText = btn.innerHTML;
      btn.disabled = true;
      btn.innerHTML = '⏳ 切换中';
      try {
        const res = await fetch('/api/switch?port=' + port);
        const data = await res.json();
        if (data.success) {
          showToast('🎉 成功切换至 ' + data.node_name + ' (:' + data.port + ') 耗时: ' + data.elapsed);
          await updateStatus();
          await updateLogs();
        } else {
          showToast('❌ 切换失败: ' + (data.error || '连接超时'));
          btn.disabled = false;
          btn.innerHTML = originalText;
        }
      } catch (e) {
        showToast('❌ 切换请求异常: ' + e);
        btn.disabled = false;
        btn.innerHTML = originalText;
      }
    }

    async function disconnectActiveNode(btn) {
      if (!btn) return;
      const originalText = btn.innerHTML;
      btn.disabled = true;
      btn.innerHTML = '⏳ 断开中';
      try {
        const res = await fetch('/api/disconnect');
        const data = await res.json();
        if (data.success) {
          showToast('🔌 ' + data.message + '，网关已恢复待命状态');
          currentActiveLatency = 0;
          await updateStatus();
          await updateLogs();
        } else {
          showToast('❌ 断开失败: ' + (data.error || '未知错误'));
          btn.disabled = false;
          btn.innerHTML = originalText;
        }
      } catch (e) {
        showToast('❌ 断开请求异常: ' + e);
        btn.disabled = false;
        btn.innerHTML = originalText;
      }
    }

    function formatUptime(seconds) {
      if (!seconds) return '刚刚启动';
      const d = Math.floor(seconds / 86400);
      const h = Math.floor((seconds % 86400) / 3600);
      const m = Math.floor((seconds % 3600) / 60);
      const s = seconds % 60;
      if (d > 0) return d + '天 ' + h + '时';
      if (h > 0) return h + '时 ' + m + '分';
      if (m > 0) return m + '分 ' + s + '秒';
      return s + '秒';
    }

    function setCityFilter(city) {
      currentFilter = city;
      currentPage = 1;
      document.querySelectorAll('.city-pill').forEach(btn => {
        if ((!city && btn.innerText === '全部') || btn.innerText === city) {
          btn.classList.add('active');
        } else {
          btn.classList.remove('active');
        }
      });
      renderTable();
    }

    function handleSearch(val) {
      currentSearch = val.trim().toLowerCase();
      currentPage = 1;
      renderTable();
    }

    function handlePageSizeChange(val) {
      pageSize = parseInt(val, 10) || 20;
      currentPage = 1;
      renderTable();
    }

    function goToPage(p) {
      if (p < 1 || p > totalPages || p === currentPage) return;
      currentPage = p;
      renderTable();
    }

    function jumpToPage() {
      const input = document.getElementById('jump-page-input');
      const val = parseInt(input.value, 10);
      if (val >= 1 && val <= totalPages) {
        goToPage(val);
      } else {
        showToast('请输入 1 到 ' + totalPages + ' 之间的页码');
      }
    }

    function clearLogView() {
      document.getElementById('log-box').innerHTML = '<div class="log-line" style="color: #64748b;">(日志视窗已清空，等待新请求分发...)</div>';
    }

    function renderTable() {
      const tbody = document.getElementById('node-tbody');
      filteredNodes = allNodes.filter(n => {
        if (currentFilter && n.city !== currentFilter) return false;
        if (!currentSearch) return true;
        return (
          n.port.toString().includes(currentSearch) ||
          n.node_name.toLowerCase().includes(currentSearch) ||
          n.node_id.toLowerCase().includes(currentSearch) ||
          n.city.toLowerCase().includes(currentSearch) ||
          n.province.toLowerCase().includes(currentSearch)
        );
      });

      const totalItems = filteredNodes.length;
      totalPages = Math.ceil(totalItems / pageSize) || 1;

      if (currentPage > totalPages) currentPage = totalPages;
      if (currentPage < 1) currentPage = 1;

      const paginationBar = document.getElementById('pagination-bar');
      if (totalItems === 0) {
        tbody.innerHTML = '<tr><td colspan="7" style="text-align: center; color: var(--text-muted); padding: 40px;">未检索到匹配的节点端口</td></tr>';
        paginationBar.style.display = 'none';
        return;
      }

      paginationBar.style.display = 'flex';

      const startIndex = (currentPage - 1) * pageSize;
      const endIndex = Math.min(startIndex + pageSize, totalItems);
      const pageList = filteredNodes.slice(startIndex, endIndex);

      let html = '';
      pageList.forEach(n => {
        const proxyAddr = '127.0.0.1:' + n.port;
        const rowClass = n.is_active ? 'active-row' : '';
        let activeLabel = '🟢 活跃直通';
        if (n.is_active && currentActiveLatency > 0) {
          activeLabel += ' (' + currentActiveLatency + 'ms)';
        }
        const statusHtml = n.is_active 
          ? '<span class="badge-active"><span class="status-dot" style="animation:none;"></span> ' + activeLabel + '</span>'
          : '<span class="badge-idle">⚪ 待命</span>';

        const safeNodeName = n.node_name.replace(/'/g, "\\'");
        const switchBtn = n.is_active
          ? '<button class="action-btn-item btn-switch-item is-active" onclick="disconnectActiveNode(this)" onmouseenter="this.innerText=\'🔴 断开\'" onmouseleave="this.innerText=\'🟢 直通\'" title="当前处于直通中，点击即可主动断开连接">🟢 直通</button>'
          : '<button class="action-btn-item btn-switch-item" onclick="switchTargetNode(' + n.port + ', \'' + safeNodeName + '\', this)" title="一键将调度网关切至此专属节点">⚡ 切换</button>';

        html += '<tr class="' + rowClass + '">' +
          '<td><span class="port-pill">' + n.port + '</span></td>' +
          '<td><strong>' + n.node_name + '</strong></td>' +
          '<td>' + n.province + '</td>' +
          '<td>' + n.city + '</td>' +
          '<td><code style="color: var(--text-sub);">' + n.node_id + '</code></td>' +
          '<td>' + statusHtml + '</td>' +
          '<td>' +
            '<div class="action-group">' +
              switchBtn +
              '<button class="action-btn-item btn-proxy-item" onclick="copyProxyAddr(\'' + proxyAddr + '\')" title="复制 SOCKS5 代理地址: 127.0.0.1:' + n.port + '">📋 代理</button>' +
              '<button class="action-btn-item btn-cmd-item" onclick="copyCurlCmd(' + n.port + ')" title="复制终端 curl 测试命令">💻 curl</button>' +
            '</div>' +
          '</td>' +
        '</tr>';
      });
      tbody.innerHTML = html;

      document.getElementById('pagination-info').innerText = 
        '显示 ' + (startIndex + 1) + ' - ' + endIndex + ' / 共 ' + totalItems + ' 节点 (' + currentPage + '/' + totalPages + '页)';

      document.getElementById('btn-first').disabled = (currentPage === 1);
      document.getElementById('btn-prev').disabled = (currentPage === 1);
      document.getElementById('btn-next').disabled = (currentPage === totalPages);
      document.getElementById('btn-last').disabled = (currentPage === totalPages);
      document.getElementById('jump-page-input').max = totalPages;

      renderPageNumbers();
    }

    function renderPageNumbers() {
      const container = document.getElementById('page-numbers');
      container.innerHTML = '';
      if (totalPages <= 1) return;

      const delta = 1;
      const range = [];
      for (let i = Math.max(2, currentPage - delta); i <= Math.min(totalPages - 1, currentPage + delta); i++) {
        range.push(i);
      }

      appendPageBtn(container, 1, currentPage === 1);
      if (currentPage - delta > 2) appendEllipsis(container);
      range.forEach(p => appendPageBtn(container, p, currentPage === p));
      if (currentPage + delta < totalPages - 1) appendEllipsis(container);
      if (totalPages > 1) appendPageBtn(container, totalPages, currentPage === totalPages);
    }

    function appendPageBtn(container, p, isActive) {
      const btn = document.createElement('button');
      btn.className = 'page-btn' + (isActive ? ' active' : '');
      btn.innerText = p;
      btn.onclick = () => goToPage(p);
      container.appendChild(btn);
    }

    function appendEllipsis(container) {
      const span = document.createElement('span');
      span.className = 'page-ellipsis';
      span.innerText = '…';
      container.appendChild(span);
    }

    async function toggleFailover(checked) {
      try {
        const res = await fetch('/api/config/failover?enable=' + checked);
        const data = await res.json();
        if (data.allow_failover) {
          showToast('🟢 已开启同城智能故障转移');
        } else {
          showToast('⚪ 已切换为严格锁定模式 (拒绝漂移)');
        }
        applyFailoverUI(data.allow_failover);
      } catch (e) {
        showToast('❌ 切换失败: ' + e);
      }
    }

    function applyFailoverUI(enabled) {
      const toggle = document.getElementById('failover-toggle');
      const badge = document.getElementById('strategy-badge');
      if (toggle && toggle.checked !== !!enabled) toggle.checked = !!enabled;
      if (enabled) {
        if (badge) {
          badge.className = 'strategy-badge active';
          badge.innerText = '同城容错已开启';
        }
      } else {
        if (badge) {
          badge.className = 'strategy-badge strict';
          badge.innerText = '严格锁定模式 (默认)';
        }
      }
    }

    async function updateStatus() {
      try {
        const res = await fetch('/api/status');
        const data = await res.json();
        
        currentActiveLatency = data.active_latency_ms || 0;

        if (data.active_node_name) {
          let nodeText = data.active_node_name + ' (:' + data.active_port + ')';
          if (currentActiveLatency > 0) {
            nodeText += ' · ' + currentActiveLatency + 'ms';
          }
          document.getElementById('val-active-node').innerText = nodeText;
        } else {
          document.getElementById('val-active-node').innerText = '待命中';
        }

        document.getElementById('val-exit-ip').innerText = data.active_exit_ip ? data.active_exit_ip.split(' ')[0] : '探测中...';
        document.getElementById('val-total-ports').innerText = data.total_nodes + ' 个';
        document.getElementById('val-active-clients').innerText = data.active_clients;

        // 渲染当前节点延迟指标卡片
        const latEl = document.getElementById('val-latency');
        if (latEl) {
          if (data.active_node_name) {
            if (currentActiveLatency > 0) {
              let color = '#10b981'; // 极佳
              let icon = '🟢';
              if (currentActiveLatency > 120) {
                color = '#f59e0b'; // 良好
                icon = '🟡';
              }
              if (currentActiveLatency > 200) {
                color = '#ef4444'; // 偏高
                icon = '🔴';
              }
              latEl.innerHTML = '<span style="color:' + color + '; font-weight:800;">' + icon + ' ' + currentActiveLatency + ' ms</span>';
            } else {
              latEl.innerHTML = '<span style="color:var(--text-muted); font-size:14px;">⏳ 测速中...</span>';
            }
          } else {
            latEl.innerHTML = '<span style="color:var(--text-muted); font-size:15px;">⚪ 待命中</span>';
          }
        }

        document.getElementById('val-uptime').innerText = formatUptime(data.uptime_seconds);

        if (data.auth_user) {
          const authEl = document.getElementById('auth-user-text');
          if (authEl) authEl.innerText = data.auth_user + ' (AES-256)';
        }

        applyFailoverUI(data.allow_failover);

        if (allNodes.length > 0) {
          let stateChanged = false;
          allNodes.forEach(n => {
            const nextActive = (data.active_node_id && n.node_id === data.active_node_id);
            if (n.is_active !== nextActive) {
              n.is_active = nextActive;
              stateChanged = true;
            }
          });
          if (stateChanged) {
            renderTable();
          }
        }
      } catch (e) {
        console.error('更新状态失败', e);
      }
    }

    async function recheckLatency() {
      const latEl = document.getElementById('val-latency');
      if (latEl) {
        latEl.innerHTML = '<span style="color:var(--primary); font-size:13px;">⏳ 测速中...</span>';
      }
      try {
        const res = await fetch('/api/ping');
        const data = await res.json();
        if (data.latency_ms > 0) {
          currentActiveLatency = data.latency_ms;
          updateStatus();
          renderTable();
          showToast('⚡ 当前节点往返延迟: ' + data.latency_ms + ' ms');
        } else {
          if (latEl) latEl.innerHTML = '<span style="color:#ef4444; font-size:13px;">❌ 测速超时</span>';
          showToast('节点测速超时或未连接');
        }
      } catch (e) {
        if (latEl) latEl.innerHTML = '<span style="color:#ef4444; font-size:13px;">❌ 测速异常</span>';
      }
    }

    async function loadNodes() {
      try {
        const res = await fetch('/api/nodes');
        allNodes = await res.json();
        renderTable();
      } catch (e) {
        console.error('加载节点失败', e);
      }
    }

    async function updateLogs() {
      try {
        const res = await fetch('/api/logs');
        const data = await res.json();
        const logBox = document.getElementById('log-box');
        if (data.lines && data.lines.length > 0) {
          const linesHtml = data.lines.map(l => {
            let clean = l.replace(/\x1b\[[0-9;]*[a-zA-Z]/g, '').replace(/\[[0-9;]*m/g, '');
            let cls = 'log-line';
            if (clean.includes('热切换成功') || clean.includes('[aijiasu OK]')) cls += ' success';
            else if (clean.includes('专属节点') || clean.includes('接收到端口')) cls += ' highlight';
            else if (clean.includes('警告') || clean.includes('长连接感知') || clean.includes('故障转移')) cls += ' warn';
            return '<div class="' + cls + '">' + clean.replace(/</g, '&lt;').replace(/>/g, '&gt;') + '</div>';
          }).join('');
          logBox.innerHTML = linesHtml;
          logBox.scrollTop = logBox.scrollHeight;
        }
      } catch (e) {
        console.error('更新日志失败', e);
      }
    }

    loadNodes();
    updateStatus();
    updateLogs();

    setInterval(updateStatus, 3000);
    setInterval(updateLogs, 4000);
  </script>
</body>
</html>
`

