# 爱加速 Docker SOCKS5 代理

本项目在 Docker 容器中运行爱加速官方 Linux 客户端，由 Go 命令行工具管理登录、节点切换和 SOCKS5 端口桥接。普通模式管理一个容器；[桌面插件模式](#斧头浏览器插件模式)可按实例管理多个容器。

普通模式默认将代理映射到宿主机 `127.0.0.1:1080`。管理端提供 Linux、macOS、Windows 的 amd64 和 arm64 版本；桌面插件构建脚本目前只生成 macOS arm64 包。

版本变更见 [更新日志](CHANGELOG.md)。

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

- **代理端口**：默认在宿主机 `127.0.0.1:1080` 提供免密 SOCKS5；可通过 `.env` 修改绑定地址和端口。
- **内部桥接**：启动和切换节点时探测客户端监听端口，并将容器的 `1080` 端口桥接过去；没有持续监测进程。
- **管理方式**：使用 `./aijiasu` 命令行，或按需启动 HTTP API。

---

## 核心特性

- 按省份、城市、节点名称或 ID 筛选并连接节点；不指定条件时从可用节点中随机选择。
- 将已选节点 ID 记入 `.aijiasu_history.json`，优先选择尚未记录的节点；用 `./aijiasu reset` 清空记录。不同节点可能共用出口 IP。
- `./aijiasu serve` 按需启动 JSON API；它默认不随容器启动。
- 二进制内置 Docker 运行文件模板。缺少文件时，`./aijiasu up` 会询问是否生成；已有文件不会被覆盖。

---

## 目录结构

```
aijiasu-docker/
├── aijiasu                 # make build 生成，不纳入 Git
├── Dockerfile              # Ubuntu 22.04、官方客户端、socat
├── docker-compose.yml      # 普通模式的容器配置
├── entrypoint.sh           # 普通模式的容器入口
├── .env.example            # 配置模板
├── .env                    # 本地账号配置，不纳入 Git
├── CHANGELOG.md            # 发布日志
├── Makefile                # Go 构建入口
├── go.mod
├── conf/
│   └── localtime
├── cmd/aijiasu/
│   ├── main.go             # 普通 CLI 与 HTTP API
│   ├── bootstrap.go        # Docker 文件模板及自举
│   ├── embedded.go         # 桌面插件的标准输入输出协议
│   └── *_test.go
└── tools/                  # 多平台管理端与 macOS 插件发布脚本
```

---

## 快速上手

### 1. 下载或构建管理端

可从 [GitHub Releases](https://github.com/axe-browser/aijiasu-docker/releases) 下载对应平台的单个二进制文件：

| 系统 | amd64 | arm64 |
|---|---|---|
| Linux | `aijiasu-linux-amd64` | `aijiasu-linux-arm64` |
| macOS | `aijiasu-macos-amd64` | `aijiasu-macos-arm64` |
| Windows | `aijiasu-windows-amd64.exe` | `aijiasu-windows-arm64.exe` |

Linux/macOS 下载后将文件重命名为 `aijiasu`，赋予执行权限，再运行 `init`。以下以 Linux amd64 为例，其他平台替换第一行的文件名：

```bash
mv aijiasu-linux-amd64 aijiasu
chmod +x aijiasu
./aijiasu init
```

Windows PowerShell 下载后可重命名并初始化（arm64 使用对应文件名）：

```powershell
Rename-Item .\aijiasu-windows-amd64.exe aijiasu.exe
.\aijiasu.exe init
```

`init` 会在当前目录生成缺失的 Docker 运行文件和 `.env.example`，不会覆盖已有文件。仓库不跟踪编译后的二进制。使用发布的二进制无需 Go；从源码构建需要 Go 1.26.5 或更新版本：

```bash
# Linux / macOS
make build

# Windows PowerShell
go build -o aijiasu.exe ./cmd/aijiasu
```

运行还需 Docker CLI、Docker Compose 插件和运行中的本机 Docker daemon；Windows 使用 Docker Desktop 的 Linux 容器模式。首次构建容器时，还需网络访问 Ubuntu 软件源及爱加速官方客户端下载地址。

### 2. 配置账号

复制 `.env.example` 为 `.env`：

```bash
cp .env.example .env
chmod 600 .env
```

Windows PowerShell 可执行 `Copy-Item .env.example .env`，并限制该文件的访问权限。

把模板中的示例账号和密码换成自己的。以下端口均使用默认值；修改 `AIJIASU_PROXY_PORT` 后，后文的代理地址也要同步替换。

```env
AIJIASU_USER=你的账号
AIJIASU_PASS=你的密码
AIJIASU_PROXY_PORT=1080
AIJIASU_BIND_IP=127.0.0.1
```

普通模式会通过 Docker Compose 将 `.env` 中的变量传入容器，账号信息也会保存在容器配置中。请限制 `.env` 文件和 Docker daemon 的访问权限。

| 环境变量名 | 默认值 | 作用说明 |
|---|---|---|
| `AIJIASU_USER` | - | 爱加速登录账号 / 手机号 |
| `AIJIASU_PASS` | - | 爱加速登录密码 |
| `AIJIASU_PROXY_PORT` | `1080` | 宿主机 SOCKS5 代理暴露端口 (如 `1090`) |
| `AIJIASU_BIND_IP` | `127.0.0.1` | 宿主机代理绑定的 IP 地址 (如 `0.0.0.0`) |
| `AIJIASU_HTTP_PORT` | `1081` | `serve` 命令的 HTTP 端口；不控制监听地址 |
| `AIJIASU_CONTAINER_NAME` | `aijiasu-runner` | 自定义 Docker 容器名称 |
| `AIJIASU_DEFAULT_NODE` | 留空 | 开机默认节点 (留空表示纯待命，不自动连接任何节点) |
| `AIJIASU_RESTART` | `unless-stopped` | 容器重启策略 (默认推荐 `unless-stopped`) |

#### 默认节点与待命

`AIJIASU_DEFAULT_NODE` 留空时，容器启动后会尝试登录，但不会自动连接节点。此时代理端口通常尚不可用；运行 `./aijiasu switch` 后再检查代理。需要启动时连接固定节点，可在 `.env` 中填写节点名称或 ID。

#### 容器重启策略

`AIJIASU_RESTART` 默认是 `unless-stopped`，也可设为 `always`、`on-failure` 或 `no`。重启策略只在容器主进程退出时生效。入口脚本会持续运行，因此客户端或桥接进程单独失效时，Docker 不一定重启容器；可用 `./aijiasu status` 检查代理状态。

### 3. 启动、连接和验证

```bash
# 启动 Docker 容器 (后台运行)
./aijiasu up

# 默认没有连接节点，此时检查状态可能显示代理未就绪
./aijiasu status

# 连接可用节点后再次检查
./aijiasu switch -p 广东
./aijiasu status
curl --proxy socks5h://127.0.0.1:1080 https://myip.ipip.net
```

Windows PowerShell 中将 `./aijiasu` 换成 `.\aijiasu.exe`。例如：

```powershell
.\aijiasu.exe up
.\aijiasu.exe switch -p 广东
.\aijiasu.exe status
```

---

## 核心切换接口 (`./aijiasu switch`)

### 1. 命令行调用方式

| 需求 | 命令格式 | 示例 |
|---|---|---|
| **指定省份** | `./aijiasu switch -p <省份>` | `./aijiasu switch -p 广东` 或 `./aijiasu switch 广东` |
| **指定城市** | `./aijiasu switch -c <城市>` | `./aijiasu switch -c 深圳` 或 `./aijiasu switch 深圳` |
| **同时指定省市** | `./aijiasu switch -p <省> -c <市>` | `./aijiasu switch -p 广东 -c 广州` |
| **指定特定节点** | `./aijiasu switch -n <节点名称/ID>` | `./aijiasu switch -n "上海 #116"` |
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

1. 每次切换时，优先从当前候选池中选择尚未记录的节点 ID。去重按节点 ID 进行，不保证出口 IP 不重复。
2. 剩余未用节点数会在切换结果中实时反馈（例如：`候选池共 32 个节点 | 本轮剩余未用 31 个节点`）。
3. 当该分类下的所有节点完整使用过一轮后，系统会自动重置轮换历史并提示：
   ```text
   💡 [去重轮换] 该范围内的所有可用节点已全部使用过一轮，已自动开启新一轮轮换！
   ```
   候选池多于一个节点时，新一轮会避开上次选中的节点；只有一个节点时仍会重复。
4. 如需立即清空去重历史，可随时运行：
   ```bash
   ./aijiasu reset
   ```

当前实现会在连接验证前记录选中的节点。因此，切换失败也可能占用一次去重记录；需要重新选择时可执行 `./aijiasu reset`。

---

## Web API 接口服务 (`./aijiasu serve`)

API 默认不随容器启动。执行 `./aijiasu serve` 后，服务会监听 **`0.0.0.0:1081`**（或指定端口），且当前接口没有鉴权或 HTTPS。请通过防火墙限制端口访问，仅在可信环境使用；不要把 API 直接暴露到公网。接口响应使用 JSON 的 `success` 字段表示操作结果，失败也可能返回 HTTP 200。

```bash
# 按需启动，默认端口 1081；也可执行 ./aijiasu serve 8080
./aijiasu serve
```

### 核心接口列表

#### 1. 登录认证接口 (`POST /login` 或 `POST /api/login`)
登录爱加速账号。优先在本机通过 `.env` 和 `./aijiasu login` 完成登录；如需调用此接口，只在受信网络中使用 POST 请求。登录成功后，程序会尝试把提交的账号密码写入宿主机 `.env`；即使写入失败，接口也可能返回登录成功。
- **请求方式**：`POST /login`
- **请求体（支持 JSON、表单；未提供时读取 `.env`）**：
  - JSON Body 示例：
    ```json
    {
      "username": "13800000000",
      "password": "your_password"
    }
    ```
  - 当前实现也兼容 URL 查询参数中的账号和密码，但 URL 容易进入浏览器历史及访问日志，不建议使用。
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
- 如果容器尚未运行，查询会尝试启动容器。
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
  - 兼容 GET，例如 `/switch?id=vvn-1024-8891`；调用方应优先使用 POST。
- 普通模式共用一个容器；不要并发发起切换或断开请求。
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
- 代理握手失败时，状态检查可能尝试重新建立容器内端口桥接。
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
| `./aijiasu login` | 从 `.env` 读取账号并登录；避免将密码放入命令参数 | `./aijiasu login` |
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

以下示例使用默认代理地址 `socks5://127.0.0.1:1080`。修改绑定地址或端口后请同步修改示例。

### Python (结合 HTTP API 自动换 IP)

需要安装 `requests[socks]`，并单独运行 `./aijiasu serve`。HTTP API 仅限可信环境使用。

```bash
python3 -m pip install 'requests[socks]'
```

```python
import requests

# 1. 调用 HTTP 接口切换至广东广州
switch_response = requests.post("http://127.0.0.1:1081/switch", json={"city": "广州"}, timeout=30)
switch_response.raise_for_status()
switch_result = switch_response.json()
if not switch_result.get("success"):
    raise RuntimeError(f"切换失败: {switch_result.get('msg')}")
print("切换成功，当前出口 IP:", switch_result.get("ip"))

# 2. 通过 1080 SOCKS5 代理采集目标数据
proxies = {
    "http": "socks5h://127.0.0.1:1080",
    "https": "socks5h://127.0.0.1:1080",
}
resp = requests.get("https://myip.ipip.net", proxies=proxies, timeout=10)
resp.raise_for_status()
print("当前请求 IP:", resp.text.strip())
```

### cURL
```bash
# 切换节点
./aijiasu switch 深圳

# 验证代理
curl -x socks5h://127.0.0.1:1080 https://myip.ipip.net
```

---

## 斧头浏览器插件模式

桌面端可使用 `./aijiasu embedded` 托管多个独立代理实例。v1.0.1 Release 不提供桌面插件包；历史 v1.0.0 Release 包含 macOS arm64 插件包。插件集成需单独构建 `aijiasu` 和 `manifest.json`。Windows 管理端仅支持普通 CLI，`embedded` 会返回不支持错误。此模式仅通过标准输入/输出通信，不启动 HTTP 服务，不读取或保存宿主机 `.env`，也不会接管普通 CLI 创建的容器。

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

- 每个请求都需要 `protocol`、`action`、`dataDir`、`instanceId`、`instanceIndex`、`proxyPort` 和 `dockerPath`。`action` 支持 `start`、`status`、`stop`、`nodes`、`switch`；`proxyPort` 为 1024–65535。`instanceId` 格式为 `axe-aijiasu-<12 位小写十六进制>-<instanceIndex>`，索引范围为 1–20。
- `username` 和 `password` 仅允许出现在 `start` 请求中，`nodeId` 仅允许用于 `start` 或 `switch`，其中 `switch` 必须提供 `nodeId`。凭据通过管道传递，不放入命令行参数或环境变量；容器内会将其保存到权限为 `0600` 的配置文件。
- 宿主须先确认 Docker daemon 为本机实例，再把 `DOCKER_HOST=unix:///绝对路径` 传入插件。插件校验的是本机 Unix socket URI 格式，daemon 的身份仍由宿主确认；传给 Docker 的环境中不含 `DOCKER_CONTEXT`。Docker Desktop 未运行时由桌面宿主负责启动并等待就绪。
- `dataDir` 是该实例专属的绝对目录；首次启动只能使用不存在或空目录。目录标记、Compose 项目、容器名称、归属 label 和本机端口必须匹配，否则拒绝接管。目录路径不能包含符号链接。
- `instanceIndex` 是桌面端的资源索引，不代表账号许可的并发数。`start` 可附带 `nodeId` 精确选择节点；未指定时选择实时可用节点列表中的第 `instanceIndex` 项（从 1 开始）。节点列表可能变化，多个节点也不保证出口 IP 不同。
- `start` 非交互生成自己的 Docker 资源，构建镜像、启动容器、通过 `docker exec -i` 向容器内权限为 `0600` 的配置注入凭据并登录，再连接节点。托管容器使用单独的待命入口，不执行普通 CLI 启动脚本的自动登录/默认连接。只有 SOCKS5 握手成功才返回 `running`。普通启动失败或超时后尝试停止已验证归属的容器，并报告无法确认清理的情况。
- 关闭桌面导致的 SIGTERM/取消只终止当前 Docker 控制命令，保留已创建的代理容器，避免中断浏览器环境；下次打开可通过 `status` 检查。启动尚未完成时被取消的容器可能处于未连接状态，须重新启动或显式停止。
- `nodes` 查询已启动并登录实例的实时可用节点，返回 `nodes:[{id,name,province,city}]`；未启动时明确报错。`switch` 必须提供完整的 `nodeId`，只连接当前列表中 ID 完全匹配的节点，成功后返回更新的 `instance`。这两项操作不接受账号密码。
- `status` 仅检查实例与代理健康，通过容器内监听进程的命令行核验当前节点，健康时通过该代理查询出口 IP；不会启动容器或修复桥接。当前节点无法核实时返回 `status:error`，不会将历史记录当作当前节点。出口 IP 查询失败时 `exitIp` 为 `""`。`stop` 只停止此实例容器，保留运行数据、镜像及其他 Docker 容器。不存在的实例返回 `stopped`。
- Docker 命令有限时和输出大小限制；`start` 总限时 9 分钟，`switch` 90 秒，其他操作 35 秒。协议错误以 `ok:false` 和中文消息返回，进程正常退出 `0`。`ok:true` 只表示请求处理成功；`status` 或 `nodes` 的 `instance.status` 仍可能是 `error`，调用方需同时检查该字段。

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

发布脚本仅支持在 macOS 上生成 arm64 包，需安装 Go 工具链，并提供系统自带的 `lipo` 与 `codesign`。构建不会运行 Docker、登录或连接节点：

```sh
python3 tools/build_provider.py
# 输出 dist/provider/darwin-arm64/{manifest.json,aijiasu}
go test ./cmd/aijiasu -run '^TestEmbedded' -count=1
```

发布脚本先构建 macOS arm64 二进制，再执行 ad-hoc 签名和验证，最后计算 SHA-256 写入清单。已有产物目录不会被覆盖，可通过 `--output` 指定新目录。ad-hoc 签名不等于正式发行签名；跨设备发布前应接入正式签名与分发流程。

---

## 构建 GitHub Release 附件

在 macOS 上执行以下命令可生成六个平台的管理端二进制文件，并对 macOS 文件进行 ad-hoc 签名。此签名不等于正式发行签名或公证。脚本只构建文件，不运行 Docker 或登录账号；默认写入系统临时目录，`--output` 可指定一个尚不存在的目录。

```bash
python3 tools/build_release.py --version 1.0.1
```

v1.0.1 Release 不包含普通管理端 ZIP、桌面插件 ZIP 或 `SHA256SUMS` 附件。需要桌面插件时，按[斧头浏览器插件模式](#斧头浏览器插件模式)单独构建。发布内容和已知限制见 [更新日志](CHANGELOG.md)。

---

## 开发验证

以下检查不启动容器；实际登录和代理连通性仍需用有效账号及运行中的 Docker 验证。

```bash
go test ./...
go vet ./...
bash -n entrypoint.sh
docker compose config --quiet
```
