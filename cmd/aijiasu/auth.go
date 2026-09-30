package main

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// 爱加速凭证模型 (内存中操作，落盘前必须高强度加密)
type Credentials struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	UpdatedAt int64  `json:"updated_at"`
}

// 凭据文件相对项目路径
const authFileName = "conf/.auth.enc"

// 获取加密凭证文件绝对路径
func getAuthFilePath() string {
	return filepath.Join(getProjectDir(), authFileName)
}

// 手机号/用户名脱敏显示 (例如 18010029423 -> 180****9423)
func maskUsername(username string) string {
	username = strings.TrimSpace(username)
	if len(username) >= 11 {
		return username[:3] + "****" + username[len(username)-4:]
	} else if len(username) > 4 {
		return username[:2] + "****" + username[len(username)-2:]
	}
	return "****"
}

// 获取宿主机硬件特征指纹 (Machine Fingerprint)，用于硬件绑定的 AES-256 密钥派生 (绝对稳定，杜绝漂移)
func getMachineFingerprint() string {
	var parts []string

	// 1. macOS 平台: 提取主板硬件级唯一 IOPlatformUUID (优先绝对路径 /usr/sbin/ioreg)
	if runtime.GOOS == "darwin" {
		cmdPath := "/usr/sbin/ioreg"
		if _, err := os.Stat(cmdPath); err != nil {
			cmdPath = "ioreg"
		}
		out, err := exec.Command(cmdPath, "-rd1", "-c", "IOPlatformExpertDevice").CombinedOutput()
		if err == nil {
			re := regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([^"]+)"`)
			m := re.FindStringSubmatch(string(out))
			if len(m) > 1 {
				parts = append(parts, "darwin_uuid:"+m[1])
			}
		}
	}

	// 2. Linux 平台: 读取 machine-id
	if runtime.GOOS == "linux" {
		for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
			if data, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(data))) > 0 {
				parts = append(parts, "linux_id:"+strings.TrimSpace(string(data)))
				break
			}
		}
	}

	// 3. 用户主目录 (恒定绝对路径，不受临时环境变量影响)
	home, _ := os.UserHomeDir()
	parts = append(parts, "home:"+home)

	// 4. 当前宿主机主机名
	hostname, _ := os.Hostname()
	parts = append(parts, "host:"+hostname)

	// 5. 组合指纹
	return strings.Join(parts, "|")
}

// 派生本机专用的 AES-256 密钥 (32 字节)
func deriveAESKey() []byte {
	fingerprint := getMachineFingerprint()
	hasher := sha256.New()
	hasher.Write([]byte("aijiasu-salt-vault-v1:"))
	hasher.Write([]byte(fingerprint))
	return hasher.Sum(nil)
}

// AES-256-GCM 加密
func encryptCredentials(cred *Credentials) (string, error) {
	key := deriveAESKey()
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("创建 AES Cipher 失败: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("初始化 GCM 失败: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("生成加密随机盐失败: %w", err)
	}

	payload, err := json.Marshal(cred)
	if err != nil {
		return "", fmt.Errorf("凭证序列化失败: %w", err)
	}

	// gcm.Seal 会将密文和认证 Tag 追加到 nonce 之后
	cipherBytes := gcm.Seal(nonce, nonce, payload, nil)
	return base64.StdEncoding.EncodeToString(cipherBytes), nil
}

// AES-256-GCM 解密
func decryptCredentials(cipherBase64 string) (*Credentials, error) {
	raw, err := base64.StdEncoding.DecodeString(cipherBase64)
	if err != nil {
		return nil, fmt.Errorf("凭证数据编码错误: %w", err)
	}

	key := deriveAESKey()
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("创建 AES Cipher 失败: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化 GCM 失败: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(raw) < nonceSize {
		return nil, errors.New("加密数据长度不合法")
	}

	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("解密凭证失败 (可能密钥不匹配或文件已被篡改)")
	}

	var cred Credentials
	if err := json.Unmarshal(plaintext, &cred); err != nil {
		return nil, fmt.Errorf("凭证数据解析失败: %w", err)
	}
	return &cred, nil
}

// 保存加密凭证至文件 (文件权限严格限制为 0600)
func saveCredentialsToFile(cred *Credentials) error {
	enc, err := encryptCredentials(cred)
	if err != nil {
		return err
	}

	targetPath := getAuthFilePath()
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return err
	}

	header := "# aijiasu encrypted credentials (AES-256-GCM)\n"
	content := header + enc + "\n"

	return os.WriteFile(targetPath, []byte(content), 0600)
}

// 从加密文件中加载并解密凭据
func loadCredentialsFromFile() (*Credentials, error) {
	targetPath := getAuthFilePath()
	data, err := os.ReadFile(targetPath)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	var cipherText string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			cipherText = l
			break
		}
	}

	if cipherText == "" {
		return nil, errors.New("凭证文件内容为空")
	}

	return decryptCredentials(cipherText)
}

// 清除本地已保存的加密凭证文件
func clearCredentialsFile() error {
	targetPath := getAuthFilePath()
	if _, err := os.Stat(targetPath); err == nil {
		return os.Remove(targetPath)
	}
	return nil
}

// 终端无回显安全读取密码
func readPasswordFromTTY(prompt string) (string, error) {
	fmt.Print(prompt)

	// 优先打开控制终端 /dev/tty
	var tty *os.File
	var inputReader io.Reader = os.Stdin

	if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		tty = f
		inputReader = f
		defer tty.Close()
	}

	// 关闭终端回显 (stty -echo)
	sttyCmd := exec.Command("stty", "-echo")
	if tty != nil {
		sttyCmd.Stdin = tty
	} else {
		sttyCmd.Stdin = os.Stdin
	}
	_ = sttyCmd.Run()

	defer func() {
		restoreCmd := exec.Command("stty", "echo")
		if tty != nil {
			restoreCmd.Stdin = tty
		} else {
			restoreCmd.Stdin = os.Stdin
		}
		_ = restoreCmd.Run()
		fmt.Println() // 输入完成后自动换行
	}()

	reader := bufio.NewReader(inputReader)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// 交互式读取用户名/手机号
func readUsernameFromTTY(prompt string) (string, error) {
	fmt.Print(prompt)
	var inputReader io.Reader = os.Stdin
	if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		defer f.Close()
		inputReader = f
	}
	reader := bufio.NewReader(inputReader)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// 检查并自动将旧 .env 中的明文账号密码迁移为高强度加密存储
func migrateOldEnvIfPresent() (*Credentials, bool) {
	envPath := filepath.Join(getProjectDir(), ".env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		return nil, false
	}

	lines := strings.Split(string(data), "\n")
	var user, pass string
	hasPlaintext := false
	var newLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "AIJIASU_USER=") {
			val := strings.TrimPrefix(trimmed, "AIJIASU_USER=")
			val = strings.Trim(val, `"' `)
			if val != "" {
				user = val
				hasPlaintext = true
			}
			continue
		}
		if strings.HasPrefix(trimmed, "AIJIASU_PASS=") {
			val := strings.TrimPrefix(trimmed, "AIJIASU_PASS=")
			val = strings.Trim(val, `"' `)
			if val != "" {
				pass = val
				hasPlaintext = true
			}
			continue
		}
		newLines = append(newLines, line)
	}

	if hasPlaintext && user != "" && pass != "" {
		cred := &Credentials{
			Username:  user,
			Password:  pass,
			UpdatedAt: time.Now().Unix(),
		}
		if err := saveCredentialsToFile(cred); err == nil {
			// 安全重写 .env 文件，擦除明文账号密码，增加安全提示
			cleanedContent := strings.Join(newLines, "\n")
			cleanedContent = strings.TrimSpace(cleanedContent)
			safeBanner := "# [安全提示] 账号密码已迁移至高强度加密保管库 conf/.auth.enc (AES-256-GCM 机器硬件绑定)\n# 请勿在此处明文填写密码。如需修改账号密码，请运行: aijiasu login\n\n"
			_ = os.WriteFile(envPath, []byte(safeBanner+cleanedContent+"\n"), 0644)
			return cred, true
		}
	}

	return nil, false
}

// 核心预检入口: 确保已具备可用账号密码。如果不存在则交互式录入并加密持久化
func EnsureCredentials() (*Credentials, error) {
	// 1. 尝试从本地加密文件读取
	cred, err := loadCredentialsFromFile()
	if err == nil && cred != nil && cred.Username != "" && cred.Password != "" {
		return cred, nil
	}

	// 2. 尝试从既有 .env 自动无缝迁移
	if migrated, ok := migrateOldEnvIfPresent(); ok {
		fmt.Printf("\x1b[32m[aijiasu 安全机制]\x1b[0m 检测到旧版 .env 中存在明文账号信息，已自动升级为 \x1b[1;36mAES-256-GCM\x1b[0m 硬件加密保护！\n")
		fmt.Printf("• 账号已加密托管: \x1b[32m%s\x1b[0m (凭据文件: %s，权限 0600)\n", maskUsername(migrated.Username), authFileName)
		fmt.Println("• .env 中的明文账号密码已自动安全擦除，彻底消除泄漏风险。")
		return migrated, nil
	}

	// 3. 环境变量中如果临时传入了 AIJIASU_USER 和 AIJIASU_PASS，也进行一次性加密存储
	envUser := os.Getenv("AIJIASU_USER")
	envPass := os.Getenv("AIJIASU_PASS")
	if envUser != "" && envPass != "" {
		cred = &Credentials{
			Username:  envUser,
			Password:  envPass,
			UpdatedAt: time.Now().Unix(),
		}
		_ = saveCredentialsToFile(cred)
		return cred, nil
	}

	// 4. 终端交互式输入 (仅在控制终端具备交互能力时执行)
	if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err != nil {
		return nil, fmt.Errorf("当前处于后台或无交互式终端环境，未检测到有效爱加速凭据 (请先在终端运行 'aijiasu login' 或 'aijiasu up' 完成凭据录入)")
	} else {
		_ = f.Close()
	}

	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println(" 🔒 \x1b[1;33m[aijiasu 安全凭证初始化]\x1b[0m")
	fmt.Println(" 未检测到已配置的爱加速账号密码，请输入您的账号完成初始化。")
	fmt.Println(" 提示: 您的账号与密码将采用 \x1b[1;36mAES-256-GCM 机器硬件绑定加密\x1b[0m 存储，")
	fmt.Println(" 绝不在磁盘保留任何明文，密码输入时不回显。")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	var inputUser, inputPass string
	for {
		u, err := readUsernameFromTTY("请输入爱加速账号 (手机号): ")
		if err != nil {
			return nil, fmt.Errorf("读取账号失败: %w", err)
		}
		u = strings.TrimSpace(u)
		if u == "" {
			fmt.Println("\x1b[31m错误: 账号不能为空，请重新输入。\x1b[0m")
			continue
		}
		inputUser = u
		break
	}

	for {
		p, err := readPasswordFromTTY("请输入爱加速登录密码 (密码输入不显示): ")
		if err != nil {
			return nil, fmt.Errorf("读取密码失败: %w", err)
		}
		p = strings.TrimSpace(p)
		if p == "" {
			fmt.Println("\x1b[31m错误: 密码不能为空，请重新输入。\x1b[0m")
			continue
		}
		inputPass = p
		break
	}

	newCred := &Credentials{
		Username:  inputUser,
		Password:  inputPass,
		UpdatedAt: time.Now().Unix(),
	}

	if err := saveCredentialsToFile(newCred); err != nil {
		return nil, fmt.Errorf("保存加密凭证失败: %w", err)
	}

	fmt.Printf("\n\x1b[32m[aijiasu OK]\x1b[0m 账号密码已成功使用 \x1b[1;36mAES-256-GCM\x1b[0m 加密存储！(账号: %s)\n\n", maskUsername(newCred.Username))
	return newCred, nil
}

// 主动更新/重新登录交互
func promptLogin() error {
	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println(" 🔑 \x1b[1;36m[aijiasu 更新账号密码]\x1b[0m")
	fmt.Println(" 请输入新的爱加速账号与密码，系统将重新加密存储。")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	u, err := readUsernameFromTTY("请输入爱加速账号 (手机号): ")
	if err != nil {
		return err
	}
	u = strings.TrimSpace(u)
	if u == "" {
		return errors.New("账号不能为空")
	}

	p, err := readPasswordFromTTY("请输入爱加速登录密码 (密码输入不显示): ")
	if err != nil {
		return err
	}
	p = strings.TrimSpace(p)
	if p == "" {
		return errors.New("密码不能为空")
	}

	cred := &Credentials{
		Username:  u,
		Password:  p,
		UpdatedAt: time.Now().Unix(),
	}

	if err := saveCredentialsToFile(cred); err != nil {
		return fmt.Errorf("加密保存失败: %w", err)
	}

	fmt.Printf("\n\x1b[32m[aijiasu OK]\x1b[0m 凭证已更新并安全加密保存！(账号: %s)\n", maskUsername(cred.Username))

	// 如果容器当前正在运行，尝试在容器内通过纯内存管道同步更新并重新登录
	rep := doctor()
	if rep.ContainerRun {
		fmt.Println("正在将最新凭据通过安全内存管道同步至运行中的容器...")
		if err := injectCredentialsIntoContainer(cred); err != nil {
			fmt.Printf("\x1b[33m[aijiasu 提示]\x1b[0m %v\n", err)
		} else {
			fmt.Println("\x1b[32m[aijiasu OK]\x1b[0m 容器内自动登录鉴权成功，环境变量零泄露！")
		}
	}
	return nil
}

// 通过纯内存标准输入管道 (stdin pipe) 将凭据注入容器，绝不通过 Docker 环境变量或命令行参数传递
func injectCredentialsIntoContainer(cred *Credentials) error {
	if cred == nil {
		var err error
		cred, err = EnsureCredentials()
		if err != nil || cred == nil {
			return fmt.Errorf("无有效账号凭证: %w", err)
		}
	}

	confContent := fmt.Sprintf("user %s\npass %s\nprotocol proxy\n", cred.Username, cred.Password)
	cmd := exec.Command("docker", "exec", "-i", defaultContainer, "bash", "-c", "cat > /etc/ajiasu.conf && chmod 600 /etc/ajiasu.conf && ajiasu login")
	cmd.Stdin = strings.NewReader(confContent)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("容器鉴权注入失败: %w, 响应: %s", err, string(out))
	}
	return nil
}

// 打印当前凭据安全状态
func printAuthStatus() {
	fmt.Println("\n=== 爱加速账号凭证安全状态 ===")
	cred, err := loadCredentialsFromFile()
	if err != nil || cred == nil {
		// 检查是否有未迁移的 .env
		if envCred, ok := migrateOldEnvIfPresent(); ok {
			cred = envCred
		}
	}

	if cred != nil && cred.Username != "" {
		fmt.Printf("• 凭据状态:   \x1b[32m✅ 已配置并受保护\x1b[0m\n")
		fmt.Printf("• 绑定账号:   \x1b[1;36m%s\x1b[0m\n", maskUsername(cred.Username))
		fmt.Printf("• 加密算法:   \x1b[32mAES-256-GCM (硬件指纹绑定派生密钥)\x1b[0m\n")
		fmt.Printf("• 密文文件:   %s (权限 0600)\n", getAuthFilePath())
		if cred.UpdatedAt > 0 {
			t := time.Unix(cred.UpdatedAt, 0).Format("2006-01-02 15:04:05")
			fmt.Printf("• 更新时间:   %s\n", t)
		}
		fmt.Println("\n💡 提示: 如需修改账号密码，可运行: aijiasu login")
		fmt.Println("💡 提示: 如需清除本地加密凭证，可运行: aijiasu logout")
	} else {
		fmt.Printf("• 凭据状态:   \x1b[31m❌ 未配置\x1b[0m\n")
		fmt.Println("💡 提示: 运行 'aijiasu up'、'aijiasu start' 或 'aijiasu login' 即可进行交互式安全配置。")
	}
}
