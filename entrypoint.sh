#!/usr/bin/env bash
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
