package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// isMissingDockerAssets 检查当前目录是否缺少 Docker 运行所必需的核心配置文件
func isMissingDockerAssets() bool {
	dir := getProjectDir()
	checkFiles := []string{
		filepath.Join(dir, "docker-compose.yml"),
		filepath.Join(dir, "Dockerfile"),
		filepath.Join(dir, "entrypoint.sh"),
		filepath.Join(dir, "conf", "localtime"),
	}

	for _, f := range checkFiles {
		info, err := os.Stat(f)
		if err != nil || !info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// promptAndBootstrapAssets 交互式询问用户是否生成 Docker 资产 (输入 y 生成，否则退出；已有文件绝不覆盖)
func promptAndBootstrapAssets() error {
	if !isMissingDockerAssets() {
		return nil
	}

	// 支持环境变量跳过询问 (适合 CI / 自动化测试)
	if os.Getenv("AIJIASU_ASSUME_YES") == "true" || os.Getenv("AIJIASU_ASSUME_YES") == "1" {
		return generateBootstrapAssets()
	}

	fmt.Println("\n⚠️  [配置缺失] 检测到当前目录缺少 Docker 运行配置文件 (docker-compose.yml, Dockerfile, entrypoint.sh 等)。")
	fmt.Print("❓ 是否立即自动生成全套 Docker 环境配置文件？[y/N]: ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println("\n已取消操作，退出程序。")
		os.Exit(0)
	}

	ans := strings.TrimSpace(strings.ToLower(input))
	if ans != "y" && ans != "yes" {
		fmt.Println("已取消生成，退出程序。")
		os.Exit(0)
	}

	fmt.Println()
	return generateBootstrapAssets()
}

// generateBootstrapAssets 释放内置资产；仅生成缺失文件，已有文件绝对不覆盖
func generateBootstrapAssets() error {
	dir := getProjectDir()

	composePath := filepath.Join(dir, "docker-compose.yml")
	dockerfilePath := filepath.Join(dir, "Dockerfile")
	entrypointPath := filepath.Join(dir, "entrypoint.sh")
	localtimePath := filepath.Join(dir, "conf", "localtime")
	envExamplePath := filepath.Join(dir, ".env.example")

	fmt.Println("➜ [自举初始化] 正在释放内置 Docker 环境配置文件...")

	// 1. 生成 docker-compose.yml (已有则不覆盖)
	if info, err := os.Stat(composePath); os.IsNotExist(err) {
		if err := os.WriteFile(composePath, []byte(defaultComposeYAML), 0644); err != nil {
			return fmt.Errorf("生成 %s 失败: %w", composePath, err)
		}
		fmt.Println("  • 已自动生成: docker-compose.yml")
	} else if err != nil {
		return fmt.Errorf("检查 %s 失败: %w", composePath, err)
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("%s 已存在但不是普通文件", composePath)
	} else {
		fmt.Println("  • 已存在保持原样: docker-compose.yml (未覆盖)")
	}

	// 2. 生成 Dockerfile (已有则不覆盖)
	if info, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
		if err := os.WriteFile(dockerfilePath, []byte(defaultDockerfile), 0644); err != nil {
			return fmt.Errorf("生成 %s 失败: %w", dockerfilePath, err)
		}
		fmt.Println("  • 已自动生成: Dockerfile")
	} else if err != nil {
		return fmt.Errorf("检查 %s 失败: %w", dockerfilePath, err)
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("%s 已存在但不是普通文件", dockerfilePath)
	} else {
		fmt.Println("  • 已存在保持原样: Dockerfile (未覆盖)")
	}

	// 3. 生成 entrypoint.sh (已有则不覆盖，必须带 0755 可执行权限)
	if info, err := os.Stat(entrypointPath); os.IsNotExist(err) {
		if err := os.WriteFile(entrypointPath, []byte(defaultEntrypointSH), 0755); err != nil {
			return fmt.Errorf("生成 %s 失败: %w", entrypointPath, err)
		}
		fmt.Println("  • 已自动生成: entrypoint.sh (已赋予执行权限)")
	} else if err != nil {
		return fmt.Errorf("检查 %s 失败: %w", entrypointPath, err)
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("%s 已存在但不是普通文件", entrypointPath)
	} else {
		fmt.Println("  • 已存在保持原样: entrypoint.sh (未覆盖)")
	}

	// 4. 生成 conf/localtime 时区文件 (已有则不覆盖)
	if info, err := os.Stat(localtimePath); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(localtimePath), 0755); err != nil {
			return fmt.Errorf("创建 %s 所在目录失败: %w", localtimePath, err)
		}
		tzBytes, err := base64.StdEncoding.DecodeString(defaultLocaltimeBase64)
		if err != nil {
			return fmt.Errorf("解码 %s 失败: %w", localtimePath, err)
		}
		if err := os.WriteFile(localtimePath, tzBytes, 0644); err != nil {
			return fmt.Errorf("生成 %s 失败: %w", localtimePath, err)
		}
		fmt.Println("  • 已自动生成: conf/localtime (Asia/Shanghai 时区文件)")
	} else if err != nil {
		return fmt.Errorf("检查 %s 失败: %w", localtimePath, err)
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("%s 已存在但不是普通文件", localtimePath)
	} else {
		fmt.Println("  • 已存在保持原样: conf/localtime (未覆盖)")
	}

	// 5. 生成 .env.example (已有则不覆盖)
	if info, err := os.Stat(envExamplePath); os.IsNotExist(err) {
		if err := os.WriteFile(envExamplePath, []byte(defaultEnvExample), 0644); err != nil {
			return fmt.Errorf("生成 %s 失败: %w", envExamplePath, err)
		}
		fmt.Println("  • 已自动生成: .env.example (配置模板)")
	} else if err != nil {
		return fmt.Errorf("检查 %s 失败: %w", envExamplePath, err)
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("%s 已存在但不是普通文件", envExamplePath)
	}

	fmt.Println("➜ [自举完成] 核心运行资产已就绪！")
	return nil
}

// 内置标准 docker-compose.yml 内容
const defaultComposeYAML = `services:
  aijiasu-runner:
    build:
      context: .
      dockerfile: Dockerfile
    image: aijiasu-runner:latest
    container_name: ${AIJIASU_CONTAINER_NAME:-aijiasu-runner}
    init: true
    restart: ${AIJIASU_RESTART:-unless-stopped}
    ports:
      - "${AIJIASU_BIND_IP:-127.0.0.1}:${AIJIASU_PROXY_PORT:-1080}:1080"
    environment:
      - TZ=${TZ:-Asia/Shanghai}
      - AIJIASU_DEFAULT_NODE=${AIJIASU_DEFAULT_NODE:-}
    env_file:
      - path: .env
        required: false
    volumes:
      - ./conf/localtime:/etc/localtime:ro
      - ./conf/localtime:/usr/share/zoneinfo/Asia/Shanghai:ro
    security_opt:
      - no-new-privileges:true
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "3"
    cap_add:
      - NET_ADMIN
`

// 内置标准 Dockerfile 内容 (支持 ARM64 / AMD64 双架构自适应构建)
const defaultDockerfile = `FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive
ENV TZ=Asia/Shanghai
ENV LANG=C.UTF-8
ENV LC_ALL=C.UTF-8

RUN apt-get update && apt-get install -y --no-install-recommends \
    wget \
    ca-certificates \
    socat \
    procps \
    curl \
    iproute2 \
    coreutils \
    && rm -rf /var/lib/apt/lists/*

COPY conf/localtime /etc/localtime
RUN echo "Asia/Shanghai" > /etc/timezone

WORKDIR /app

# 根据系统架构自动拉取对应架构的官方 4.2.3.0 客户端
RUN ARCH=$(uname -m) && \
    if [ "$ARCH" = "aarch64" ] || [ "$ARCH" = "arm64" ]; then \
      DOWNLOAD_URL="https://www.91ajs.com/files/downloads/linux/ajiasu-aarch64-4.2.3.0.tar.gz"; \
    else \
      DOWNLOAD_URL="https://www.91ajs.com/files/downloads/linux/ajiasu-amd64-4.2.3.0.tar.gz"; \
    fi && \
    echo "Downloading aijiasu for $ARCH from $DOWNLOAD_URL" && \
    wget "$DOWNLOAD_URL" -O ajiasu.tar.gz && \
    tar -zxvf ajiasu.tar.gz && \
    mv ajiasu /usr/local/bin/ajiasu && \
    chmod +x /usr/local/bin/ajiasu && \
    rm ajiasu.tar.gz

COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

EXPOSE 1080
ENTRYPOINT ["/app/entrypoint.sh"]
`

// 内置标准 entrypoint.sh 脚本内容
const defaultEntrypointSH = `#!/usr/bin/env bash
set -e

# 确保中国标准时区 (Asia/Shanghai / CST-8) 生效
if [ ! -f /usr/share/zoneinfo/Asia/Shanghai ] && [ -f /etc/localtime ]; then
    mkdir -p /usr/share/zoneinfo/Asia
    cp /etc/localtime /usr/share/zoneinfo/Asia/Shanghai
fi
export TZ="Asia/Shanghai"

echo "[aijiasu-docker] 启动爱加速 Docker 容器 (系统时间: $(date -R))..."

# 优雅退出信号捕获
cleanup() {
    echo "[aijiasu-docker] 捕获到终止信号，正在清理断开连接..."
    ajiasu disconnect || true
    pkill -f socat || true
    exit 0
}
trap cleanup SIGINT SIGTERM

# 检查是否存在用户名密码环境变量 (向后兼容)
if [ -n "$AIJIASU_USER" ] && [ -n "$AIJIASU_PASS" ]; then
    echo "[aijiasu-docker] 正在初始化 /etc/ajiasu.conf..."
    cat <<EOF > /etc/ajiasu.conf
user $AIJIASU_USER
pass $AIJIASU_PASS
protocol proxy
EOF
    chmod 600 /etc/ajiasu.conf
    # 立即抹除容器内存中的敏感环境变量
    unset AIJIASU_USER AIJIASU_PASS
fi

if [ -f /etc/ajiasu.conf ]; then
    chmod 600 /etc/ajiasu.conf
    echo "[aijiasu-docker] 正在执行爱加速自动登录..."
    ajiasu login || echo "[aijiasu-docker] 自动登录返回非零，可能需核对账号密码"
else
    echo "[aijiasu-docker] 容器待命中，等待调度宿主端通过安全内存管道注入凭据..."
fi

# 检查是否指定了开机自动连接的节点
if [ -n "$AIJIASU_DEFAULT_NODE" ]; then
    echo "[aijiasu-docker] 检测到配置了开机默认节点，正在连接: $AIJIASU_DEFAULT_NODE..."
    ajiasu connect "$AIJIASU_DEFAULT_NODE" || true
else
    echo "[aijiasu-docker] 未配置默认节点 (AIJIASU_DEFAULT_NODE 留空)，容器保持待命状态，等待按需连接..."
fi

# 检查并探测实际内部端口 (默认 18888，兼容 1080、28888、38888 等)
INTERNAL_PORT=$(ss -H -lntp 2>/dev/null | grep -E '"ajiasu"' | awk '{print $4}' | grep -oE '[0-9]+$' | head -n1 || true)
if [ -z "$INTERNAL_PORT" ]; then
    INTERNAL_PORT=18888
fi

if [ "$INTERNAL_PORT" != "1080" ]; then
    echo "[aijiasu-docker] 启动 SOCKS5 代理端口桥接: 0.0.0.0:1080 -> 127.0.0.1:$INTERNAL_PORT..."
    pkill -f 'socat TCP-LISTEN:1080' || true
    socat TCP-LISTEN:1080,fork,reuseaddr,bind=0.0.0.0 TCP:127.0.0.1:$INTERNAL_PORT &
else
    echo "[aijiasu-docker] 检测到爱加速内部直接监听 1080 端口，无需额外的 socat 桥接。"
fi

echo "[aijiasu-docker] 爱加速 Docker 代理服务已就绪 (对外监听端口 1080)。"

# 持续前台阻塞
while true; do
    sleep 86400 &
    wait $!
done
`

// 内置标准 .env.example 模板内容
const defaultEnvExample = `# ==============================================================================
# 爱加速 Docker 代理服务核心配置 (.env)
# ==============================================================================

# 1. 爱加速账号认证 (必填，也可以在启动后通过 Web API /login 或 CLI 动态登录)
AIJIASU_USER=13800000000
AIJIASU_PASS=your_password

# 2. 宿主机映射的 SOCKS5 代理端口 (默认 1080，如冲突可自定义为 1090, 21080 等)
AIJIASU_PROXY_PORT=1080

# 3. 代理监听绑定的 IP (默认 127.0.0.1 仅本机可用；设为 0.0.0.0 可供局域网内其他设备连接)
AIJIASU_BIND_IP=127.0.0.1

# 4. 可选：Web JSON API 服务端口 (默认 1081；注意：API 服务默认不启动，仅在执行 aijiasu serve 时按需开启)
# AIJIASU_HTTP_PORT=1081

# 5. 可选：容器启动时自动连接的默认节点 (默认留空，留空表示开机不自动连接任何节点，保持纯待命状态)
# AIJIASU_DEFAULT_NODE=上海 #1

# 6. 可选：Docker 容器自定义名称 (默认 aijiasu-runner)
# AIJIASU_CONTAINER_NAME=aijiasu-runner

# 7. 可选：容器重启策略 (默认 unless-stopped；详见 README 说明；可选 no / always / on-failure)
# AIJIASU_RESTART=unless-stopped
`

// 内置 conf/localtime 中国时区二进制文件的 base64 字符串
const defaultLocaltimeBase64 = `VFppZjIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAdAAAAAwAAAAyAAAAAoJeigKF5BPDIWV6AyQn5cMnTvQDLBYrwy3xAANI7PvDTi3uA1EKt8NVFIgDWTL/w1zy/ANgGZnDZHfKA2UF88B66UiAfaZuQIH6EoCFJfZAiZ6EgIylfkCRHgyAlEnwQJidlICbyXhAoB0cgKNJAEAIBAgECAQIBAgECAQIBAgECAQIBAgECAQIBAgECAABx1wAAAAB+kAEEAABwgAAITE1UAENEVABDU1QAVFppZjIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAdAAAAAwAAAAz/////fjZDKf////+gl6KA/////6F5BPD/////yFlegP/////JCflw/////8nTvQD/////ywWK8P/////LfEAA/////9I7PvD/////04t7gP/////UQq3w/////9VFIgD/////1ky/8P/////XPL8A/////9gGZnD/////2R3ygP/////ZQXzwAAAAAB66UiAAAAAAH2mbkAAAAAAgfoSgAAAAACFJfZAAAAAAImehIAAAAAAjKV+QAAAAACRHgyAAAAAAJRJ8EAAAAAAmJ2UgAAAAACbyXhAAAAAAKAdHIAAAAAAo0kAQAgECAQIBAgECAQIBAgECAQIBAgECAQIBAgECAQIAAHHXAAAAAH6QAQQAAHCAAAhMTVQAQ0RUAENTVAAKQ1NULTgK`

// ensureBootstrapAssets 保持向后兼容，委托给 generateBootstrapAssets
func ensureBootstrapAssets() error {
	return generateBootstrapAssets()
}
