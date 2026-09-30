package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultContainer = "aijiasu-runner"
	defaultProxyHost = "127.0.0.1"
	connectPattern   = "(^|/)ajiasu connect( |$)"
)

type NodeItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	City   string `json:"city"`
}

type DoctorReport struct {
	DockerInstalled   bool   `json:"docker_installed"`
	DaemonRunning     bool   `json:"daemon_running"`
	ContainerRun      bool   `json:"container_running"`
	LoggedIn          bool   `json:"logged_in"`
	CurrentStatus     string `json:"current_status"`
	BackendHealthy    bool   `json:"backend_healthy"`
	BackendPort       int    `json:"backend_port"`
	DispatcherRunning bool   `json:"dispatcher_running"`
	DispatcherPorts   int    `json:"dispatcher_ports"`
	ActivePort        int    `json:"active_port"`
	ActiveTarget      string `json:"active_target"`
	ExitIP            string `json:"exit_ip"`
	Error             string `json:"error,omitempty"`
}

// 提取城市名
var cityRegexp = regexp.MustCompile(`^([\p{Han}A-Za-z]+)`)

func extractCity(name string) string {
	name = strings.TrimSpace(name)
	m := cityRegexp.FindStringSubmatch(name)
	if len(m) > 1 {
		return m[1]
	}
	return "其他"
}

// 执行宿主机命令
func runCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// 执行容器内命令
func dockerExec(args ...string) (string, error) {
	cmdArgs := append([]string{"exec", defaultContainer}, args...)
	cmd := exec.Command("docker", cmdArgs...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// 原生 SOCKS5 握手验证
func verifySocks5(host string, port int, timeout time.Duration) error {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(timeout))
	// SOCKS5 协商 (VER=5, NMETHODS=1, METHOD=0)
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if buf[0] != 0x05 || buf[1] != 0x00 {
		return fmt.Errorf("代理握手未返回 NO AUTH 成功响应: %v", buf)
	}
	return nil
}

// 通过 SOCKS5 代理获取当前出口 IP 与归属地
func fetchExitIP(host string, port int, timeout time.Duration) string {
	proxyAddr := net.JoinHostPort(host, strconv.Itoa(port))

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := net.DialTimeout("tcp", proxyAddr, timeout)
			if err != nil {
				return nil, err
			}
			// 1. SOCKS5 协商
			if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
				conn.Close()
				return nil, err
			}
			buf := make([]byte, 2)
			if _, err := io.ReadFull(conn, buf); err != nil || buf[0] != 0x05 || buf[1] != 0x00 {
				conn.Close()
				return nil, fmt.Errorf("socks5 handshake error")
			}

			// 2. SOCKS5 CONNECT
			hostPart, portPart, err := net.SplitHostPort(addr)
			if err != nil {
				conn.Close()
				return nil, err
			}
			p, _ := strconv.Atoi(portPart)
			req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(hostPart))}
			req = append(req, []byte(hostPart)...)
			req = append(req, byte(p>>8), byte(p&0xff))

			if _, err := conn.Write(req); err != nil {
				conn.Close()
				return nil, err
			}

			resp := make([]byte, 4)
			if _, err := io.ReadFull(conn, resp); err != nil || resp[1] != 0x00 {
				conn.Close()
				return nil, fmt.Errorf("socks5 connect fail: %v", resp)
			}

			// 读取后续地址字段
			switch resp[3] {
			case 0x01:
				io.CopyN(io.Discard, conn, 4+2)
			case 0x03:
				l := make([]byte, 1)
				io.ReadFull(conn, l)
				io.CopyN(io.Discard, conn, int64(l[0])+2)
			case 0x04:
				io.CopyN(io.Discard, conn, 16+2)
			}
			return conn, nil
		},
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}

	req, err := http.NewRequest("GET", "https://myip.ipip.net", nil)
	if err == nil {
		req.Header.Set("User-Agent", "curl/8.7.1")
		resp, err := client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			b, err := io.ReadAll(resp.Body)
			if err == nil {
				text := strings.TrimSpace(string(b))
				text = strings.TrimPrefix(text, "当前 IP：")
				text = strings.TrimPrefix(text, "当前 IP:")
				text = strings.Join(strings.Fields(text), " ")
				if text != "" {
					return text
				}
			}
		}
	}

	// 备用回退：系统 curl 探测
	proxyArg := fmt.Sprintf("socks5h://%s:%d", host, port)
	out, err := runCmd("curl", "-s", "--connect-timeout", "3", "-m", "4", "-x", proxyArg, "https://myip.ipip.net")
	if err == nil {
		text := strings.TrimSpace(out)
		text = strings.TrimPrefix(text, "当前 IP：")
		text = strings.TrimPrefix(text, "当前 IP:")
		text = strings.Join(strings.Fields(text), " ")
		if text != "" {
			return text
		}
	}
	return ""
}

// 检查爱加速内部实际监听端口
func detectInternalPort(pid string) int {
	out, err := dockerExec("ss", "-H", "-lntp")
	if err != nil {
		return 0
	}
	lines := strings.Split(out, "\n")
	re := regexp.MustCompile(`(?:\*|127\.0\.0\.1|0\.0\.0\.0|\[::\]):(\d+)\s+`)
	for _, l := range lines {
		if strings.Contains(l, fmt.Sprintf("pid=%s,", pid)) {
			m := re.FindStringSubmatch(l)
			if len(m) > 1 {
				p, _ := strconv.Atoi(m[1])
				if p > 0 && p != 1080 {
					return p
				}
			}
		}
	}
	return 0
}

// 动态确保容器内 socat 桥接规则
func ensureBridge(targetPort int) error {
	if targetPort <= 0 {
		targetPort = 18888
	}

	psOut, _ := dockerExec("ps", "-eo", "args")
	expected := fmt.Sprintf("TCP:127.0.0.1:%d", targetPort)
	if strings.Contains(psOut, "socat TCP-LISTEN:1080") && strings.Contains(psOut, expected) {
		return nil
	}

	_, _ = dockerExec("pkill", "-f", "^socat TCP-LISTEN:1080")
	time.Sleep(100 * time.Millisecond)

	socatCmd := fmt.Sprintf("nohup socat TCP-LISTEN:1080,fork,reuseaddr,bind=0.0.0.0 TCP:127.0.0.1:%d >/dev/null 2>&1 &", targetPort)
	_, err := dockerExec("bash", "-c", socatCmd)
	time.Sleep(150 * time.Millisecond)
	return err
}

// 获取当前爱加速连接进程 PID
func getConnectPids() []string {
	out, err := dockerExec("pgrep", "-f", connectPattern)
	if err != nil {
		return nil
	}
	fields := strings.Fields(strings.TrimSpace(out))
	var pids []string
	for _, f := range fields {
		if _, err := strconv.Atoi(f); err == nil {
			pids = append(pids, f)
		}
	}
	return pids
}

// 检查并自动启动爱加速 Docker 容器服务 (aijiasu up)
func ensureContainerRunning() error {
	out, err := runCmd("docker", "inspect", "-f", "{{.State.Running}}", defaultContainer)
	if err == nil && strings.TrimSpace(out) == "true" {
		return nil
	}

	fmt.Printf("  ➜ [Docker 服务] 检测到爱加速容器 (%s) 未运行，正在自动启动服务 (aijiasu up)...\n", defaultContainer)
	dir := getProjectDir()
	cmd := exec.Command("docker", "compose", "up", "-d")
	cmd.Dir = dir
	composeOut, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("自动启动 Docker 服务失败: %w (%s)", err, strings.TrimSpace(string(composeOut)))
	}

	// 等待容器启动完成并处于已登录就绪状态，且云端节点列表已同步
	fmt.Print("  ➜ [等待就绪] 等待爱加速初始化与节点同步")
	ready := false
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(1 * time.Second)
		fmt.Print(".")
		out, err := dockerExec("ajiasu", "list")
		if err == nil && strings.Contains(out, " ok ") {
			ready = true
			break
		}
	}
	fmt.Println()

	if !ready {
		return fmt.Errorf("爱加速服务启动超时，请检查账号密码 (运行 aijiasu auth / aijiasu login) 或运行 aijiasu logs 查看日志")
	}

	fmt.Println("  ➜ [服务就绪] 爱加速 Docker 容器已就绪！")
	return nil
}

// 获取所有节点列表
func fetchNodes() ([]NodeItem, error) {
	if err := ensureContainerRunning(); err != nil {
		return nil, err
	}

	out, err := dockerExec("ajiasu", "list")
	if err != nil {
		return nil, fmt.Errorf("执行 ajiasu list 失败: %w (%s)", err, out)
	}

	lines := strings.Split(out, "\n")
	var list []NodeItem
	for _, line := range lines {
		parts := strings.Fields(strings.TrimSpace(line))
		if len(parts) >= 3 && parts[1] == "ok" {
			id := parts[0]
			name := strings.Join(parts[2:], " ")
			list = append(list, NodeItem{
				ID:     id,
				Name:   name,
				Status: parts[1],
				City:   extractCity(name),
			})
		}
	}
	return list, nil
}

// 诊断当前服务环境
func doctor() DoctorReport {
	rep := DoctorReport{
		BackendPort: defaultBackendPort,
	}
	if _, err := runCmd("docker", "--version"); err == nil {
		rep.DockerInstalled = true
	} else {
		rep.Error = "未检测到 Docker CLI，请先启动或安装 Docker"
		return rep
	}

	if _, err := runCmd("docker", "info"); err == nil {
		rep.DaemonRunning = true
	} else {
		rep.Error = "Docker 守护进程未运行，请先启动 Docker Desktop"
		return rep
	}

	out, err := runCmd("docker", "inspect", "-f", "{{.State.Running}}", defaultContainer)
	if err == nil && strings.TrimSpace(out) == "true" {
		rep.ContainerRun = true
	} else {
		rep.Error = fmt.Sprintf("容器 %s 未在运行，可执行 aijiasu up 启动", defaultContainer)
		return rep
	}

	stOut, _ := dockerExec("ajiasu", "status")
	rep.CurrentStatus = strings.TrimSpace(stOut)
	if !strings.Contains(strings.ToLower(stOut), "not log") && !strings.Contains(stOut, "未登录") {
		rep.LoggedIn = true
	}

	pids := getConnectPids()
	if len(pids) > 0 {
		internalPort := detectInternalPort(pids[0])
		if internalPort > 0 {
			_ = ensureBridge(internalPort)
		}
	}

	// 检查后端端口健康度
	if err := verifySocks5(defaultProxyHost, defaultBackendPort, 1500*time.Millisecond); err == nil {
		rep.BackendHealthy = true
		rep.ExitIP = fetchExitIP(defaultProxyHost, defaultBackendPort, 3*time.Second)
	}

	// 检查调度器运行状态
	rep.DispatcherRunning = isDispatcherRunning()
	cfg, err := loadPortConfig()
	if err == nil {
		rep.DispatcherPorts = len(cfg.Mappings)
	}

	return rep
}

// 切换节点
func switchNode(keyword string) error {
	if err := ensureContainerRunning(); err != nil {
		return err
	}

	node, err := switchNodeInternal(keyword)
	if err != nil {
		return err
	}

	exitIP := fetchExitIP(defaultProxyHost, defaultBackendPort, 4*time.Second)
	fmt.Printf("\n\x1b[32m[aijiasu OK]\x1b[0m 节点切换成功！当前节点: %s (%s)\n", node.Name, node.ID)
	fmt.Printf("后端代理宿主地址: socks5://%s:%d\n", defaultProxyHost, defaultBackendPort)
	if exitIP != "" {
		fmt.Printf("当前出口公网 IP:  %s\n", exitIP)
	}
	return nil
}

func getProjectDir() string {
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err == nil {
			return dir
		}
	}
	return "/Users/wangbaolong/workspace/aijiasu-docker"
}

func forwardCompose(args ...string) {
	dir := getProjectDir()

	// 对于启动类命令 (up / start / restart)，先确保凭证已就绪并进行预检
	isStartCmd := false
	for _, a := range args {
		if a == "up" || a == "start" || a == "restart" {
			isStartCmd = true
			break
		}
	}

	cmd := exec.Command("docker", append([]string{"compose"}, args...)...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	var cred *Credentials
	if isStartCmd {
		var err error
		cred, err = EnsureCredentials()
		if err != nil {
			fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu 错误]\x1b[0m 凭证校验失败: %v\n", err)
			os.Exit(1)
		}
	}

	if err := cmd.Run(); err != nil {
		os.Exit(1)
	}

	if isStartCmd && cred != nil {
		time.Sleep(1 * time.Second)
		if err := injectCredentialsIntoContainer(cred); err != nil {
			fmt.Printf("\x1b[33m[aijiasu 提示]\x1b[0m 同步容器凭据: %v\n", err)
		} else {
			fmt.Println("\x1b[32m[aijiasu 安全机制]\x1b[0m 凭据已通过纯内存管道注入容器鉴权，Docker 环境变量与元数据零泄漏！")
		}
	}
}

func printHelp() {
	fmt.Print(`
爱加速 (Aijiasu) 高性能 Go 原生智能代理管理与多端口路由工具

用法:
  aijiasu <命令> [参数]

安全认证命令 (AES-256-GCM 硬件绑定加密):
  auth / credentials       查看当前爱加速账号凭证加密保护状态
  login                    交互式配置/更新爱加速账号与密码 (安全掩码输入)
  logout                   清除本地保存的加密凭证文件

核心命令 (指纹浏览器虚拟多端口):
  ports [-g/--group]       查看指纹浏览器虚拟多端口映射表
                           • aijiasu ports       : 平铺列表视图 (直观展示各端口绑定节点与当前活跃状态)
                           • aijiasu ports -g    : 官方省份二级分类视图 (省份 -> 城市/节点 -> 端口，一目了然)
                           • aijiasu provinces   : 省份二级分类视图快捷命令
  port add <端口> <城市>   添加或修改指纹浏览器端口映射 (如: aijiasu port add 1100 天津)
  port del <端口>          删除指定的端口映射
  port reset               重置为系统默认推荐的全国 20 大热门省市端口映射表

  sync / update [--reorder] 与爱加速云端实时同步节点变动 (新增/下线自动处理)
                           • aijiasu sync        : 安全增量更新 (已有节点端口 100% 锁定不变，仅追加新节点或移除失效节点)
                           • aijiasu sync --reorder: 全量重新排版重置端口

  serve [start/stop/status] 多端口智能路由分发服务 (按需流量自动热切换)
                           • aijiasu serve       : 前台运行并实时输出流量分发与切换日志
                           • aijiasu serve -d    : 后台守护模式启动
                           • aijiasu serve stop  : 停止后台分发服务
                           • aijiasu serve status: 查看分发服务运行状态

基础管理命令:
  status                   检查 Docker 容器、爱加速登录状态、后端 SOCKS5 与当前出口 IP
  switch [节点/城市]       智能切换底层节点 (支持城市模糊搜索如: "上海"、精确名称或 ID)
  nodes [选项/关键字]      查看全量可用节点列表
                           • aijiasu nodes            : 列出全量可用节点
                           • aijiasu nodes -s         : 查看各城市节点数量统计概览
                           • aijiasu nodes -p         : 按省份二级分类统计节点
                           • aijiasu nodes -g         : 按城市分组展开节点列表
                           • aijiasu nodes 广州       : 筛选包含该关键字的节点
  disconnect               断开当前爱加速底层连接
  test [端口]              测试代理健康握手与出口 IP (默认测试后端 1079 或指定端口如 1080)
  up / start               一键启动 Docker 服务 (自动预检与加密凭据)
  down / stop              停止并销毁 Docker 容器及分发服务
  restart                  重启容器
  logs                     查看容器运行日志
  help                     显示此帮助信息
`)
}

func main() {
	args := os.Args[1:]
	cmd := "status"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}

	handlers := map[string]func([]string){
		"up":            cmdUp,
		"start":         cmdUp,
		"down":          cmdDown,
		"stop":          cmdDown,
		"restart":       cmdRestart,
		"logs":          cmdLogs,
		"status":        cmdStatus,
		"ports":         cmdPorts,
		"list-ports":    cmdPorts,
		"provinces":     cmdProvinces,
		"sync":          cmdSync,
		"update":        cmdSync,
		"sync-nodes":    cmdSync,
		"update-nodes":  cmdSync,
		"port":          cmdPort,
		"serve":         cmdServe,
		"server":        cmdServe,
		"failover":      cmdFailover,
		"nodes":         cmdNodes,
		"list":          cmdNodes,
		"switch":        cmdSwitch,
		"disconnect":    cmdDisconnect,
		"test":          cmdTest,
		"auth":          cmdAuth,
		"credential":    cmdAuth,
		"credentials":   cmdAuth,
		"login":         cmdLogin,
		"logout":        cmdLogout,
		"help":          cmdHelp,
		"--help":        cmdHelp,
		"-h":            cmdHelp,
	}

	handler, ok := handlers[cmd]
	if !ok {
		cmdHelp(args)
		return
	}
	handler(args)
}

func printStatus(label string, ok bool) {
	if ok {
		fmt.Printf("%-18s \x1b[32m✅ 就绪\x1b[0m\n", label)
	} else {
		fmt.Printf("%-18s \x1b[31m❌ 未运行/未就绪\x1b[0m\n", label)
	}
}
