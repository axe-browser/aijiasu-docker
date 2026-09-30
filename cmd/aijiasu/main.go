package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	fallbackContainer  = "aijiasu-runner"
	fallbackProxyHost  = "127.0.0.1"
	fallbackProxyPort  = 1080
	fallbackHTTPPort   = 1081
	historyFileName    = ".aijiasu_history.json"
	internalBridgePort = 1080 // 爱加速容器内部固定 socat 桥接端口
)

// NodeItem 代理节点信息 (包含 id、省份、市区、编号等)
type NodeItem struct {
	ID       string `json:"id"`
	Province string `json:"province"`
	City     string `json:"city"`
	Number   string `json:"number"`
	Name     string `json:"name"`
	Status   string `json:"status"`
}

// SwitchResult 节点切换结果
type SwitchResult struct {
	Node         NodeItem `json:"node"`
	ProxyAddr    string   `json:"proxy_addr"`
	ExitIP       string   `json:"exit_ip"`
	Remaining    int      `json:"remaining"`
	TotalInPool  int      `json:"total_in_pool"`
	RoundReset   bool     `json:"round_reset"`
}

// DoctorReport 服务诊断报告
type DoctorReport struct {
	DockerInstalled bool   `json:"docker_installed"`
	DaemonRunning   bool   `json:"daemon_running"`
	ContainerRun    bool   `json:"container_running"`
	LoggedIn        bool   `json:"logged_in"`
	CurrentStatus   string `json:"current_status"`
	ProxyHealthy    bool   `json:"proxy_healthy"`
	ExitIP          string `json:"exit_ip"`
	APIRunning      bool   `json:"api_running"`
	APIPort         int    `json:"api_port,omitempty"`
	Error           string `json:"error,omitempty"`
}

// DedupHistory 去重历史结构体
type DedupHistory struct {
	UsedNodeIDs []string `json:"used_node_ids"`
	LastNodeID  string   `json:"last_node_id"`
	UpdatedAt   string   `json:"updated_at"`
}

var (
	historyMutex sync.Mutex
	rng          = rand.New(rand.NewSource(time.Now().UnixNano()))
)

// 全国省份及直辖市定义
var municipalities = []string{"北京", "上海", "天津", "重庆"}

var provinceList = []string{
	"广东", "江苏", "浙江", "山东", "河南", "四川", "湖北", "湖南",
	"河北", "福建", "安徽", "辽宁", "江西", "陕西", "山西", "黑龙江",
	"吉林", "云南", "贵州", "广西", "内蒙古", "新疆", "甘肃", "海南",
	"宁夏", "青海", "西藏",
}

// 常见城市到省份的反查映射字典 (覆盖全国主要地级市)
var cityToProvinceMap = map[string]string{
	// 广东
	"广州": "广东", "深圳": "广东", "珠海": "广东", "汕头": "广东", "佛山": "广东",
	"韶关": "广东", "湛江": "广东", "肇庆": "广东", "江门": "广东", "茂名": "广东",
	"惠州": "广东", "梅州": "广东", "汕尾": "广东", "河源": "广东", "阳江": "广东",
	"清远": "广东", "东莞": "广东", "中山": "广东", "潮州": "广东", "揭阳": "广东", "云浮": "广东",

	// 江苏
	"南京": "江苏", "无锡": "江苏", "徐州": "江苏", "常州": "江苏", "苏州": "江苏",
	"南通": "江苏", "连云港": "江苏", "淮安": "江苏", "盐城": "江苏", "扬州": "江苏",
	"镇江": "江苏", "泰州": "江苏", "宿迁": "江苏",

	// 浙江
	"杭州": "浙江", "宁波": "浙江", "温州": "浙江", "嘉兴": "浙江", "湖州": "浙江",
	"绍兴": "浙江", "金华": "浙江", "衢州": "浙江", "舟山": "浙江", "台州": "浙江", "丽水": "浙江",

	// 山东
	"济南": "山东", "青岛": "山东", "淄博": "山东", "枣庄": "山东", "东营": "山东",
	"烟台": "山东", "潍坊": "山东", "济宁": "山东", "泰安": "山东", "威海": "山东",
	"日照": "山东", "临沂": "山东", "德州": "山东", "聊城": "山东", "滨州": "山东", "菏泽": "山东",

	// 河南
	"郑州": "河南", "开封": "河南", "洛阳": "河南", "平顶山": "河南", "安阳": "河南",
	"鹤壁": "河南", "新乡": "河南", "焦作": "河南", "濮阳": "河南", "许昌": "河南",
	"漯河": "河南", "三门峡": "河南", "南阳": "河南", "商丘": "河南", "信阳": "河南",
	"周口": "河南", "驻马店": "河南",

	// 四川
	"成都": "四川", "自贡": "四川", "攀枝花": "四川", "泸州": "四川", "德阳": "四川",
	"绵阳": "四川", "广元": "四川", "遂宁": "四川", "内江": "四川", "乐山": "四川",
	"南充": "四川", "眉山": "四川", "宜宾": "四川", "广安": "四川", "达州": "四川",
	"雅安": "四川", "巴中": "四川", "资阳": "四川",

	// 湖北
	"武汉": "湖北", "黄石": "湖北", "十堰": "湖北", "宜昌": "湖北", "襄阳": "湖北",
	"鄂州": "湖北", "荆门": "湖北", "孝感": "湖北", "荆州": "湖北", "黄冈": "湖北",
	"咸宁": "湖北", "随州": "湖北", "恩施": "湖北",

	// 湖南
	"长沙": "湖南", "株洲": "湖南", "湘潭": "湖南", "衡阳": "湖南", "邵阳": "湖南",
	"岳阳": "湖南", "常德": "湖南", "张家界": "湖南", "益阳": "湖南", "郴州": "湖南",
	"永州": "湖南", "怀化": "湖南", "娄底": "湖南", "湘西": "湖南",

	// 河北
	"石家庄": "河北", "唐山": "河北", "秦皇岛": "河北", "邯郸": "河北", "邢台": "河北",
	"保定": "河北", "张家口": "河北", "承德": "河北", "沧州": "河北", "廊坊": "河北", "衡水": "河北",

	// 福建
	"福州": "福建", "厦门": "福建", "莆田": "福建", "三明": "福建", "泉州": "福建",
	"漳州": "福建", "南平": "福建", "龙岩": "福建", "宁德": "福建",

	// 安徽
	"合肥": "安徽", "芜湖": "安徽", "蚌埠": "安徽", "淮南": "安徽", "马鞍山": "安徽",
	"淮北": "安徽", "铜陵": "安徽", "安庆": "安徽", "黄山": "安徽", "滁州": "安徽",
	"阜阳": "安徽", "宿州": "安徽", "六安": "安徽", "亳州": "安徽", "池州": "安徽", "宣城": "安徽",

	// 辽宁
	"沈阳": "辽宁", "大连": "辽宁", "鞍山": "辽宁", "抚顺": "辽宁", "本溪": "辽宁",
	"丹东": "辽宁", "锦州": "辽宁", "营口": "辽宁", "阜新": "辽宁", "辽阳": "辽宁",
	"盘锦": "辽宁", "铁岭": "辽宁", "朝阳": "辽宁", "葫芦岛": "辽宁",

	// 江西
	"南昌": "江西", "景德镇": "江西", "萍乡": "江西", "九江": "江西", "新余": "江西",
	"鹰潭": "江西", "赣州": "江西", "吉安": "江西", "宜春": "江西", "抚州": "江西", "上饶": "江西",

	// 陕西
	"西安": "陕西", "铜川": "陕西", "宝鸡": "陕西", "咸阳": "陕西", "渭南": "陕西",
	"延安": "陕西", "汉中": "陕西", "榆林": "陕西", "安康": "陕西", "商洛": "陕西",

	// 山西
	"太原": "山西", "大同": "山西", "阳泉": "山西", "长治": "山西", "晋城": "山西",
	"朔州": "山西", "晋中": "山西", "运城": "山西", "忻州": "山西", "临汾": "山西", "吕梁": "山西",

	// 黑龙江
	"哈尔滨": "黑龙江", "齐齐哈尔": "黑龙江", "鸡西": "黑龙江", "鹤岗": "黑龙江", "双鸭山": "黑龙江",
	"大庆": "黑龙江", "伊春": "黑龙江", "佳木斯": "黑龙江", "七台河": "黑龙江", "牡丹江": "黑龙江",
	"黑河": "黑龙江", "绥化": "黑龙江",

	// 吉林
	"长春": "吉林", "吉林市": "吉林", "四平": "吉林", "辽源": "吉林", "通化": "吉林",
	"白山": "吉林", "松原": "吉林", "白城": "吉林", "延边": "吉林",

	// 云南
	"昆明": "云南", "曲靖": "云南", "玉溪": "云南", "保山": "云南", "昭通": "云南",
	"丽江": "云南", "普洱": "云南", "临沧": "云南", "大理": "云南", "楚雄": "云南", "红河": "云南",

	// 贵州
	"贵阳": "贵州", "六盘水": "贵州", "遵义": "贵州", "安顺": "贵州", "毕节": "贵州",
	"铜仁": "贵州", "黔西南": "贵州", "黔东南": "贵州", "黔南": "贵州",

	// 广西
	"南宁": "广西", "柳州": "广西", "桂林": "广西", "梧州": "广西", "北海": "广西",
	"防城港": "广西", "钦州": "广西", "贵港": "广西", "玉林": "广西", "百色": "广西",
	"贺州": "广西", "河池": "广西", "来宾": "广西", "崇左": "广西",

	// 内蒙古
	"呼和浩特": "内蒙古", "包头": "内蒙古", "乌海": "内蒙古", "赤峰": "内蒙古", "通辽": "内蒙古",
	"鄂尔多斯": "内蒙古", "呼伦贝尔": "内蒙古", "巴彦淖尔": "内蒙古", "乌兰察布": "内蒙古",

	// 新疆
	"乌鲁木齐": "新疆", "克拉玛依": "新疆", "吐鲁番": "新疆", "哈密": "新疆",

	// 甘肃
	"兰州": "甘肃", "嘉峪关": "甘肃", "金昌": "甘肃", "白银": "甘肃", "天水": "甘肃",
	"武威": "甘肃", "张掖": "甘肃", "平凉": "甘肃", "酒泉": "甘肃", "庆阳": "甘肃",
	"定西": "甘肃", "陇南": "甘肃",

	// 海南
	"海口": "海南", "三亚": "海南", "三沙": "海南", "儋州": "海南",

	// 宁夏
	"银川": "宁夏", "石嘴山": "宁夏", "吴忠": "宁夏", "固原": "宁夏", "中卫": "宁夏",

	// 青海
	"西宁": "青海", "海东": "青海",

	// 西藏
	"拉萨": "西藏", "日喀则": "西藏", "昌都": "西藏", "林芝": "西藏", "山南": "西藏", "那曲": "西藏",
}

var cityRegexp = regexp.MustCompile(`^([\p{Han}A-Za-z]+)`)

// 解析节点省份与城市
func parseNodeLocation(name string) (province, city string) {
	name = strings.TrimSpace(name)

	// 1. 直辖市检查 (北京/上海/天津/重庆)
	for _, m := range municipalities {
		if strings.Contains(name, m) {
			return m, m
		}
	}

	// 2. 检查显式省份名
	for _, p := range provinceList {
		if strings.Contains(name, p) {
			province = p
			break
		}
	}

	// 3. 检查地级市名
	for c, p := range cityToProvinceMap {
		if strings.Contains(name, c) {
			city = c
			if province == "" {
				province = p
			}
			return province, city
		}
	}

	// 4. 正则备选抽取
	if city == "" {
		matches := cityRegexp.FindStringSubmatch(name)
		if len(matches) > 1 {
			city = matches[1]
			if province == "" {
				if p, ok := cityToProvinceMap[city]; ok {
					province = p
				} else {
					province = city
				}
			}
		} else {
			city = "其他"
			if province == "" {
				province = "其他"
			}
		}
	}

	return province, city
}

var numberRegexps = []*regexp.Regexp{
	regexp.MustCompile(`[#＃](\d+)`),
	regexp.MustCompile(`(\d+)$`),
}

// 提取节点编号 (如: #116 -> "116", #01 -> "01")
func extractNodeNumber(name string) string {
	name = strings.TrimSpace(name)
	for _, re := range numberRegexps {
		m := re.FindStringSubmatch(name)
		if len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

// 获取项目根目录
func getProjectDir() string {
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err == nil {
			return dir
		}
	}
	cwd, err := os.Getwd()
	if err == nil {
		if _, err := os.Stat(filepath.Join(cwd, "docker-compose.yml")); err == nil {
			return cwd
		}
	}
	return "."
}

// 获取去重历史文件路径
func getHistoryFilePath() string {
	return filepath.Join(getProjectDir(), historyFileName)
}

// 读取去重历史
func loadHistory() DedupHistory {
	historyMutex.Lock()
	defer historyMutex.Unlock()

	var hist DedupHistory
	data, err := os.ReadFile(getHistoryFilePath())
	if err != nil {
		return hist
	}
	_ = json.Unmarshal(data, &hist)
	return hist
}

// 保存去重历史
func saveHistory(hist DedupHistory) {
	historyMutex.Lock()
	defer historyMutex.Unlock()

	hist.UpdatedAt = time.Now().Format(time.RFC3339)
	data, err := json.MarshalIndent(hist, "", "  ")
	if err == nil {
		_ = os.WriteFile(getHistoryFilePath(), data, 0644)
	}
}

// 清空去重历史
func resetHistory() {
	historyMutex.Lock()
	defer historyMutex.Unlock()

	_ = os.Remove(getHistoryFilePath())
}

// 执行宿主机命令
func runCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// 执行容器内命令
func dockerExec(args ...string) (string, error) {
	cmdArgs := append([]string{"exec", getContainerName()}, args...)
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
	// 协商 (VER=5, NMETHODS=1, METHOD=0: NO AUTH)
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if buf[0] != 0x05 || buf[1] != 0x00 {
		return fmt.Errorf("SOCKS5 握手非 0x00 响应: %v", buf)
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
			// 1. SOCKS5 握手协商
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

	// 备用回退: 系统 curl
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

var ipv4Regexp = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

// 提取纯净 IP 地址
func extractPureIP(raw string) string {
	m := ipv4Regexp.FindString(raw)
	if m != "" {
		return m
	}
	return strings.TrimSpace(raw)
}

// 读取 .env 文件为键值对字典
func loadEnvMap() map[string]string {
	res := make(map[string]string)
	envPath := filepath.Join(getProjectDir(), ".env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		return res
	}
	lines := strings.Split(string(data), "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "#") || !strings.Contains(l, "=") {
			continue
		}
		parts := strings.SplitN(l, "=", 2)
		k := strings.TrimSpace(parts[0])
		v := strings.TrimSpace(parts[1])
		v = strings.Trim(v, `"'`)
		res[k] = v
	}
	return res
}

// 获取配置项 (优先级: 进程环境变量 > .env 文件 > 默认后备值)
func getEnvConfig(key, defVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	envMap := loadEnvMap()
	if val, ok := envMap[key]; ok && val != "" {
		return val
	}
	return defVal
}

// 获取宿主机映射的 SOCKS5 代理端口 (默认 1080)
func getProxyPort() int {
	str := getEnvConfig("AIJIASU_PROXY_PORT", strconv.Itoa(fallbackProxyPort))
	if p, err := strconv.Atoi(str); err == nil && p > 0 {
		return p
	}
	return fallbackProxyPort
}

// 获取宿主机连接 SOCKS5 的 Host (默认 127.0.0.1)
func getProxyHost() string {
	host := getEnvConfig("AIJIASU_BIND_IP", "")
	if host == "" {
		host = getEnvConfig("AIJIASU_PROXY_HOST", fallbackProxyHost)
	}
	if host == "0.0.0.0" || host == "" {
		return "127.0.0.1"
	}
	return host
}

// 获取 Docker 容器名称 (默认 aijiasu-runner)
func getContainerName() string {
	return getEnvConfig("AIJIASU_CONTAINER_NAME", fallbackContainer)
}

// 获取 Web JSON API 服务端口 (默认 1081)
func getHTTPPort() int {
	str := getEnvConfig("AIJIASU_HTTP_PORT", "")
	if str == "" {
		str = getEnvConfig("AIJIASU_WEB_PORT", strconv.Itoa(fallbackHTTPPort))
	}
	if p, err := strconv.Atoi(str); err == nil && p > 0 {
		return p
	}
	return fallbackHTTPPort
}

// 读取宿主机 .env 文件或环境变量中的账号密码
func readEnvCredentials() (string, string) {
	u := getEnvConfig("AIJIASU_USER", "")
	p := getEnvConfig("AIJIASU_PASS", "")
	return u, p
}

// 同步更新宿主机 .env 文件，保证下次容器重启凭据有效
func updateEnvFile(user, pass string) error {
	envPath := filepath.Join(getProjectDir(), ".env")
	data, err := os.ReadFile(envPath)
	var lines []string
	if err == nil {
		lines = strings.Split(string(data), "\n")
	}

	foundUser := false
	foundPass := false
	var newLines []string

	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "AIJIASU_USER=") {
			newLines = append(newLines, fmt.Sprintf("AIJIASU_USER=%s", user))
			foundUser = true
		} else if strings.HasPrefix(trimmed, "AIJIASU_PASS=") {
			newLines = append(newLines, fmt.Sprintf("AIJIASU_PASS=%s", pass))
			foundPass = true
		} else {
			newLines = append(newLines, l)
		}
	}

	if !foundUser {
		newLines = append(newLines, fmt.Sprintf("AIJIASU_USER=%s", user))
	}
	if !foundPass {
		newLines = append(newLines, fmt.Sprintf("AIJIASU_PASS=%s", pass))
	}

	return os.WriteFile(envPath, []byte(strings.Join(newLines, "\n")), 0600)
}

// LoginAccount 执行爱加速账号登录
func LoginAccount(user, pass string) error {
	if err := ensureContainerRunning(); err != nil {
		return err
	}
	user = strings.TrimSpace(user)
	pass = strings.TrimSpace(pass)
	if user == "" || pass == "" {
		return fmt.Errorf("用户名和密码不能为空")
	}

	// 1. 安全将凭据写入容器内 /etc/ajiasu.conf (避免密码暴露在命令行参数中)
	confContent := fmt.Sprintf("user %s\npass %s\nprotocol proxy\n", user, pass)
	cmd := exec.Command("docker", "exec", "-i", getContainerName(), "bash", "-c", "cat > /etc/ajiasu.conf && chmod 600 /etc/ajiasu.conf")
	cmd.Stdin = strings.NewReader(confContent)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("写入容器配置文件失败: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	// 2. 调用 ajiasu login
	out, err := dockerExec("ajiasu", "login")
	if err != nil && !strings.Contains(out, "Login Result:") {
		return fmt.Errorf("执行登录命令失败: %w (%s)", err, strings.TrimSpace(out))
	}

	// 3. 提取结果与错误消息
	// 典型输出示例:
	// Login Result: WrongPassword
	// Login Message: 错误的用户名或密码。
	lines := strings.Split(out, "\n")
	var loginResult, loginMessage string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Login Result:") {
			loginResult = strings.TrimSpace(strings.TrimPrefix(trimmed, "Login Result:"))
		} else if strings.HasPrefix(trimmed, "Login Message:") {
			loginMessage = strings.TrimSpace(strings.TrimPrefix(trimmed, "Login Message:"))
		}
	}

	if loginResult != "" && !strings.EqualFold(loginResult, "success") && !strings.EqualFold(loginResult, "ok") {
		if loginMessage != "" {
			return fmt.Errorf("%s", loginMessage)
		}
		return fmt.Errorf("%s", loginResult)
	}

	if strings.Contains(out, "Can not open config file") {
		return fmt.Errorf("无法读取配置文件")
	}

	// 4. 同步更新宿主机 .env 文件
	_ = updateEnvFile(user, pass)

	return nil
}

// 确保 Docker 容器处于运行就绪状态
func ensureContainerRunning() error {
	container := getContainerName()
	out, err := runCmd("docker", "inspect", "-f", "{{.State.Running}}", container)
	if err == nil && strings.TrimSpace(out) == "true" {
		return nil
	}

	// 启动前确保环境与挂载资产已就绪 (若缺失则交互询问用户是否生成)
	if err := promptAndBootstrapAssets(); err != nil {
		return fmt.Errorf("初始化 Docker 环境资产失败: %w", err)
	}

	fmt.Printf("  ➜ [Docker 服务] 检测到爱加速容器 (%s) 未运行，正在启动...\n", container)
	dir := getProjectDir()
	cmd := exec.Command("docker", "compose", "up", "-d")
	cmd.Dir = dir
	composeOut, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("启动 Docker 服务失败: %w (%s)", err, strings.TrimSpace(string(composeOut)))
	}

	// 轮询等待容器内进程响应 (最多 5 秒)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		if _, err := dockerExec("true"); err == nil {
			break
		}
	}

	fmt.Println("  ➜ [服务就绪] 爱加速容器已就绪！")
	return nil
}

// 获取全量可用节点
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
			prov, city := parseNodeLocation(name)
			num := extractNodeNumber(name)
			list = append(list, NodeItem{
				ID:       id,
				Province: prov,
				City:     city,
				Number:   num,
				Name:     name,
				Status:   parts[1],
			})
		}
	}

	if len(list) == 0 {
		if strings.Contains(out, "Can not open config file") || strings.Contains(out, "WrongPassword") || strings.Contains(out, "Login Result:") || strings.Contains(out, "not log") {
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "Login Message:") {
					return nil, fmt.Errorf("%s", strings.TrimSpace(strings.TrimPrefix(trimmed, "Login Message:")))
				}
			}
			return nil, fmt.Errorf("爱加速未登录或凭据失效，请先调用登录接口")
		}
	}

	return list, nil
}

// FilterOptions 过滤条件
type FilterOptions struct {
	Province string // 指定省份 (如: 广东, 山东, 上海)
	City     string // 指定城市 (如: 广州, 深圳, 枣庄)
	Node     string // 指定特定节点名称或 ID (如: 上海 #116, vvn-xxxx)
	Reset    bool   // 是否在挑选前清空去重历史
}

// 探测爱加速容器内当前实际监听的内部端口 (兼容 18888, 28888, 38888, 1080 等)
func detectInternalPort() int {
	out, err := dockerExec("ss", "-H", "-lntp")
	if err != nil {
		return 0
	}
	lines := strings.Split(out, "\n")
	re := regexp.MustCompile(`(?:\*|127\.0\.0\.1|0\.0\.0\.0|\[::\]):(\d+)\s+`)
	for _, l := range lines {
		if strings.Contains(l, `"ajiasu"`) {
			m := re.FindStringSubmatch(l)
			if len(m) > 1 {
				p, _ := strconv.Atoi(m[1])
				if p > 0 {
					return p
				}
			}
		}
	}
	return 0
}

// 自适应确保容器内 1080 桥接指向爱加速实际监听的内部端口
func adaptBridge(targetPort int) error {
	if targetPort <= 0 {
		targetPort = 18888
	}

	// 1. 若内部直接在 1080 监听，清理多余 socat 并直接放行
	if targetPort == internalBridgePort {
		_, _ = dockerExec("pkill", "-f", "^socat TCP-LISTEN:1080")
		return nil
	}

	// 2. 检查当前是否已经存在指向该端口的 socat
	psOut, _ := dockerExec("ps", "-eo", "args")
	expected := fmt.Sprintf("TCP:127.0.0.1:%d", targetPort)
	if strings.Contains(psOut, "socat TCP-LISTEN:1080") && strings.Contains(psOut, expected) {
		return nil
	}

	// 3. 动态更新转发规则
	_, _ = dockerExec("pkill", "-f", "^socat TCP-LISTEN:1080")
	time.Sleep(100 * time.Millisecond)

	socatCmd := fmt.Sprintf("nohup socat TCP-LISTEN:1080,fork,reuseaddr,bind=0.0.0.0 TCP:127.0.0.1:%d >/dev/null 2>&1 &", targetPort)
	_, err := dockerExec("bash", "-c", socatCmd)
	time.Sleep(100 * time.Millisecond)
	return err
}

// 核心节点切换与去重调度函数
func SwitchProxy(opt FilterOptions) (*SwitchResult, error) {
	if opt.Reset {
		resetHistory()
	}

	allNodes, err := fetchNodes()
	if err != nil {
		return nil, err
	}
	if len(allNodes) == 0 {
		return nil, fmt.Errorf("爱加速节点列表为空，请检查账号登录状态")
	}

	// 1. 过滤候选池
	var candidates []NodeItem

	// 智能清理输入条件
	provFilter := strings.TrimSpace(opt.Province)
	cityFilter := strings.TrimSpace(opt.City)
	nodeFilter := strings.TrimSpace(opt.Node)

	for _, n := range allNodes {
		// 指定特定节点优先匹配 (名称或 ID)
		if nodeFilter != "" {
			if strings.EqualFold(n.ID, nodeFilter) || strings.EqualFold(n.Name, nodeFilter) ||
				strings.Contains(strings.ToLower(n.Name), strings.ToLower(nodeFilter)) ||
				strings.Contains(strings.ToLower(n.ID), strings.ToLower(nodeFilter)) {
				candidates = append(candidates, n)
			}
			continue
		}

		// 省份匹配
		if provFilter != "" {
			if !strings.EqualFold(n.Province, provFilter) && !strings.Contains(n.Name, provFilter) {
				continue
			}
		}

		// 城市匹配
		if cityFilter != "" {
			if !strings.EqualFold(n.City, cityFilter) && !strings.Contains(n.Name, cityFilter) {
				continue
			}
		}

		candidates = append(candidates, n)
	}

	if len(candidates) == 0 {
		var desc []string
		if provFilter != "" {
			desc = append(desc, "省份="+provFilter)
		}
		if cityFilter != "" {
			desc = append(desc, "城市="+cityFilter)
		}
		if nodeFilter != "" {
			desc = append(desc, "节点="+nodeFilter)
		}
		cond := strings.Join(desc, ", ")
		if cond == "" {
			cond = "全部"
		}
		return nil, fmt.Errorf("未找到符合条件 [%s] 的可用代理节点", cond)
	}

	// 2. 去重算法：挑选未使用的节点
	hist := loadHistory()
	usedMap := make(map[string]bool)
	for _, id := range hist.UsedNodeIDs {
		usedMap[id] = true
	}

	var unused []NodeItem
	for _, c := range candidates {
		if !usedMap[c.ID] {
			unused = append(unused, c)
		}
	}

	var chosen NodeItem
	roundReset := false

	if len(unused) > 0 {
		// 从未使用的节点中随机选一个
		chosen = unused[rng.Intn(len(unused))]
	} else {
		// 该筛选池内的所有节点都已经使用过一轮，自动重置当前候选集合的历史
		roundReset = true
		candMap := make(map[string]bool)
		for _, c := range candidates {
			candMap[c.ID] = true
		}

		// 从历史记录中剔除当前候选池的 ID
		var newUsed []string
		for _, id := range hist.UsedNodeIDs {
			if !candMap[id] {
				newUsed = append(newUsed, id)
			}
		}
		hist.UsedNodeIDs = newUsed

		// 为避免连续两次使用同一个节点，若候选多于 1 个则避开上一个节点
		var pool []NodeItem
		for _, c := range candidates {
			if len(candidates) > 1 && c.ID == hist.LastNodeID {
				continue
			}
			pool = append(pool, c)
		}
		if len(pool) == 0 {
			pool = candidates
		}
		chosen = pool[rng.Intn(len(pool))]
	}

	// 更新去重历史
	hist.UsedNodeIDs = append(hist.UsedNodeIDs, chosen.ID)
	hist.LastNodeID = chosen.ID
	saveHistory(hist)

	remaining := len(unused) - 1
	if roundReset {
		remaining = len(candidates) - 1
	}
	if remaining < 0 {
		remaining = 0
	}

	// 3. 执行切换并自适应内部端口 (兼容 1080/18888/28888/38888 等)
	_, _ = dockerExec("ajiasu", "disconnect")
	_, _ = dockerExec("pkill", "-f", "ajiasu connect")
	time.Sleep(200 * time.Millisecond)

	connectCmd := fmt.Sprintf("nohup ajiasu connect \"%s\" > /tmp/ajiasu-connect.log 2>&1 &", chosen.ID)
	if _, err := dockerExec("bash", "-c", connectCmd); err != nil {
		return nil, fmt.Errorf("启动连接进程失败: %w", err)
	}

	// 轮询等待底层进程端口建立 (最多 6 秒)，并自适应调整 1080 桥接
	var internalPort int
	portDeadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(portDeadline) {
		internalPort = detectInternalPort()
		if internalPort > 0 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	if internalPort > 0 {
		_ = adaptBridge(internalPort)
	} else {
		_ = adaptBridge(18888)
	}

	// 等待 SOCKS5 握手通畅
	proxyHost := getProxyHost()
	proxyPort := getProxyPort()
	var lastErr error
	deadline := time.Now().Add(8 * time.Second)
	proxyOK := false
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		if err := verifySocks5(proxyHost, proxyPort, 1*time.Second); err == nil {
			proxyOK = true
			break
		} else {
			lastErr = err
		}
	}

	if !proxyOK {
		logOut, _ := dockerExec("cat", "/tmp/ajiasu-connect.log")
		return nil, fmt.Errorf("代理握手未就绪: %v (%s)", lastErr, strings.TrimSpace(logOut))
	}

	exitIP := fetchExitIP(proxyHost, proxyPort, 3*time.Second)

	return &SwitchResult{
		Node:        chosen,
		ProxyAddr:   fmt.Sprintf("socks5://%s:%d", proxyHost, proxyPort),
		ExitIP:      exitIP,
		Remaining:   remaining,
		TotalInPool: len(candidates),
		RoundReset:  roundReset,
	}, nil
}

// 诊断当前服务环境
func doctor() DoctorReport {
	rep := DoctorReport{}
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

	container := getContainerName()
	out, err := runCmd("docker", "inspect", "-f", "{{.State.Running}}", container)
	if err == nil && strings.TrimSpace(out) == "true" {
		rep.ContainerRun = true
	} else {
		rep.Error = fmt.Sprintf("容器 %s 未在运行，请先执行 aijiasu up 启动", container)
		return rep
	}

	stOut, _ := dockerExec("ajiasu", "status")
	rep.CurrentStatus = strings.TrimSpace(stOut)
	if !strings.Contains(strings.ToLower(stOut), "not log") && !strings.Contains(stOut, "未登录") {
		rep.LoggedIn = true
	}

	proxyHost := getProxyHost()
	proxyPort := getProxyPort()
	if err := verifySocks5(proxyHost, proxyPort, 1500*time.Millisecond); err == nil {
		rep.ProxyHealthy = true
		rep.ExitIP = fetchExitIP(proxyHost, proxyPort, 3*time.Second)
	} else {
		// 自愈机制: 探测内部实际端口并尝试重新桥接
		if port := detectInternalPort(); port > 0 {
			_ = adaptBridge(port)
			if err := verifySocks5(proxyHost, proxyPort, 1500*time.Millisecond); err == nil {
				rep.ProxyHealthy = true
				rep.ExitIP = fetchExitIP(proxyHost, proxyPort, 3*time.Second)
			}
		}
	}

	// 探测本地 Web API Server 是否处于运行中 (默认不启动)
	apiPort := getHTTPPort()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", apiPort), 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		rep.APIRunning = true
		rep.APIPort = apiPort
	}

	return rep
}

// 启动 Web API 服务 (纯 JSON 接口，无任何 HTML)
func runHTTPServer(port int) {
	mux := http.NewServeMux()

	// 1. 节点的 JSON 接口: GET /nodes 或 GET /api/nodes
	// 提供 id、省份、市区、编号等信息，供用户自行选择节点
	handleNodes := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		q := r.URL.Query()
		prov := q.Get("province")
		if prov == "" {
			prov = q.Get("p")
		}
		city := q.Get("city")
		if city == "" {
			city = q.Get("c")
		}

		nodes, err := fetchNodes()
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"msg":     err.Error(),
			})
			return
		}

		var filtered []NodeItem
		for _, n := range nodes {
			if prov != "" && !strings.Contains(n.Province, prov) && !strings.Contains(n.Name, prov) {
				continue
			}
			if city != "" && !strings.Contains(n.City, city) && !strings.Contains(n.Name, city) {
				continue
			}
			filtered = append(filtered, n)
		}

		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"total":   len(filtered),
			"nodes":   filtered,
		})
	}
	mux.HandleFunc("/nodes", handleNodes)
	mux.HandleFunc("/api/nodes", handleNodes)

	// 2. 切换节点接口: POST /switch 或 GET /switch
	// 严格规范返回: { success: true, msg: 消息 } 或 { success: false, msg: '错误消息：如：连接异常' }
	handleSwitch := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		var opt FilterOptions

		// 支持 POST 请求的 JSON Body
		if r.Method == http.MethodPost {
			var bodyMap map[string]interface{}
			bodyBytes, _ := io.ReadAll(r.Body)
			if len(bodyBytes) > 0 {
				_ = json.Unmarshal(bodyBytes, &bodyMap)
				if val, ok := bodyMap["id"].(string); ok && val != "" {
					opt.Node = val
				}
				if val, ok := bodyMap["node"].(string); ok && val != "" {
					opt.Node = val
				}
				if val, ok := bodyMap["province"].(string); ok && val != "" {
					opt.Province = val
				}
				if val, ok := bodyMap["city"].(string); ok && val != "" {
					opt.City = val
				}
				if val, ok := bodyMap["reset"].(bool); ok {
					opt.Reset = val
				}
			}
		}

		// 兼容 URL Query 参数
		q := r.URL.Query()
		if opt.Node == "" {
			opt.Node = q.Get("id")
			if opt.Node == "" {
				opt.Node = q.Get("node")
				if opt.Node == "" {
					opt.Node = q.Get("n")
				}
			}
		}
		if opt.Province == "" {
			opt.Province = q.Get("province")
			if opt.Province == "" {
				opt.Province = q.Get("p")
			}
		}
		if opt.City == "" {
			opt.City = q.Get("city")
			if opt.City == "" {
				opt.City = q.Get("c")
			}
		}
		if !opt.Reset {
			opt.Reset = q.Get("reset") == "true" || q.Get("reset") == "1"
		}

		res, err := SwitchProxy(opt)
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"msg":     err.Error(),
				"ip":      "",
			})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"msg":     "ok",
			"ip":      extractPureIP(res.ExitIP),
		})
	}
	mux.HandleFunc("/switch", handleSwitch)
	mux.HandleFunc("/api/switch", handleSwitch)

	// 3. 中断连接接口: POST /disconnect 或 GET /disconnect
	handleDisconnect := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		_, err1 := dockerExec("pkill", "-f", "ajiasu connect")
		_, err2 := dockerExec("ajiasu", "disconnect")

		if err1 != nil && err2 != nil {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"msg":     "中断连接失败",
			})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"msg":     "ok",
		})
	}
	mux.HandleFunc("/disconnect", handleDisconnect)
	mux.HandleFunc("/api/disconnect", handleDisconnect)

	// 4. 登录接口: POST /login 或 POST /api/login
	handleLogin := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		var user, pass string

		// 支持 POST 请求的 JSON Body
		if r.Method == http.MethodPost {
			var bodyMap map[string]interface{}
			bodyBytes, _ := io.ReadAll(r.Body)
			if len(bodyBytes) > 0 {
				_ = json.Unmarshal(bodyBytes, &bodyMap)
				if val, ok := bodyMap["username"].(string); ok && val != "" {
					user = val
				} else if val, ok := bodyMap["user"].(string); ok && val != "" {
					user = val
				} else if val, ok := bodyMap["account"].(string); ok && val != "" {
					user = val
				}
				if val, ok := bodyMap["password"].(string); ok && val != "" {
					pass = val
				} else if val, ok := bodyMap["pass"].(string); ok && val != "" {
					pass = val
				}
			}
		}

		// 兼容 Form 表单提交
		if user == "" || pass == "" {
			_ = r.ParseForm()
			if u := r.FormValue("username"); u != "" {
				user = u
			} else if u := r.FormValue("user"); u != "" {
				user = u
			}
			if p := r.FormValue("password"); p != "" {
				pass = p
			} else if p := r.FormValue("pass"); p != "" {
				pass = p
			}
		}

		// 兼容 URL Query 参数
		if user == "" || pass == "" {
			q := r.URL.Query()
			if u := q.Get("username"); u != "" {
				user = u
			} else if u := q.Get("user"); u != "" {
				user = u
			}
			if p := q.Get("password"); p != "" {
				pass = p
			} else if p := q.Get("pass"); p != "" {
				pass = p
			}
		}

		// 如果未传参，尝试从 .env 自动读取已配置的凭据
		if user == "" || pass == "" {
			envU, envP := readEnvCredentials()
			if envU != "" && envP != "" {
				user = envU
				pass = envP
			}
		}

		if user == "" || pass == "" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"msg":     "用户名或密码不能为空",
			})
			return
		}

		if err := LoginAccount(user, pass); err != nil {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"msg":     err.Error(),
			})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"msg":     "ok",
		})
	}
	mux.HandleFunc("/login", handleLogin)
	mux.HandleFunc("/api/login", handleLogin)

	// 辅助状态接口: GET /status
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		rep := doctor()
		msg := "代理服务正常畅通"
		if !rep.ProxyHealthy {
			msg = "代理端口未就绪或未连接节点"
			if rep.Error != "" {
				msg = rep.Error
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": rep.ProxyHealthy,
			"msg":     msg,
			"data":    rep,
		})
	})

	addr := fmt.Sprintf("0.0.0.0:%d", port)
	fmt.Printf("\n🚀 [Web 服务] 爱加速 Web JSON API 服务已启动，监听: http://127.0.0.1:%d\n", port)
	fmt.Println("  1. 登录认证接口    : POST /login (支持 JSON: {\"username\":\"...\", \"password\":\"...\"})")
	fmt.Println("  2. 节点列表 (JSON) : GET  /nodes 或 /api/nodes (提供 id、省份、市区、编号等)")
	fmt.Println("  3. 切换节点接口    : POST /switch (支持 JSON: {\"id\": \"...\"}) 或 GET /switch?id=xxx")
	fmt.Println("  4. 中断连接接口    : POST /disconnect 或 GET /disconnect")
	fmt.Println("  5. 状态查询接口    : GET  /status")
	fmt.Println()

	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "Web 服务退出: %v\n", err)
	}
}

func forwardCompose(args ...string) {
	// 如果是启动容器命令，检查是否缺失 Docker 配置文件并询问用户确认生成
	if len(args) > 0 && (args[0] == "up" || args[0] == "restart" || args[0] == "build") {
		if err := promptAndBootstrapAssets(); err != nil {
			fmt.Fprintf(os.Stderr, "初始化 Docker 环境资产失败: %v\n", err)
			os.Exit(1)
		}
	}
	dir := getProjectDir()
	cmd := exec.Command("docker", append([]string{"compose"}, args...)...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Print(`
爱加速 (Aijiasu) 高可用代理切换控制端 (Go 原生版本)

用法:
  aijiasu <命令> [参数]

核心命令:
  login [账号] [密码]        【登录接口】登录爱加速账号 (支持传参或自动读取 .env 凭据)
  switch [选项/关键词]       【核心切换接口】切换代理节点，支持指定省份、城市、特定节点，内置自动去重
                           选项:
                             -p, --province <省份>   指定省份 (如: 广东, 山东, 浙江)
                             -c, --city <城市>       指定城市 (如: 广州, 深圳, 枣庄)
                             -n, --node <节点/ID>    指定特定节点名称或 ID
                             -r, --reset             清空去重历史后重新挑选
                           快捷传参:
                             aijiasu switch 广东     (自动识别为省份)
                             aijiasu switch 广州     (自动识别为城市)
                             aijiasu switch "上海 #116" (特定节点)
                             aijiasu switch          (全局随机挑选未用节点)

  serve [端口]             启动轻量 HTTP API 服务 (默认监听 1081 端口)
                           供各类爬虫、自动化脚本直接通过 HTTP POST/GET 请求换 IP 或登录

  reset                    手动清空去重历史记录

辅助管理命令:
  init                     【自举初始化】检查并自动生成当前目录缺失的 Dockerfile, Compose 与时区资产
  status                   查看当前 Docker 容器、登录状态与出口 IP
  nodes [-p 省] [-c 市]    查看符合条件的可用节点列表与省市分布
  disconnect               断开当前连接
  up / down / logs         Docker 容器启动、停止与日志查看
`)
}

func printStatus(label string, ok bool) {
	if ok {
		fmt.Printf("%-18s \x1b[32m✅ 就绪\x1b[0m\n", label)
	} else {
		fmt.Printf("%-18s \x1b[31m❌ 未运行/未就绪\x1b[0m\n", label)
	}
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "embedded" {
		embeddedMain(os.Stdin, os.Stdout)
		return
	}
	args := os.Args[1:]
	cmd := "status"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}

	switch cmd {
	case "init":
		if err := ensureBootstrapAssets(); err != nil {
			fmt.Fprintf(os.Stderr, "初始化环境资产失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("\x1b[32m[aijiasu OK]\x1b[0m 当前目录 Docker 环境与挂载资产已全部就绪。")

	case "up", "start":
		forwardCompose("up", "-d")
	case "down", "stop":
		forwardCompose("down")
	case "restart":
		forwardCompose("restart")
	case "logs":
		forwardCompose("logs", "-f")

	case "status":
		fmt.Println("\n=== 爱加速 Docker 代理服务状态检查 ===")
		rep := doctor()
		proxyHost := getProxyHost()
		proxyPort := getProxyPort()
		printStatus("Docker CLI:       ", rep.DockerInstalled)
		printStatus("Docker Daemon:    ", rep.DaemonRunning)
		printStatus(fmt.Sprintf("容器状态 (%s):", getContainerName()), rep.ContainerRun)
		printStatus("爱加速登录状态:    ", rep.LoggedIn)

		if rep.ProxyHealthy {
			fmt.Printf("SOCKS5 代理端口:   \x1b[32m✅ %s:%d 畅通\x1b[0m\n", proxyHost, proxyPort)
			if rep.ExitIP != "" {
				fmt.Printf("当前出口 IP:       \x1b[32m%s\x1b[0m\n", rep.ExitIP)
			}
		} else {
			fmt.Printf("SOCKS5 代理端口:   \x1b[31m❌ %s:%d 未就绪或未连接节点\x1b[0m\n", proxyHost, proxyPort)
		}

		if rep.APIRunning {
			fmt.Printf("Web API 接口服务:  \x1b[32m✅ 运行中 (http://127.0.0.1:%d)\x1b[0m\n", rep.APIPort)
		} else {
			fmt.Printf("Web API 接口服务:  \x1b[90m⚪ 未启动 (默认不启动，可按需执行 aijiasu serve 开启)\x1b[0m\n")
		}

		if rep.Error != "" {
			fmt.Printf("\n\x1b[33m提示:\x1b[0m %s\n", rep.Error)
		}
		fmt.Println()

	case "switch":
		// 解析 switch 参数
		fs := flag.NewFlagSet("switch", flag.ContinueOnError)
		prov := fs.String("p", "", "指定省份")
		provLong := fs.String("province", "", "指定省份")
		city := fs.String("c", "", "指定城市")
		cityLong := fs.String("city", "", "指定城市")
		node := fs.String("n", "", "指定特定节点名称或 ID")
		nodeLong := fs.String("node", "", "指定特定节点名称或 ID")
		reset := fs.Bool("r", false, "清空去重历史后重新挑选")
		resetLong := fs.Bool("reset", false, "清空去重历史后重新挑选")

		_ = fs.Parse(args)
		restArgs := fs.Args()

		p := *prov
		if p == "" {
			p = *provLong
		}
		c := *city
		if c == "" {
			c = *cityLong
		}
		n := *node
		if n == "" {
			n = *nodeLong
		}
		r := *reset || *resetLong

		// 如果没有显式传 flag，从剩余位置参数中智能推断
		if p == "" && c == "" && n == "" && len(restArgs) > 0 {
			raw := strings.Join(restArgs, " ")
			// 1. 是否是直辖市
			isMun := false
			for _, m := range municipalities {
				if strings.Contains(raw, m) {
					c = m
					p = m
					isMun = true
					break
				}
			}
			if !isMun {
				// 2. 是否是省份
				isProv := false
				for _, provItem := range provinceList {
					if strings.Contains(raw, provItem) {
						p = provItem
						isProv = true
						break
					}
				}
				// 3. 是否是城市
				if !isProv {
					for cityItem, provMap := range cityToProvinceMap {
						if strings.Contains(raw, cityItem) {
							c = cityItem
							p = provMap
							break
						}
					}
				}
				// 4. 若既不是纯省份也不是纯城市，作为节点关键词匹配
				if p == "" && c == "" {
					n = raw
				}
			}
		}

		opt := FilterOptions{
			Province: p,
			City:     c,
			Node:     n,
			Reset:    r,
		}

		fmt.Println("  ➜ [切换请求] 正在执行智能代理切换...")
		if opt.Province != "" {
			fmt.Printf("     • 指定省份: %s\n", opt.Province)
		}
		if opt.City != "" {
			fmt.Printf("     • 指定城市: %s\n", opt.City)
		}
		if opt.Node != "" {
			fmt.Printf("     • 指定节点: %s\n", opt.Node)
		}
		if opt.Reset {
			fmt.Println("     • [重置历史] 本次切换清空去重历史记录")
		}

		res, err := SwitchProxy(opt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n\x1b[31m[切换失败]\x1b[0m %v\n", err)
			os.Exit(1)
		}

		if res.RoundReset {
			fmt.Printf("\n💡 \x1b[33m[去重轮换]\x1b[0m 该范围内的所有可用节点已全部使用过一轮，已自动开启新一轮轮换！\n")
		}

		fmt.Printf("\n\x1b[32m[切换成功]\x1b[0m 代理已连接至: %s\n", res.Node.Name)
		fmt.Printf("  • 节点 ID  : %s\n", res.Node.ID)
		fmt.Printf("  • 省份/城市: %s / %s\n", res.Node.Province, res.Node.City)
		fmt.Printf("  • 代理地址 : %s\n", res.ProxyAddr)
		if res.ExitIP != "" {
			fmt.Printf("  • 出口 IP  : \x1b[32m%s\x1b[0m\n", res.ExitIP)
		}
		fmt.Printf("  • 去重统计 : 候选池共 %d 个节点 | 本轮剩余未用 %d 个节点\n\n", res.TotalInPool, res.Remaining)

	case "serve":
		port := getHTTPPort()
		if len(args) > 0 {
			if p, err := strconv.Atoi(args[0]); err == nil && p > 0 {
				port = p
			}
		}
		runHTTPServer(port)

	case "reset":
		resetHistory()
		fmt.Println("\x1b[32m[aijiasu OK]\x1b[0m 节点去重历史记录已清空，下一轮切换将重新从全部候选节点中挑选。")

	case "nodes", "list":
		fs := flag.NewFlagSet("nodes", flag.ContinueOnError)
		prov := fs.String("p", "", "按省份筛选")
		city := fs.String("c", "", "按城市筛选")
		summary := fs.Bool("s", false, "显示省份与城市统计概览")
		_ = fs.Parse(args)

		nodes, err := fetchNodes()
		if err != nil {
			fmt.Fprintf(os.Stderr, "\x1b[31m[aijiasu ERROR]\x1b[0m %v\n", err)
			os.Exit(1)
		}

		pFilter := *prov
		cFilter := *city
		if pFilter == "" && cFilter == "" && len(fs.Args()) > 0 {
			pFilter = fs.Args()[0]
		}

		var matched []NodeItem
		for _, n := range nodes {
			if pFilter != "" && !strings.Contains(n.Province, pFilter) && !strings.Contains(n.Name, pFilter) {
				continue
			}
			if cFilter != "" && !strings.Contains(n.City, cFilter) && !strings.Contains(n.Name, cFilter) {
				continue
			}
			matched = append(matched, n)
		}

		if *summary {
			provCount := make(map[string]int)
			cityCount := make(map[string]int)
			for _, n := range matched {
				provCount[n.Province]++
				cityCount[n.City]++
			}
			fmt.Printf("\n=== 节点概览统计 (共 %d 个节点，涵盖 %d 个省份，%d 个城市) ===\n", len(matched), len(provCount), len(cityCount))
			type ItemCount struct {
				Name  string
				Count int
			}
			var pList []ItemCount
			for k, v := range provCount {
				pList = append(pList, ItemCount{k, v})
			}
			sort.Slice(pList, func(i, j int) bool { return pList[i].Count > pList[j].Count })
			for _, it := range pList {
				fmt.Printf("  - %-8s : %d 个节点\n", it.Name, it.Count)
			}
			fmt.Println()
			return
		}

		fmt.Printf("\n=== 可用节点列表 (共 %d 个节点) ===\n", len(matched))
		for _, n := range matched {
			fmt.Printf("  • [%s] %-20s (%s - %s)\n", n.ID, n.Name, n.Province, n.City)
		}
		fmt.Printf("\n共 %d 个节点。支持使用 'aijiasu switch -p 省份' 或 'aijiasu switch -c 城市' 切换。\n\n", len(matched))

	case "login":
		var u, p string
		if len(args) >= 2 {
			u = args[0]
			p = args[1]
		} else {
			u, p = readEnvCredentials()
		}

		if u == "" || p == "" {
			fmt.Println("用法: aijiasu login <用户名/手机号> <密码>")
			fmt.Println("或者在 .env 文件中配置 AIJIASU_USER 和 AIJIASU_PASS 后直接运行 aijiasu login")
			os.Exit(1)
		}

		fmt.Printf("➜ 正在使用账号 [%s] 登录爱加速...\n", u)
		if err := LoginAccount(u, p); err != nil {
			fmt.Fprintf(os.Stderr, "\n\x1b[31m[aijiasu ERROR]\x1b[0m 登录失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("\n\x1b[32m[aijiasu OK]\x1b[0m 登录成功！")

	case "disconnect":
		fmt.Println("正在断开爱加速节点连接...")
		_, _ = dockerExec("pkill", "-f", "ajiasu connect")
		_, _ = dockerExec("ajiasu", "disconnect")
		fmt.Println("\x1b[32m[aijiasu OK]\x1b[0m 已成功断开连接。")

	case "help", "--help", "-h":
		printHelp()

	default:
		printHelp()
	}
}
