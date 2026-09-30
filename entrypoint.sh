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
    echo "[aijiasu-docker] 正在连接默认节点: $AIJIASU_DEFAULT_NODE..."
    ajiasu connect "$AIJIASU_DEFAULT_NODE" || true
fi

# 动态端口桥接同步函数
sync_bridge() {
    local target_port="${1:-18888}"
    local detected_port
    detected_port=$(ss -H -lntp 2>/dev/null | grep -E '"ajiasu"' | awk '{print $4}' | grep -oE '[0-9]+$' | grep -v '^1080$' | head -n1 || true)
    
    if [ -n "$detected_port" ]; then
        target_port="$detected_port"
    fi

    if ps -ef | grep -v grep | grep -q "socat TCP-LISTEN:1080.*TCP:127.0.0.1:$target_port"; then
        return 0
    fi

    echo "[aijiasu-docker] 配置端口转发: 0.0.0.0:1080 -> 127.0.0.1:$target_port..."
    pkill -f 'socat TCP-LISTEN:1080' || true
    sleep 0.1
    socat TCP-LISTEN:1080,fork,reuseaddr,bind=0.0.0.0 TCP:127.0.0.1:$target_port &
}

# 初始建立桥接
sync_bridge 18888

# 启动后台守护：监控内部实际端口变动并自愈（兼容 18888、28888、38888 等动态端口）
(
    while true; do
        sleep 3
        current_port=$(ss -H -lntp 2>/dev/null | grep -E '"ajiasu"' | awk '{print $4}' | grep -oE '[0-9]+$' | grep -v '^1080$' | head -n1 || true)
        if [ -n "$current_port" ]; then
            if ! ps -ef | grep -v grep | grep -q "socat TCP-LISTEN:1080.*TCP:127.0.0.1:$current_port"; then
                echo "[aijiasu-docker] 检测到爱加速内部端口变更为 $current_port，正在重定向 1080 桥接..."
                pkill -f 'socat TCP-LISTEN:1080' || true
                sleep 0.1
                socat TCP-LISTEN:1080,fork,reuseaddr,bind=0.0.0.0 TCP:127.0.0.1:$current_port &
            fi
        fi
    done
) &

echo "[aijiasu-docker] 爱加速 Docker 代理服务已就绪，端口动态自适应守护已启动。"

# 持续前台阻塞
exec tail -f /dev/null
