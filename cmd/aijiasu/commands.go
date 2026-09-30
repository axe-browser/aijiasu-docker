package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// cmdUp 启动容器与相关服务
func cmdUp(args []string) {
	forwardCompose("up", "-d")
	time.Sleep(1 * time.Second)
	if !isDispatcherRunning() {
		fmt.Println("\n💡 提示: 可运行 'aijiasu serve -d' 启动多端口分发服务，或 'aijiasu ports' 查看指纹浏览器端口配置。")
	}
}

// cmdDown 停止分发器并停止容器
func cmdDown(args []string) {
	if isDispatcherRunning() {
		_ = stopDispatcherServer()
	}
	forwardCompose("down")
}

// cmdRestart 重启服务与容器
func cmdRestart(args []string) {
	if isDispatcherRunning() {
		_ = stopDispatcherServer()
	}
	forwardCompose("restart")
}

// cmdLogs 实时跟踪容器日志
func cmdLogs(args []string) {
	forwardCompose("logs", "-f")
}

// cmdStatus 系统状态健康诊断
func cmdStatus(args []string) {
	fmt.Println("\n=== 爱加速 Docker 代理服务状态检查 (Go 原生) ===")
	rep := doctor()
	printStatus("Docker CLI:       ", rep.DockerInstalled)
	printStatus("Docker Daemon:    ", rep.DaemonRunning)
	printStatus(fmt.Sprintf("容器状态 (%s):", defaultContainer), rep.ContainerRun)
	printStatus("爱加速登录状态:    ", rep.LoggedIn)

	cred, _ := loadCredentialsFromFile()
	if cred == nil {
		cred, _ = migrateOldEnvIfPresent()
	}
	if cred != nil && cred.Username != "" {
		fmt.Printf("凭据安全保护:      \x1b[32m✅ AES-256-GCM 硬件加密 (%s)\x1b[0m\n", maskUsername(cred.Username))
	} else {
		fmt.Printf("凭据安全保护:      \x1b[33m⚪ 未配置 (运行 aijiasu login 进行交互式录入)\x1b[0m\n")
	}

	if rep.BackendHealthy {
		fmt.Printf("后端 SOCKS5 出口:  \x1b[32m✅ 127.0.0.1:%d 畅通\x1b[0m\n", rep.BackendPort)
		if rep.ExitIP != "" {
			fmt.Printf("当前出口公网 IP:   \x1b[32m%s\x1b[0m\n", rep.ExitIP)
		}
	} else {
		fmt.Printf("后端 SOCKS5 出口:  \x1b[31m❌ 127.0.0.1:%d 未就绪或未连接节点\x1b[0m\n", rep.BackendPort)
	}

	if rep.DispatcherRunning {
		fmt.Printf("多端口路由服务:    \x1b[32m✅ 运行中 (已监听 %d 个虚拟端口，供指纹浏览器配置)\x1b[0m\n", rep.DispatcherPorts)
		fmt.Printf("Web 管理控制台:    \x1b[1;36mhttp://127.0.0.1:%d\x1b[0m\n", defaultWebPort)
		cfg, _ := loadPortConfig()
		if cfg != nil {
			if cfg.AllowFailover {
				fmt.Printf("同城容灾策略:      \x1b[32m🟢 智能故障转移已开启 (超时自动接替同城备用)\x1b[0m\n")
			} else {
				fmt.Printf("同城容灾策略:      \x1b[33m⚪ 严格锁定模式 (默认，绝不漂移物理节点)\x1b[0m\n")
			}
		}
	} else {
		fmt.Printf("多端口路由服务:    \x1b[33m⚪ 未运行 (可运行 'aijiasu serve -d' 后台启动)\x1b[0m\n")
	}

	if rep.Error != "" {
		fmt.Printf("\n\x1b[33m提示:\x1b[0m %s\n", rep.Error)
	}
	fmt.Println()
}

// cmdPorts 查看虚拟端口映射表 (平铺/树状/JSON/CSV)
func cmdPorts(args []string) {
	isGroup := false
	isJSON := false
	isCSV := false
	showAll := false
	var cleanArgs []string

	for _, a := range args {
		switch a {
		case "-g", "--group", "-t", "--tree":
			isGroup = true
		case "--json":
			isJSON = true
		case "--csv":
			isCSV = true
		case "-a", "--all":
			showAll = true
		default:
			cleanArgs = append(cleanArgs, a)
		}
	}
	kw := strings.Join(cleanArgs, " ")

	cfg, err := loadPortConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m 加载端口配置失败: %v\n", err)
		os.Exit(1)
	}

	if isCSV {
		exportPortsCSV(cfg)
		return
	}

	if isJSON {
		b, _ := json.MarshalIndent(cfg, "", "  ")
		fmt.Println(string(b))
		return
	}

	activeNodeID, _ := getCurrentConnectedNodeInfo()

	if isGroup {
		printGroupedProvincePortTree(cfg, activeNodeID, kw)
	} else {
		printFlatPortTable(cfg, activeNodeID, kw, showAll)
	}
}

// cmdProvinces 省份二级分类树状视图
func cmdProvinces(args []string) {
	cfg, err := loadPortConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m 加载端口配置失败: %v\n", err)
		os.Exit(1)
	}
	activeNodeID, _ := getCurrentConnectedNodeInfo()
	kw := strings.Join(args, " ")
	printGroupedProvincePortTree(cfg, activeNodeID, kw)
}

// cmdSync 与爱加速官方云端同步节点
func cmdSync(args []string) {
	forceReorder := false
	for _, a := range args {
		if a == "--reorder" || a == "--force" {
			forceReorder = true
		}
	}
	fmt.Println("\n=== 正在同步爱加速官方云端最新可用节点 ===")
	report, cfg, err := syncNodesWithCloud(forceReorder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m 节点同步失败: %v\n", err)
		os.Exit(1)
	}
	printSyncReport(report, cfg)
}

// cmdPort 端口微调命令组 (add/del/reset)
func cmdPort(args []string) {
	if len(args) == 0 {
		fmt.Println("用法: aijiasu port [add|del|reset] ...")
		return
	}
	subCmd := args[0]
	switch subCmd {
	case "add":
		if len(args) < 3 {
			fmt.Println("用法: aijiasu port add <端口号> <城市/节点名称> [用途描述]")
			fmt.Println("示例: aijiasu port add 1100 天津 \"跨境电商店铺环境\"")
			return
		}
		p, err := strconv.Atoi(args[1])
		if err != nil {
			fmt.Println("错误: 端口必须为数字")
			return
		}
		target := args[2]
		desc := ""
		if len(args) >= 4 {
			desc = strings.Join(args[3:], " ")
		}
		cfg, err := addOrUpdatePortMapping(p, target, desc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m %v\n", err)
			return
		}
		fmt.Printf("\x1b[32m[aijiasu OK]\x1b[0m 成功添加/更新端口映射: 127.0.0.1:%d ➜ %s (%s)\n", p, target, desc)
		if isDispatcherRunning() {
			fmt.Println("  ➜ 正在热刷新多端口路由分发服务...")
			_ = stopDispatcherServer()
			_ = startDispatcherDaemon()
		}
		activeNodeID, _ := getCurrentConnectedNodeInfo()
		printFlatPortTable(cfg, activeNodeID, "", false)

	case "del", "remove", "rm":
		if len(args) < 2 {
			fmt.Println("用法: aijiasu port del <端口号>")
			return
		}
		p, err := strconv.Atoi(args[1])
		if err != nil {
			fmt.Println("错误: 端口必须为数字")
			return
		}
		cfg, err := deletePortMapping(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m %v\n", err)
			return
		}
		fmt.Printf("\x1b[32m[aijiasu OK]\x1b[0m 成功删除端口 %d 的映射配置。\n", p)
		if isDispatcherRunning() {
			fmt.Println("  ➜ 正在热刷新多端口路由分发服务...")
			_ = stopDispatcherServer()
			_ = startDispatcherDaemon()
		}
		activeNodeID, _ := getCurrentConnectedNodeInfo()
		printFlatPortTable(cfg, activeNodeID, "", false)

	case "reset":
		nodes, err := fetchNodes()
		if err != nil {
			fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m 获取节点失败: %v\n", err)
			return
		}
		mappings := generateAllNodeMappings(nodes, defaultBasePort)
		cfg := &PortConfig{
			Version:     "2.0",
			BasePort:    defaultBasePort,
			BackendPort: defaultBackendPort,
			TotalNodes:  len(mappings),
			Mappings:    mappings,
		}
		if err := savePortConfig(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m 重置失败: %v\n", err)
			return
		}
		fmt.Printf("\x1b[32m[aijiasu OK]\x1b[0m 已成功重置为全量 %d 个节点的连续端口映射表！\n", len(mappings))
		if isDispatcherRunning() {
			fmt.Println("  ➜ 正在热刷新多端口路由分发服务...")
			_ = stopDispatcherServer()
			_ = startDispatcherDaemon()
		}
		activeNodeID, _ := getCurrentConnectedNodeInfo()
		printFlatPortTable(cfg, activeNodeID, "", false)

	default:
		fmt.Println("用法: aijiasu port reset (重置为全量节点的自然端口映射)")
	}
}

// cmdServe 多端口路由分发守护服务控制
func cmdServe(args []string) {
	action := "foreground"
	if len(args) > 0 {
		switch args[0] {
		case "start", "-d", "--daemon":
			action = "daemon"
		case "stop":
			action = "stop"
		case "status":
			action = "status"
		case "--foreground":
			action = "foreground"
		default:
			action = "foreground"
		}
	}

	switch action {
	case "status":
		if isDispatcherRunning() {
			fmt.Println("\x1b[32m[aijiasu OK]\x1b[0m 虚拟多端口路由分发服务正在运行中。")
			cfg, _ := loadPortConfig()
			if cfg != nil {
				fmt.Printf("• 后端出口: 127.0.0.1:%d\n", cfg.BackendPort)
				fmt.Printf("• 监听端口: 共 %d 个 (1080~1099 等)\n", len(cfg.Mappings))
				if cfg.AllowFailover {
					fmt.Println("• 容灾策略: \x1b[32m🟢 智能故障转移已开启 (超时自动接替同城备用)\x1b[0m")
				} else {
					fmt.Println("• 容灾策略: \x1b[33m⚪ 严格锁定模式 (默认，绝不漂移物理节点)\x1b[0m")
				}
			}
			fmt.Printf("• Web 控制台: \x1b[1;36mhttp://127.0.0.1:%d\x1b[0m\n", defaultWebPort)
			fmt.Printf("• 日志文件: %s\n", getDispatcherLogPath())
		} else {
			fmt.Println("\x1b[33m[aijiasu 提示]\x1b[0m 虚拟多端口路由分发服务当前未在运行。")
			fmt.Println("💡 运行 'aijiasu serve' 或 'aijiasu serve -d' 即可启动。")
		}
	case "stop":
		if err := stopDispatcherServer(); err != nil {
			fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m %v\n", err)
			os.Exit(1)
		}
	case "daemon":
		if _, err := EnsureCredentials(); err != nil {
			fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu 错误]\x1b[0m 凭据校验失败: %v\n", err)
			os.Exit(1)
		}
		if err := startDispatcherDaemon(); err != nil {
			fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m %v\n", err)
			os.Exit(1)
		}
	case "foreground":
		if _, err := EnsureCredentials(); err != nil {
			fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu 错误]\x1b[0m 凭据校验失败: %v\n", err)
			os.Exit(1)
		}
		if err := runDispatcherServer(true); err != nil {
			fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m %v\n", err)
			os.Exit(1)
		}
	}
}

// cmdFailover 同城故障转移容灾策略切换
func cmdFailover(args []string) {
	if len(args) == 0 || args[0] == "status" {
		cfg, err := loadPortConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m 读取配置失败: %v\n", err)
			return
		}
		if cfg.AllowFailover {
			fmt.Println("\n当前容灾策略: \x1b[32m🟢 开启同城智能故障转移\x1b[0m")
			fmt.Println("说明: 专属节点若连接超时，将自动秒级接替同城备用节点。")
			fmt.Println("提示: 如需严格锁定节点不漂移，可运行: aijiasu failover off")
		} else {
			fmt.Println("\n当前容灾策略: \x1b[33m⚪ 严格锁定模式 (默认)\x1b[0m")
			fmt.Println("说明: 专属节点若不可用直接报错，坚决不自动漂移至其他物理节点。")
			fmt.Println("提示: 如需开启同城容错转移，可运行: aijiasu failover on")
		}
		return
	}

	sub := strings.ToLower(args[0])
	switch sub {
	case "on", "enable", "open", "true", "1":
		_, err := setAllowFailover(true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m 设置失败: %v\n", err)
			return
		}
		fmt.Println("\n\x1b[32m[aijiasu OK]\x1b[0m 已开启【同城智能故障转移】！")
		fmt.Println("• 策略行为: 专属节点若连接超时，将自动秒级接驳同城备用节点，保证指纹浏览器连接不中断。")
		fmt.Println("• 当前配置已持久化至 ports.json，运行中的分发器已实时生效。")

	case "off", "disable", "close", "false", "0":
		_, err := setAllowFailover(false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m 设置失败: %v\n", err)
			return
		}
		fmt.Println("\n\x1b[32m[aijiasu OK]\x1b[0m 已切换为【严格锁定模式 (默认)】！")
		fmt.Println("• 策略行为: 专属节点若不可用直接报错拦截，坚决不发生物理节点漂移。")
		fmt.Println("• 当前配置已持久化至 ports.json，运行中的分发器已实时生效。")

	default:
		fmt.Println("用法: aijiasu failover [on|off|status]")
		fmt.Println("示例:")
		fmt.Println("  aijiasu failover off  # 恢复严格锁定模式 (默认，绝不漂移节点)")
		fmt.Println("  aijiasu failover on   # 开启同城容错智能转移")
	}
}

// cmdNodes 列出爱加速可用节点列表
func cmdNodes(args []string) {
	isSummary := false
	isGroup := false
	isProvince := false
	isJSON := false
	var cleanArgs []string

	for _, a := range args {
		switch a {
		case "-s", "--summary":
			isSummary = true
		case "-g", "--group":
			isGroup = true
		case "-p", "--province":
			isProvince = true
		case "--json":
			isJSON = true
		default:
			cleanArgs = append(cleanArgs, a)
		}
	}
	kw := strings.ToLower(strings.Join(cleanArgs, " "))

	nodes, err := fetchNodes()
	if err != nil {
		fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m %v\n", err)
		os.Exit(1)
	}

	var matched []NodeItem
	if kw != "" {
		for _, n := range nodes {
			if strings.Contains(strings.ToLower(n.Name), kw) || strings.Contains(strings.ToLower(n.ID), kw) {
				matched = append(matched, n)
			}
		}
	} else {
		matched = nodes
	}

	if isJSON {
		b, _ := json.MarshalIndent(matched, "", "  ")
		fmt.Println(string(b))
		return
	}

	cityMap := make(map[string][]NodeItem)
	for _, n := range matched {
		cityMap[n.City] = append(cityMap[n.City], n)
	}

	if isProvince {
		groups := aggregateNodesByProvince(matched)
		fmt.Printf("\n=== 按省份二级分类统计 (共 %d 个可用节点，覆盖 %d 个省份) ===\n", len(matched), len(groups))
		for _, g := range groups {
			fmt.Printf("【%s】(共 %d 个节点):\n", g.Province.Name, g.TotalNodes)
			sortedCities := sortCitiesByNodeCount(g.CityNodes)
			for _, c := range sortedCities {
				fmt.Printf("   • %-8s : %d 个节点\n", c, len(g.CityNodes[c]))
			}
		}
		fmt.Println()
		return
	}

	if isSummary {
		fmt.Printf("\n=== 节点概览 (共 %d 个节点，覆盖 %d 个城市/区域) ===\n", len(matched), len(cityMap))
		type CityCount struct {
			City  string
			Count int
		}
		var list []CityCount
		for c, items := range cityMap {
			list = append(list, CityCount{City: c, Count: len(items)})
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].Count > list[j].Count
		})
		for _, cc := range list {
			fmt.Printf("  - %-10s : %d 个节点\n", cc.City, cc.Count)
		}
		fmt.Println("\n💡 提示: 可使用 'aijiasu ports -g' 查看按省份分类及端口映射关系。")
		return
	}

	if isGroup {
		fmt.Printf("\n=== 按城市分组节点列表 (共 %d 个节点，覆盖 %d 个区域) ===\n", len(matched), len(cityMap))
		var cities []string
		for c := range cityMap {
			cities = append(cities, c)
		}
		sort.Strings(cities)
		for _, c := range cities {
			items := cityMap[c]
			fmt.Printf("\n【%s】(%d 个节点):\n", c, len(items))
			for _, it := range items {
				fmt.Printf("   • [%s] %s (%s)\n", it.ID, it.Name, it.Status)
			}
		}
		fmt.Println()
		return
	}

	fmt.Printf("\n=== 所有可用节点列表 (共 %d 个可用节点) ===\n", len(matched))
	for _, it := range matched {
		fmt.Printf("  - [%s] %s (%s)\n", it.ID, it.Name, it.Status)
	}
	fmt.Printf("\n共 %d 个节点。\n", len(matched))
	fmt.Println("💡 提示:")
	fmt.Println("  - 指纹浏览器多端口映射表: aijiasu ports")
	fmt.Println("  - 全国省份二级分类树: aijiasu ports -g (或 aijiasu provinces)")
	fmt.Println("  - 城市概览统计: aijiasu nodes -s")
}

// cmdSwitch 手动直连指定节点
func cmdSwitch(args []string) {
	kw := strings.Join(args, " ")
	if err := switchNode(kw); err != nil {
		fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m 节点切换失败: %v\n", err)
		os.Exit(1)
	}
}

// cmdDisconnect 断开爱加速物理连接
func cmdDisconnect(args []string) {
	fmt.Println("正在断开爱加速节点连接...")
	_, _ = dockerExec("pkill", "-f", connectPattern)
	_, _ = dockerExec("ajiasu", "disconnect")
	fmt.Println("\x1b[32m[aijiasu OK]\x1b[0m 已成功断开连接。")
}

// cmdTest 测试 SOCKS5 代理通道与公网出口
func cmdTest(args []string) {
	portToTest := defaultBackendPort
	if len(args) > 0 {
		if p, err := strconv.Atoi(args[0]); err == nil && p > 0 {
			portToTest = p
		}
	} else if isDispatcherRunning() {
		portToTest = 1080
	}

	fmt.Printf("测试 SOCKS5 代理通道健康状态 (127.0.0.1:%d)...\n", portToTest)
	if err := verifySocks5(defaultProxyHost, portToTest, 3*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m 端口 127.0.0.1:%d 代理连接失败: %v\n", portToTest, err)
		os.Exit(1)
	}
	fmt.Println("SOCKS5 本地握手正常，正在获取出口公网 IP 与归属地...")
	ip := fetchExitIP(defaultProxyHost, portToTest, 4*time.Second)
	if ip != "" {
		fmt.Printf("\x1b[32m[aijiasu OK]\x1b[0m 代理通道完全畅通！出口 IP: %s\n", ip)
	} else {
		fmt.Println("\x1b[32m[aijiasu OK]\x1b[0m 代理握手正常，但未从探针获取到出口信息。")
	}
}

// cmdAuth 查看账号加密凭证状态
func cmdAuth(args []string) {
	printAuthStatus()
}

// cmdLogin 交互式输入并硬件加密保存凭据
func cmdLogin(args []string) {
	if err := promptLogin(); err != nil {
		fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m %v\n", err)
		os.Exit(1)
	}
}

// cmdLogout 清理本地硬件加密凭据
func cmdLogout(args []string) {
	if err := clearCredentialsFile(); err != nil {
		fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m 清除本地凭证失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\x1b[32m[aijiasu OK]\x1b[0m 本地加密凭证已清除。")
	fmt.Println("正在断开底层爱加速连接...")
	_, _ = dockerExec("pkill", "-f", connectPattern)
	_, _ = dockerExec("ajiasu", "disconnect")
}

// cmdHelp 显示帮助信息
func cmdHelp(args []string) {
	printHelp()
}
