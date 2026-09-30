package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	portsConfigFileName = "ports.json"
	defaultBackendPort  = 1079 // 爱加速容器映射在宿主机上的内部代理端口
	defaultBasePort     = 1080 // 默认起始端口
)

// 节点与端口一一对应的映射项
type PortItem struct {
	Port     int    `json:"port"`      // 监听端口，如 1080
	NodeID   string `json:"node_id"`   // 节点全局唯一 ID，如 "vvn-26-633"
	NodeName string `json:"node_name"` // 节点名称，如 "北京 #3"
	Province string `json:"province"`  // 所属省份，如 "北京市"
	City     string `json:"city"`      // 所属城市，如 "北京"
	Enabled  bool   `json:"enabled"`   // 是否启用
}

// 端口配置文件结构
type PortConfig struct {
	Version       string     `json:"version"`
	BasePort      int        `json:"base_port"`      // 起始基准端口 (默认 1080)
	BackendPort   int        `json:"backend_port"`   // 爱加速内部 SOCKS5 代理端口 (默认 1079)
	TotalNodes    int        `json:"total_nodes"`    // 已映射节点总数
	AllowFailover bool       `json:"allow_failover"` // 是否允许同城智能故障转移 (默认 false: 严格模式，绝不漂移)
	Mappings      []PortItem `json:"mappings"`
}

// 获取 ports.json 绝对路径
func getPortsConfigPath() string {
	dir := getProjectDir()
	return filepath.Join(dir, portsConfigFileName)
}

// 自然排序比较：使 "北京 #2" 排在 "北京 #10" 前面
func naturalLess(s1, s2 string) bool {
	re := regexp.MustCompile(`(\d+|\D+)`)
	parts1 := re.FindAllString(s1, -1)
	parts2 := re.FindAllString(s2, -1)

	minLen := len(parts1)
	if len(parts2) < minLen {
		minLen = len(parts2)
	}

	for i := 0; i < minLen; i++ {
		p1, p2 := parts1[i], parts2[i]
		if p1 == p2 {
			continue
		}
		isNum1 := len(p1) > 0 && unicode.IsDigit(rune(p1[0]))
		isNum2 := len(p2) > 0 && unicode.IsDigit(rune(p2[0]))

		if isNum1 && isNum2 {
			n1, err1 := strconv.Atoi(p1)
			n2, err2 := strconv.Atoi(p2)
			if err1 == nil && err2 == nil && n1 != n2 {
				return n1 < n2
			}
		}
		return p1 < p2
	}
	return len(parts1) < len(parts2)
}

// 获取省份在全国标准排序中的权重索引
func getProvinceSortOrder(provinceName string) int {
	for idx, p := range provinces {
		if p.Name == provinceName || p.Short == provinceName || strings.HasPrefix(provinceName, p.Short) {
			return idx
		}
	}
	return 999
}

// 为全量节点按 [省份 -> 城市 -> 节点名称自然数] 生成连续稳定的端口映射
func generateAllNodeMappings(nodes []NodeItem, basePort int) []PortItem {
	if basePort <= 0 {
		basePort = defaultBasePort
	}

	// 复制并规范节点
	type SortableNode struct {
		Item          NodeItem
		ProvinceOrder int
		ProvinceName  string
	}

	var sortable []SortableNode
	for _, n := range nodes {
		city := n.City
		if city == "" {
			city = extractCity(n.Name)
		}
		pDef, _ := findProvinceByCity(city)
		sortable = append(sortable, SortableNode{
			Item:          n,
			ProvinceOrder: getProvinceSortOrder(pDef.Name),
			ProvinceName:  pDef.Name,
		})
	}

	// 复合多级排序：省份顺位 -> 城市名 -> 节点名自然序 -> 节点ID
	sort.Slice(sortable, func(i, j int) bool {
		if sortable[i].ProvinceOrder != sortable[j].ProvinceOrder {
			return sortable[i].ProvinceOrder < sortable[j].ProvinceOrder
		}
		if sortable[i].Item.City != sortable[j].Item.City {
			return sortable[i].Item.City < sortable[j].Item.City
		}
		if sortable[i].Item.Name != sortable[j].Item.Name {
			return naturalLess(sortable[i].Item.Name, sortable[j].Item.Name)
		}
		return sortable[i].Item.ID < sortable[j].Item.ID
	})

	var result []PortItem
	currentPort := basePort
	for _, sn := range sortable {
		// 跳过后端占用端口
		if currentPort == defaultBackendPort {
			currentPort++
		}
		city := sn.Item.City
		if city == "" {
			city = extractCity(sn.Item.Name)
		}
		result = append(result, PortItem{
			Port:     currentPort,
			NodeID:   sn.Item.ID,
			NodeName: sn.Item.Name,
			Province: sn.ProvinceName,
			City:     city,
			Enabled:  true,
		})
		currentPort++
	}
	return result
}

// 加载端口配置 (如果未初始化或节点列表更新，则自动增量同步并固化)
func loadPortConfig() (*PortConfig, error) {
	path := getPortsConfigPath()

	var cfg *PortConfig
	if _, err := os.Stat(path); err == nil {
		data, err := os.ReadFile(path)
		if err == nil {
			c := &PortConfig{}
			if err := json.Unmarshal(data, c); err == nil && len(c.Mappings) > 100 {
				cfg = c
			}
		}
	}

	// 如果文件不存在或不是全节点结构，从容器获取全量节点自动初始化
	if cfg == nil {
		nodes, err := fetchNodes()
		if err != nil {
			return nil, fmt.Errorf("获取可用节点列表失败: %w", err)
		}
		if len(nodes) == 0 {
			return nil, fmt.Errorf("容器返回的可用节点列表为空")
		}

		mappings := generateAllNodeMappings(nodes, defaultBasePort)
		cfg = &PortConfig{
			Version:     "2.0",
			BasePort:    defaultBasePort,
			BackendPort: defaultBackendPort,
			TotalNodes:  len(mappings),
			Mappings:    mappings,
		}
		if err := savePortConfig(cfg); err != nil {
			return nil, fmt.Errorf("保存端口映射表失败: %w", err)
		}
		return cfg, nil
	}

	// 检查是否有新节点上线需要追加分配端口
	existingMap := make(map[string]bool)
	maxPort := cfg.BasePort
	for _, m := range cfg.Mappings {
		existingMap[m.NodeID] = true
		if m.Port > maxPort {
			maxPort = m.Port
		}
	}

	// 动态检查新节点 (轻量快速校验)
	nodes, err := fetchNodes()
	if err == nil && len(nodes) > len(cfg.Mappings) {
		added := false
		for _, n := range nodes {
			if !existingMap[n.ID] {
				maxPort++
				if maxPort == cfg.BackendPort {
					maxPort++
				}
				city := n.City
				if city == "" {
					city = extractCity(n.Name)
				}
				pDef, _ := findProvinceByCity(city)
				cfg.Mappings = append(cfg.Mappings, PortItem{
					Port:     maxPort,
					NodeID:   n.ID,
					NodeName: n.Name,
					Province: pDef.Name,
					City:     city,
					Enabled:  true,
				})
				added = true
			}
		}
		if added {
			cfg.TotalNodes = len(cfg.Mappings)
			_ = savePortConfig(cfg)
		}
	}

	return cfg, nil
}

// 保存端口配置到磁盘
func savePortConfig(cfg *PortConfig) error {
	path := getPortsConfigPath()
	cfg.TotalNodes = len(cfg.Mappings)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// 设置是否允许同城智能故障转移 (并同步给当前运行中的调度器)
func setAllowFailover(enable bool) (*PortConfig, error) {
	cfg, err := loadPortConfig()
	if err != nil {
		return nil, err
	}
	cfg.AllowFailover = enable
	if err := savePortConfig(cfg); err != nil {
		return nil, err
	}

	// 动态同步给全局运行中的调度器单例
	dispatcher.mu.Lock()
	if dispatcher.cfg != nil {
		dispatcher.cfg.AllowFailover = enable
	}
	dispatcher.allowFailover = enable
	dispatcher.mu.Unlock()

	return cfg, nil
}

// 手动添加或修改端口映射 (支持自定义指定端口绑定某个节点)
func addOrUpdatePortMapping(port int, target string, desc string) (*PortConfig, error) {
	if port < 1024 || port > 65535 {
		return nil, fmt.Errorf("端口号必须在 1024~65535 之间")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("目标节点名称或 ID 不能为空")
	}

	cfg, err := loadPortConfig()
	if err != nil {
		return nil, err
	}

	if port == cfg.BackendPort {
		return nil, fmt.Errorf("端口 %d 被爱加速后端内部占用，请使用其他端口", port)
	}

	// 在节点池中寻找匹配的节点
	nodes, err := fetchNodes()
	if err != nil {
		return nil, err
	}
	var matched *NodeItem
	tLower := strings.ToLower(target)
	for _, n := range nodes {
		if strings.ToLower(n.ID) == tLower || strings.ToLower(n.Name) == tLower {
			matched = &n
			break
		}
	}
	if matched == nil {
		for _, n := range nodes {
			if strings.Contains(strings.ToLower(n.Name), tLower) {
				matched = &n
				break
			}
		}
	}
	if matched == nil {
		return nil, fmt.Errorf("未找到匹配 \"%s\" 的可用节点", target)
	}

	city := matched.City
	if city == "" {
		city = extractCity(matched.Name)
	}
	pDef, _ := findProvinceByCity(city)

	updated := false
	for i := range cfg.Mappings {
		if cfg.Mappings[i].Port == port {
			cfg.Mappings[i].NodeID = matched.ID
			cfg.Mappings[i].NodeName = matched.Name
			cfg.Mappings[i].Province = pDef.Name
			cfg.Mappings[i].City = city
			cfg.Mappings[i].Enabled = true
			updated = true
			break
		}
	}

	if !updated {
		cfg.Mappings = append(cfg.Mappings, PortItem{
			Port:     port,
			NodeID:   matched.ID,
			NodeName: matched.Name,
			Province: pDef.Name,
			City:     city,
			Enabled:  true,
		})
	}

	sort.Slice(cfg.Mappings, func(i, j int) bool {
		return cfg.Mappings[i].Port < cfg.Mappings[j].Port
	})

	if err := savePortConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// 手动删除端口映射
func deletePortMapping(port int) (*PortConfig, error) {
	cfg, err := loadPortConfig()
	if err != nil {
		return nil, err
	}

	var newMappings []PortItem
	found := false
	for _, m := range cfg.Mappings {
		if m.Port == port {
			found = true
			continue
		}
		newMappings = append(newMappings, m)
	}

	if !found {
		return nil, fmt.Errorf("未找到端口 %d 的映射配置", port)
	}

	cfg.Mappings = newMappings
	if err := savePortConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// 节点变动详情项
type NodeChangeItem struct {
	Port     int
	NodeID   string
	NodeName string
	Province string
	City     string
	Detail   string
}

// 节点同步更新结果报告
type SyncReport struct {
	TotalCloudNodes int
	TotalLocalNodes int
	UnchangedCount  int
	Added           []NodeChangeItem
	Removed         []NodeChangeItem
	Updated         []NodeChangeItem
}

// 与爱加速官方云端实时同步节点列表 (支持增量安全更新与强制全量重排)
func syncNodesWithCloud(forceReorder bool) (*SyncReport, *PortConfig, error) {
	fmt.Println("  ➜ [1/3] 正在从爱加速云端拉取全量最新可用节点列表...")
	cloudNodes, err := fetchNodes()
	if err != nil {
		return nil, nil, fmt.Errorf("拉取云端节点失败: %w", err)
	}
	if len(cloudNodes) == 0 {
		return nil, nil, fmt.Errorf("拉取到的云端节点列表为空，请核对网络或登录状态")
	}

	cfg, err := loadPortConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("加载本地端口配置失败: %w", err)
	}

	report := &SyncReport{
		TotalCloudNodes: len(cloudNodes),
		TotalLocalNodes: len(cfg.Mappings),
	}

	if forceReorder {
		fmt.Println("  ➜ [2/3] 采用【全量重排模式】按全国省份与自然顺序重新连续分配所有端口...")
		newMappings := generateAllNodeMappings(cloudNodes, defaultBasePort)
		cfg.Mappings = newMappings
		cfg.TotalNodes = len(newMappings)
		if err := savePortConfig(cfg); err != nil {
			return nil, nil, fmt.Errorf("保存新端口配置失败: %w", err)
		}
		report.UnchangedCount = len(newMappings)
		return report, cfg, nil
	}

	fmt.Println("  ➜ [2/3] 采用【增量安全更新模式】比对节点变动，严格锁定已有节点端口不变...")

	// 构建云端节点字典索引
	cloudMap := make(map[string]NodeItem)
	for _, n := range cloudNodes {
		cloudMap[n.ID] = n
	}

	// 找出当前已使用的最大端口号，以及已映射的节点 ID
	usedPorts := make(map[int]bool)
	localMap := make(map[string]PortItem)
	maxPort := cfg.BasePort - 1

	for _, m := range cfg.Mappings {
		usedPorts[m.Port] = true
		localMap[m.NodeID] = m
		if m.Port > maxPort {
			maxPort = m.Port
		}
	}

	// 1. 检查本地节点是否有下线或更名
	var validMappings []PortItem
	for _, m := range cfg.Mappings {
		cloudNode, exists := cloudMap[m.NodeID]
		if !exists {
			// 官方已下线该节点
			report.Removed = append(report.Removed, NodeChangeItem{
				Port:     m.Port,
				NodeID:   m.NodeID,
				NodeName: m.NodeName,
				Province: m.Province,
				City:     m.City,
			})
			continue
		}

		// 检查是否有更名
		if cloudNode.Name != m.NodeName {
			report.Updated = append(report.Updated, NodeChangeItem{
				Port:     m.Port,
				NodeID:   m.NodeID,
				NodeName: cloudNode.Name,
				Province: m.Province,
				City:     m.City,
				Detail:   fmt.Sprintf("原名称: %s ➜ 新名称: %s", m.NodeName, cloudNode.Name),
			})
			m.NodeName = cloudNode.Name
		} else {
			report.UnchangedCount++
		}
		validMappings = append(validMappings, m)
	}

	// 2. 检查是否有新增节点
	nextPort := maxPort + 1
	for _, n := range cloudNodes {
		if _, exists := localMap[n.ID]; !exists {
			// 找到下一个空闲端口
			for usedPorts[nextPort] || nextPort == cfg.BackendPort {
				nextPort++
			}
			usedPorts[nextPort] = true

			city := n.City
			if city == "" {
				city = extractCity(n.Name)
			}
			pDef, _ := findProvinceByCity(city)

			newItem := PortItem{
				Port:     nextPort,
				NodeID:   n.ID,
				NodeName: n.Name,
				Province: pDef.Name,
				City:     city,
				Enabled:  true,
			}
			validMappings = append(validMappings, newItem)
			report.Added = append(report.Added, NodeChangeItem{
				Port:     nextPort,
				NodeID:   n.ID,
				NodeName: n.Name,
				Province: pDef.Name,
				City:     city,
			})
			nextPort++
		}
	}

	// 按端口排序
	sort.Slice(validMappings, func(i, j int) bool {
		return validMappings[i].Port < validMappings[j].Port
	})

	cfg.Mappings = validMappings
	cfg.TotalNodes = len(validMappings)

	// 3. 写入磁盘并联动刷新
	if len(report.Added) > 0 || len(report.Removed) > 0 || len(report.Updated) > 0 {
		fmt.Println("  ➜ [3/3] 检测到节点变动，正在固化最新端口配置到 ports.json...")
		if err := savePortConfig(cfg); err != nil {
			return nil, nil, fmt.Errorf("保存更新后的端口配置失败: %w", err)
		}
	} else {
		fmt.Println("  ➜ [3/3] 本地映射与云端完全一致，无需改动配置。")
	}

	return report, cfg, nil
}

// 格式化输出节点同步结果报告
func printSyncReport(report *SyncReport, cfg *PortConfig) {
	fmt.Println("\n==========================================================================================================")
	fmt.Println("                       爱加速官方节点云端同步与端口映射更新报告")
	fmt.Println("==========================================================================================================")
	fmt.Printf("• 爱加速云端可用节点: %d 个\n", report.TotalCloudNodes)
	fmt.Printf("• 本地映射配置节点: %d 个 (覆盖端口: 127.0.0.1:%d ~ %d)\n",
		cfg.TotalNodes, cfg.BasePort, cfg.BasePort+cfg.TotalNodes-1)
	fmt.Println("----------------------------------------------------------------------------------------------------------")
	fmt.Println(" 变更汇总:")
	fmt.Printf("   🟢 新增节点: \x1b[1;32m%d\x1b[0m 个\n", len(report.Added))
	fmt.Printf("   🔴 下线节点: \x1b[1;31m%d\x1b[0m 个\n", len(report.Removed))
	fmt.Printf("   🟡 更名更新: \x1b[1;33m%d\x1b[0m 个\n", len(report.Updated))
	fmt.Printf("   ⚪ 稳定保持: \x1b[32m%d\x1b[0m 个 (已有端口映射 100%% 锁定不变)\n", report.UnchangedCount)

	if len(report.Added) > 0 {
		fmt.Println("\n 🟢 新增节点及分配端口明细:")
		for idx, a := range report.Added {
			if idx >= 15 {
				fmt.Printf("      ... 其余 %d 个新增节点请运行 'aijiasu ports' 查阅\n", len(report.Added)-15)
				break
			}
			fmt.Printf("      + 端口 127.0.0.1:%-5d ➜ %-16s (%s, %s/%s)\n",
				a.Port, a.NodeName, a.NodeID, a.Province, a.City)
		}
	}

	if len(report.Removed) > 0 {
		fmt.Println("\n 🔴 官方已下线节点 (已从端口表中移除):")
		for idx, r := range report.Removed {
			if idx >= 10 {
				fmt.Printf("      ... 其余 %d 个下线节点\n", len(report.Removed)-10)
				break
			}
			fmt.Printf("      - 原端口 127.0.0.1:%-5d ➜ %-16s (%s, %s/%s)\n",
				r.Port, r.NodeName, r.NodeID, r.Province, r.City)
		}
	}

	if len(report.Updated) > 0 {
		fmt.Println("\n 🟡 节点名称更新明细:")
		for _, u := range report.Updated {
			fmt.Printf("      ~ 端口 127.0.0.1:%-5d ➜ %s (%s)\n", u.Port, u.Detail, u.NodeID)
		}
	}

	hasChanges := len(report.Added) > 0 || len(report.Removed) > 0 || len(report.Updated) > 0

	fmt.Println("\n----------------------------------------------------------------------------------------------------------")
	if hasChanges {
		fmt.Printf("\x1b[32m[aijiasu OK]\x1b[0m 节点同步完成！最新配置已保存至 ports.json (当前共 %d 个专属端口)。\n", cfg.TotalNodes)
		if isDispatcherRunning() {
			fmt.Println("  ➜ 检测到后台分发服务正在运行，正在自动热重载以应用最新端口监听...")
			_ = stopDispatcherServer()
			_ = startDispatcherDaemon()
		} else {
			fmt.Println("💡 提示: 可运行 'aijiasu serve -d' 启动分发服务。")
		}
	} else {
		fmt.Println("\x1b[32m[aijiasu OK]\x1b[0m 当前本地所有节点映射与爱加速云端 100% 同步，无需任何变更！")
	}
	fmt.Println("  • 查看最新平铺映射表: aijiasu ports")
	fmt.Println("  • 查看省份二级树状图: aijiasu provinces")
	fmt.Println("  • 导出指纹浏览器 CSV: aijiasu ports --csv > proxies.csv")
}

// 获取当前爱加速容器正在连接的实际节点信息 (返回: 节点ID, 节点名称)
func getCurrentConnectedNodeInfo() (string, string) {
	out, err := dockerExec("ps", "-eo", "args")
	if err != nil {
		return "", ""
	}
	re := regexp.MustCompile(`ajiasu connect\s+([^\s]+)`)
	m := re.FindStringSubmatch(out)
	if len(m) > 1 {
		nodeID := m[1]
		listOut, err := dockerExec("ajiasu", "list")
		if err == nil {
			lines := strings.Split(listOut, "\n")
			for _, l := range lines {
				parts := strings.Fields(strings.TrimSpace(l))
				if len(parts) >= 3 && parts[0] == nodeID {
					return nodeID, strings.Join(parts[2:], " ")
				}
			}
		}
		return nodeID, nodeID
	}
	return "", ""
}

// 平铺表格视图展示（支持按省份/城市/端口/名称筛选与分页查看）
func printFlatPortTable(cfg *PortConfig, activeNodeID string, keyword string, showAll bool) {
	kw := strings.ToLower(strings.TrimSpace(keyword))

	var filtered []PortItem
	for _, item := range cfg.Mappings {
		if kw == "" {
			filtered = append(filtered, item)
		} else {
			portStr := strconv.Itoa(item.Port)
			if strings.Contains(portStr, kw) ||
				strings.Contains(strings.ToLower(item.NodeName), kw) ||
				strings.Contains(strings.ToLower(item.NodeID), kw) ||
				strings.Contains(strings.ToLower(item.City), kw) ||
				strings.Contains(strings.ToLower(item.Province), kw) {
				filtered = append(filtered, item)
			}
		}
	}

	fmt.Println("\n==========================================================================================================")
	fmt.Printf("                   指纹浏览器每个节点独立端口映射表 (平铺视图 · 全量 %d 个节点)\n", cfg.TotalNodes)
	fmt.Println("==========================================================================================================")
	_, activeNodeName := getCurrentConnectedNodeInfo()
	if activeNodeName != "" {
		fmt.Printf("当前底层已连节点: \x1b[32m%s\x1b[0m (ID: %s)\n", activeNodeName, activeNodeID)
	} else {
		fmt.Println("当前底层已连节点: \x1b[33m未连接 (在指纹浏览器发起访问时自动秒级连接)\x1b[0m")
	}
	fmt.Printf("独立端口映射范围: 127.0.0.1:%d ~ 127.0.0.1:%d (共 %d 个端口，每节点专属)\n",
		cfg.BasePort, cfg.BasePort+cfg.TotalNodes-1, cfg.TotalNodes)
	if kw != "" {
		fmt.Printf("筛选关键字: \"%s\" (匹配到 %d 个节点)\n", keyword, len(filtered))
	}
	fmt.Println("----------------------------------------------------------------------------------------------------------")
	fmt.Printf("%-8s | %-16s | %-16s | %-10s | %-8s | %s\n", "监听端口", "节点名称", "节点 ID", "所属省份", "所属城市", "当前状态")
	fmt.Println("----------------------------------------------------------------------------------------------------------")

	maxDisplay := 50
	if showAll || kw != "" || len(filtered) <= maxDisplay {
		maxDisplay = len(filtered)
	}

	for i := 0; i < maxDisplay; i++ {
		item := filtered[i]
		isActive := (activeNodeID != "" && item.NodeID == activeNodeID)
		statusStr := "\x1b[90m⚪ 待命 (请求即切)\x1b[0m"
		if isActive {
			statusStr = "\x1b[32m🟢 活跃中 (当前直通)\x1b[0m"
		}

		fmt.Printf("127.0.0.1:%-5d | %-16s | %-16s | %-10s | %-8s | %s\n",
			item.Port,
			item.NodeName,
			item.NodeID,
			item.Province,
			item.City,
			statusStr,
		)
	}
	fmt.Println("----------------------------------------------------------------------------------------------------------")

	if len(filtered) > maxDisplay {
		fmt.Printf("💡 已显示前 %d 条（共 %d 个匹配节点）。\n", maxDisplay, len(filtered))
		fmt.Println("  • 筛选特定城市/省份: aijiasu ports 北京  (或 aijiasu ports 上海 / aijiasu ports 广东)")
		fmt.Println("  • 筛选特定端口:     aijiasu ports 1085")
		fmt.Println("  • 展开全部平铺列表: aijiasu ports --all")
	} else {
		fmt.Printf("共展示 %d 个节点端口映射。\n", len(filtered))
	}
	fmt.Println("  • 省份二级分类视图: aijiasu ports -g (或 aijiasu provinces)")
	fmt.Println("  • 导出为 CSV 表格: aijiasu ports --csv > nodes_ports.csv")
}

// 导出 CSV 格式数据（方便直接批量导入 AdsPower/Hubstudio 等指纹浏览器）
func exportPortsCSV(cfg *PortConfig) {
	fmt.Println("端口,省份,城市,节点名称,节点ID,代理类型,代理主机,完整代理地址")
	for _, item := range cfg.Mappings {
		fmt.Printf("%d,%s,%s,%s,%s,socks5,127.0.0.1,socks5://127.0.0.1:%d\n",
			item.Port, item.Province, item.City, item.NodeName, item.NodeID, item.Port)
	}
}

// 省份二级分类树状视图展示
func printGroupedProvincePortTree(cfg *PortConfig, activeNodeID string, targetProvince string) {
	targetProvince = strings.TrimSpace(targetProvince)

	fmt.Println("\n==========================================================================================================")
	fmt.Printf("              全国各省份可用节点分布与指纹浏览器端口映射 (省份二级分类视图 · 全量 %d 节点)\n", cfg.TotalNodes)
	fmt.Println("==========================================================================================================")
	_, activeNodeName := getCurrentConnectedNodeInfo()
	if activeNodeName != "" {
		fmt.Printf("当前底层已连节点: \x1b[32m%s\x1b[0m (ID: %s)\n", activeNodeName, activeNodeID)
	}
	fmt.Printf("映射端口段总览: 127.0.0.1:%d ~ 127.0.0.1:%d (每个节点分配一个唯一固定端口)\n",
		cfg.BasePort, cfg.BasePort+cfg.TotalNodes-1)
	if targetProvince != "" {
		fmt.Printf("指定查看省份: 【%s】\n", targetProvince)
	}
	fmt.Println("----------------------------------------------------------------------------------------------------------")

	// 聚合省份 -> 城市 -> 节点映射项
	type CityGroup struct {
		City  string
		Items []PortItem
	}
	type ProvinceGroup struct {
		Name       string
		TotalNodes int
		StartPort  int
		EndPort    int
		CityMap    map[string][]PortItem
		CityList   []string
	}

	provOrder := make(map[string]int)
	for i, p := range provinces {
		provOrder[p.Name] = i
	}

	groupsMap := make(map[string]*ProvinceGroup)
	for _, item := range cfg.Mappings {
		pName := item.Province
		if pName == "" {
			pName = "其他地区"
		}
		g, ok := groupsMap[pName]
		if !ok {
			g = &ProvinceGroup{
				Name:      pName,
				StartPort: item.Port,
				EndPort:   item.Port,
				CityMap:   make(map[string][]PortItem),
			}
			groupsMap[pName] = g
		}
		g.TotalNodes++
		if item.Port < g.StartPort {
			g.StartPort = item.Port
		}
		if item.Port > g.EndPort {
			g.EndPort = item.Port
		}
		if len(g.CityMap[item.City]) == 0 {
			g.CityList = append(g.CityList, item.City)
		}
		g.CityMap[item.City] = append(g.CityMap[item.City], item)
	}

	// 保持省份标准排序
	var sortedGroups []*ProvinceGroup
	for _, p := range provinces {
		if g, ok := groupsMap[p.Name]; ok {
			sortedGroups = append(sortedGroups, g)
		}
	}
	if g, ok := groupsMap["其他地区"]; ok {
		sortedGroups = append(sortedGroups, g)
	}

	for _, g := range sortedGroups {
		if targetProvince != "" && !strings.Contains(g.Name, targetProvince) {
			continue
		}

		fmt.Printf("\n【%s】(共 \x1b[1;33m%d\x1b[0m 个节点，独立端口段: \x1b[1;36m127.0.0.1:%d ~ %d\x1b[0m):\n",
			g.Name, g.TotalNodes, g.StartPort, g.EndPort)

		// 遍历省份下的城市
		for _, city := range g.CityList {
			items := g.CityMap[city]
			cStartPort := items[0].Port
			cEndPort := items[len(items)-1].Port

			fmt.Printf("  📍 %-8s (共 %d 个节点，端口: %d ~ %d):\n", city, len(items), cStartPort, cEndPort)

			// 节点展示控制：如果未指定特定省份且节点很多，展示代表性节点
			maxNodesToShow := 8
			if targetProvince != "" || len(items) <= 12 {
				maxNodesToShow = len(items)
			}

			for i := 0; i < maxNodesToShow; i++ {
				it := items[i]
				isLast := (i == maxNodesToShow-1) && (maxNodesToShow == len(items))
				branch := "     ├── "
				if isLast {
					branch = "     └── "
				}
				activeTag := ""
				if activeNodeID != "" && it.NodeID == activeNodeID {
					activeTag = " \x1b[32m[🟢 当前活跃连接]\x1b[0m"
				}
				fmt.Printf("%s127.0.0.1:%-5d ➜ %-16s (%s)%s\n",
					branch, it.Port, it.NodeName, it.NodeID, activeTag)
			}

			if len(items) > maxNodesToShow {
				fmt.Printf("     └── ... 其余 %d 个节点端口依次顺延 (运行 'aijiasu ports %s' 查看本市全部节点)\n",
					len(items)-maxNodesToShow, city)
			}
		}
	}

	fmt.Println("\n----------------------------------------------------------------------------------------------------------")
	fmt.Println("💡 快速使用指南:")
	fmt.Println("  • 针对单个省份展开全部节点: aijiasu provinces 广东  (或 aijiasu ports -g 江苏)")
	fmt.Println("  • 平铺查看指定城市全部端口: aijiasu ports 广州")
	fmt.Println("  • 启动按需分发服务:         aijiasu serve -d")
	fmt.Println("  • 指纹浏览器代理配置:       协议 socks5, 主机 127.0.0.1, 端口填入目标节点的对应端口即可")
}
