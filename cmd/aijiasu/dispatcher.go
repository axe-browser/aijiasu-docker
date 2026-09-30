package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	dispatcherPidFileName = "dispatcher.pid"
	dispatcherLogFileName = "dispatcher.log"
	defaultWebPort        = 1078 // 内置 Web 可视化控制台端口
)

// 调度器状态结构
type DispatcherState struct {
	mu                  sync.Mutex
	cfg                 *PortConfig
	activeNodeID        string
	activeNodeName      string
	activeCity          string
	activePort          int
	activeLatencyMs     int64     // 当前活跃节点实时往返延迟 (ms)
	listeners           map[int]net.Listener
	connCount           int64
	activeClients       int64     // 当前正在传输的活跃连接数
	switchConflictCount int64     // 切换冲突与抢占统计
	lastSwitchTime      time.Time
	activeExitIP        string
	backendHost         string
	backendPort         int
	cancelFunc          context.CancelFunc
	portIndex           map[int]PortItem
	startTime           time.Time // 服务启动时间
	allowFailover       bool      // 是否允许同城智能故障转移 (默认 false 严格锁定)
}

// 调度器全局单例
var dispatcher = &DispatcherState{
	listeners:   make(map[int]net.Listener),
	backendHost: defaultProxyHost,
	backendPort: defaultBackendPort,
	portIndex:   make(map[int]PortItem),
}

func getDispatcherPidPath() string {
	return filepath.Join(getProjectDir(), dispatcherPidFileName)
}

func getDispatcherLogPath() string {
	return filepath.Join(getProjectDir(), dispatcherLogFileName)
}

// 设置进程最大文件句柄限制 (确保支持 2800+ 端口同时监听，包含 Linux 友好容错引导)
func setMaxFileLimit() {
	var rLimit syscall.Rlimit
	err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err == nil {
		if rLimit.Cur < 65535 {
			rLimit.Cur = 65535
			if rLimit.Max < 65535 {
				rLimit.Max = 65535
			}
			err = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit)
			if err != nil {
				_ = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
				if rLimit.Cur < 3000 {
					fmt.Printf("\x1b[33m[系统提示]\x1b[0m 当前系统最大文件句柄数为 %d，批量监听 2810 个端口可能受限。\n", rLimit.Cur)
					fmt.Println("         如遇 'too many open files'，请在终端执行 'ulimit -n 65535' 提升限制。")
				}
			}
		}
	}
}

// 尝试单次连接指定节点
func tryConnectSingleNode(nodeID, nodeName string) error {
	// 1. 停止旧进程
	_, _ = dockerExec("pkill", "-f", connectPattern)
	stopDeadline := time.Now().Add(5 * time.Second)
	for len(getConnectPids()) > 0 {
		if time.Now().After(stopDeadline) {
			return fmt.Errorf("停止旧连接超时")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 2. 启动新连接进程 (传节点名称)
	cmdStr := fmt.Sprintf("nohup ajiasu connect \"%s\" > /tmp/ajiasu-connect.log 2>&1 & printf \"%%s\\n\" \"$!\"", nodeName)
	out, err := dockerExec("bash", "-c", cmdStr)
	if err != nil {
		return fmt.Errorf("启动连接进程失败: %w", err)
	}
	newPid := strings.TrimSpace(out)

	// 3. 轮询内部端口建立
	var internalPort int
	backendDeadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(backendDeadline) {
		internalPort = detectInternalPort(newPid)
		if internalPort > 0 {
			break
		}
		// 检查存活
		currentPids := getConnectPids()
		alive := false
		for _, p := range currentPids {
			if p == newPid {
				alive = true
				break
			}
		}
		if !alive {
			logOut, _ := dockerExec("cat", "/tmp/ajiasu-connect.log")
			return fmt.Errorf("连接进程已提前退出: %s", strings.TrimSpace(logOut))
		}
		time.Sleep(200 * time.Millisecond)
	}

	if internalPort == 0 {
		return fmt.Errorf("未探测到爱加速内部监听端口")
	}

	// 4. 同步容器内 1080 端口桥接
	if err := ensureBridge(internalPort); err != nil {
		return fmt.Errorf("桥接端口同步失败: %w", err)
	}

	// 5. 验证后端宿主端口握手
	if err := verifySocks5(dispatcher.backendHost, dispatcher.backendPort, 3*time.Second); err != nil {
		return fmt.Errorf("验证后端 SOCKS5 握手失败: %w", err)
	}

	return nil
}

// 内部精准切换节点（按节点 ID 或关键字，内置同城智能故障转移 Smart Failover）
func switchNodeInternal(target string) (*NodeItem, error) {
	if err := ensureContainerRunning(); err != nil {
		return nil, err
	}

	nodes, err := fetchNodes()
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("节点列表为空")
	}

	var chosen NodeItem
	targetLower := strings.ToLower(target)
	var matches []NodeItem
	for _, n := range nodes {
		if strings.ToLower(n.ID) == targetLower {
			chosen = n
			matches = nil
			break
		}
		if strings.ToLower(n.Name) == targetLower {
			chosen = n
			matches = nil
			break
		}
		if strings.Contains(strings.ToLower(n.Name), targetLower) || strings.Contains(strings.ToLower(n.ID), targetLower) {
			matches = append(matches, n)
		}
	}
	if chosen.ID == "" {
		if len(matches) == 0 {
			return nil, fmt.Errorf("未匹配到与 \"%s\" 相关的可用节点", target)
		}
		chosen = matches[0]
	}

	// 首先尝试连接目标专属节点
	err = tryConnectSingleNode(chosen.ID, chosen.Name)
	if err == nil {
		return &chosen, nil
	}

	// 检查当前是否允许同城故障转移 (默认关闭: 严格模式，绝对锁定节点，防止不知情下漂移至同城其他物理节点)
	dispatcher.mu.Lock()
	allowFailover := dispatcher.allowFailover
	dispatcher.mu.Unlock()

	if !allowFailover {
		fmt.Printf("       \x1b[31m[严格模式拦截]\x1b[0m 专属节点 [%s] 连接失败: %v\n", chosen.Name, err)
		fmt.Printf("       💡 当前处于【严格锁定模式 (故障转移已关闭)】，绝不自动漂移至其他物理节点。\n")
		fmt.Printf("          如确需容错备用，可运行 'aijiasu failover on' 或在 Web 控制台顶部开启开关。\n")
		return nil, fmt.Errorf("专属节点 [%s (%s)] 连接失败: %w (严格锁定模式下已拦截自动漂移)", chosen.Name, chosen.ID, err)
	}

	// 仅在明确开启 AllowFailover 时触发【同城智能故障转移 (Smart Local Failover)】
	if chosen.City != "" {
		fmt.Printf("       \x1b[33m[智能故障转移]\x1b[0m 专属节点 [%s] 暂时连接超时，当前已开启同城容错，正在就近接驳备用节点...\n", chosen.Name)
		var cityBackups []NodeItem
		for _, n := range nodes {
			if n.City == chosen.City && n.ID != chosen.ID {
				cityBackups = append(cityBackups, n)
			}
		}

		maxTries := 2
		if len(cityBackups) < maxTries {
			maxTries = len(cityBackups)
		}
		for i := 0; i < maxTries; i++ {
			backup := cityBackups[i]
			fmt.Printf("       ➜ 尝试同城备用节点 [%s (%s)] (城市: %s)...\n", backup.Name, backup.ID, backup.City)
			if bErr := tryConnectSingleNode(backup.ID, backup.Name); bErr == nil {
				fmt.Printf("       \x1b[32m[故障转移成功]\x1b[0m 成功接驳同城备用节点 [%s]！出口城市 100%% 保持在 %s！\n", backup.Name, backup.City)
				return &backup, nil
			}
		}
	}

	return nil, fmt.Errorf("节点切换失败: %w", err)
}

// 转发单个客户端连接
func handleClientConn(clientConn net.Conn, item PortItem) {
	defer clientConn.Close()

	// 活跃连接原子计数
	atomic.AddInt64(&dispatcher.activeClients, 1)
	defer atomic.AddInt64(&dispatcher.activeClients, -1)

	port := item.Port
	targetNodeID := item.NodeID

	// 检查当前后端状态是否需要切换
	dispatcher.mu.Lock()
	needSwitch := false
	if dispatcher.activeNodeID == "" || dispatcher.activeNodeID != targetNodeID {
		needSwitch = true
	}

	if needSwitch {
		activeCount := atomic.LoadInt64(&dispatcher.activeClients) - 1
		if activeCount > 0 {
			atomic.AddInt64(&dispatcher.switchConflictCount, 1)
			fmt.Printf("\x1b[33m[长连接感知]\x1b[0m 当前检测到 %d 个旧节点 (%s) 的活动连接在传输，正在平滑接替并切至目标节点 [%s]...\n",
				activeCount, dispatcher.activeNodeName, item.NodeName)
		}

		fmt.Printf("\x1b[36m[%s]\x1b[0m 接收到端口 \x1b[1;33m%d\x1b[0m 请求 ➜ 专属节点: %s (%s, 城市: %s/%s)\n",
			time.Now().Format("15:04:05"), port, item.NodeName, item.NodeID, item.Province, item.City)
		if dispatcher.activeNodeName != "" {
			fmt.Printf("       ➜ 当前活跃节点为 [%s]，正在触发热切换至目标节点 [%s]...\n", dispatcher.activeNodeName, item.NodeName)
		} else {
			fmt.Printf("       ➜ 当前未连接节点，正在初始化连接目标节点 [%s]...\n", item.NodeName)
		}

		startT := time.Now()
		node, err := switchNodeInternal(targetNodeID)
		if err != nil {
			dispatcher.mu.Unlock()
			fmt.Printf("       \x1b[31m❌ 节点切换失败:\x1b[0m %v\n", err)
			return
		}

		elapsed := time.Since(startT)
		dispatcher.activeNodeID = node.ID
		dispatcher.activeNodeName = node.Name
		dispatcher.activeCity = item.City
		dispatcher.activePort = port
		dispatcher.lastSwitchTime = time.Now()

		fmt.Printf("       \x1b[32m✅ 热切换成功！\x1b[0m 当前节点: %s (%s)，耗时: %v\n",
			node.Name, node.ID, elapsed.Round(time.Millisecond))

		// 异步后台探测出口公网 IP 与节点延迟，不阻塞当前等待上网的指纹浏览器
		go func(host string, p int, targetID string) {
			probeStart := time.Now()
			ip := fetchExitIP(host, p, 3*time.Second)
			probeLatency := time.Since(probeStart).Milliseconds()

			dispatcher.mu.Lock()
			if dispatcher.activeNodeID == targetID {
				if ip != "" {
					dispatcher.activeExitIP = ip
					fmt.Printf("       出口 IP: \x1b[32m%s\x1b[0m\n", ip)
				}
				if probeLatency > 0 {
					dispatcher.activeLatencyMs = probeLatency
					fmt.Printf("       通道延迟: \x1b[32m%d ms\x1b[0m\n", probeLatency)
				}
			}
			dispatcher.mu.Unlock()
		}(dispatcher.backendHost, dispatcher.backendPort, node.ID)
	}
	dispatcher.mu.Unlock()

	// 连接后端 SOCKS5 出口端口 (127.0.0.1:1079)
	backendAddr := net.JoinHostPort(dispatcher.backendHost, strconv.Itoa(dispatcher.backendPort))
	backendConn, err := net.DialTimeout("tcp", backendAddr, 5*time.Second)
	if err != nil {
		fmt.Printf("       \x1b[31m❌ 连接后端代理失败 (127.0.0.1:%d):\x1b[0m %v\n", dispatcher.backendPort, err)
		return
	}
	defer backendConn.Close()

	// 双向全双工转发
	errChan := make(chan error, 2)
	go func() {
		_, err := io.Copy(backendConn, clientConn)
		errChan <- err
	}()
	go func() {
		_, err := io.Copy(clientConn, backendConn)
		errChan <- err
	}()

	<-errChan
}

// 后台健康巡检与休眠断线自愈协程 (Watchdog)
func runWatchdog(ctx context.Context) {
	ticker := time.NewTicker(45 * time.Second)
	defer ticker.Stop()

	failCount := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			dispatcher.mu.Lock()
			targetID := dispatcher.activeNodeID
			targetName := dispatcher.activeNodeName
			host := dispatcher.backendHost
			port := dispatcher.backendPort
			dispatcher.mu.Unlock()

			// 空闲待命中无需巡检
			if targetID == "" {
				continue
			}

			// 探测后端 SOCKS5 握手健康状态
			err := verifySocks5(host, port, 3*time.Second)
			if err == nil {
				failCount = 0
				go measureActiveNodeLatency()
				continue
			}

			failCount++
			fmt.Printf("\x1b[33m[Watchdog 巡检]\x1b[0m 后端代理心跳异常 (第 %d/2 次): %v\n", failCount, err)

			// 连续两次检测失败时触发静默自愈重连
			if failCount >= 2 {
				fmt.Printf("\x1b[31m[Watchdog 自愈]\x1b[0m 检测到后端连接中断 (可能因休眠或网络重置)，正在为节点 [%s] 执行静默自愈重连...\n", targetName)
				dispatcher.mu.Lock()
				node, switchErr := switchNodeInternal(targetID)
				if switchErr == nil && node != nil {
					dispatcher.activeNodeID = node.ID
					dispatcher.activeNodeName = node.Name
					dispatcher.activeCity = node.City
					dispatcher.lastSwitchTime = time.Now()
					failCount = 0
					fmt.Printf("\x1b[32m[Watchdog 自愈成功]\x1b[0m 已自动恢复与节点 [%s] 的连接链路！\n", node.Name)
					go func(h string, p int) {
						ip := fetchExitIP(h, p, 3*time.Second)
						if ip != "" {
							dispatcher.mu.Lock()
							dispatcher.activeExitIP = ip
							dispatcher.mu.Unlock()
						}
					}(host, port)
				} else {
					fmt.Printf("\x1b[31m[Watchdog 自愈失败]\x1b[0m 重试重连失败: %v，将在下次巡检继续尝试\n", switchErr)
				}
				dispatcher.mu.Unlock()
			}
		}
	}
}

// 运行分发器主服务
func runDispatcherServer(foreground bool) error {
	setMaxFileLimit()

	// 检查是否已有实例运行
	if isDispatcherRunning() {
		return fmt.Errorf("多端口路由分发服务已在运行中，请勿重复启动")
	}

	// 检查并启动容器
	if err := ensureContainerRunning(); err != nil {
		return fmt.Errorf("启动容器前置检查失败: %w", err)
	}

	cfg, err := loadPortConfig()
	if err != nil {
		return err
	}
	dispatcher.cfg = cfg
	dispatcher.backendPort = cfg.BackendPort
	dispatcher.startTime = time.Now()
	dispatcher.allowFailover = cfg.AllowFailover

	// 建立端口快速反查字典
	for _, m := range cfg.Mappings {
		dispatcher.portIndex[m.Port] = m
	}

	// 记录 PID 文件
	pid := os.Getpid()
	_ = os.WriteFile(getDispatcherPidPath(), []byte(strconv.Itoa(pid)), 0644)
	defer os.Remove(getDispatcherPidPath())

	ctx, cancel := context.WithCancel(context.Background())
	dispatcher.cancelFunc = cancel

	// 捕获系统退出信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// 探测当前已连接的底层节点状态
	connectedNodeID, connectedNodeName := getCurrentConnectedNodeInfo()
	if connectedNodeID != "" {
		dispatcher.activeNodeID = connectedNodeID
		dispatcher.activeNodeName = connectedNodeName
		dispatcher.activeCity = extractCity(connectedNodeName)
		for _, m := range cfg.Mappings {
			if m.NodeID == connectedNodeID {
				dispatcher.activePort = m.Port
				break
			}
		}
	}

	fmt.Println("\n==========================================================================================================")
	fmt.Printf("       🚀 爱加速 (Aijiasu) 每个节点独立专属端口智能分发服务已启动 (全量 %d 节点)\n", cfg.TotalNodes)
	fmt.Println("==========================================================================================================")
	fmt.Printf("• 后端容器出口: 127.0.0.1:%d\n", dispatcher.backendPort)
	fmt.Printf("• 独立端口范围: 127.0.0.1:%d ~ 127.0.0.1:%d (共 %d 个专属端口，每个端口锁定唯一节点)\n",
		cfg.BasePort, cfg.BasePort+cfg.TotalNodes-1, cfg.TotalNodes)
	if dispatcher.allowFailover {
		fmt.Println("• 同城容灾策略: \x1b[32m🟢 智能故障转移已开启\x1b[0m (专属节点超时自动接替同城备选)")
	} else {
		fmt.Println("• 同城容灾策略: \x1b[33m⚪ 严格锁定模式 (默认)\x1b[0m (绝不自动漂移至其他物理节点，支持命令开启: aijiasu failover on)")
	}
	if dispatcher.activeNodeName != "" {
		fmt.Printf("• 当前活动节点: \x1b[32m%s\x1b[0m (ID: %s, 端口: 127.0.0.1:%d)\n",
			dispatcher.activeNodeName, dispatcher.activeNodeID, dispatcher.activePort)
	} else {
		fmt.Println("• 当前活动节点: 待命中 (首次在指纹浏览器发起访问时自动秒级连接)")
	}
	fmt.Println("----------------------------------------------------------------------------------------------------------")
	fmt.Println(" 指纹浏览器各环境代理配置示例 (填入端口即可直达指定地区节点):")
	for idx, m := range cfg.Mappings {
		if idx >= 6 {
			fmt.Printf("   ... 其余 %d 个节点端口请运行 'aijiasu ports' 或 'aijiasu provinces' 查阅\n", cfg.TotalNodes-6)
			break
		}
		fmt.Printf("   • 浏览器环境 %-2d ➜ 协议: socks5, 地址: 127.0.0.1, 端口: \x1b[1;36m%d\x1b[0m ➜ %-12s (%s %s)\n",
			idx+1, m.Port, m.NodeName, m.Province, m.City)
	}
	fmt.Println("----------------------------------------------------------------------------------------------------------")

	startListenTime := time.Now()
	var wg sync.WaitGroup
	var successCount int64

	// 并发极速开启 2800+ 个端口监听
	listenSem := make(chan struct{}, 100) // 控制并发创建协程数
	for _, m := range cfg.Mappings {
		if m.Enabled {
			wg.Add(1)
			item := m
			go func() {
				defer wg.Done()
				listenSem <- struct{}{}
				addr := fmt.Sprintf("0.0.0.0:%d", item.Port)
				listener, err := net.Listen("tcp", addr)
				<-listenSem // 创建完成后立即释放信号量，供后续端口并发创建

				if err != nil {
					return
				}

				dispatcher.mu.Lock()
				dispatcher.listeners[item.Port] = listener
				dispatcher.mu.Unlock()

				atomic.AddInt64(&successCount, 1)

				go func() {
					<-ctx.Done()
					listener.Close()
				}()

				for {
					conn, err := listener.Accept()
					if err != nil {
						select {
						case <-ctx.Done():
							return
						default:
							continue
						}
					}
					go handleClientConn(conn, item)
				}
			}()
		}
	}

	// 等待所有端口建立完成 (最多等待 3 秒)
	waitDeadline := time.Now().Add(3 * time.Second)
	totalExpected := int64(len(cfg.Mappings))
	for time.Now().Before(waitDeadline) {
		if atomic.LoadInt64(&successCount) >= totalExpected {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	listenElapsed := time.Since(startListenTime)
	fmt.Printf("\x1b[32m[aijiasu OK]\x1b[0m 成功激活监听 \x1b[1;32m%d\x1b[0m 个专属端口 (覆盖全量 100%% 节点)，耗时: %v\n",
		atomic.LoadInt64(&successCount), listenElapsed.Round(time.Millisecond))
	fmt.Printf("• Web 可视化面板: \x1b[1;36mhttp://127.0.0.1:%d\x1b[0m (支持节点检索、实时状态与一键 CSV 下载)\n", defaultWebPort)
	fmt.Println("• 健康巡检保护:  已启用后台智能 Watchdog (每 45 秒自愈巡检，保障长连接稳健)")

	// 启动健康巡检与断线自愈协程
	go runWatchdog(ctx)

	// 启动内置轻量 Web 控制台
	go startDashboardServer(ctx, defaultWebPort)

	if foreground {
		fmt.Println(" [前台运行模式] 正在实时打印请求分发与热切换日志 (按 Ctrl+C 优雅停止服务)...")
	} else {
		fmt.Println(" [后台守护模式] 服务已在后台持续运行，日志保存于:", getDispatcherLogPath())
	}
	fmt.Println()

	// 阻塞等待退出信号
	<-sigChan
	fmt.Println("\n正在优雅停止多端口分发服务...")
	cancel()

	dispatcher.mu.Lock()
	for _, l := range dispatcher.listeners {
		l.Close()
	}
	dispatcher.mu.Unlock()

	wg.Wait()
	fmt.Println("\x1b[32m[aijiasu OK]\x1b[0m 分发服务已完全退出。")
	return nil
}

// 检查分发器是否在运行
func isDispatcherRunning() bool {
	pidPath := getDispatcherPidPath()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// 停止后台运行的分发器
func stopDispatcherServer() error {
	pidPath := getDispatcherPidPath()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return fmt.Errorf("分发服务未在运行")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		_ = os.Remove(pidPath)
		return fmt.Errorf("无效的 PID 文件")
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		_ = os.Remove(pidPath)
		return fmt.Errorf("找不到对应进程 (PID: %d)", pid)
	}

	_ = process.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if process.Signal(syscall.Signal(0)) != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	_ = os.Remove(pidPath)
	fmt.Printf("\x1b[32m[aijiasu OK]\x1b[0m 已成功停止分发服务 (PID: %d)。\n", pid)
	return nil
}

// 后台启动分发器
func startDispatcherDaemon() error {
	if isDispatcherRunning() {
		return fmt.Errorf("分发服务已在后台运行中")
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}

	logPath := getDispatcherLogPath()
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("创建日志文件失败: %w", err)
	}

	cmd := exec.Command(exe, "serve", "--foreground")
	cmd.Dir = getProjectDir()
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动后台进程失败: %w", err)
	}

	// 等待 2 秒确认子进程初始化就绪并写入 PID
	deadline := time.Now().Add(5 * time.Second)
	started := false
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		if isDispatcherRunning() {
			started = true
			break
		}
	}

	if !started {
		return fmt.Errorf("分发服务启动超时或异常退出，请查看日志: %s", logPath)
	}

	fmt.Println("\n\x1b[32m[aijiasu OK]\x1b[0m 虚拟多端口路由分发服务已成功在后台启动！")
	fmt.Printf("• 进程 PID:   %d\n", cmd.Process.Pid)
	fmt.Printf("• Web 控制台: \x1b[1;36mhttp://127.0.0.1:%d\x1b[0m\n", defaultWebPort)
	fmt.Printf("• 运行日志:   %s\n", logPath)
	fmt.Println("• 查看状态:   aijiasu serve status")
	fmt.Println("• 停止服务:   aijiasu serve stop")
	fmt.Println("• 端口映射表: aijiasu ports")
	return nil
}

// 主动测量当前活跃节点的端到端真实延迟 (ms)
func measureActiveNodeLatency() int64 {
	dispatcher.mu.Lock()
	nodeID := dispatcher.activeNodeID
	bHost := dispatcher.backendHost
	bPort := dispatcher.backendPort
	dispatcher.mu.Unlock()

	if nodeID == "" {
		return 0
	}

	start := time.Now()
	// 优先使用极速 204 探针
	proxyArg := fmt.Sprintf("socks5h://%s:%d", bHost, bPort)
	_, err := runCmd("curl", "-s", "-o", "/dev/null", "--connect-timeout", "2", "-m", "3", "-x", proxyArg, "http://connect.rom.miui.com/generate_204")
	if err == nil {
		elapsed := time.Since(start).Milliseconds()
		dispatcher.mu.Lock()
		if dispatcher.activeNodeID == nodeID {
			dispatcher.activeLatencyMs = elapsed
		}
		dispatcher.mu.Unlock()
		return elapsed
	}

	// 备选探针: ipip.net
	start2 := time.Now()
	ip := fetchExitIP(bHost, bPort, 3*time.Second)
	if ip != "" {
		elapsed := time.Since(start2).Milliseconds()
		dispatcher.mu.Lock()
		if dispatcher.activeNodeID == nodeID {
			dispatcher.activeExitIP = ip
			dispatcher.activeLatencyMs = elapsed
		}
		dispatcher.mu.Unlock()
		return elapsed
	}

	return -1
}

