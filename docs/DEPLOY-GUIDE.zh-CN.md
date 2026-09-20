TalkIntent 是一套面向企业研发团队的分布式工作区感知与异步协同系统。在常规研发生态中，团队成员为了解彼此的最新代码分支、改动进展、对外暴露接口或本地服务调试端口，往往需要频繁进行即时打字询问，打断工作流并产生大量协同阻滞。TalkIntent 采用“中心调度协调（Hub）+ 开发者本地驻留探针守护进程（Daemon）”的双层拓扑架构，使团队成员可以直接使用自然语言向同事的工作区提问。系统通过在被提问成员机器本地运行的 ReAct 架构智能体探针（Reasoning + Acting，即交替进行逻辑思考与工具调用的自主智能体），在受控沙箱中综合 Git 差异与工作区状态生成答复，在不打扰开发者本人且严格恪守本地隐私规则的前提下，实现研发进展的透明同步。

本文档面向首次接触 TalkIntent 的企业客户团队，包括负责服务端部署与凭据分发的系统管理员，以及使用客户端开展协同的全体研发工程师。

## 1. 系统架构与安全设计概述

### 1.1 系统核心组件与拓扑关系
TalkIntent 系统由两个核心交付单元构成：中心协调服务 Hub 与客户端常驻守护进程 Daemon。Hub 作为集群的调度与分发中枢，负责维护团队成员花名册、路由提问请求、管理离线 FIFO 提问队列、记录追加式（Append-only）审计事件流，并承载内嵌的 Web 监控仪表盘与飞书 Webhook 接收网关。

开发者本地节点上常驻运行的 Daemon 负责与 Hub 保持双向通信长连接，持续感知本地注册的代码仓库，并在接收到提问分发请求时拉起本地现场探针（On-Site Probe）。现场探针是一个由本地大模型驱动的 ReAct 智能体（Reasoning + Acting，交替进行推理与工具调用），内置了 `git_status`、`git_diff`、`git_log`、`read_file`、`listening_ports` 等一组受限的本地只读感知工具，以及用于结构化安全拦截的 `refuse` 控制工具。

### 1.2 本地主权与数据外发物理边界
TalkIntent 严格贯彻“数据留在本地，仅结论上报中心”的本地主权安全原则。探针在工作区内执行的所有具体操作（读取源代码文件正文、执行 Git Diff 比较、读取 Commit 提交记录、扫描本地监听端口）均在工程师本人的物理机沙箱内完成。开发者本地的大模型 API 密钥、自定义 CA 证书、工作区源代码全文、Git 差异详情以及探针内部的工具调用参数与调用堆栈，绝对不会离开本地机器，更不会上传至 Hub 服务端。

节点向中心 Hub 发送的网络数据具有严格的物理边界，仅限定在 WebSocket 响应报文（`query_response`）的 7 个固定字段中：
1. `query_id`：提问的全局唯一标识。
2. `status`：终态状态枚举，仅包括 `completed`（完成）、`refused`（拒答）、`error`（异常）、`timeout`（超时）。
3. `answer`：大模型最终合成的自然语言答复或脱敏后的拒答理由。守护进程限制其最大长度为 16KB，超出部分予以截断并追加 `[truncated by TalkIntent daemon]` 标记。
4. `tools_used`：执行期间调用的工具名称数组（例如 `["git_diff", "git_status"]`），仅包含工具名字符串，不含工具入参与返回正文。
5. `duration_ms`：探针端到端执行耗时（毫秒）。
6. `token_usage`：包含大模型调用的 `prompt_tokens`、`completion_tokens`、`total_tokens` 审计计数。
7. `error_message`：仅在状态为 `error` 或 `timeout` 时由探针返回，且已经过内置正则脱敏器过滤。

### 1.3 网络连通性与防火墙策略
系统的网络拓扑具有高度的内网穿透友好性。开发者机器上的 Daemon 进程通过向 Hub 主动发起单向出站长连接（HTTP 升级至 WebSocket，路径为 `/ws/daemon`）进行通信。

因此，**开发者个人开发机完全不需要开放任何入站 TCP 端口，不需要公网 IP，亦无需打通任何复杂的内网穿透或端口映射**。企业网络防火墙只需放行 Hub 所在服务器的入站端口（默认 HTTP/WS 端口 `:8080`，生产环境若挂载反向代理则对外仅暴露 `443/tcp`），以及开发者开发机访问 Hub 与大模型网关的出站流量。若启用飞书机器人企业级集成，Hub 所在服务器还需具备访问飞书开放平台（open.feishu.cn:443/tcp）的出站 HTTPS 权限。

## 2. 二进制可执行文件获取与编译

TalkIntent 采用纯 Go 语言（Go 1.26+）编写，整个工程除标准库外仅引入 `github.com/coder/websocket` 单一依赖，无 Cgo 依赖，不使用 SQLite 等外部动态库，原生支持多操作系统的纯静态单二进制构建。

需要向客户明确说明的是，**TalkIntent 代码仓库当前不提供预编译的 GitHub Releases 发行包**。客户团队获取二进制文件的途径分为两种：由系统管理员在一台构建机上集中交叉编译各平台二进制并分发给团队，或者由具备 Go 1.26+ 环境的工程师直接从源码本地构建。

### 2.0 源码获取与前置依赖准备
编译前请确保构建机已就绪以下基础环境：
- **Git** 命令行工具（2.20+）；
- **Go** 语言工具链（Go 1.26+）。

使用 Git 克隆 TalkIntent 源代码仓库并进入项目目录：
```bash
git clone https://github.com/Sskift/talkintent.git
cd talkintent
```

### 2.1 管理员交叉编译分发路线（推荐）
管理员在一台安装有 Go 1.26+ 的机器上检出代码仓库后，可针对团队成员使用的操作系统与芯片架构，一键交叉编译出静态二进制文件。

在 Linux 或 macOS 终端中执行：
```bash
# Windows x86_64 平台 (产物约 12MB)
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o bin/talkintent-windows-amd64.exe ./cmd/talkintent
# Windows ARM64 平台
GOOS=windows GOARCH=arm64 go build -ldflags="-s -w" -o bin/talkintent-windows-arm64.exe ./cmd/talkintent
# macOS Apple Silicon 芯片 (M1/M2/M3/M4, 产物约 11MB)
GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o bin/talkintent-darwin-arm64 ./cmd/talkintent
# macOS Intel 芯片 (x86_64)
GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o bin/talkintent-darwin-amd64 ./cmd/talkintent
# Linux x86_64 平台 (产物约 12MB)
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/talkintent-linux-amd64 ./cmd/talkintent
# Linux ARM64 平台
GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o bin/talkintent-linux-arm64 ./cmd/talkintent
```

在 Windows PowerShell 终端中执行：
```powershell
# Windows x86_64
$env:GOOS="windows"; $env:GOARCH="amd64"; go build -ldflags="-s -w" -o bin/talkintent-windows-amd64.exe ./cmd/talkintent
# macOS Apple Silicon
$env:GOOS="darwin"; $env:GOARCH="arm64"; go build -ldflags="-s -w" -o bin/talkintent-darwin-arm64 ./cmd/talkintent
# Linux x86_64
$env:GOOS="linux"; $env:GOARCH="amd64"; go build -ldflags="-s -w" -o bin/talkintent-linux-amd64 ./cmd/talkintent
```

管理员将生成的单文件分发给对应成员后，成员重命名为 `talkintent`（macOS/Linux 需赋予执行权限 `chmod +x talkintent`）或 `talkintent.exe`（Windows），并放置于系统的可执行路径（`PATH`）中即可。

### 2.2 本地源码直接构建路线
若团队成员本地已具备 Go 1.26+ 环境，可直接在仓库根目录执行构建命令。

在 Linux 或 macOS 终端中执行：
```bash
go build -o bin/talkintent ./cmd/talkintent
./bin/talkintent version
```

在 Windows PowerShell 终端中执行：
```powershell
go build -o bin/talkintent.exe ./cmd/talkintent
.\bin\talkintent.exe version
```

## 3. Hub 协调服务端部署（管理员）

### 3.1 Hub 命令行参数与数据目录结构
Hub 服务通过子命令 `talkintent hub` 启动。启动时 Hub 会使用 `0700` 权限确保存储目录存在，并在其中维护核心数据。

| 命令行标志 | 环境变量覆盖 | 默认值 | 作用说明 |
| :--- | :--- | :--- | :--- |
| `-addr` | 无 | `":8080"` | 服务端 HTTP 与 WebSocket 监听地址 |
| `-data-dir` | 无 | `"./data"` | 核心事件流、审计记录与凭据存储目录 |
| `-admin-token` | `TALKINTENT_ADMIN_TOKEN` | `""` | 管理员认证令牌，缺省则自动生成落盘 |
| `-public-url` | 无 | `""` | Hub 对外公网根地址（如 `https://hub.example.com`） |
| `-json` | 无 | `false` | 以结构化 JSON 格式输出启动元数据 |

当前版本的 Hub 仅读取 `TALKINTENT_ADMIN_TOKEN` 这一个环境变量；监听地址、数据目录与公网地址必须通过命令行标志传入（下文 systemd / NSSM / Docker Compose 示例均已显式写入标志）。

初始化后，数据目录下会生成以下文件，全部施加 `0600` 文件系统权限：
1. `admin.token`：存放管理员明文 Token，用于管理员 CLI 鉴权。
2. `events.jsonl`：系统唯一核心持久化存储，按行追加记录成员注册、邀请码消费、在线会话、提问审计与加密凭证事件。
3. `master.key`：由 `crypto/rand` 生成的 32 字节 AES-256 随机主密钥，用于飞书凭据的 AES-GCM 加密存储。
4. `salt`：由 `crypto/rand` 生成的 32 字节随机盐，用于邀请码的 HMAC-SHA256 哈希存储。

### 3.2 核心参数 `-public-url` 的配置要求与语义
在生产部署中，必须显式传递 `-public-url` 参数（例如 `-public-url https://hub.example.com`）。该配置承担三项核心职责：
1. **WebSocket 跨域 Origin 准入白名单**：Daemon 建立 WebSocket 连接时 Hub 会校验 Origin 请求头，设置该参数会自动将对应 Host 纳入准入白名单，防止连接被拒。
2. **入网配对结果声明**：成员调用配对接口时，Hub 返回给客户端的 `HubURL` 依赖此参数，客户端据此保存到本地 `config.json`。
3. **飞书回调 Webhook 生成**：配置飞书机器人时，系统生成的 Webhook 地址依赖此公共根路径。

### 3.3 Hub 服务部署与生产持久化运行方案
根据企业基础设施现状，管理员可从以下四种运行方案中选择其一完成 Hub 部署与启动：

#### 方案 A：Linux Systemd 服务配置（推荐）
1. 创建系统专用运行用户与数据目录：
```bash
sudo useradd -r -s /bin/false -d /var/lib/talkintent talkintent
sudo mkdir -p /var/lib/talkintent/data
sudo chown -R talkintent:talkintent /var/lib/talkintent
sudo cp bin/talkintent /usr/local/bin/talkintent
sudo chmod +x /usr/local/bin/talkintent
```
2. 创建服务单元文件 `/etc/systemd/system/talkintent-hub.service`：
```ini
[Unit]
Description=TalkIntent Hub Server
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
User=talkintent
Group=talkintent
WorkingDirectory=/var/lib/talkintent
ExecStart=/usr/local/bin/talkintent hub -addr 127.0.0.1:8080 -data-dir /var/lib/talkintent/data -public-url https://hub.example.com
Restart=always
RestartSec=5s
LimitNOFILE=65536
KillMode=mixed
TimeoutStopSec=15s
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```
3. 加载并启动服务：
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now talkintent-hub
sudo systemctl status talkintent-hub
```

#### 方案 B：Windows 持久化运行方案
Go 编译的二进制文件未直接实现 Windows 服务控制管理器（SCM，Service Control Manager）调度接口，直接使用系统自带的 `sc.exe create` 会因无响应超时报错。推荐使用第三方开源服务管理工具 NSSM（Non-Sucking Service Manager）包装为系统服务。

1. 获取与安装 NSSM：
可通过 Windows 官方包管理器一键安装：
```powershell
winget install nssm
```
或从 NSSM 官方网站（https://nssm.cc/download）下载解压，将其 `nssm.exe` 放置于系统 PATH 路径中。

2. 注册并启动 Windows 服务：
```powershell
# 准备数据目录
New-Item -ItemType Directory -Force -Path "C:\talkintent\data"

# 安装与配置服务
nssm install TalkIntentHub "C:\talkintent\bin\talkintent.exe" "hub -addr :8080 -data-dir C:\talkintent\data -public-url https://hub.example.com"
nssm set TalkIntentHub AppDirectory "C:\talkintent"
nssm set TalkIntentHub Start SERVICE_AUTO_START
nssm start TalkIntentHub
```

亦可通过 Windows 任务计划程序（Task Scheduler）配置开机自动启动：
```cmd
schtasks /Create /TN "TalkIntentHub" /TR "C:\talkintent\bin\talkintent.exe hub -addr :8080 -data-dir C:\talkintent\data -public-url https://hub.example.com" /SC ONSTART /RU SYSTEM
```

#### 方案 C：Docker Compose 独立生产部署方案
在企业容器化运维中，部署 Hub 可采用 Docker Compose 编排。**注意：`deploy/compose/Dockerfile` 位于源码仓库中。若直接在源码根目录下执行构建部署**，可在仓库根目录下创建 `compose.prod.yaml`：
```yaml
services:
  hub:
    image: talkintent:latest
    build:
      context: .
      dockerfile: deploy/compose/Dockerfile
    container_name: talkintent-hub
    restart: unless-stopped
    command:
      - "hub"
      - "-addr"
      - ":8080"
      - "-data-dir"
      - "/data"
      - "-public-url"
      - "https://hub.example.com"
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - /var/lib/talkintent/data:/data
```
在源码根目录执行启动：
```bash
docker compose -f compose.prod.yaml up -d --build
```

**若在独立的纯部署目录（例如 `/opt/talkintent-hub`，不含完整代码仓库）中运行**，应先在源码机上构建 Docker 镜像：
```bash
docker build -t talkintent:latest -f deploy/compose/Dockerfile .
```
随后在独立部署目录下的 `compose.prod.yaml` 中移除 `build:` 段落，直接使用 `image: talkintent:latest`，即可通过 `docker compose up -d` 常驻启动。

#### 方案 D：本地前台快速测试运行（开发/联调）
在测试或验证阶段，可直接在前台运行 Hub：
```bash
talkintent hub -addr 127.0.0.1:8080 -data-dir ./data -public-url http://localhost:8080
```

### 3.4 管理员凭据（Admin Token）安全读取与生命周期
Admin Token 是管理员生成成员邀请码和访问特权接口的唯一凭证。

#### 凭据解析优先级与自动生成
Hub 解析 Admin Token 的优先级为：命令行标志 `-admin-token` \> 环境变量 `TALKINTENT_ADMIN_TOKEN` \> 数据目录下 `admin.token` 文件。若均为空，Hub 首次启动时会自动生成以 `ti_adm_` 开头加 48 位十六进制随机字符组成的 55 位强随机令牌，并写入 `<data-dir>/admin.token`。

#### 安全回显规则与物理读取
系统遵循严格的安全审计规范：**Hub 在控制台标准输出和日志中绝不打印完整 Admin Token**，仅打印其 SHA-256 指纹的前 16 位与字符长度（例如 `Admin Token Fingerprint: sha256:89b858553139e8cc... (length: 55 chars)`）。

因此，在 Hub 服务首次启动完成后，管理员必须从实际配置的数据目录中读取生成的 `admin.token` 明文：
- **本地前台测试运行（默认 `./data`）**：
  直接在当前运行目录下读取：
  ```bash
  cat ./data/admin.token
  ```
- **生产 Linux Systemd 服务（目录 `/var/lib/talkintent/data`）**：
  ```bash
  sudo cat /var/lib/talkintent/data/admin.token
  ```
- **生产 Windows NSSM 服务（目录 `C:\talkintent\data`）**：
  ```powershell
  Get-Content C:\talkintent\data\admin.token
  ```
- **Docker Compose 容器部署（挂载宿主机 `/var/lib/talkintent/data`）**：
  ```bash
  sudo cat /var/lib/talkintent/data/admin.token
  # 或直接在容器内读取
  docker exec talkintent-hub cat /data/admin.token
  ```

### 3.5 反向代理与 Nginx 生产配置规范
Hub 服务端**不原生支持 TLS 监听**，在生产环境中必须部署在具有 TLS 终结能力的反向代理（如 Nginx）之后。由于客户端 Daemon 依赖 `/ws/daemon` 维持双向心跳长连接（心跳周期为 20 秒），反向代理必须正确配置协议升级与长超时时间（建议 3600 秒），防止连接被中途切断。

Nginx 生产配置示例：
```nginx
server {
    listen 443 ssl http2;
    server_name hub.example.com;

    ssl_certificate /etc/ssl/certs/hub.example.com.crt;
    ssl_certificate_key /etc/ssl/private/hub.example.com.key;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location /ws/daemon {
        proxy_pass http://127.0.0.1:8080/ws/daemon;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "Upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
```

## 4. 成员邀请与配对管理（管理员）

命令调用说明：本节及后续操作示例均假设已将 `talkintent` 可执行文件放置于系统 `PATH` 路径中，故直接使用 `talkintent <subcommand>` 形式调用。若未配置 PATH，请在当前二进制所在目录下使用 `./talkintent`（Linux/macOS）或 `.\talkintent.exe`（Windows）替代执行。

### 4.1 邀请码生成与参数规则
新成员入网必须使用管理员发放的单次邀请码。管理员使用 `talkintent invite` 命令生成邀请凭据。

在终端中执行：
```bash
talkintent invite -hub https://hub.example.com -admin-token "ti_adm_xxx" -name zhangsan -alias "张三,san" -expires-hours 72
```

亦可直接使用位置参数指定成员名称：
```bash
talkintent invite -hub https://hub.example.com -admin-token "ti_adm_xxx" zhangsan
```

输出内容包含邀请码、到期时间及配对指引：
```text
Created Invite Code for member: zhangsan
  Invite Code: INV-CE1C1E9C-C60A8A8D
  Expires At:  2026-09-23 09:57:45

The member can join using:
  talkintent pair --hub https://hub.example.com --code INV-CE1C1E9C-C60A8A8D
```
- 参数说明：这里的 `-name zhangsan` 代表被邀请成员在团队花名册中的唯一用户名。
- 格式规范：邀请码格式为 `INV-<8位十六进制>-<8位十六进制>`，具有 64-bit 熵值，入库存储带 Salt 的 HMAC-SHA256 哈希值，CLI 默认有效期为 72 小时。

### 4.2 邀请码消费与作废机制
邀请码在客户端执行配对后立即被消费，Hub 在事件流中追加 `invite_consumed` 事件，不可二次使用。若超出有效期限，配对接口直接拒绝。

系统目前未提供 `talkintent invite revoke` 命令。若邀请码意外泄露需紧急作废，管理员可在本地运行一次虚假配对将其提前消费掉（例如 `talkintent pair -config /tmp/burn.json -hub <url> -code <code_to_burn> -name dummy`，务必带 `-config` 指向临时文件，否则会覆盖管理员本机自己的 `~/.talkintent/config.json`）；或停止 Hub 服务后，从 `events.jsonl` 中移除对应的 `invite_created` 记录行后重启 Hub。

### 4.3 凭据轮换与成员停用管理
1. **管理员 Token 轮换**：直接覆盖 `<dataDir>/admin.token` 中的 Token 字符串或更新 `TALKINTENT_ADMIN_TOKEN` 环境变量，重启 Hub 服务即刻生效。
2. **成员 Token 吊销**：当前版本未提供在线删除成员 API。若员工离职需强制注销凭据，标准做法是停止该成员机器上的 Daemon 并清理本地 `config.json`。若需在服务端彻底作废，管理员停机后在 `events.jsonl` 中移除该成员的 `member_token_indexed` 行后重启 Hub，此后该 Token 将直接返回 HTTP 401 Unauthorized。

## 5. 开发者节点配置与守护进程（开发人员）

### 5.0 开发机前置准备
在开始客户端配置之前，请确认开发机已满足以下前置环境依赖：
1. **Git 命令行工具**：已安装并在系统 `PATH` 环境变量中可用（通过 `git --version` 验证）。现场探针的底层感知工具（`git_status`、`git_diff`、`git_log`）强依赖操作系统 Git 命令。
2. **操作系统支持**：支持 Windows 10/11（`x86_64`/`ARM64`）、macOS 12+ (Apple Silicon/Intel) 或主流 Linux 发行版。
3. **大模型访问凭据**：已就绪可用的大模型凭据（例如 OpenAI API Key、Claude API Key 或企业自建大模型网关访问地址与令牌）。
4. **网络连通性**：开发机能够出站访问 Hub 服务的 HTTP/WebSocket 端口（默认为 `:8080` 或反向代理 `:443`），以及大模型服务提供商网关的 HTTPS 端口。

### 5.1 步骤一：入网配对（`pair`）
开发者从管理员处获取专属邀请码后，在个人开发机上执行配对命令：
```bash
talkintent pair -hub https://hub.example.com -code INV-CE1C1E9C-C60A8A8D -name dev-laptop
```
- **参数 `-name` 的核心作用**：在 `pair` 命令中，参数 `-name dev-laptop` 用于指定**当前设备的机器标识（Machine Label）**，例如 `dev-laptop` 或 `office-desktop`。该名称用于多设备协作时的会话标识与路由定位。**若省略该参数，CLI 默认自动获取当前主机的 Hostname**。特别注意：切勿将此处的设备名与管理员发放邀请码时的成员用户名（如 `zhangsan`）混淆。

配对成功后，本地会生成配置文件 `~/.talkintent/config.json`，在 POSIX 系统上权限严格限制为 `0600`。该文件保存专属成员长期令牌 `ti_mem_...`，Hub 服务端仅留存该令牌的哈希散列。

### 5.2 步骤二：注册本地监控工作区（`workspace`）
配对完成后方可添加工作区。工作区必须为本地已存在的有效 Git 仓库目录。

在 Linux 或 macOS 终端中执行：
```bash
# 注册本地代码工作区，指定别名 backend-service
talkintent workspace add /home/user/projects/backend-service backend-service
# 查看已注册的工作区清单
talkintent workspace list
# 移除工作区
talkintent workspace remove backend-service
```

在 Windows PowerShell 终端中执行：
```powershell
# 注册本地代码工作区
talkintent workspace add C:\projects\backend-service backend-service
# 查看工作区列表
talkintent workspace list
# 移除工作区
talkintent workspace remove backend-service
```

### 5.3 步骤三：配置与测试本地大模型（`llm`）
本地现场探针由开发者机器上的大模型驱动。凭据仅保存在本地 `config.json` 中，绝不上报 Hub。系统原生支持 OpenAI 兼容协议与 Anthropic 协议。

#### 选项 A：配置 OpenAI 兼容网关
```bash
talkintent llm set -provider openai -base-url https://api.openai.com/v1 -api-key "sk-proj-xxx" -model gpt-4o
```

#### 选项 B：配置 Anthropic Claude 网关
```bash
talkintent llm set -provider anthropic -base-url https://api.anthropic.com/v1 -api-key "sk-ant-xxx" -model claude-3-7-sonnet
```
注：针对 Anthropic 模型的响应，探针内部已包含针对 `thinking` 思考块的自动过滤机制，不会因存在思考段落导致解析异常。

#### 选项 C：企业私有网关（自签私有 CA、SNI 覆盖与测试跳过校验）
若企业内部通过内网 IP 直连自建大模型网关，需配置自定义 CA 证书池与 SNI 主机名覆盖：

在 Linux 或 macOS 终端中执行：
```bash
talkintent llm set -provider openai -base-url https://10.0.0.1:8443/v1 -api-key "custom-key" -model gpt-4o -ca-file /etc/ssl/gateway-ca.pem -tls-server-name gateway.internal
```
在 Windows PowerShell 终端中执行：
```powershell
talkintent llm set -provider openai -base-url https://10.0.0.1:8443/v1 -api-key "custom-key" -model gpt-4o -ca-file C:\certs\gateway-ca.pem -tls-server-name gateway.internal
```

- **参数 `-insecure-skip-verify` 说明与安全警告**：
  若企业内网网关处于隔离测试联调阶段，且无法挂载或导入私有 CA 证书文件，CLI 提供了 `-insecure-skip-verify` 标志：
  ```bash
  talkintent llm set -provider openai -base-url https://10.0.0.1:8443/v1 -api-key "custom-key" -model gpt-4o -insecure-skip-verify
  ```
  ⚠️ **安全警告**：`-insecure-skip-verify` 会完全关闭 TLS 证书链与主机名的安全校验，使通信面临中间人攻击（MITM）隐患，**仅限在内网受信的私有测试环境中用于临时排错，严禁在任何生产或公网环境中使用**。

#### 查看脱敏配置与连通性验证
```bash
talkintent llm show
talkintent llm test
```
执行 `llm test` 时，探针会向配置的网关发送单次最小测试请求。系统遵循安全审计规范，**绝不打印模型生成的对话正文**，仅输出连接状态与 Token 消耗统计（例如 `Status: OK`、`Total Tokens: 162`）。

### 5.4 步骤四：初始化与配置本地主权隐私守则（`privacy`）
在启动守护进程对外提供协同服务前，**必须先完成本地自然语言隐私守则的初始化与配置**，防止节点刚上线便在防线空虚状态下响应外界提问。

1. 初始化默认隐私模板：
```bash
talkintent privacy init
```
该命令会在 `~/.talkintent/privacy-prompt.md` 生成包含基础安全防线的模板（已存在则不覆盖）。

2. 查看与获取编辑路径：
```bash
talkintent privacy show
talkintent privacy edit-path
```
使用你熟悉的文本编辑器打开 `edit-path` 输出的文件路径，按需添加团队或个人保密要求（详见第 6 节配置示例）。

3. 本地 Dry-Run 拦截演练：
```bash
talkintent privacy test "问一下 feature/confidential 分支在做什么"
```
该命令会在本地拉起大模型进行无跨机流量的模拟提问，验证自然语言守则是否如期调用 `refuse` 工具实施拦截。

### 5.5 步骤五：守护进程启动与常驻管理（`daemon`）
确认工作区、大模型和隐私规则就绪后，启动守护进程：
- **后台脱离启动（推荐）**：必须追加 `-detach` 参数，程序会将标准输出重定向至 `~/.talkintent/daemon.log`，将子进程 PID 写入 `~/.talkintent/daemon.pid`，随后父进程退出，防止因终端关闭导致服务终止。
- **前台测试运行**：不带 `-detach` 参数时在前台运行，终端直观打印连接日志，按 Ctrl+C 触发优雅排空（最长等待 15 秒在途任务完成）后退出。

```bash
# 后台脱离启动
talkintent daemon start -detach
# 查看守护进程运行状态
talkintent daemon status
# 优雅停止守护进程
talkintent daemon stop
```

### 5.6 步骤六：节点综合状态审查（`status`）
开发者可通过 `status` 命令一键排查本地运行环境健全性：
```bash
talkintent status
```
该命令会依次检验：本地配置与根目录物理路径；成员配对凭证与机器标识；Daemon 是否处于运行中及其 PID；带 Token 调用 Hub 接口检验连通性并输出往返时延（Latency MS）；本地已注册的工作区清单；本地生效的大模型配置摘要。

### 5.7 高级场景：单成员多设备（台式机+笔记本）部署规范
一名团队成员允许同时在公司台式机与移动笔记本等多台设备上运行 Daemon。

**切勿为第二台设备申请新邀请码**（否则会产生同名新成员冲突，导致其他同事提问时因存在重名候选人而报错）。标准多设备部署步骤如下：
1. 在第二台设备上完成二进制安装；
2. 将第一台机器上的配置文件 `~/.talkintent/config.json` 完整复制到第二台机器的对应路径；
3. 打开第二台机器的 `~/.talkintent/config.json`，**仅修改其中的 `machine_name` 字段**（例如分别命名为 `"desktop-office"` 与 `"laptop-mac"`），保留相同的 `member_id` 与 `token`；
4. 在第二台设备上独立配置本地 Git 工作区路径（`talkintent workspace add`）；
5. 启动第二台机器上的守护进程（`talkintent daemon start -detach`）。

Hub 允许多机器会话同时在线，并优先将提问路由给挂载了对应工作区的机器；若提问未指定工作区，则智能路由给最近活跃的心跳机器。

## 6. 本地主权自然语言隐私守则深度配置

### 6.1 隐私规则文件定位与累加合并机制
TalkIntent 允许工程师使用纯自然语言制定隐私边界。规则文件支持双层目录结构：
- **全局隐私规则**：默认路径为 `~/.talkintent/privacy-prompt.md`。
- **工作区专属规则**：位于 `<workspaceRoot>/.talkintent/privacy-prompt.md` 或 `<workspaceRoot>/privacy-prompt.md`。

特别说明其合并原则：**工作区规则与全局规则为累加关系（Additive Concatenation），工作区规则绝不会覆盖或消除全局规则**。探针在装配 System Prompt 时，会严格按如下顺序拼装：
1. 强制常驻安全底线（MANDATORY BASELINE SECURITY GUARDRAILS）。
2. 隐私与拒答行为规范（PRIVACY & REFUSAL INSTRUCTIONS）。
3. 用户自定义规则：包含全局隐私规则，紧接着是工作区专属规则。

系统指令明确要求大模型将自然语言隐私守则置于提问者的 Query 之上。即使提问者在问题中设计 Prompt 注入攻击，大模型也必须优先遵从隐私守则。

### 6.2 工具层物理硬黑名单与防御纵深
除自然语言外，探针在底层工具执行层面施加了不可绕过的物理防御：
1. **文件扩展名与敏感路径硬黑名单**：在执行 `read_file` 或检索时，底层强制拦截命中 `*.pem`、`*.key`、`*.crt`、`*.pfx`、`*.p12`、`id_rsa*`、`id_ed25519*`、`*.pub`、`.env`、`.env.*`、`*secret*`、`*credential*`、`*token*`、`*password*`、`.git/config`、`*aws/credentials*` 以及 `/.ssh/`、`/.gnupg/` 等敏感模式的文件，直接拒绝执行并返回错误。
2. **Git 天花板隔离**：探针执行 Git 工具时强制注入环境变量 `GIT_CEILING_DIRECTORIES=<workspaceRoot>`，阻止 Git 向上遍历父级目录。
3. **Git Diff 差异剥离**：提取差异块时逐行解析文件路径，命中黑名单的文件差异块被彻底剥离，绝不传给大模型。
4. **内置正则后置脱敏器**：探针内置针对 10 项敏感模式（私有 IPv4 地址、OpenAI API Key、GitHub Token、AWS 凭证、Bearer 与 JWT Token）的脱敏器，对模型输出正文、拒答理由与报错文本统一替换为 `[REDACTED]`。

### 6.3 结构化拒答行为与脚本退出码规范
当提问触碰自然语言隐私守则时，大模型会主动调用注册的控制工具 `refuse(reason)`。探针侦测到该调用后，立刻中止后续工具调用，将状态标记为 `refused`，并将脱敏后的理由写入 `answer`。

提示：在自动化 CI/CD 或 Shell 脚本中调用 `talkintent ask` 时，若被提问方的隐私守则判定拒答（status 为 refused），CLI 会以状态码 1 退出。即便开启了 `-json` 标志，进程退出码依旧为 1。若脚本开启了 `set -e`，必须追加 `|| true`（例如 `talkintent ask -json ... || true`）以防止脚本中断。

### 6.4 推荐生产级中文自然语言规则示例
开发者可将以下规则直接写入 `~/.talkintent/privacy-prompt.md` 或工作区下的 `privacy-prompt.md` 中：
```markdown
# Sovereign Natural-Language Privacy Guardrails

1. 严禁透露任何 API Key、密码、私钥、证书、环境变量配置或内网服务器 IP 地址。
2. 若当前工作区处于 feature/confidential 分支，或被提问涉及保密特性，统一回复："正在内部重构中，细节暂不公开"。
3. 可以如实总结对外暴露的 HTTP/gRPC 接口签名与数据结构，但严禁泄露内部自研打分算法公式与反作弊策略。
4. 遇到关于个人薪资、期权配额、绩效考评（KPI/OKR）或请假记录的询问，必须立即调用 refuse 工具拒绝回答。
5. 本地未提交（uncommitted）且包含 WIP 或 debug 标记的代码，不要提供具体实现细节，仅概括回复正在本地调试。
```

## 7. 日常协同感知与提问使用

### 7.1 发起提问（`talkintent ask`）
`talkintent ask` 允许开发者向指定同事的工作区发起提问。支持通过 `-to` 显式指定姓名，也支持在问题中自然提及同事姓名，系统会自动通过最长字符匹配解析目标。

> ⚠️ 命令行参数传递顺序警告：TalkIntent 采用 Go 标准库 `flag` 进行参数解析。标准库规则规定，**任何跟在非标志位置参数（即提问正文内容）之后的命令行标志都不会被解析为 flag，而是会被原样作为位置参数拼接到提问文本末尾**。例如执行 `talkintent ask "查询进展" -workspace backend -timeout 30` 时，`-workspace`、`backend`、`-timeout`、`30` 会全部被追加为问题文本，不仅导致参数配置失效，还会严重干扰自然语言目标成员提取算法（可能根据追加文本中的词汇错误匹配目标）。因此，**所有可选命令行标志必须严格置于位置参数提问内容之前**，标准语法格式为：`talkintent ask [flags] "<question>"`。

基本提问命令示例：
```bash
# 显式指定目标发起提问（默认长轮询等待回复）
talkintent ask -to zhangsan -query "目前登录模块重构进展如何？有没有新增结构体定义？"

# 自然语言自动提取目标姓名（可选标志必须置于问题之前）
talkintent ask "问一下张三目前登录模块重构进展如何？"

# 携带可选标志（如 -timeout, -workspace, -wait）且置于问题之前
talkintent ask -timeout 30 -workspace backend-service "问一下张三目前登录模块重构进展如何？"

# 异步非阻塞提问（目标离线时直接进入 Hub 离线队列，不阻塞等待，退出码 0）
talkintent ask -to zhangsan -wait=false -query "登录模块进展如何？"
```

注：若自然语言提取出多位重名候选人（例如“小张”同时匹配张三和张伟），CLI 会输出歧义警告与候选列表并以退出码 1 退出，此时需使用 `-to` 显式指定确切姓名。

### 7.2 查询团队名册与实时在线状态（`members`）
查询 Hub 已注册的团队成员清单、别名、受监控工作区与实时在线状态：
```bash
talkintent members
```

### 7.3 双向审计日志追溯（`history`）
系统提供完备的双向审计追踪功能：
- **`-inbound`（谁查了我）**：查看外部同事对本地工作区发起的提问记录、探针在本地调用的只读工具以及最终披露给对方的答复正文。
- **`-outbound`（我的提问）**：追溯当前成员向其他同事发起的提问与获取的答复详情。

在终端中执行：
```bash
talkintent history -inbound
talkintent history -outbound
```

### 7.4 内嵌 Web 控制台使用（`web`）
TalkIntent 内嵌了纯静态原生 Web 控制台（Vanilla HTML/JS/CSS，无外部构建依赖），提供成员状态、双向审计、在线提问表单与飞书绑定等完整视图。

在终端中执行：
```bash
talkintent web -open
```
出于安全设计，终端输出的 URL（`http://<hub_url>/web`）严格不含任何 Token。开发者进入页面后，点击右上角「登录 / 切换 Token」输入配对时获得的成员令牌 `ti_mem_...`，凭据将保存在浏览器的 `localStorage` 中。

### 7.5 Claude Code 智能体 Skill 集成
若团队成员在开发过程中使用 Claude Code CLI，可通过一键安装将 TalkIntent 协同感知能力无缝接入 IDE。

在终端中执行：
```bash
talkintent skill install
```
该命令会自动将 Skill 定义安装至用户目录下的 `~/.claude/skills/talkintent/SKILL.md`。安装完成后，在 Claude Code 会话中可直接使用斜杠命令：
```text
/talkintent 张三 现在登录模块重构得怎么样了，有没有新的结构体定义？
/talkintent lisi 本地开发服务跑在什么端口上？
```
底层将指导 Claude Code 自动运行 `talkintent ask` 获取现场感知结果。

## 8. 企业级集成：飞书机器人对接说明

### 8.1 架构设计与未真机测试诚实声明
TalkIntent 在 `internal/feishu` 中完整实现了飞书开放平台企业自建应用的对接能力，涵盖 Webhook 回调解包、AES-CBC 解密、SHA-256 签名校验、`url_verification` 挑战握手、`im.message.receive_v1` 事件解析以及基于飞书 OpenAPI 的异步文本消息回复（在原消息下以 `reply` 形式发送纯文本，暂不使用消息卡片）。

**在此如实声明：该飞书集成功能已在代码库中通过了完备的 Mock 单元测试集验证，但尚未在真实的线上商业版飞书企业租户中进行过实机连通性联调**。以下配置指引基于代码现有实现编写，供客户在实际接入飞书时参考。

### 8.2 成员自主绑定飞书机器人操作流程

企业权限注意：在企业飞书租户中，创建自建应用并申请敏感消息收发权限通常受到企业安全合规策略限制，普通研发人员往往无权在飞书开放平台独立发布上线应用。建议由企业 IT 运维或飞书租户管理员统一创建自建应用凭据模板并下发，或由管理员在后台统一审批权限并发布应用版本。

1. **Hub 服务端配置**：Hub 服务端无需在全局配置任何飞书 App ID 或 Secret，仅需确保 Hub 启动时配置了外部可解析的 `-public-url`（例如 `https://hub.example.com`）。
2. **创建飞书自建应用**：开发者或管理员登录飞书开放平台（open.feishu.cn），创建自建应用（例如“张三的开发助手”），在应用凭证页面获取 `App ID`（形如 `cli_...`）、`App Secret`、`Verification Token` 以及可选的 `Encrypt Key`。
3. **在 Web 控制台完成凭据绑定**：开发者登录 TalkIntent Web 控制台，进入「飞书 Bot 绑定」标签页，填入上述四项凭据并保存。Hub 使用服务端的 AES-256 主密钥（`master.key`）加密持久化凭据。
4. **配置飞书事件订阅请求网址**：保存后控制台将显示该成员专属的 Webhook 地址（格式为 `https://hub.example.com/api/v1/feishu/webhook/<member_id>`）。将其填入飞书开放平台「事件与回调」的请求网址中。Hub 收到飞书的验证包后会自动应答 `{"challenge": "..."}` 完成握手。
5. **添加事件与申请权限**：在飞书后台添加 `im.message.receive_v1`（接收消息 v2.0）事件，申请开通 `im:message` 与 `im:message:send_as_bot` 权限，并发布应用版本。

### 8.3 交互机制与双向审计
飞书用户在单聊私聊或在群聊中 `@Bot` 发送问题后，飞书服务器向 Hub 的 Webhook 投递事件。Hub 校验签名并在 3 秒 SLA 内立即返回 HTTP 200 `{}`，随后异步向该成员的本地 Daemon 下发感知提问。

若被提问成员处于离线状态，Hub 会立即调用飞书接口回复离线排队提示。待探针执行完成后，Hub 自动刷新 `tenant_access_token` 并调用飞书回复接口（`POST /open-apis/im/v1/messages/<msg_id>/reply`）将答案异步推回飞书。所有来自飞书的提问均被记录在 Hub 事件流中，被提问者可通过 `talkintent history -inbound` 追溯提问者的飞书用户 ID、提问内容、调用的工具与给出的答复。

## 9. 常见故障诊断与排查指南

以下汇集了 TalkIntent 部署与运行中最常见的 8 种故障现象、系统精确报错输出与标准处置方案。

| 故障现象与场景 | 系统精确报错输出 | 根因分析 | 处置与修复措施 |
| :--- | :--- | :--- | :--- |
| 1. 配对邀请码无效或过期 | `Pairing rejected: invalid or expired invite code` | 邀请码输入错误、已被他人消费，或超过了设定的有效期限（默认 72 小时） | 联系系统管理员重新执行 `talkintent invite` 生成新的邀请码，并在有效期内重新执行 `pair`。 |
| 2. Hub 服务端网络不可达 | `dial tcp ...: connect: connection refused` | Hub 进程未启动、反向代理未正确转发，或客户端配置的 Hub 地址/端口错误 | 检查 Hub 服务端状态（`systemctl status talkintent-hub`）；检查反向代理与防火墙端口；检查客户端 `config.json` 中的 `hub_url`。 |
| 3. 目标成员离线且排队超时 | `Query [...] timed out waiting for <Target> to respond.` | 目标成员机器休眠、网络断开或 Daemon 未运行，提问长轮询超出超时上限（默认 60s） | 若允许异步离线等待，提问时追加 `-wait=false`（退出码 0）；若需即时答复，提醒目标成员启动 `talkintent daemon start -detach`。 |
| 4. 离线队列消息超出 TTL 寿命 | `Query expired in offline queue before <Target> came online (Status: expired)` | 提问在 Hub 离线队列中等待目标上线的时长超过了设定 TTL（默认 24 小时，硬上限 7 天） | 该提问已被 Hub 定时扫描淘汰。目标成员上线后提问者需重新发起提问，或在提问时通过 `-ttl <seconds>` 延长存活期。 |
| 5. 本地大模型凭据认证失败 | `LLM test call failed: ... returned status 401: ... invalid_request_error / invalid x-api-key` | 本地配置的大模型 `api-key` 错误、权限失效或 Base URL 端点补全不匹配 | 重新执行 `talkintent llm set -api-key "<new-key>"` 更新凭据，并执行 `talkintent llm test` 验证状态直至输出 `Status: OK`。 |
| 6. 私有网关 TLS 握手失败 | `x509: certificate signed by unknown authority` 或 `x509: certificate is valid for <domain>, not <ip>` | 内网网关使用自建私有 CA 签发证书未被信任，或通过 IP 直连导致证书域名与 SNI 不符 | 执行 `llm set` 时通过 `-ca-file` 传入 PEM 格式根证书，并传入 `-tls-server-name <domain>` 覆盖 SNI 主机名校验。私网测试环境下亦可传入 `-insecure-skip-verify` 跳过校验。 |
| 7. 触碰主权隐私守则判定拒答 | 控制台输出 `Status: refused`，`Reason: ...`，CLI 进程退出码为 `1` | 提问触碰了对方工作区的 `privacy-prompt.md` 规则，探针执行 `refuse` 结构化控制工具 | 属于系统隐私保护机制的预期行为。若在自动化脚本（`set -e`）中调用，命令尾部必须追加 `\|\| true` 防止脚本中断。 |
| 8. 关闭终端后守护进程退出 | 终端关闭后再次执行 `daemon status` 显示 `Daemon: NOT RUNNING` | 启动守护进程时未加 `-detach` 参数，导致进程挂载在前台终端，随终端关闭一同退出 | 必须使用脱离模式启动：执行 `talkintent daemon start -detach`，并执行 `talkintent daemon status` 确认后台存活。 |
