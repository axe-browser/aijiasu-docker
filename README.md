# 爱加速 (Aijiasu) Docker 独立代理服务 (精简高可用版)

## 斧头浏览器插件模式

桌面端可使用 `aijiasu embedded` 托管多个独立代理实例。此模式仅通过标准输入/输出通信，不启动 HTTP 服务，不读取或保存宿主机 `.env`，不会接管普通 CLI 创建的容器。原有 CLI 和 HTTP 接口保持原有行为。

宿主向 stdin 写入一个 JSON 对象并关闭输入；插件输出唯一 JSON 响应并退出。协议是 `aijiasu-stdio-v1`：

```json
{
  "protocol": "aijiasu-stdio-v1",
  "action": "start",
  "dataDir": "/absolute/private/runtime/1",
  "instanceId": "axe-aijiasu-123456789abc-1",
  "instanceIndex": 1,
  "proxyPort": 21081,
  "dockerPath": "/absolute/path/to/docker",
  "username": "YOUR_ACCOUNT",
  "password": "YOUR_PASSWORD"
}
```

- `action` 支持 `start`、`status`、`stop`、`nodes`、`switch`；账号密码只允许出现在 `start` 中。凭据通过管道传递，不能放入命令行参数、环境变量或日志。
- 宿主须先确认 Docker daemon 为本机实例，将经过验证的 `DOCKER_HOST=unix:///绝对路径` 传入插件；插件保留该连接且不向 Docker 传递 `DOCKER_CONTEXT`，拒绝未确认或远程连接。Docker Desktop 未运行时由桌面宿主负责启动并等待就绪。
- `dataDir` 是该实例专属的绝对目录；首次启动只能使用不存在或空目录。目录标记、Compose 项目、容器名称、归属 label 和本机端口必须匹配，否则拒绝接管。目录路径不能包含符号链接。
- `instanceIndex` 为 1–20，必须与 `instanceId` 后缀一致；这是桌面的资源上限，不代表账号许可的并发数。`start` 可附带 `nodeId` 精确选择节点；未指定时仍选择节点列表中的对应项。所选节点不可用、节点不足或账号登录失败时明确报错。节点列表可能变化，多个节点也不保证出口 IP 不同。
- `start` 非交互生成自己的 Docker 资源，构建镜像、启动容器、通过 `docker exec -i` 向容器内权限为 `0600` 的配置注入凭据并登录，再连接节点。托管容器使用单独的待命入口，不执行普通 CLI 启动脚本的自动登录/默认连接。只有 SOCKS5 握手成功才返回 `running`。普通启动失败或超时后尝试停止已验证归属的容器，并报告无法确认清理的情况。
- 关闭桌面导致的 SIGTERM/取消只终止当前 Docker 控制命令，保留已创建的代理容器，避免中断浏览器环境；下次打开可通过 `status` 检查。启动尚未完成时被取消的容器可能处于未连接状态，须重新启动或显式停止。
- `nodes` 查询已启动并登录实例的实时可用节点，返回 `nodes:[{id,name,province,city}]`；未启动时明确报错。`switch` 必须提供完整的 `nodeId`，只连接当前列表中 ID 完全匹配的节点，成功后返回更新的 `instance`。这两项操作不接受账号密码。
- `status` 仅检查实例与代理健康，通过容器内监听进程的命令行核验当前节点，健康时通过该代理查询出口 IP；不会启动容器或修复桥接。当前节点无法核实时返回 `status:error`，不会将历史记录当作当前节点。`stop` 只停止此实例容器，保留运行数据、镜像及其他 Docker 容器。不存在的实例返回 `stopped`。
- Docker 命令有限时和输出大小限制；`start` 总限时 9 分钟，`switch` 90 秒，其他操作 35 秒。协议错误以 `ok:false` 和安全中文消息返回，进程正常退出 `0`；不可将退出 `0` 单独视为操作成功。

成功响应：

```json
{"protocol":"aijiasu-stdio-v1","ok":true,"instance":{"id":"axe-aijiasu-123456789abc-1","status":"running","proxy":"socks5://127.0.0.1:21081","exitIp":"203.0.113.10","nodeId":"vvn-example"}}
```

`nodes` 成功响应沿用同一协议，并附带节点列表；`switch` 使用上面的实例响应格式：

```json
{"protocol":"aijiasu-stdio-v1","ok":true,"instance":{"id":"axe-aijiasu-123456789abc-1","status":"running","proxy":"socks5://127.0.0.1:21081","exitIp":"203.0.113.10","nodeId":"vvn-example"},"nodes":[{"id":"vvn-example","name":"广东广州 #01","province":"广东","city":"广州"}]}
```

失败响应：

```json
{"protocol":"aijiasu-stdio-v1","ok":false,"error":"爱加速登录失败，请检查账号密码和账号允许的并行数量。"}
```

插件控制器可离线安装，但首次启动仍需 Docker CLI、Compose、运行中的本机 daemon，以及下载基础镜像和官方 Linux 客户端所需的网络。此模式不会自动安装 Docker。

在本项目构建独立发布包（不会运行 Docker、登录或连接节点）：

```sh
python3 tools/build_provider.py
# 输出 dist/provider/darwin-arm64/{manifest.json,aijiasu}
go test ./cmd/aijiasu -run '^TestEmbedded' -count=1
```

发布脚本先构建 macOS arm64 二进制，再执行 ad-hoc 签名和验证，最后计算 SHA-256 写入清单。桌面仓库只接收该发布目录并校验固定清单/哈希，不导入本项目源码，也不依赖相邻 checkout 的路径。发布到其他电脑前，发行方应接入自己的正式签名和发布流程；已有产物目录不会被脚本覆盖，可通过 `--output` 指定新的目录。

本项目是将**爱加速 (Aijiasu)** 官方 Linux 客户端容器化封装的独立 SOCKS5 代理服务。管理端采用 **Go 原生独立二进制工具**，砍掉了臃肿的端口自动嗅探与复杂多端口映射机制，返璞归真，专注于**高可用、低延迟、省市节点精准切换与智能去重**。

支持 ARM64 (Apple Silicon Mac) 与 AMD64 (x86_64 Linux/Windows) 双架构，对外提供统一稳定的本地 SOCKS5 代理端口（`127.0.0.1:1080`），供自动化脚本、数据采集、爬虫及各类业务服务无感连接。

---

## 架构概览

```
[ 业务服务 / 爬虫 / 脚本 / cURL ]
                 │
                 ▼ (SOCKS5 协议: 127.0.0.1:1080)
┌────────────────────────────────────────────────────────┐
│ Docker 容器 (aijiasu-runner)                            │
│                                                        │
│   轻量自适应端口桥接:                                   │
│   0.0.0.0:1080 ────────────► 127.0.0.1:[内部实际端口]   │
│                              (兼容 1080/18888/28888等) │
│                                   │                    │
│                                   ▼                    │
│                          爱加速 Linux 客户端 (Proxy)    │
└───────────────────────────────────┼────────────────────┘
                                    │ (加密代理专线)
                                    ▼
                            [ 全国静态节点网络 ]
```

- **对外代理端口**：固定为 `127.0.0.1:1080` (标准 SOCKS5 协议，免密直连)。
- **自适应内部端口兼容**：底层自动探测爱加速客户端实际监听端口（兼容默认 `18888`、残留占用的 `28888`/`38888` 或直接监听的 `1080`），单次自适应桥接，无需常驻后台守护，零冗余开销。
- **核心控制端**：独立 Go 原生命令行管理工具 `./aijiasu` 与轻量 HTTP 切换 API。

---

## 核心特性

1. **底层端口变动无感自适应**：
   - 彻底砍掉了复杂的后台轮询守护进程与虚拟多端口分发器；
   - 切换节点时自动探知底层实际监听的本地端口（`18888`、`28888`、`1080` 等），毫秒级热桥接，保证宿主机 `127.0.0.1:1080` 始终畅通。
2. **精准切换接口**：
   - **按省份切换**：指定省份（如广东、山东、浙江等），在省内节点中智能挑选。
   - **按城市切换**：指定城市（如广州、深圳、枣庄、杭州等），精准定位城市节点。
   - **按特定节点直连**：支持输入具体节点全称（如 `上海 #116`）或节点 ID（`vvn-xxxx`）。
   - **全局随机轮换**：不传参数时从全国可用节点中随机选取。
3. **智能去重防重复**：
   - 记录历史已用节点，每次切换优先从未使用的候选节点中挑选，**彻底避免频繁重复命中同一个节点/IP**。
   - 当某省份/城市或全部节点被完整使用一轮后，**自动重置该分类历史**并开启新一轮轮换（同时避开上一个刚使用的节点）。
   - 支持一键清空去重历史。
4. **CLI + HTTP 双模支持**：
   - 既可通过终端命令行 `./aijiasu switch` 一键换 IP；
   - 也支持运行 `./aijiasu serve` 启动轻量本地 HTTP API，供各类爬虫或后台服务直接通过 `GET /switch` 发起换 IP 请求。
5. **单一二进制自举开箱即用 (Zero-Dependency Self-Bootstrapping)**：
   - 全套 Docker 运行资产（`docker-compose.yml`、`Dockerfile`、`entrypoint.sh`、时区文件等）全部内置于单个 `./aijiasu` 二进制中；
   - **交互式安全确认**：在全新目录下执行 `./aijiasu up` 时，若检测到缺失 Docker 文件，会自动交互提示用户确认 `[y/N]`，用户输入 `y` 才会生成并启动，输入其他则安全退出；
   - **绝对不覆盖已有配置**：若当前目录已有配置文件，直接复用启动，绝不会覆盖用户的任何自定义修改。

---

## 目录结构

```
aijiasu-docker/
├── aijiasu                 # Go 编译生成的原生独立二进制工具 (直接运行)
├── Dockerfile              # 双架构轻量 Dockerfile (Ubuntu 22.04 + ajiasu + socat)
├── docker-compose.yml      # Docker Compose 服务配置
├── entrypoint.sh           # 容器入口：自动登录、固定端口转发与待命
├── .env.example            # 环境变量模板
├── .env                    # 账号密码配置文件 (请勿提交到 Git)
├── Makefile                # 一键编译命令
├── go.mod                  # Go 模块描述
├── conf/
│   └── localtime           # Asia/Shanghai 中国时区文件
└── cmd/
    └── aijiasu/
        ├── main.go         # 核心控制端源码 (含省市映射、去重算法、CLI与HTTP API)
        └── main_test.go    # 单元测试 (覆盖省市识别与去重算法)
```

---

## 快速上手

### 1. 配置账号与自定义参数

复制 `.env.example` 为 `.env`：

```bash
cp .env.example .env
```

编辑 `.env`（支持自定义代理端口、绑定 IP 与 Web 服务端口等）：
```env
# 1. 账号与密码 (必填或通过 API/CLI 登录)
AIJIASU_USER=你的手机号/账号
AIJIASU_PASS=你的密码

# 2. 宿主机 SOCKS5 代理端口 (默认 1080，可自定义为 1090、21080 等避免端口冲突)
AIJIASU_PROXY_PORT=1090

# 3. 宿主机代理监听绑定 IP (默认 127.0.0.1 仅本机可用；设为 0.0.0.0 供局域网其他机器使用)
AIJIASU_BIND_IP=127.0.0.1

# 4. Web JSON API 服务端口 (默认 1081)
AIJIASU_HTTP_PORT=1081
```

| 环境变量名 | 默认值 | 作用说明 |
|---|---|---|
| `AIJIASU_USER` | - | 爱加速登录账号 / 手机号 |
| `AIJIASU_PASS` | - | 爱加速登录密码 |
| `AIJIASU_PROXY_PORT` | `1080` | 宿主机 SOCKS5 代理暴露端口 (如 `1090`) |
| `AIJIASU_BIND_IP` | `127.0.0.1` | 宿主机代理绑定的 IP 地址 (如 `0.0.0.0`) |
| `AIJIASU_HTTP_PORT` | `1081` | Web JSON API 接口服务监听端口 |
| `AIJIASU_CONTAINER_NAME` | `aijiasu-runner` | 自定义 Docker 容器名称 |
| `AIJIASU_DEFAULT_NODE` | 留空 | 开机默认节点 (留空表示纯待命，不自动连接任何节点) |
| `AIJIASU_RESTART` | `unless-stopped` | 容器重启策略 (默认推荐 `unless-stopped`) |

#### 深度说明 1：默认节点与待命机制 (`AIJIASU_DEFAULT_NODE`)
- **开机不自动连接（默认行为）**：
  如果 `AIJIASU_DEFAULT_NODE` 保持留空（未配置），容器启动后**绝对不会自动连接任何节点**，保持纯净待命状态，无任何公网流量消耗。
- **按需连接**：
  由前端、爬虫或业务脚本通过 Web API（`POST /switch`）或命令行（`./aijiasu switch`）按需触发切换。
- **固定初始节点（可选）**：
  仅当确实需要在开机瞬间就固定连入某个特定节点时，才填写此项（例如：`AIJIASU_DEFAULT_NODE=上海 #1` 或指定节点 ID）。

#### 深度说明 2：容器重启策略 (`AIJIASU_RESTART`)
Docker 的容器重启并非简单的 `true/false` 开关，而是精细的**状态机策略**：
- **`unless-stopped` (本项目默认，强烈推荐)**：
  - **自愈恢复**：运行中遇到客户端闪退、异常崩溃，或者宿主机重启、Docker Desktop 重启时，会自动重新拉起；
  - **尊重手动关机**：如果用户主动执行了 `./aijiasu down` 或 `docker stop`，Docker 会记住此状态，下次机器开机或重启 Docker 时**绝不会**自作主张强行启动，体验最自然。
- **`always` (无条件重启)**：
  - 无论正常退出还是异常崩溃，Docker 都会无条件反复尝试拉起；即使手动执行了 `docker stop`，在下次 Docker 服务重启时它依然会被强制再次启动。
- **`on-failure` (仅报错崩溃时重启)**：
  - 只有当容器以非零状态码退出（如闪退、异常报错崩溃）时才会重启；如果程序是正常执行结束（退出码 0），Docker 不会重启它。
- **`no` (相当于 false，不重启)**：
  - 容器一旦退出无论原因均不再重启，适合本地测试或一次性执行任务。

### 2. 启动服务与状态检查

```bash
# 启动 Docker 容器 (后台运行)
./aijiasu up

# 检查服务状态
./aijiasu status
```

输出示例：
```text
=== 爱加速 Docker 代理服务状态检查 ===
Docker CLI:        ✅ 就绪
Docker Daemon:     ✅ 就绪
容器状态 (aijiasu-runner): ✅ 就绪
爱加速登录状态:           ✅ 就绪
SOCKS5 代理端口:   ✅ 127.0.0.1:1080 畅通
当前出口 IP:       113.108.88.xx 来自于：中国 广东 广州 联通
```

---

## 核心切换接口 (`./aijiasu switch`)

### 1. 命令行调用方式

| 需求 | 命令格式 | 示例 |
|---|---|---|
| **指定省份** | `./aijiasu switch -p <省份>` | `./aijiasu switch -p 广东` 或 `./aijiasu switch 广东` |
| **指定城市** | `./aijiasu switch -c <城市>` | `./aijiasu switch -c 深圳` 或 `./aijiasu switch 深圳` |
| **同时指定省市** | `./aijiasu switch -p <省> -c <市>` | `./aijiasu switch -p 广东 -c 广州` |
| **指定特定节点** | `./aijiasu switch -n <节点名称/ID>` | `./aijiasu switch -n "上海 #116"` 或 `./aijiasu switch "上海 #116"` |
| **全局随机切换** | `./aijiasu switch` | `./aijiasu switch` |
| **清空历史并切换** | `./aijiasu switch -r [其他参数]` | `./aijiasu switch -r 广东` |

切换输出示例：
```text
  ➜ [切换请求] 正在执行智能代理切换...
     • 指定城市: 深圳

[切换成功] 代理已连接至: 广东深圳电信 #03
  • 节点 ID  : vvn-1882-9901
  • 省份/城市: 广东 / 深圳
  • 代理地址 : socks5://127.0.0.1:1080
  • 出口 IP  : 119.122.35.xx 来自于：中国 广东 深圳 电信
  • 去重统计 : 候选池共 32 个节点 | 本轮剩余未用 31 个节点
```

### 2. 去重机制说明

1. 每次切换节点时，系统会自动在当前匹配的省份/城市候选池中，**排除已使用过的节点**，优先随机挑选全新节点。
2. 剩余未用节点数会在切换结果中实时反馈（例如：`候选池共 32 个节点 | 本轮剩余未用 31 个节点`）。
3. 当该分类下的所有节点完整使用过一轮后，系统会自动重置轮换历史并提示：
   ```text
   💡 [去重轮换] 该范围内的所有可用节点已全部使用过一轮，已自动开启新一轮轮换！
   ```
   同时新一轮挑选会自动避开上一轮最后一个使用的节点，确保绝不连续重复。
4. 如需立即清空去重历史，可随时运行：
   ```bash
   ./aijiasu reset
   ```

---

## Web API 接口服务 (`./aijiasu serve`)

> [!IMPORTANT]
> **API Server 默认不启动**：
> 启动 Docker 服务（`./aijiasu up` 或 `docker compose up`）仅运行纯净的 SOCKS5 代理容器（`1080` 端口），**绝不会在后台默认启动任何 Web API 服务**，彻底杜绝端口占用与多余系统资源开销。
> 只有当您需要通过 HTTP 接口换 IP 时，才显式通过 `./aijiasu serve` 按需启动。

```bash
# 按需启动 Web JSON API 服务 (默认监听 1081 端口，可自定义端口如 ./aijiasu serve 8080)
./aijiasu serve
```

### 核心接口列表

#### 1. 登录认证接口 (`POST /login` 或 `POST /api/login`)
向爱加速客户端安全注入账号与密码凭据并完成认证：
- **请求方式**：`POST /login`
- **请求体（支持 JSON / Form 表单 / 自动读取 .env）**：
  - JSON Body 示例：
    ```json
    {
      "username": "13800000000",
      "password": "your_password"
    }
    ```
- **返回规范**：
  - **登录成功**：
    ```json
    {
      "success": true,
      "msg": "ok"
    }
    ```
  - **登录失败（返回真实错误，无冗余前缀）**：
    ```json
    {
      "success": false,
      "msg": "错误的用户名或密码。"
    }
    ```

#### 2. 节点列表查询接口 (`GET /nodes` 或 `GET /api/nodes`)
提供节点的 `id`、`province`（省份）、`city`（市区）、`number`（编号）等结构化信息，供调用方或前端自行选择：
- **请求方式**：`GET /nodes`（支持参数 `?province=广东&city=广州`）
- **返回示例**：
```json
{
  "success": true,
  "total": 30,
  "nodes": [
    {
      "id": "vvn-1024-8891",
      "province": "广东",
      "city": "广州",
      "number": "02",
      "name": "广东广州联通 #02",
      "status": "ok"
    },
    {
      "id": "vvn-1882-9901",
      "province": "广东",
      "city": "深圳",
      "number": "03",
      "name": "广东深圳电信 #03",
      "status": "ok"
    }
  ]
}
```

#### 3. 切换节点接口 (`POST /switch` 或 `GET /switch`)
根据传入的节点 ID、特定节点名称或省市范围执行切换与去重。
- **请求方式**：`POST /switch`（支持 JSON Body 或 URL Query 参数）
  - JSON Body 示例：`{"id": "vvn-1024-8891"}` 或 `{"province": "广东", "city": "广州"}`
  - URL Query 示例：`/switch?id=vvn-1024-8891` 或 `/switch?city=广州`
- **返回规范（严格约定）**：
  - **切换成功时返回**：
    ```json
    {
      "success": true,
      "msg": "ok",
      "ip": "113.108.88.25"
    }
    ```
  - **切换失败时返回**：
    ```json
    {
      "success": false,
      "msg": "未找到符合条件的可用代理节点",
      "ip": ""
    }
    ```

#### 4. 中断连接接口 (`POST /disconnect` 或 `GET /disconnect`)
主动断开当前的爱加速代理连接。
- **请求方式**：`POST /disconnect` 或 `GET /disconnect`
- **返回示例**：
  - **成功断开**：
    ```json
    {
      "success": true,
      "msg": "ok"
    }
    ```
  - **断开失败**：
    ```json
    {
      "success": false,
      "msg": "中断连接失败"
    }
    ```

#### 5. 辅助状态查询接口 (`GET /status`)
- **返回示例**：
```json
{
  "success": true,
  "msg": "代理服务正常畅通",
  "data": {
    "docker_installed": true,
    "daemon_running": true,
    "container_running": true,
    "logged_in": true,
    "proxy_healthy": true,
    "exit_ip": "113.108.88.xx"
  }
}
```

---

## 常用命令汇总

| 命令 | 说明 | 示例 |
|---|---|---|
| `./aijiasu login [账号] [密码]` | 登录爱加速账号 (支持传参或读取 .env) | `./aijiasu login 13800000000 123456` |
| `./aijiasu switch [参数]` | 核心切换命令，支持省、市、节点及自动去重 | `./aijiasu switch 广州` |
| `./aijiasu serve [端口]` | 启动 HTTP 切换 API 服务 | `./aijiasu serve 1081` |
| `./aijiasu reset` | 手动清空去重历史记录 | `./aijiasu reset` |
| `./aijiasu status` | 查看容器状态、登录状态及出口 IP | `./aijiasu status` |
| `./aijiasu nodes [-p/-c/-s]` | 查询可用节点列表或统计概览 | `./aijiasu nodes -p 广东` |
| `./aijiasu disconnect` | 断开当前代理连接 | `./aijiasu disconnect` |
| `./aijiasu init` | 检查并自动释放当前目录缺失的 Docker 运行资产 | `./aijiasu init` |
| `./aijiasu up` | 一键启动后台容器 (若缺失配置自动自举生成) | `./aijiasu up` |
| `./aijiasu down` | 停止并移除容器 | `./aijiasu down` |
| `./aijiasu logs` | 查看容器实时运行日志 | `./aijiasu logs` |

---

## 业务服务调用示例

代理连接地址恒定为：**`socks5://127.0.0.1:1080`**

### Python (结合 HTTP API 自动换 IP)
```python
import requests

# 1. 调用 HTTP 接口切换至广东广州
switch_resp = requests.post("http://127.0.0.1:1081/switch", json={"city": "广州"}).json()
if switch_resp.get("success"):
    print("切换成功，当前出口 IP:", switch_resp.get("ip"))
else:
    print("切换失败:", switch_resp.get("msg"))

# 2. 通过 1080 SOCKS5 代理采集目标数据
proxies = {
    "http": "socks5://127.0.0.1:1080",
    "https": "socks5://127.0.0.1:1080",
}
resp = requests.get("https://myip.ipip.net", proxies=proxies)
print("当前请求 IP:", resp.text.strip())
```

### cURL
```bash
# 切换节点
./aijiasu switch 深圳

# 验证代理
curl -x socks5h://127.0.0.1:1080 https://myip.ipip.net
```
