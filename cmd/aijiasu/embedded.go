package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

const embeddedProtocol = "aijiasu-stdio-v1"
const embeddedOwnerLabel = "org.axebrowser.aijiasu.instance"
const embeddedDirectoryLabel = "org.axebrowser.aijiasu.directory"
const embeddedMaxInput = 16 * 1024
const embeddedMaxOutput = 2 * 1024 * 1024

var embeddedID = regexp.MustCompile(`^axe-aijiasu-[a-f0-9]{12}-([1-9]|1[0-9]|20)$`)
var embeddedNodeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,199}$`)

type embeddedRequest struct {
	Protocol      string `json:"protocol"`
	Action        string `json:"action"`
	DataDir       string `json:"dataDir"`
	InstanceID    string `json:"instanceId"`
	ProxyPort     int    `json:"proxyPort"`
	DockerPath    string `json:"dockerPath"`
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	NodeID        string `json:"nodeId,omitempty"`
	InstanceIndex int    `json:"instanceIndex"`
}

type embeddedNode struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Province string `json:"province"`
	City     string `json:"city"`
}

type embeddedInstance struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Proxy  string `json:"proxy"`
	ExitIP string `json:"exitIp"`
	NodeID string `json:"nodeId"`
}

type embeddedResponse struct {
	Protocol string            `json:"protocol"`
	OK       bool              `json:"ok"`
	Instance *embeddedInstance `json:"instance,omitempty"`
	Nodes    *[]embeddedNode   `json:"nodes,omitempty"`
	Error    string            `json:"error,omitempty"`
}

type embeddedRecord struct {
	Protocol   string `json:"protocol"`
	InstanceID string `json:"instanceId"`
	ProxyPort  int    `json:"proxyPort"`
}

type embeddedContainer struct {
	Name       string
	State      struct{ Running bool }
	Config     struct{ Labels map[string]string }
	HostConfig struct {
		PortBindings map[string][]struct{ HostIP, HostPort string }
	}
}

type embeddedRunner func(context.Context, string, string, []string, string) (string, error)

type embeddedWorker struct {
	request embeddedRequest
	run     embeddedRunner
	probe   func(string, int, time.Duration) error
}

func decodeEmbedded(input io.Reader) (embeddedRequest, error) {
	var request embeddedRequest
	content, err := io.ReadAll(io.LimitReader(input, embeddedMaxInput+1))
	if err != nil || len(content) > embeddedMaxInput {
		return request, errors.New("插件请求过大或无法读取。")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return request, errors.New("插件请求必须是单个 JSON 对象。")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || fields[name] != nil {
			return request, errors.New("插件请求包含重复或无效字段。")
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return request, errors.New("插件请求 JSON 格式无效。")
		}
		fields[name] = value
	}
	if _, err = decoder.Token(); err != nil {
		return request, errors.New("插件请求 JSON 格式无效。")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return request, errors.New("插件仅接受一次请求。")
	}
	decoder = json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil {
		return request, errors.New("插件请求字段无效。")
	}
	if request.Action != "start" && (fields["username"] != nil || fields["password"] != nil) {
		return request, errors.New("仅启动请求允许提供账号密码。")
	}
	if request.Action != "start" && request.Action != "switch" && fields["nodeId"] != nil {
		return request, errors.New("此插件操作不接受节点 ID。")
	}
	return request, validateEmbedded(request)
}

func validateEmbedded(request embeddedRequest) error {
	match := embeddedID.FindStringSubmatch(request.InstanceID)
	if request.Protocol != embeddedProtocol || len(match) != 2 || match[1] != strconv.Itoa(request.InstanceIndex) {
		return errors.New("插件协议或实例标识无效。")
	}
	if request.Action != "start" && request.Action != "status" && request.Action != "stop" &&
		request.Action != "nodes" && request.Action != "switch" {
		return errors.New("插件操作无效。")
	}
	if (request.NodeID != "" && !embeddedNodeID.MatchString(request.NodeID)) ||
		(request.Action == "switch" && request.NodeID == "") ||
		(request.Action != "start" && request.Action != "switch" && request.NodeID != "") {
		return errors.New("请选择有效的爱加速节点 ID。")
	}
	if request.ProxyPort < 1024 || request.ProxyPort > 65535 {
		return errors.New("代理端口必须在 1024–65535 之间。")
	}
	if !filepath.IsAbs(request.DataDir) || filepath.Clean(request.DataDir) == string(filepath.Separator) ||
		!filepath.IsAbs(request.DockerPath) || strings.IndexFunc(request.DataDir+request.DockerPath, unicode.IsControl) >= 0 {
		return errors.New("插件工作目录和 Docker 路径必须为有效绝对路径。")
	}
	if request.Action == "start" {
		for _, value := range []string{request.Username, request.Password} {
			if strings.TrimSpace(value) == "" || len(value) > 1024 ||
				strings.IndexFunc(value, unicode.IsControl) >= 0 {
				return errors.New("请输入有效账号密码；爱加速配置不接受控制字符。")
			}
		}
	}
	return nil
}

type boundedEmbeddedBuffer struct {
	content  []byte
	overflow bool
}

func (buffer *boundedEmbeddedBuffer) Write(value []byte) (int, error) {
	remaining := embeddedMaxOutput - len(buffer.content)
	if len(value) > remaining {
		buffer.overflow = true
		buffer.content = append(buffer.content, value[:remaining]...)
	} else {
		buffer.content = append(buffer.content, value...)
	}
	return len(value), nil
}

func runEmbeddedDocker(ctx context.Context, executable, directory string, args []string, input string) (string, error) {
	host, err := url.Parse(os.Getenv("DOCKER_HOST"))
	if err != nil || host.Scheme != "unix" || host.Host != "" || !filepath.IsAbs(host.Path) ||
		host.User != nil || host.RawQuery != "" || host.Fragment != "" {
		return "", errors.New("本机 Docker 连接尚未确认，插件拒绝连接远程 Docker。")
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = directory
	for _, key := range []string{"HOME", "PATH", "TMPDIR", "DOCKER_CONFIG", "DOCKER_HOST"} {
		if value, ok := os.LookupEnv(key); ok {
			command.Env = append(command.Env, key+"="+value)
		}
	}
	command.Stdin = strings.NewReader(input)
	var output boundedEmbeddedBuffer
	command.Stdout, command.Stderr = &output, &output
	command.WaitDelay = 2 * time.Second
	err = command.Run()
	if err != nil || output.overflow {
		return "", errors.New("Docker 操作未完成，请检查 Docker 状态、网络或账号并重试。")
	}
	return string(output.content), nil
}

func (worker embeddedWorker) docker(ctx context.Context, timeout time.Duration, input string, args ...string) (string, error) {
	child, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return worker.run(child, worker.request.DockerPath, worker.request.DataDir, args, input)
}

func (worker embeddedWorker) containerExec(ctx context.Context, input string, args ...string) (string, error) {
	return worker.docker(ctx, 30*time.Second, input, append([]string{"exec", "-i", worker.request.InstanceID}, args...)...)
}

func (worker embeddedWorker) directoryLabel() string {
	hash := sha256.Sum256([]byte(filepath.Clean(worker.request.DataDir)))
	return hex.EncodeToString(hash[:])
}

func (worker embeddedWorker) inspect(ctx context.Context) (*embeddedContainer, error) {
	id := worker.request.InstanceID
	output, err := worker.docker(ctx, 15*time.Second, "", "container", "ls", "--all", "--filter", "name=^/"+id+"$", "--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(output) == "" {
		return nil, nil
	}
	if strings.TrimSpace(output) != id {
		return nil, errors.New("容器名称冲突，请检查插件实例。")
	}
	output, err = worker.docker(ctx, 15*time.Second, "", "container", "inspect", id)
	var containers []embeddedContainer
	if err != nil || json.Unmarshal([]byte(output), &containers) != nil || len(containers) != 1 {
		return nil, errors.New("无法验证爱加速容器身份。")
	}
	container := containers[0]
	ports := container.HostConfig.PortBindings["1080/tcp"]
	if container.Name != "/"+id || container.Config.Labels[embeddedOwnerLabel] != id ||
		container.Config.Labels[embeddedDirectoryLabel] != worker.directoryLabel() ||
		container.Config.Labels["com.docker.compose.project"] != id ||
		len(ports) != 1 || ports[0].HostIP != "127.0.0.1" || ports[0].HostPort != strconv.Itoa(worker.request.ProxyPort) {
		return nil, errors.New("同名容器不属于此插件实例，已拒绝接管或停止。")
	}
	return &container, nil
}

func embeddedSafePath(path string) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return errors.New("无法检查插件目录权限。")
		}
		if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
			return errors.New("插件工作目录不能包含符号链接或普通文件。")
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}

func embeddedWriteAsset(path string, content []byte, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Size() > embeddedMaxOutput {
			return errors.New("插件资源文件类型无效。")
		}
		previous, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(previous, content) {
			return errors.New("插件目录包含不同的配置文件，已保留原文件，请使用新的实例目录。")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return errors.New("无法读取插件资源文件。")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return errors.New("无法创建插件资源文件。")
	}
	_, err = file.Write(content)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.New("无法保存插件资源文件。")
	}
	return nil
}

func (worker embeddedWorker) record() []byte {
	content, _ := json.Marshal(embeddedRecord{embeddedProtocol, worker.request.InstanceID, worker.request.ProxyPort})
	return content
}

func (worker embeddedWorker) prepare() error {
	directory := worker.request.DataDir
	if err := embeddedSafePath(directory); err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return errors.New("无法创建插件实例目录。")
	}
	marker := filepath.Join(directory, ".instance.json")
	if _, err := os.Lstat(marker); os.IsNotExist(err) {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			return errors.New("插件实例目录非空且没有归属记录，已拒绝接管。")
		}
	}
	if err := embeddedWriteAsset(marker, worker.record(), 0600); err != nil {
		return err
	}
	if err := embeddedSafePath(filepath.Join(directory, "conf")); err != nil {
		return err
	}
	if os.MkdirAll(filepath.Join(directory, "conf"), 0700) != nil {
		return errors.New("无法创建插件运行资源目录。")
	}
	zone, _ := base64.StdEncoding.DecodeString(defaultLocaltimeBase64)
	labels := map[string]string{embeddedOwnerLabel: worker.request.InstanceID, embeddedDirectoryLabel: worker.directoryLabel()}
	compose := map[string]any{"services": map[string]any{"proxy": map[string]any{
		"build": map[string]any{"context": ".", "dockerfile": "Dockerfile", "labels": labels},
		"image": "axe-aijiasu:" + worker.request.InstanceID, "container_name": worker.request.InstanceID,
		"entrypoint": []string{"sh", "-c", "trap 'exit 0' TERM INT; while :; do sleep 3600 & wait \"$!\"; done"},
		"init":       true, "restart": "unless-stopped", "labels": labels,
		"ports":        []string{fmt.Sprintf("127.0.0.1:%d:1080", worker.request.ProxyPort)},
		"environment":  map[string]string{"TZ": "Asia/Shanghai", "AIJIASU_DEFAULT_NODE": ""},
		"security_opt": []string{"no-new-privileges:true"}, "cap_add": []string{"NET_ADMIN"},
		"logging": map[string]any{"driver": "json-file", "options": map[string]string{"max-size": "10m", "max-file": "3"}},
	}}}
	composeBytes, _ := json.Marshal(compose)
	for _, asset := range []struct {
		name    string
		content []byte
		mode    os.FileMode
	}{
		{"compose.json", composeBytes, 0600}, {"Dockerfile", []byte(defaultDockerfile), 0600},
		{".dockerignore", []byte("*\n!Dockerfile\n!entrypoint.sh\n!conf/\n!conf/localtime\n"), 0600},
		{"entrypoint.sh", []byte(defaultEntrypointSH), 0700}, {"conf/localtime", zone, 0600},
	} {
		if err := embeddedWriteAsset(filepath.Join(directory, asset.name), asset.content, asset.mode); err != nil {
			return err
		}
	}
	return nil
}

func (worker embeddedWorker) lock() (*os.File, error) {
	path := filepath.Join(worker.request.DataDir, ".operation.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, errors.New("无法锁定插件实例。")
	}
	if syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		file.Close()
		return nil, errors.New("此爱加速实例正在操作，请稍后重试。")
	}
	return file, nil
}

func (worker embeddedWorker) compose(ctx context.Context, args ...string) error {
	prefix := []string{"compose", "--env-file", os.DevNull, "--project-directory", worker.request.DataDir,
		"--file", filepath.Join(worker.request.DataDir, "compose.json"), "--project-name", worker.request.InstanceID}
	_, err := worker.docker(ctx, 6*time.Minute, "", append(prefix, args...)...)
	return err
}

func (worker embeddedWorker) stop(ctx context.Context) error {
	_, err := worker.docker(ctx, 20*time.Second, "", "container", "stop", "--time", "10", worker.request.InstanceID)
	return err
}

func embeddedPause(ctx context.Context, duration time.Duration) error {
	select {
	case <-ctx.Done():
		return errors.New("爱加速操作已超时或取消，请检查实例状态。")
	case <-time.After(duration):
		return nil
	}
}

func embeddedNodeItems(output string) []embeddedNode {
	result := make([]embeddedNode, 0)
	seen := make(map[string]bool)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[1] == "ok" && embeddedNodeID.MatchString(fields[0]) && !seen[fields[0]] {
			name := strings.Join(fields[2:], " ")
			if len(name) > 160 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
				continue
			}
			seen[fields[0]] = true
			province, city := parseNodeLocation(name)
			result = append(result, embeddedNode{fields[0], name, province, city})
		}
	}
	return result
}

func embeddedNodes(output string) []string {
	items := embeddedNodeItems(output)
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.ID)
	}
	return result
}

func (worker embeddedWorker) availableNodes(ctx context.Context) ([]embeddedNode, error) {
	output, err := worker.containerExec(ctx, "", "ajiasu", "list")
	if err != nil {
		return nil, errors.New("无法读取爱加速节点列表，请确认实例已登录。")
	}
	return embeddedNodeItems(output), nil
}

func embeddedHasNode(nodes []embeddedNode, id string) bool {
	for _, node := range nodes {
		if node.ID == id {
			return true
		}
	}
	return false
}

func (worker embeddedWorker) liveNode(ctx context.Context) (string, error) {
	output, err := worker.containerExec(ctx, "", "ss", "-H", "-lntp")
	if err != nil {
		return "", errors.New("无法核验当前爱加速节点。")
	}
	processes := make(map[string]bool)
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, `"ajiasu"`) {
			continue
		}
		match := regexp.MustCompile(`"ajiasu",pid=([0-9]+)`).FindStringSubmatch(line)
		if len(match) == 2 {
			processes[match[1]] = true
		}
	}
	if len(processes) != 1 {
		return "", errors.New("当前爱加速节点无法确认，请检查实例状态。")
	}
	var pid string
	for value := range processes {
		pid = value
	}
	output, err = worker.containerExec(ctx, "", "ps", "-ww", "-p", pid, "-o", "args=")
	if err != nil {
		return "", errors.New("无法核验当前爱加速节点。")
	}
	fields := strings.Fields(output)
	if len(fields) != 3 || filepath.Base(fields[0]) != "ajiasu" || fields[1] != "connect" || !embeddedNodeID.MatchString(fields[2]) {
		return "", errors.New("当前爱加速节点无法确认，请检查实例状态。")
	}
	return fields[2], nil
}

func (worker embeddedWorker) start(ctx context.Context) (node, exitIP string, err error) {
	if err = worker.compose(ctx, "build", "proxy"); err != nil {
		return "", "", errors.New("爱加速镜像构建失败，请检查 Docker 和下载网络。")
	}
	defer func() {
		// Closing the desktop cancels its command, but must not stop browser proxy containers.
		if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
			cleanup, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			container, inspectErr := worker.inspect(cleanup)
			if inspectErr != nil {
				err = fmt.Errorf("%s 容器状态未能确认，请在插件中心重新检查。", err)
			} else if container != nil && container.State.Running && worker.stop(cleanup) != nil {
				err = fmt.Errorf("%s 容器停止未能确认，请在插件中心重新检查。", err)
			}
		}
	}()
	if err = worker.compose(ctx, "up", "--detach", "--no-build", "proxy"); err != nil {
		return "", "", errors.New("爱加速容器启动失败，请检查端口占用和 Docker 状态。")
	}
	container, err := worker.inspect(ctx)
	if err != nil || container == nil || !container.State.Running {
		return "", "", errors.New("爱加速容器未就绪或身份验证失败。")
	}
	// Credentials go through a pipe; the host never stores an .env or passes secrets in argv/env.
	credentials := "user " + worker.request.Username + "\npass " + worker.request.Password + "\nprotocol proxy\n"
	_, err = worker.containerExec(ctx, credentials, "sh", "-c", "set -e; umask 077; cat > /etc/ajiasu.conf; chmod 600 /etc/ajiasu.conf")
	if err != nil {
		return "", "", errors.New("无法配置爱加速账号，请检查容器状态。")
	}
	output, err := worker.containerExec(ctx, "", "ajiasu", "login")
	loginOK := false
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Login Result:") {
			value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Login Result:"))
			loginOK = strings.EqualFold(value, "success") || strings.EqualFold(value, "ok")
		}
	}
	if err != nil || !loginOK {
		return "", "", errors.New("爱加速登录失败，请检查账号密码和账号允许的并行数量。")
	}
	nodes, err := worker.availableNodes(ctx)
	if err != nil {
		return "", "", err
	}
	if worker.request.NodeID != "" {
		if !embeddedHasNode(nodes, worker.request.NodeID) {
			return "", "", errors.New("所选爱加速节点不在当前可用列表中，请刷新节点后重试。")
		}
		node = worker.request.NodeID
	} else {
		if len(nodes) < worker.request.InstanceIndex {
			return "", "", errors.New("可用节点不足，请减少并行数量或检查爱加速账号状态。")
		}
		node = nodes[worker.request.InstanceIndex-1].ID
	}
	exitIP, err = worker.connect(ctx, node)
	if err != nil {
		return "", "", err
	}
	return node, exitIP, nil
}

func (worker embeddedWorker) connect(ctx context.Context, node string) (string, error) {
	_, _ = worker.containerExec(ctx, "", "ajiasu", "disconnect")
	_, _ = worker.containerExec(ctx, "", "pkill", "-f", "^ajiasu connect")
	_, err := worker.containerExec(ctx, "", "sh", "-c", "nohup ajiasu connect \"$1\" >/tmp/ajiasu-connect.log 2>&1 </dev/null &", "sh", node)
	if err != nil {
		return "", errors.New("爱加速节点连接启动失败。")
	}
	var output string
	port := 18888
	for attempt := 0; attempt < 24; attempt++ {
		output, _ = worker.containerExec(ctx, "", "ss", "-H", "-lntp")
		found := false
		for _, line := range strings.Split(output, "\n") {
			if !strings.Contains(line, `"ajiasu"`) {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			_, text, parseErr := net.SplitHostPort(fields[3])
			if parseErr != nil {
				continue
			}
			value, parseErr := strconv.Atoi(text)
			if parseErr == nil && value > 0 && value <= 65535 {
				port, found = value, true
				break
			}
		}
		if found {
			break
		}
		if err = embeddedPause(ctx, 250*time.Millisecond); err != nil {
			return "", err
		}
	}
	_, _ = worker.containerExec(ctx, "", "pkill", "-f", "^socat TCP-LISTEN:1080")
	if port != 1080 {
		_, err = worker.containerExec(ctx, "", "sh", "-c", "nohup socat TCP-LISTEN:1080,fork,reuseaddr,bind=0.0.0.0 TCP:127.0.0.1:\"$1\" >/dev/null 2>&1 </dev/null &", "sh", strconv.Itoa(port))
		if err != nil {
			return "", errors.New("爱加速代理端口映射失败。")
		}
	}
	connected := false
	for attempt := 0; attempt < 24; attempt++ {
		if worker.probe("127.0.0.1", worker.request.ProxyPort, time.Second) == nil {
			connected = true
			break
		}
		if err = embeddedPause(ctx, 300*time.Millisecond); err != nil {
			return "", err
		}
	}
	if !connected {
		return "", errors.New("爱加速 SOCKS5 代理尚未就绪，请检查节点或账号并行限制。")
	}
	current, err := worker.liveNode(ctx)
	if err != nil || current != node {
		return "", errors.New("爱加速当前节点与所选节点不一致，请检查实例状态。")
	}
	if err = worker.saveConnection(node); err != nil {
		return "", err
	}
	return worker.exitIP(ctx), nil
}

func (worker embeddedWorker) exitIP(ctx context.Context) string {
	output, err := worker.containerExec(ctx, "", "curl", "--silent", "--fail", "--max-time", "8", "--proxy", "socks5h://127.0.0.1:1080", "https://api.ipify.org")
	if address := net.ParseIP(strings.TrimSpace(output)); err == nil && address != nil {
		return address.String()
	}
	return ""
}

func (worker embeddedWorker) saveConnection(node string) error {
	path := filepath.Join(worker.request.DataDir, ".connection.json")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("插件连接记录不是普通文件，无法保存节点信息。")
	}
	content, _ := json.Marshal(map[string]string{"nodeId": node})
	file, err := os.CreateTemp(worker.request.DataDir, ".connection-*")
	if err != nil {
		return errors.New("无法保存插件节点记录，请检查目录权限。")
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(content)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil || os.Rename(file.Name(), path) != nil {
		return errors.New("无法保存插件节点记录，请检查目录权限。")
	}
	return nil
}

func (worker embeddedWorker) lastNode() string {
	path := filepath.Join(worker.request.DataDir, ".connection.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return ""
	}
	content, err := os.ReadFile(path)
	var record map[string]string
	if err != nil || json.Unmarshal(content, &record) != nil || len(record) != 1 || !embeddedNodeID.MatchString(record["nodeId"]) {
		return ""
	}
	return record["nodeId"]
}

func (worker embeddedWorker) execute(ctx context.Context) (*embeddedInstance, error) {
	instance, _, err := worker.executeWithNodes(ctx)
	return instance, err
}

func (worker embeddedWorker) executeWithNodes(ctx context.Context) (*embeddedInstance, []embeddedNode, error) {
	request := worker.request
	result := &embeddedInstance{ID: request.InstanceID, Status: "stopped", Proxy: fmt.Sprintf("socks5://127.0.0.1:%d", request.ProxyPort)}
	if err := embeddedSafePath(request.DataDir); err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(request.DataDir); os.IsNotExist(err) && request.Action != "start" {
		if request.Action == "nodes" || request.Action == "switch" {
			return nil, nil, errors.New("爱加速服务尚未启动，请先启动插件实例。")
		}
		return result, nil, nil
	}
	if request.Action == "start" {
		if err := worker.prepare(); err != nil {
			return nil, nil, err
		}
	} else if err := embeddedWriteAssetReadOnly(filepath.Join(request.DataDir, ".instance.json"), worker.record()); err != nil {
		return nil, nil, err
	}
	lock, err := worker.lock()
	if err != nil {
		return nil, nil, err
	}
	defer lock.Close()
	container, err := worker.inspect(ctx)
	if err != nil {
		return nil, nil, err
	}
	if request.Action == "stop" {
		if container != nil && container.State.Running {
			if err := worker.stop(ctx); err != nil {
				return nil, nil, err
			}
		}
		return result, nil, nil
	}
	if request.Action == "status" || request.Action == "nodes" {
		if container != nil && container.State.Running {
			result.Status = "error"
			if worker.probe("127.0.0.1", request.ProxyPort, time.Second) == nil {
				if node, err := worker.liveNode(ctx); err == nil {
					result.Status = "running"
					result.NodeID, result.ExitIP = node, worker.exitIP(ctx)
				}
			}
		}
		if request.Action == "nodes" {
			if container == nil || !container.State.Running {
				return nil, nil, errors.New("爱加速服务尚未启动，请先启动插件实例。")
			}
			nodes, err := worker.availableNodes(ctx)
			return result, nodes, err
		}
		return result, nil, nil
	}
	if request.Action == "switch" {
		if container == nil || !container.State.Running {
			return nil, nil, errors.New("爱加速服务尚未启动，请先启动插件实例。")
		}
		nodes, err := worker.availableNodes(ctx)
		if err != nil {
			return nil, nil, err
		}
		if !embeddedHasNode(nodes, request.NodeID) {
			return nil, nil, errors.New("所选爱加速节点不在当前可用列表中，请刷新节点后重试。")
		}
		result.ExitIP, err = worker.connect(ctx, request.NodeID)
		if err != nil {
			return nil, nil, err
		}
		result.Status, result.NodeID = "running", request.NodeID
		return result, nil, nil
	}
	result.NodeID, result.ExitIP, err = worker.start(ctx)
	if err != nil {
		return nil, nil, err
	}
	result.Status = "running"
	return result, nil, nil
}

func embeddedWriteAssetReadOnly(path string, content []byte) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return errors.New("插件实例没有有效的归属记录，已拒绝接管。")
	}
	previous, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(previous, content) {
		return errors.New("插件实例归属或端口不匹配，已拒绝接管。")
	}
	return nil
}

func embeddedMain(input io.Reader, output io.Writer) {
	response := embeddedResponse{Protocol: embeddedProtocol}
	request, err := decodeEmbedded(input)
	if err == nil {
		signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		timeout := 35 * time.Second
		if request.Action == "start" {
			timeout = 9 * time.Minute
		} else if request.Action == "switch" {
			timeout = 90 * time.Second
		}
		ctx, cancel := context.WithTimeout(signalContext, timeout)
		defer cancel()
		worker := embeddedWorker{request: request, run: runEmbeddedDocker, probe: verifySocks5}
		var nodes []embeddedNode
		response.Instance, nodes, err = worker.executeWithNodes(ctx)
		if request.Action == "nodes" && err == nil {
			response.Nodes = &nodes
		}
	}
	if err != nil {
		response.Error = err.Error()
	} else {
		response.OK = true
	}
	_ = json.NewEncoder(output).Encode(response)
}
