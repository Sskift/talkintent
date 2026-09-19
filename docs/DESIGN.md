# TalkIntent Architecture and Design Specification

Version: 1.0.0  
Status: Frozen Architectural Baseline  
Module: `github.com/Sskift/talkintent`  
Target: Go 1.26+

---

## 1. Executive Summary & Core Philosophy

### 1.1 The Problem
In modern distributed engineering teams, engineers constantly face a dilemma between **information synchronization** and **uninterrupted deep work**:
1. **Asker Latency**: When Engineer A needs to know Engineer B's current progress, branch state, uncommitted API struct changes, or local debugging status, Engineer A must wait for Engineer B to read messages and respond. If Engineer B is in a different timezone, offline, or heads-down coding, latency stretches to hours.
2. **Answerer Interruption**: Context switching destroys engineering productivity. Every "quick question" breaks flow state, even when the question is just "what port is your service running on" or "what is the request schema for the new auth endpoint".
3. **Privacy and Trust**: Engineers cannot and will not expose raw shell access, open telemetry of keystrokes, or unrestricted file sharing. They need fine-grained, sovereign, natural-language privacy controls over what parts of their work-in-progress are public, blurred, or strictly confidential.

### 1.2 The TalkIntent Solution
**TalkIntent** (研发协同感知系统) is an asynchronous Q&A architecture pairing **distributed on-site agent probes** running in developer workspaces with a **central coordination Hub**:
- **Zero Asker Latency**: Anyone authorized can query a teammate's dev workspace state at any time ("How is the auth refactoring coming along?", "What ports are listening?", "What is the new schema in `types.go`?"). Even if the teammate is offline, the query is queued with a TTL and answered automatically as soon as their machine connects.
- **Zero Answerer Interruption**: A background client daemon (`talkintent daemon`) intercepts incoming queries and dispatches a lightweight, ephemeral, read-only LLM probe agent (`internal/probe`). The probe inspects git diffs, branch status, recent edits, configs, and open ports, then synthesizes a human-like, accurate answer.
- **Sovereign Privacy Guardrails**: The answering developer maintains absolute control. Privacy rules are defined in plain natural language (`privacy-prompt.md`) rather than complex YAML DSLs. The probe agent enforces these rules natively as system prompt guardrails, supported by hard file denylists and regex post-redactors.
- **Strict Data Locality**: Raw file contents, diffs, credentials, and intermediate reasoning steps **never leave the developer's machine**. Only the synthesized final answer, the list of tool names invoked, duration, and token counts are sent to the Hub.
- **Local Model Sovereignty**: The answering engineer configures their own LLM credentials (`base_url`, `api_key`, `model`) locally. The Hub never holds or sees member LLM keys.

---

## 2. System Topology and Components

TalkIntent operates across four network topologies:
1. **Local Docker Compose**: Hub + 3 client containers + mock LLM for testing.
2. **Multi-User Linux Host**: Single Linux VM with multiple Unix users (e.g. StarPub-Docker dev containers), each running their own daemon.
3. **Cross-Machine**: Remote Linux Hub reachable via public IP or VPN; Windows/macOS client daemons.
4. **NAT / Public Internet**: All client daemons initiate **outbound WebSocket connections** to the Hub. The Hub is the sole network listener. No port forwarding or public IPs are required on client machines.

```
       +-----------------------------------------------------------+
       |                        Central Hub                        |
       |  - WebSocket Dispatcher (/ws/daemon)                      |
       |  - Member Registry & In-Memory Routing Table              |
       |  - Offline Message Queue (TTL-backed)                     |
       |  - REST API Engine & Long-Polling Coordinator             |
       |  - Audit Storage (Append-only JSONL + Index)              |
       |  - Embedded Web UI (Static HTML/CSS/JS via embed.FS)      |
       |  - Feishu Bot Webhook Gateway & IM Replier                |
       +--------------^----------------------------^---------------+
                      |                            |
          REST Queries|HTTP            REST Queries|HTTP
                      |                            |
       +--------------+--------+    +--------------+---------------+
       | Claude Code / Terminal|    | Feishu Open Platform (Cloud) |
       | Skill: talkintent ask |    | Per-member bot events        |
       +-----------------------+    +------------------------------+
                      ^                            ^
                      | Outbound WS                | Outbound WS
                      v                            v
       +-------------------------------+  +-------------------------------+
       |   Client Machine A (Alice)    |  |    Client Machine B (Bob)     |
       | - talkintent daemon (PID)     |  | - talkintent daemon (PID)     |
       | - Outbound WS to Hub          |  | - Outbound WS to Hub          |
       | - Local config (~/.talkintent)|  | - Local config (~/.talkintent)|
       | - Local LLM Keys (OpenAI/Anth)|  | - Local LLM Keys (OpenAI/Anth)|
       |                               |  |                               |
       |   +-----------------------+   |  |   +-----------------------+   |
       |   | Probe Agent (Spawned) |   |  |   | Probe Agent (Spawned) |   |
       |   | - Privacy Guardrails  |   |  |   | - Privacy Guardrails  |   |
       |   | - Read-only Tools     |   |  |   | - Read-only Tools     |   |
       |   | - Tool Sandbox Check  |   |  |   | - Tool Sandbox Check  |   |
       |   | - Regex Redactor      |   |  |   | - Regex Redactor      |   |
       |   +-----------+-----------+   |  +---+-----------+-----------+   |
       |               |               |                  |               |
       |   +-----------v-----------+   |      +-----------v-----------+   |
       |   | Workspaces: Git, Diffs|   |      | Workspaces: Git, Diffs|   |
       |   | Files, Ports, Specs   |   |      | Files, Ports, Specs   |   |
       |   +-----------------------+   |      +-----------------------+   |
       +-------------------------------+  +-------------------------------+
```

### 2.1 Component Responsibilities

| Component | Location | Responsibility |
|---|---|---|
| `cmd/talkintent` | Single Binary | Entrypoint for all subcommands: `hub`, `pair`, `daemon`, `ask`, `members`, `history`, `llm`, `privacy`, `web`, `skill`, `status`, `version`. |
| `internal/hub` | Hub Server | Listens on HTTP/WS port. Authenticates clients, maintains WebSocket connections, handles query routing, coordinates long-polling, forwards Feishu webhooks, hosts Web UI. |
| `internal/store` | Hub Server | Zero-cgo, zero-sqlite storage. Append-only JSONL log (`events.jsonl`) with crash-resilient in-memory indexing. Handles members, invites, queries, and audit records. |
| `internal/daemon` | Client Node | Runs in background. Connects outbound to Hub via WebSocket (`/ws/daemon`). Manages heartbeat, receives query tasks, dispatches probe runs, uploads responses. |
| `internal/probe` | Client Node | Ephemeral agent orchestrator. Instantiates tool-use loop, loads privacy rules, enforces budgets (steps, tokens, timeouts), executes tools in sandbox, runs post-redactor. |
| `internal/probe/llm` | Client Node | Multi-provider client supporting OpenAI `/v1/chat/completions` (tools) and Anthropic `/v1/messages` (tools) dialects. Communicates directly with user's configured LLM endpoint. |
| `internal/probe/tools` | Client Node | Read-only tool implementations: `git_status`, `git_diff`, `git_log`, `list_dir`, `read_file`, `grep_search`, `recent_files`, `listening_ports`, `find_api_specs`. Enforces sandbox boundaries and hard denylists. |
| `internal/feishu` | Hub Server | Feishu Open Platform integration. Verifies challenge tokens, verifies SHA256 signatures, decrypts AES payloads, parses `im.message.receive_v1`, triggers queries, replies back to Feishu chats with `tenant_access_token`. |
| `internal/cli` | Client Node | Terminal interactions, table formatting, pairing workflows, local privacy test runner, Claude Code skill installer. |
| `internal/config` | Client & Hub | Config loading and saving. Enforces file permission `0600` for secret safety. Resolves `$TALKINTENT_HOME` and `~/.talkintent/config.json`. |
| `web` | Hub Server | Self-contained Web UI embedded via `embed.FS`. Single-page application in standard HTML5/ES6/CSS. Zero external CDN dependencies. |

---

## 3. End-to-End 4-Step Loop Data Flow

```
[Asker]              [Hub REST]            [Hub Router/Queue]         [Client Daemon]       [Probe Agent]       [Target Workspace]
   |                      |                        |                          |                   |                     |
   | (1) POST /queries    |                        |                          |                   |                     |
   |--------------------->|                        |                          |                   |                     |
   |                      | Resolve target member  |                          |                   |                     |
   |                      | Check auth & rate-limit|                          |                   |                     |
   |                      |----------------------->|                          |                   |                     |
   |                      |                        | Check if online          |                   |                     |
   |                      |                        |--[YES: Send via WS]----->|                   |                     |
   |                      |                        |--[NO: Enqueue TTL]       |                   |                     |
   |                      |                        |                          |                   |                     |
   |                      |                        |                          | (2) Spawn Probe   |                     |
   |                      |                        |                          |------------------>|                     |
   |                      |                        |                          |                   | Hot-reload privacy  |
   |                      |                        |                          |                   | System prompt build |
   |                      |                        |                          |                   |                     |
   |                      |                        |                          |                   | (3) Tool-use Loop   |
   |                      |                        |                          |                   |----read git/diff--->|
   |                      |                        |                          |                   |<---file content-----|
   |                      |                        |                          |                   |                     |
   |                      |                        |                          |                   | Enforce Guardrails  |
   |                      |                        |                          |                   | Regex Redaction     |
   |                      |                        |                          |<--final answer----|                     |
   |                      |                        |<--query_response (WS)----|                   |                     |
   |                      |                        |                          |                   |                     |
   |                      | Store audit event      |                          |                   |                     |
   |                      | Deliver to long-poller |                          |                   |                     |
   | (4) Query Result     |<-----------------------|                          |                   |                     |
   |<---------------------|                        |                          |                   |                     |
   | (Or Feishu IM reply) |                        |                          |                   |                     |
```

### Step 1: Query Initiation
- **Sources**:
  - Claude Code Skill: `/talkintent ask "问一下张三现在登录模块重构得怎么样了"`
  - CLI: `talkintent ask --target zhangsan --query "What is the new auth endpoint?"`
  - Web UI: Query console in member dashboard
  - Feishu Bot: Direct message to Zhang San's personal Feishu Bot
- **Request**: Sent to Hub via `POST /api/v1/queries` with Bearer token.
- **Target Resolution**: Asker supplies a natural-language name, handle, or alias (e.g., "张三", "zhangsan", "三哥"). Hub matches against `member.Name` and `member.Aliases`.
  - Exact match: Route immediately.
  - Ambiguous match: Return HTTP 400 with list of matched candidate members (`CandidateMembers`).
  - No match: Return HTTP 404.

### Step 2: Hub Validation & Routing
- Hub validates asker permissions.
- Hub assigns a unique `query_id` (UUIDv4) and timestamps the query.
- Hub checks if the target member has an active WebSocket connection:
  - **Online**: Sends `query_request` frame over WebSocket to the target daemon immediately.
  - **Offline**: Pushes the query to the member's in-memory and persistent offline queue with a configurable TTL (default 24 hours).
- If the asker called with `?wait=30s` (long polling), the HTTP handler pauses on a channel notification.

### Step 3: Daemon Execution & Probe Reasoning
- The daemon receives `query_request` on its WebSocket loop.
- It spawns a probe run within a concurrency-limited worker pool.
- **Probe Lifecycle**:
  1. **Hot-Reload Privacy**: Reads global `~/.talkintent/privacy-prompt.md` and workspace-specific `<workspace>/.talkintent/privacy-prompt.md`.
  2. **System Prompt Synthesis**: Builds system instructions combining base persona, workspace summaries, tool definitions, and the explicit **Privacy Guardrails** section.
  3. **Tool-Use Loop**:
     - The probe talks to the locally configured LLM (OpenAI or Anthropic dialect).
     - Model requests tool calls (e.g., `git_status`, `git_diff`, `read_file`).
     - Tool runner checks sandbox boundaries: paths outside configured workspace roots are rejected.
     - Tool runner checks file denylist: `.env`, `id_rsa`, `*.pem`, `*.key`, `*token*`, etc., return permission denied errors.
     - Reading tools enforce byte-size caps (default 64 KB).
     - Loop repeats until model produces a final text response or budget limits (max 10 steps, 30s timeout, max tokens) are reached.
  4. **Post-Redaction**: Output passes through regex filters stripping unintended API keys, private IPv4/IPv6 addresses, and JWT tokens.
  5. **Data Stripping**: Raw tool inputs and file contents are discarded. Only the final textual answer, tool names list (`["git_status", "read_file"]`), execution duration, and token usage are preserved.

### Step 4: Response Relay & Audit
- Daemon sends `query_response` message over WebSocket to Hub.
- Hub marks the query state as `completed` (or `refused`, `error`, `timeout`).
- Hub appends an audit event to the append-only JSONL log.
- Hub notifies any waiting long-poll HTTP request.
- If the query originated from Feishu, Hub calls the Feishu Open Platform API using the member's `tenant_access_token` to reply directly to the Feishu chat thread.
- Both asker and answerer can immediately view the transaction in their respective Web UI audit logs.

---

## 4. Offline Queue Semantics

TalkIntent guarantees asynchronous availability across timezones and off-hours.

```
       [Query Enqueued] ---> (Store in Memory & JSONL) ---> [Wait for Reconnect]
              |                                                     |
       TTL Clock Ticking                                     [Daemon Reconnects]
              |                                                     |
    [Exceeded TTL (e.g. 24h)]                               [Pop FIFO Queue]
              |                                                     |
       Mark "expired"                                       [Dispatch to Daemon]
       Record in Audit                                              |
       Notify if Asker Polling                              [Process Query]
```

### 4.1 Queue Specifications
1. **Persistence**: Queued messages are written to `$DATA_DIR/events.jsonl` under event type `query_enqueued`. They survive Hub restarts.
2. **TTL (Time to Live)**: Default 24 hours (`86400s`), customizable per query up to 7 days.
3. **Ordering**: Per-member FIFO (First-In, First-Out).
4. **Reconnection Handshake**:
   - When a daemon establishes a WebSocket session (`GET /ws/daemon`), it sends `daemon_hello`.
   - Hub acknowledges with `hub_ack` and inspects the member's offline queue.
   - Hub immediately drains queued `query_request` frames sequentially, observing the daemon's advertised concurrency limit (default 2).
5. **Asker Experience**:
   - Asker receives `status: "queued"`, `queue_position: N`, `query_id: "..."`.
   - Asker can pass `?wait=X` to hold HTTP connection. If member does not connect before `wait` expires, HTTP returns current status `queued`.
   - Asker can poll `GET /api/v1/queries/{id}` later or rely on Feishu / Claude Code notifications.

---

## 5. Security & Privacy Model

### 5.1 Authentication & Tokens
- **Admin Token**: Configured on the Hub via environment variable `TALKINTENT_ADMIN_TOKEN` or generated on first start and stored in `$DATA_DIR/admin.token`. Used to create invite codes.
- **Invite Codes**: 8-character or UUID-based single-use tokens generated by admin (`POST /api/v1/admin/invites`). Has expiration (e.g., 48 hours).
- **Member Tokens**: Generated when a client pairs with the Hub (`talkintent pair --hub <url> --code <code>`).
- **At-Rest Hashing**: All tokens (invite codes, member tokens) are stored in the Hub's store as **SHA-256 hashes**. Plaintext tokens are never stored on the Hub.
- **Client Credential Storage**: Stored in `~/.talkintent/config.json` with strict POSIX permissions `0600` (user read/write only). On Windows, access is restricted to the current user SID.

### 5.2 Local LLM Credential Sovereignty
- **Never Transmitted**: Client LLM settings (`base_url`, `api_key`, `provider`, `model`) reside exclusively on the client machine.
- Hub has zero knowledge of client LLM configurations.
- Answering costs and API usage are billed to each member's personal or team provider key.

### 5.3 Data Egress Boundary
The boundary between what leaves the developer's machine and what stays local is absolute:

| Data Item | Transmitted to Hub? | Notes |
|---|---|---|
| Final Answer Text | **YES** | Synthesized summary, post-redacted |
| Tools Used | **YES** | Tool names only (e.g. `["git_status", "read_file"]`) |
| Execution Metrics | **YES** | Elapsed time, token counts, error status |
| File Contents | **NO (STRICT)** | Processed purely in local probe memory |
| Git Diffs & Commit Messages | **NO (STRICT)** | Processed purely in local probe memory |
| Directory Trees & Filenames | **NO (STRICT)** | Processed purely in local probe memory |
| LLM Reasoning / Chain of Thought | **NO (STRICT)** | Kept local, only final answer sent |
| Privacy Prompts | **NO (STRICT)** | Guardrail stays on local disk |
| Environment Variables & Secrets | **NO (STRICT)** | Hard denylisted from probe tools |

### 5.4 Workspace Sandbox & Hard Denylist
The probe agent's tool execution engine enforces two layers of physical constraints:
1. **Directory Sandbox**:
   - Every workspace has an absolute root path.
   - Any path argument containing `..` that resolves outside configured workspace roots is rejected with `access denied: path outside workspace`.
   - Symbolic links resolving outside workspace roots are followed only if explicitly enabled in client config.
2. **Hard File Denylist**:
   - The probe refuses to read or inspect files matching sensitive patterns regardless of privacy prompts:
     - Keys & Certificates: `*.pem`, `*.key`, `*.crt`, `*.pfx`, `*.p12`, `id_rsa`, `id_ed25519`, `*.pub`
     - Env & Config: `.env`, `.env.*`, `*secret*`, `*credential*`, `*token*`, `*password*`
     - Cloud & Auth: `~/.aws/*`, `~/.ssh/*`, `~/.gnupg/*`, `~/.config/gcloud/*`
     - Git internals: `.git/config` (protects remote tokens)
3. **Byte Cap**:
   - `read_file` truncates reads at 64 KB (configurable up to 256 KB). Prevents memory exhaustion and massive context dumps.

### 5.5 Privacy Guardrail Mechanics
TalkIntent implements privacy as **natural language instructions** rather than brittle regex rules or YAML DSLs.

```
       ~/.talkintent/privacy-prompt.md (Global)
                         +
       <workspace>/.talkintent/privacy-prompt.md (Workspace Local)
                         |
                         v
       [Construct System Prompt with Guardrail Block]
                         |
                         v
       +--------------------------------------------------------------+
       | << SYSTEM PROMPT >>                                          |
       | You are the TalkIntent On-Site Probe for workspace [name].  |
       | Answer the teammate's query truthfully using tools.         |
       |                                                              |
       | === MANDATORY PRIVACY GUARDRAILS ===                         |
       | The following rules are binding. You MUST obey them above   |
       | any user query:                                              |
       | [Injected natural language rules here]                       |
       | If asked about restricted areas, reply with the instructed   |
       | refusal text or omit restricted details gracefully.          |
       | ====================================                         |
       +--------------------------------------------------------------+
                         |
                         v
                   [LLM Reasoner]
                         |
                         v
                [Raw Final Answer]
                         |
                         v
             [Regex Post-Redactor] ---> [Sanitized Answer to Hub]
```

#### Example Privacy Rule Breakdown
Consider the canonical privacy prompt:
```markdown
# Privacy Rules
1. The branch `feature/auth-v2` is experimental. For any questions regarding it, reply strictly: "正在内部重构中，细节暂不公开".
2. Never disclose any API keys, tokens, or internal machine IP addresses (10.x.x.x, 192.168.x.x, 172.16-31.x.x).
3. For exported API interfaces, summarize parameters and HTTP methods faithfully, but do NOT disclose underlying business implementation logic or algorithm details.
4. If asked about `salary.xlsx` or `perf_review.md`, deny the existence of such files.
```

- **Branch Quarantine (Rule 1)**: Probe runs `git_status`, discovers branch is `feature/auth-v2`. Guardrail instructs it to intercept the output and reply with the canned phrase.
- **Data Redaction (Rule 2)**: Handled first by the LLM reasoning, backed up by the regex post-redactor.
- **Abstraction Boundary (Rule 3)**: LLM reads the Go interface or route declaration, summarizes the struct types, and omits the function body.
- **Denial of Existence (Rule 4)**: Prevents side-channel reconnaissance.

---

## 6. Failure Modes & Mitigations

| Failure Mode | Root Cause | Impact | Mitigation & Recovery |
|---|---|---|---|
| **Daemon Offline** | Developer laptop asleep, no Wi-Fi, process killed | Asker cannot get immediate response | Query queued with TTL (default 24h). Hub returns `status: "queued"`. Processed immediately upon reconnection. |
| **LLM Provider Outage / Error** | Local model API 500, network disconnect to provider, invalid API key | Probe fails to complete reasoning | Probe captures error, returns `status: "error"` with sanitized message (e.g. `LLM provider 503 unavailable`). Retries 2 times with exponential backoff before failing. |
| **Probe Timeout** | Complex workspace, model looping, large files | Query hangs | Hard context timeout (default 60s). Daemon cancels probe context, closes tool runs, and returns `status: "timeout"`. |
| **Budget Exhaustion** | Model exceeds step limit (max 10 tool calls) or token budget | Infinite tool loops | Probe terminates tool loop upon reaching step 10. Forces model to synthesize best-effort answer with existing context or return partial summary. |
| **Oversize Output** | Model outputs massive dump (>64 KB) | WebSocket congestion, Hub store bloat | Daemon truncates response to 16 KB with trailing `[truncated by TalkIntent daemon]`. |
| **Feishu Token Expiry** | `tenant_access_token` expired (valid 2h) | Feishu reply fails | Hub caches `tenant_access_token` with automatic refresh 5 minutes before expiry. In case of 400 invalid token, evicts cache and re-fetches. |
| **Corrupted JSONL** | Abrupt power off during write | Hub startup fails | Hub store parser reads line-by-line. If last line is truncated/partial, it discards the incomplete line, logs warning, and keeps all prior valid events. |

---

## 7. Work Packages Breakdown

To enable rapid, conflict-free parallel implementation across multiple engineers or autonomous subagents, the TalkIntent codebase is partitioned into **8 disjoint work packages**.

### 7.1 Architecture Freeze Notice
The following files constitute the **Frozen Architecture Foundation**. They are authored and locked by the Architect:
- `go.mod` and `go.sum` (Go 1.26, `github.com/coder/websocket` as sole external dependency)
- `internal/protocol/**` (All shared WebSocket frames, REST requests/responses, and error models)
- `docs/DESIGN.md` (System specification)
- `docs/PROTOCOL.md` (Wire protocol specification)
- `.github/workflows/ci.yml` (Continuous integration pipeline)

No subsequent work package may modify or re-litigate the shared types in `internal/protocol` or dependencies in `go.mod` without explicit architectural approval.

### 7.2 Work Package Matrix

```
+---------------------------------------------------------------------------------------+
|                                    Work Packages                                      |
+------+-------------------------+----------------------------------+-------------------+
| ID   | Title                   | Owned Paths (Strictly Disjoint)  | Primary Consumer  |
+------+-------------------------+----------------------------------+-------------------+
| WP1  | Core Storage Engine     | internal/store/**                | WP2, WP5          |
| WP2  | Hub Server & Dispatch   | internal/hub/**                  | WP3, WP6, WP7     |
| WP3  | Client Daemon Transport | internal/daemon/**               | WP6               |
| WP4  | Probe Agent & Sandbox   | internal/probe/**                | WP3               |
| WP5  | Feishu Bot Gateway      | internal/feishu/**               | WP2               |
| WP6  | CLI, Config & Skill     | internal/cli/**, internal/config/**| End User        |
|      |                         | cmd/talkintent/**, skills/**     |                   |
| WP7  | Embedded Web Dashboard  | web/**                           | WP2, End User     |
| WP8  | Mock LLM & E2E Scenarios| test/**, deploy/**               | CI & QA           |
+------+-------------------------+----------------------------------+-------------------+
```

### 7.3 Detailed Package Specifications

#### WP1: Core Storage Engine & In-Memory Indexing
- **Owned Paths**: `internal/store/**`
- **Summary**: Implements the zero-cgo, zero-sqlite storage layer. Reads and appends to `events.jsonl` under `$DATA_DIR`. Rebuilds in-memory indexes on startup for members, invite codes, query states, and audit records. Provides atomic write locks, SHA-256 token hashing, snapshot creation, and clean error handling for partial lines.
- **Interfaces Consumed**: `internal/protocol` (shared models).
- **Acceptance Tests**:
  - `store_test.go`: Append 1000 events, restart store, verify index reconstruction matches exactly.
  - Test crash recovery with half-written trailing line.
  - Test member lookup by exact name and aliases.
  - Test token hash verification.

#### WP2: Hub Server, WebSocket Manager & Routing Core
- **Owned Paths**: `internal/hub/**`
- **Summary**: Implements the central HTTP and WebSocket listener. Manages connected client daemons via `/ws/daemon`, routes incoming queries from REST to online daemons, coordinates the offline queue with TTL expiration, handles long-polling query waits (`?wait=30s`), mounts the static Web UI, and enforces admin/member authentication.
- **Interfaces Consumed**: `internal/protocol`, `internal/store` (`store.Store`), `web` (`embed.FS`).
- **Acceptance Tests**:
  - `hub_test.go`: Spin up httptest server, pair client, connect mock daemon over WS, post query via REST, receive WS query frame, return WS answer, verify REST receives answer.
  - Test offline queueing: post query to disconnected member, verify stored in queue; connect daemon, verify immediate delivery.

#### WP3: Client Daemon & Resilient WebSocket Transport
- **Owned Paths**: `internal/daemon/**`
- **Summary**: Implements the background worker daemon (`talkintent daemon`). Connects outbound to Hub via WebSocket with automatic reconnection and exponential backoff. Responds to ping/pong heartbeats, receives `query_request` frames, invokes the probe agent with concurrency throttling (default 2), and reports `query_response` back to Hub.
- **Interfaces Consumed**: `internal/protocol`, `internal/config`, `internal/probe` (`probe.Agent`).
- **Acceptance Tests**:
  - `daemon_test.go`: Mock WebSocket Hub server, verify daemon connects with Bearer token, sends `daemon_hello`, responds to heartbeats, dispatches incoming query to probe mock, sends `query_response`.
  - Verify backoff reconnection when Hub closes connection.

#### WP4: Probe Agent, Tool Sandbox & Privacy Guardrails
- **Owned Paths**: `internal/probe/**`
- **Summary**: Implements the ephemeral probe agent. Supports both OpenAI `/v1/chat/completions` and Anthropic `/v1/messages` tool-calling dialects. Constructs system prompts with hot-reloaded privacy guardrails from `privacy-prompt.md`. Implements all 9 read-only tools (`git_status`, `git_diff`, `git_log`, `list_dir`, `read_file`, `grep_search`, `recent_files`, `listening_ports`, `find_api_specs`) strictly sandboxed to workspace roots with hard denylists. Applies regex post-redactor.
- **Interfaces Consumed**: `internal/protocol`.
- **Acceptance Tests**:
  - `probe_test.go`: Run probe against mock LLM; verify tool call execution and final answer synthesis.
  - `sandbox_test.go`: Verify directory traversal (`../`) is blocked. Verify `.env`, `id_rsa`, `token` files return permission denied.
  - `privacy_test.go`: Test canonical rule ("feature/auth-v2 branch returns canned text").
  - `redactor_test.go`: Verify leaked AWS keys and private IPs are redacted.

#### WP5: Feishu Bot Gateway & Webhook Engine
- **Owned Paths**: `internal/feishu/**`
- **Summary**: Implements Feishu Open Platform bot integration. Handles webhook URL challenge verification, SHA-256 signature checking, AES-CBC payload decryption, and event parsing for `im.message.receive_v1`. Extracts query text, calls Hub router to process query asynchronously, and replies to the Feishu message using `tenant_access_token`. Includes fake Feishu server for automated tests.
- **Interfaces Consumed**: `internal/protocol`, `internal/store`.
- **Acceptance Tests**:
  - `feishu_test.go`: Send encrypted challenge, verify plain response. Send encrypted message event with valid signature, verify query dispatched and reply API called on fake Feishu server.

#### WP6: CLI Subcommands, Config Management & Claude Code Skill
- **Owned Paths**: `internal/cli/**`, `internal/config/**`, `cmd/talkintent/**`, `skills/**`
- **Summary**: Implements the user-facing CLI binary and subcommands: `pair`, `daemon`, `ask`, `members`, `history`, `llm`, `privacy`, `web`, `skill`, `status`, `version`. Manages `0600` config file persistence in `~/.talkintent/config.json`. Implements `talkintent privacy test "<query>"` for local dry-run debugging. Packages Claude Code skill definition and install command.
- **Interfaces Consumed**: `internal/protocol`, `internal/store`, `internal/hub`, `internal/daemon`, `internal/probe`.
- **Acceptance Tests**:
  - `cli_test.go`: Test flag parsing and output formatting for all subcommands.
  - `config_test.go`: Test config serialization, loading, and permission verification (0600).
  - Test skill installation into mock `.claude/skills` directory.

#### WP7: Embedded Web UI Dashboard
- **Owned Paths**: `web/**`
- **Summary**: Implements the vanilla HTML5/ES6/CSS dashboard embedded directly into the Go binary. Provides member token login, real-time member online/offline status, inbound audit view (who asked what about my workspace), outbound audit view, query detail timeline with tool inspection, Feishu bot binding manager, and admin invite generator. Zero external build steps, zero external CDN scripts.
- **Interfaces Consumed**: `internal/protocol` (via Hub REST endpoints).
- **Acceptance Tests**:
  - `web_test.go`: Verify embedded static files are non-empty and served with correct MIME types (`text/html`, `application/javascript`, `text/css`).
  - Test basic DOM elements present in `index.html`.

#### WP8: Mock LLM Server, End-to-End Scenarios & Deployment Suites
- **Owned Paths**: `test/**`, `deploy/**`
- **Summary**: Implements reusable standalone mock LLM server supporting both OpenAI and Anthropic formats. Authors Docker Compose multi-container deployment (Hub + 3 Daemons + Mock LLM). Authors StarPub multi-Linux-user deployment script. Authors end-to-end integration test exercising the full 4-step loop in-process.
- **Interfaces Consumed**: `internal/protocol`, `cmd/talkintent`.
- **Acceptance Tests**:
  - `e2e_test.go`: Spin up Hub, 2 client daemons, and mock LLM in a single test process. Pair clients, trigger query, verify probe runs tools, returns answer, and audit log records event.
  - Verify docker compose syntax and starpub runner script execution.
