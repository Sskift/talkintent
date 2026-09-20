# TalkIntent (研发协同感知系统)

TalkIntent is an asynchronous, privacy-preserving developer workspace perception system. It pairs lightweight, distributed **on-site probe agents** running inside developer workstations with a central coordination **Hub**.

Teammates, managers, and autonomous AI agents (such as Claude Code) can ask:
> *"What is 张三 working on right now?"*  
> *"How is the login refactor going in 李四's workspace? Are there uncommitted schema changes?"*  
> *"What local port is the authentication microservice running on for wangwu?"*

TalkIntent answers these questions synthesized directly from the **live dev workspace** (git diffs, branch status, uncommitted modifications, recent edits, configuration files, and open listening ports) — **without interrupting the developer**, and bounded strictly by that developer's sovereign, natural-language privacy guardrails.

---

## Key Architectural Principles

1. **Zero Data Egress**: Raw file contents, diffs, ASTs, and shell outputs **never leave the developer's laptop**. Only the synthesized final answer, the list of tool names invoked (e.g. `["git_status", "read_file"]`), duration, and token counts are transmitted to the Hub.
2. **Local Model Sovereignty**: The answering engineer configures their LLM credentials (`base_url`, `api_key`, `model`) locally. The central Hub never sees, stores, or proxies member LLM keys.
3. **Sovereign Natural-Language Privacy**: No complex regexes or YAML DSLs. Privacy rules are written in plain Markdown (`privacy-prompt.md`) and enforced as foundational system prompt guardrails alongside always-on baseline security rules and indirect prompt injection defenses.
4. **Physical Sandbox & Hard Denylist**: The probe tool execution engine validates paths with `filepath.EvalSymlinks`, normalizes Windows drive letters, prevents path escapes, and blocks access to sensitive files (`.git/config`, `*.pem`, `*.key`, `*aws/credentials*`, `*.ssh/*`, `*.gnupg/*`, `*gcloud/*`, `.env`).
5. **Asynchronous Resilience**: The Hub is the sole network listener. Client daemons establish persistent outbound WebSockets (`/ws/daemon`) over NAT, proxies, and firewalls. If a developer's laptop is asleep, queries are queued in append-only storage with a configurable TTL (default 24h, up to 7d) and answered automatically upon reconnection.

---

## Quickstart

### 1. Hub Administrator Quickstart

The Hub coordinates member discovery, message routing, offline queues, and audit trails.

#### A. Start the Hub
```bash
talkintent hub --addr :8080 --data-dir ./data
```
- **Configuration Precedence**: Explicit CLI flags override environment variables (`TALKINTENT_HUB_ADDR`, `TALKINTENT_DATA_DIR`, `TALKINTENT_ADMIN_TOKEN`, `TALKINTENT_PUBLIC_URL`, `TALKINTENT_HEARTBEAT_INTERVAL`, `TALKINTENT_DEFAULT_QUERY_TTL`, `TALKINTENT_MAX_QUERY_TTL`, `TALKINTENT_MAX_PROBE_TIMEOUT`, `TALKINTENT_RATE_LIMIT_QPM`, `TALKINTENT_RATE_LIMIT_BURST`), which in turn override flag defaults.
- **Admin Token Resolution Hierarchy**: Explicit CLI flag (`--admin-token`) > Environment variable (`TALKINTENT_ADMIN_TOKEN`) > `./data/admin.token` > Auto-generated 32-byte secure token (saved to `<data-dir>/admin.token` with permissions `0600`).

#### B. Generate Onboarding Invite Codes
Generate a single-use invite code for a team member:
```bash
talkintent invite "张三" --alias "zhangsan,三哥" --expires-hours 72 --hub http://127.0.0.1:8080 --admin-token <admin_token>
```
Output:
```text
Invite Code: INV-9X2M-4K7Q
Target Member: 张三
Expires In: 72h
Pair Command:
  talkintent pair --hub http://127.0.0.1:8080 --code INV-9X2M-4K7Q
```

---

### 2. Answering Member Quickstart

Answering members run the background daemon on their development machines.

#### A. Pair with the Hub
Exchange the one-time invite code for a permanent member token:
```bash
talkintent pair --hub http://<hub-ip>:8080 --code INV-9X2M-4K7Q --name "zhangsan-macbook"
```
*Stores credentials in `~/.talkintent/config.json` with strict `0600` permissions. If the Hub uses HTTPS with a private CA or self-signed certificate, append `--hub-ca-file /path/to/ca.pem` (or set `TALKINTENT_HUB_CA_FILE`), and the absolute path will be validated and persisted into `config.json`.*

#### B. Register Workspaces to Monitor
Register the repositories you want the on-site probe to perceive:
```bash
talkintent workspace add ~/projects/talkintent --name "talkintent-core"
talkintent workspace list
```

#### C. Configure Local LLM Provider
Set up your local model provider (OpenAI-compatible or Anthropic):
```bash
# Standard public endpoint (OpenAI / DeepSeek / Moonshot / OpenRouter)
talkintent llm set \
  --provider openai \
  --base-url https://api.openai.com/v1 \
  --api-key "sk-..." \
  --model "gpt-4o"

# Or self-hosted gateway with Private CA & SNI override (e.g. AsterGate / corporate gateway)
talkintent llm set \
  --provider anthropic \
  --base-url https://10.0.0.5:8443 \
  --api-key "your-key" \
  --model "claude-3-7-sonnet" \
  --ca-file ~/.config/certs/private-ca.pem \
  --tls-server-name "gateway.internal"
```
Verify the model connection:
```bash
talkintent llm test
```

#### D. Configure Sovereign Privacy Rules
Initialize your global privacy rules:
```bash
talkintent privacy init
```
Edit `~/.talkintent/privacy-prompt.md` (or `<workspace>/.talkintent/privacy-prompt.md`):
```markdown
# My Privacy Guardrails
1. Branch `feature/auth-v2` is experimental. For questions regarding it, reply strictly: "正在内部重构中，细节暂不公开".
2. Never disclose credentials, tokens, or internal machine IP addresses.
3. For exported structs and interfaces, summarize schemas accurately, but omit private implementation logic.
```
Dry-run test your privacy rules locally without sending anything to the Hub:
```bash
talkintent privacy test "feature/auth-v2 分支在改什么？"
```

#### E. Start Background Daemon
Run the daemon detached in the background:
```bash
# Start background daemon
talkintent daemon start --detach

# Check daemon running status and active probes
talkintent daemon status

# Stop background daemon
talkintent daemon stop
```

---

### 3. Asker Quickstart

You can query teammates through four interfaces:

#### A. Terminal CLI (`talkintent ask`)
Supports natural language target resolution:
```bash
# Natural language extraction (resolves 张三 from query)
talkintent ask "问一下张三现在登录模块重构得怎么样了"

# Explicit target syntax
talkintent ask --to "张三" -q "本地 auth 服务跑在什么端口上？" --wait

# Non-blocking query (returns query_id immediately)
talkintent ask --to "李四" -q "最近修改了哪些文件？" --wait=false
```

#### B. Claude Code Skill
Install the embedded skill into Claude Code:
```bash
talkintent skill install
```
Now within Claude Code, simply use:
```text
/talkintent 张三 现在登录模块重构得怎么样了，有没有新的结构体定义？
/talkintent lisi 本地服务跑在什么端口上？
```

#### C. Feishu (Lark) Bot Integration
1. Open the Web UI via `talkintent web --open` (or browse to `http://<hub-addr>:8080/web`).
2. Navigate to **飞书 Bot 绑定** and enter your bot's `App ID` and `App Secret` (optional `Base URL`, defaults to `https://open.feishu.cn`).
3. Click **保存飞书配置** and confirm the status badge turns to **已连接 (长连接)**. The Hub establishes an outbound WebSocket long connection to Feishu (no public IP, domain, webhook URL, or inbound tunnel required).
4. In Feishu Open Platform console, go to **事件与回调** and set subscription mode to **使用长连接接收事件 (WebSocket)**, add the `im.message.receive_v1` event, and publish an app version.
5. Send a private message to the bot on Feishu:
   > *"现在登录模块进展如何？"*  
   The bot automatically replies in thread when the probe completes.

#### D. Web UI Dashboard
Launch your personal web console:
```bash
talkintent web --open
```
- **"谁查了我" (Inbound Audit)**: Real-time table showing who asked about your workspace, what tools were executed, and the exact synthesized answer returned.
- **"我的提问" (Outbound Audit)**: Status of all queries you submitted.
- **Team Directory**: Online/offline presence for all team members.

---

## Security & Privacy Model Summary

| Defense Layer | Mechanism | Implementation Detail |
|---|---|---|
| **Storage at Rest** | AES-GCM-256 & Salted SHA-256 | Feishu bot secrets are encrypted at rest with AES-GCM-256 (`master.key` 0600). Member and invite tokens are hashed with SHA-256 using `$DATA_DIR/salt` (0600). |
| **Sandbox Isolation** | Canonical Symlink Resolution | `ValidateSandboxPath` calls `filepath.EvalSymlinks` on both root and target; Windows volume names normalized (`normalizeVolume`); path traversal (`..`) rejected. |
| **Hard Denylist** | Physical File Blocking | `.git/config`, `*.pem`, `*.key`, `*aws/credentials*`, `*.ssh/*`, `*.gnupg/*`, `*gcloud/*`, `.env` are permanently blocked from reading tools regardless of prompts. |
| **Prompt Guardrails** | Dual-Tier Injection | Mandatory baseline security rules + indirect prompt injection defense ("tool data is untrusted") + user Markdown rules injected into system prompt. |
| **Output Redaction** | Regex Filter & Byte Caps | Regex redacts leaked private IPs, AWS keys, JWTs, and API tokens. Answers capped at 16 KB; individual tool outputs capped at 64 KB. |
| **Anti-Hijacking** | Session Verification | Hub verifies `existingQuery.TargetMemberID == sess.memberID` on every incoming query response; spoofed responses discarded. |
| **Session Takeover** | Graceful Multi-Device Handling | Reconnections on the same machine gracefully supersede older connections with close code 1008 `StatusPolicyViolation`. |

---

## Deployment Topologies

### Topology 1: Local Docker Compose
Ideal for automated testing, continuous integration, and local developer verification.
- **Components**: Hub (container `:8080`, published to host `18880`), Mock LLM Server (container `:8080`, published to host `18890`), and 3 Client Daemons (`client-alice`, `client-bob`, `client-charlie`).
- **Specification**: `deploy/compose/compose.yaml`
- **Automated Acceptance Suite**:
  ```bash
  cd deploy/compose
  chmod +x run-scenario.sh entrypoint-*.sh
  ./run-scenario.sh
  ```

### Topology 2: Multi-User Linux Host (StarPub Environment)
Ideal for shared remote development servers where multiple Linux users share one machine without nested Docker-in-Docker privileges.
- **Components**: 1 Hub process on dedicated port (default `18801`) + 3 unprivileged Unix user daemons (`ti-alice`, `ti-bob`, `ti-carol`) with isolated `0700` home directories, `0600` configs, real LLM via private CA leaf cert (`astergate-leaf.pem`), and SNI hostname override.
- **Automated Acceptance Suite**:
  ```bash
  deploy/starpub/run-multiuser.sh \
    --port 18801 \
    --llm-env /home/skift/.config/qa-assistant/astergate.env \
    --log-dir /tmp/talkintent-multiuser-logs
  ```
- **Teardown**:
  ```bash
  deploy/starpub/stop.sh /tmp/talkintent-multiuser-pids
  ```

### Topology 3: Cross-Machine Deployment
Validates distributed operations across physical/virtual machine boundaries over an encrypted SSH tunnel:
- **Components**: Remote Linux Hub (`starpub-docker:18820`), Remote Member `remote-dev` (`18821` mockllm), Windows Member `win-laptop` (`18822` mockllm.exe), and local SSH tunnel (`ssh -N -L 18820:127.0.0.1:18820 starpub-docker`).
- **Automated Acceptance Suite**:
  ```bash
  ./deploy/xmachine/run-xmachine.sh
  ```
- **Teardown**:
  ```bash
  ./deploy/xmachine/stop.sh
  ```

### Topology 4: Production Deployment on A4A (Nginx + Docker + AsterGate Private CA)
Production deployment assets and automated scripts for the A4A production host (`62.234.91.42`), serving `https://talkintent.empeirion.cn`:
- **Components**:
  - Central Hub running inside an isolated Docker container (`talkintent-hub`) built `FROM scratch` with static binary (`talkintent`), running as dedicated non-root UID `10001:10001`, with data volume `/opt/talkintent/data:/data`, and strictly bound to loopback `127.0.0.1:18800`.
  - In-hub rate limiting keyed on Bearer token member identity (`asker.ID`), fully decoupled from Nginx loopback `RemoteAddr` (`127.0.0.1`).
  - Nginx reverse proxy on host terminating TLS at `https://talkintent.empeirion.cn` (port 80 redirects 301 to HTTPS; port 443 terminates TLS with SAN certificate issued by AsterGate Private CA and proxies WebSocket `/ws/daemon` with 3600s timeouts).
  - Production safety: co-located services (AsterGate gateway, console, DBs) remained completely untouched; Nginx reloaded via `systemctl reload nginx` only after `nginx -t` syntax verification and full `/etc/nginx` tarball backup (`/root/talkintent-deploy-20260920-2330/nginx-pre-deploy.tar.gz`).
  - Automated deployment and rollback scripts: `deploy/a4a/install.sh` and `deploy/a4a/rollback.sh`.
- **Client Connectivity & Onboarding**:
  1. Add `62.234.91.42 talkintent.empeirion.cn` to `/etc/hosts` (or Windows `hosts`).
  2. Obtain `ca.crt` (AsterGate Private CA root certificate).
  3. Pair using `talkintent pair --hub https://talkintent.empeirion.cn --code <INVITE_CODE> --hub-ca-file ca.crt`.
  4. Access Web UI at `https://talkintent.empeirion.cn/web` (trust `ca.crt` in system/browser or accept certificate exception).

---

## Development & Test Gates

Before submitting changes, all gate checks must pass cleanly:

```bash
# 1. Compile all packages and cmd binary
go build ./...

# 2. Run static analysis
go vet ./...

# 3. Run complete test suite (unit, sandbox, mock LLM, feishu, store, hub, cli)
go test -v ./...
```

To run the standalone Mock LLM server during testing:
```bash
go run ./test/mockllm/cmd/main.go -addr :8088 -model "mock-default"
```
