# TalkIntent Wire Protocol Specification

Version: 1.0.0  
Status: Frozen Protocol Specification  
Base Path: `/api/v1`  
WebSocket Path: `/ws/daemon`

---

## 1. Protocol Architecture & Common Conventions

### 1.1 Transport Channels
TalkIntent defines two primary transport channels:
1. **Outbound WebSocket (`/ws/daemon`)**: Established by background client daemons (`talkintent daemon`) to the central Hub. Used for persistent bidirectional signaling, heartbeats, task push, and answer delivery. Survives NAT and firewalls.
2. **REST API (`/api/v1/*`)**: Used by clients (CLI, Claude Code Skill, Web UI, Feishu Open Platform) to initiate queries, query status, configure members, and retrieve audit trails.

### 1.2 Common JSON Envelope & Versioning
All WebSocket frames use a standardized top-level JSON envelope with an explicit `version` field.

```json
{
  "version": "v1",
  "type": "message_type_name",
  "id": "msg_01J8ABCDEF1234567890",
  "timestamp": 1726828800000,
  "payload": {}
}
```

#### Envelope Fields
| Field | Type | Description |
|---|---|---|
| `version` | string | Protocol version. Fixed to `"v1"`. Clients must reject major version mismatches. |
| `type` | string | Message type discriminator. |
| `id` | string | Unique message identifier (UUIDv4 or ULID). Used for tracing and request-response matching. |
| `timestamp` | int64 | Epoch millisecond timestamp of message generation. |
| `payload` | object | Type-specific payload body. |

#### Compatibility Rules
- **Forward Compatibility**: Receivers must ignore unknown JSON properties in both the envelope and payload objects.
- **Breaking Changes**: Any breaking schema modification will increment `version` to `"v2"`.

### 1.3 Standard Error Format
All REST error responses follow a uniform JSON schema:

```json
{
  "error": {
    "code": "TARGET_OFFLINE",
    "message": "Target member 'zhangsan' is currently offline. Query has been queued.",
    "details": {
      "target_id": "mem_01J8ABC...",
      "queue_id": "qry_01J8XYZ..."
    }
  }
}
```

| Code | HTTP Status | Description |
|---|---|---|
| `UNAUTHORIZED` | 401 | Missing or invalid Bearer token. |
| `FORBIDDEN` | 403 | Token lacks required permission (e.g. non-admin calling admin endpoint). |
| `INVALID_ARGUMENT` | 400 | Malformed JSON body or invalid parameter values. |
| `AMBIGUOUS_TARGET` | 400 | Target name matches multiple members. Candidates returned in details. |
| `NOT_FOUND` | 404 | Target member or query ID not found. |
| `CONFLICT` | 409 | Resource already exists (e.g. duplicate member name). |
| `RATE_LIMITED` | 429 | Query submission rate limit exceeded. |
| `INTERNAL_ERROR` | 500 | Unexpected server error. |

---

## 2. WebSocket Protocol (`/ws/daemon`)

### 2.1 Handshake & Authentication
The client daemon initiates an HTTP Upgrade request:
```http
GET /ws/daemon HTTP/1.1
Host: hub.talkintent.internal
Upgrade: websocket
Connection: Upgrade
Sec-WebSocket-Version: 13
Authorization: Bearer <member_token>
X-Client-Version: 1.0.0
X-Machine-Name: zhangsan-mbp
```
- **Status 101**: Connection upgraded successfully.
- **Status 401**: Missing or invalid member token.
- **Status 403**: Member account disabled.

**Transport & Session Guarantees**:
- **Frame Read Limit**: Both client and Hub configure WebSocket read limits to 2 MB (`conn.SetReadLimit(2 * 1024 * 1024)`) to avoid frame limit disconnections on large summaries or multi-file inspections.
- **Session Takeover**: When a member reconnects, Hub issues a fresh UUID `session_id` in `hub_ack` and gracefully closes any previous session for that member ID with code `StatusPolicyViolation`.
- **Heartbeat Timeouts**: Daemons ping every 20s. The Hub prunes dead sockets if no frame arrives within 50s (2.5x). Daemons abort the connection if pong response exceeds 10s.
- **Anti-Hijacking**: Hub verifies that `query_response.query_id` belongs to a query targeting that authenticated session's member ID; spoofed responses are rejected.

Upon upgrade, the client daemon **MUST** immediately send `daemon_hello`.

---

### 2.2 Client -> Hub Message Types

#### 2.2.1 `daemon_hello`
Announces daemon readiness, machine details, and configured workspace roots.

```json
{
  "version": "v1",
  "type": "daemon_hello",
  "id": "msg_01J8HELLO001",
  "timestamp": 1726828800100,
  "payload": {
    "session_id": "sess_01J8HELLO001",
    "member_id": "mem_01J8ABC123",
    "client_version": "1.0.0",
    "machine_name": "zhangsan-mbp",
    "os": "darwin",
    "arch": "arm64",
    "max_concurrency": 2,
    "workspaces": [
      {
        "id": "ws_backend",
        "name": "talkintent-backend",
        "root_path": "/Users/zhangsan/projects/talkintent",
        "has_privacy_prompt": true
      }
    ]
  }
}
```

| Field | Type | Description |
|---|---|---|
| `member_id` | string | Authenticated member ID. |
| `client_version` | string | Semantic version of the running daemon. |
| `machine_name` | string | Hostname or machine label. |
| `os` / `arch` | string | Operating system and CPU architecture. |
| `max_concurrency`| int | Maximum parallel probe runs allowed on this node (default: 2). |
| `workspaces` | array | List of active workspace definitions known to daemon. |

#### 2.2.2 `heartbeat_ping`
Sent periodically by the daemon (default interval: 20s) to keep connection alive.
```json
{
  "version": "v1",
  "type": "heartbeat_ping",
  "id": "msg_01J8PING001",
  "timestamp": 1726828820000,
  "payload": {
    "active_probe_count": 0
  }
}
```

#### 2.2.3 `query_response`
Transmits the completed result of a probe execution back to the Hub.
```json
{
  "version": "v1",
  "type": "query_response",
  "id": "msg_01J8RESP001",
  "timestamp": 1726828824500,
  "payload": {
    "query_id": "qry_01J8QRY999",
    "status": "completed",
    "answer": "张三目前正在 feature/auth-v2 分支重构 JWT 验证中间件，最新提交完成了 RSA 密钥解析。本地修改了 3 个文件（未提交），新增了 RefreshToken 结构体。",
    "tools_used": ["git_status", "git_diff", "read_file"],
    "duration_ms": 4210,
    "token_usage": {
      "prompt_tokens": 1280,
      "completion_tokens": 145,
      "total_tokens": 1425
    },
    "error_message": ""
  }
}
```

| Field | Type | Description |
|---|---|---|
| `query_id` | string | ID of the originating query. |
| `status` | string | Execution outcome: `"completed"`, `"refused"`, `"error"`, `"timeout"`. |
| `answer` | string | Final synthesized natural-language response. Empty on fatal error. |
| `tools_used` | array[string] | List of tool names invoked. Tool arguments and outputs are NOT included. |
| `duration_ms` | int64 | Total execution time in milliseconds. |
| `token_usage` | object | LLM token usage breakdown. |
| `error_message`| string | Present when status is `"error"` or `"timeout"`. |

---

### 2.3 Hub -> Client Message Types

#### 2.3.1 `hub_ack`
Sent by Hub in response to `daemon_hello`.
```json
{
  "version": "v1",
  "type": "hub_ack",
  "id": "msg_01J8ACK001",
  "timestamp": 1726828800150,
  "payload": {
    "session_id": "sess_01J8ACKUUID",
    "authenticated": true,
    "heartbeat_interval_sec": 20,
    "pending_queries_count": 1
  }
}
```

#### 2.3.2 `heartbeat_pong`
Sent by Hub immediately upon receiving `heartbeat_ping`.
```json
{
  "version": "v1",
  "type": "heartbeat_pong",
  "id": "msg_01J8PONG001",
  "timestamp": 1726828820010,
  "payload": {
    "server_time": 1726828820010
  }
}
```

#### 2.3.3 `query_request`
Dispatched by Hub to target daemon to trigger an on-site probe execution.
```json
{
  "version": "v1",
  "type": "query_request",
  "id": "msg_01J8REQ001",
  "timestamp": 1726828805000,
  "payload": {
    "query_id": "qry_01J8QRY999",
    "asker_id": "mem_01J8LISI",
    "asker_name": "李四",
    "asker_type": "member",
    "query": "问一下张三现在登录模块重构得怎么样了，有没有新的结构体定义？",
    "target_workspace": "",
    "timeout_seconds": 60,
    "created_at": 1726828805000
  }
}
```

| Field | Type | Description |
|---|---|---|
| `query_id` | string | Unique query identifier. |
| `asker_id` | string | Member ID of the requester. |
| `asker_name` | string | Human-readable name of the requester. |
| `asker_type` | string | Originator type: `"member"`, `"feishu"`, `"anonymous"`. |
| `query` | string | Raw query text. |
| `target_workspace`| string | Optional workspace identifier. If empty, probe inspects all registered workspaces. |
| `timeout_seconds` | int | Maximum execution time allowed before daemon aborts (default: 60). |
| `created_at` | int64 | Epoch millisecond timestamp of query creation. |

#### 2.3.4 `query_cancel`
Sent by Hub if the asker cancels the query or HTTP long-poll client disconnects before execution begins.
```json
{
  "version": "v1",
  "type": "query_cancel",
  "id": "msg_01J8CAN001",
  "timestamp": 1726828810000,
  "payload": {
    "query_id": "qry_01J8QRY999",
    "reason": "asker_cancelled"
  }
}
```

---

## 3. REST API Specification

### 3.1 Authentication & Invite Management

#### 3.1.1 Generate Invite Code (Admin Only)
Creates a single-use invite code for a new member.
- **Route**: `POST /api/v1/admin/invites`
- **Headers**: `Authorization: Bearer <admin_token>`
- **Request Body**:
```json
{
  "target_name": "王五",
  "aliases": ["wangwu", "小王"],
  "expires_in_hours": 48
}
```
- **Response 201 Created**:
```json
{
  "code": "INV-7K9M-2X4Q",
  "expires_at": 1727001600000,
  "target_name": "王五"
}
```

#### 3.1.2 Pair Client with Invite Code
Exchanges a one-time invite code for a permanent member token.
- **Route**: `POST /api/v1/auth/pair`
- **Request Body**:
```json
{
  "invite_code": "INV-7K9M-2X4Q",
  "machine_name": "wangwu-laptop",
  "client_version": "1.0.0"
}
```
- **Response 200 OK**:
```json
{
  "member_id": "mem_01J8WANGWU",
  "member_name": "王五",
  "token": "ti_mem_9f8a7b6c5d4e3f2a1b0c9d8e7f6a5b4c",
  "hub_url": "http://127.0.0.1:8080"
}
```

---

### 3.2 Member Directory & Profile

#### 3.2.1 List Members
Retrieves all registered team members, their aliases, and real-time online status.
- **Route**: `GET /api/v1/members`
- **Headers**: `Authorization: Bearer <member_token>`
- **Response 200 OK**:
```json
{
  "members": [
    {
      "id": "mem_01J8ZHANGSAN",
      "name": "张三",
      "aliases": ["zhangsan", "三哥", "zs"],
      "online": true,
      "last_seen_at": 1726828820000,
      "machine_name": "zhangsan-mbp",
      "workspaces": ["talkintent-backend", "talkintent-web"],
      "has_feishu_bot": true
    },
    {
      "id": "mem_01J8LISI",
      "name": "李四",
      "aliases": ["lisi", "四弟"],
      "online": false,
      "last_seen_at": 1726815000000,
      "machine_name": "lisi-desktop",
      "workspaces": ["talkintent-docs"],
      "has_feishu_bot": false
    }
  ]
}
```

#### 3.2.2 Get Member Detail
- **Route**: `GET /api/v1/members/{id}`
- **Headers**: `Authorization: Bearer <member_token>`
- **Response 200 OK**: Single `MemberInfo` object.

#### 3.2.3 Update Current Member Profile
Allows member to modify their own name and aliases.
- **Route**: `PUT /api/v1/members/me`
- **Headers**: `Authorization: Bearer <member_token>`
- **Request Body**:
```json
{
  "name": "张三",
  "aliases": ["zhangsan", "三哥", "老张"]
}
```
- **Response 200 OK**: Updated `MemberInfo` object.

---

### 3.3 Query Lifecycle & Long-Polling

#### 3.3.1 Submit a Query
Initiates a query targeting a colleague's workspace.
- **Route**: `POST /api/v1/queries`
- **Headers**: `Authorization: Bearer <member_token>`
- **Request Body**:
```json
{
  "target": "张三",
  "query": "问一下张三现在登录模块重构得怎么样了",
  "target_workspace": "",
  "timeout_seconds": 60,
  "ttl_seconds": 86400,
  "idempotency_key": "cli_submit_01J8ABC",
  "wait": true
}
```

##### Success Response (Target Online & Dispatched) — 202 Accepted:
```json
{
  "query_id": "qry_01J8QRY999",
  "status": "dispatched",
  "target_member_id": "mem_01J8ZHANGSAN",
  "target_member_name": "张三",
  "created_at": 1726828805000
}
```

##### Success Response (Target Offline & Queued) — 202 Accepted:
```json
{
  "query_id": "qry_01J8QRY999",
  "status": "queued",
  "target_member_id": "mem_01J8LISI",
  "target_member_name": "李四",
  "queue_position": 1,
  "ttl_expires_at": 1726915205000,
  "created_at": 1726828805000
}
```

##### Ambiguity Error (Multiple Matches) — 400 Bad Request:
```json
{
  "error": {
    "code": "AMBIGUOUS_TARGET",
    "message": "Target '小张' matches multiple members. Please specify exact name.",
    "details": {
      "candidates": [
        {"id": "mem_01J8ZHANGSAN", "name": "张三", "matched_alias": "小张"},
        {"id": "mem_01J8ZHANGWEI", "name": "张伟", "matched_alias": "小张"}
      ]
    }
  }
}
```

#### 3.3.2 Get Query Detail & Long-Polling
Retrieves query status and answer.
- **Route**: `GET /api/v1/queries/{id}?wait={duration}`
- **Query Parameter `wait`**: Optional duration string (e.g. `30s`, `10s`, max `60s`).
  - If query is already in a terminal state (`completed`, `refused`, `error`, `timeout`, `expired`), returns immediately.
  - If query is still in `dispatched` or `queued`, the server holds the HTTP connection until the query completes or `wait` expires.

##### Response 200 OK (Completed):
```json
{
  "query_id": "qry_01J8QRY999",
  "status": "completed",
  "asker_id": "mem_01J8LISI",
  "asker_name": "李四",
  "target_member_id": "mem_01J8ZHANGSAN",
  "target_member_name": "张三",
  "query": "问一下张三现在登录模块重构得怎么样了",
  "answer": "张三目前正在 feature/auth-v2 分支重构 JWT 验证中间件，最新提交完成了 RSA 密钥解析。本地修改了 3 个文件（未提交），新增了 RefreshToken 结构体。",
  "tools_used": ["git_status", "git_diff", "read_file"],
  "duration_ms": 4210,
  "created_at": 1726828805000,
  "completed_at": 1726828809210
}
```

##### Response 200 OK (Still Pending after wait):
```json
{
  "query_id": "qry_01J8QRY999",
  "status": "queued",
  "asker_id": "mem_01J8LISI",
  "asker_name": "李四",
  "target_member_id": "mem_01J8ZHANGSAN",
  "target_member_name": "张三",
  "query": "问一下张三现在登录模块重构得怎么样了",
  "queue_position": 1,
  "created_at": 1726828805000
}
```

---

### 3.4 Audit Trails

#### 3.4.1 Inbound Audit Log
Shows who queried the authenticated member's dev workspace, when, what was asked, and what was answered.
- **Route**: `GET /api/v1/audit/inbound?limit=50&offset=0`
- **Headers**: `Authorization: Bearer <member_token>`
- **Response 200 OK**:
```json
{
  "total": 1,
  "entries": [
    {
      "query_id": "qry_01J8QRY999",
      "timestamp": 1726828805000,
      "asker_name": "李四",
      "asker_type": "member",
      "query": "问一下张三现在登录模块重构得怎么样了",
      "status": "completed",
      "answer": "张三目前正在 feature/auth-v2 分支...",
      "tools_used": ["git_status", "git_diff"],
      "duration_ms": 4210
    }
  ]
}
```

#### 3.4.2 Outbound Audit Log
Shows all queries initiated by the authenticated member.
- **Route**: `GET /api/v1/audit/outbound?limit=50&offset=0`
- **Headers**: `Authorization: Bearer <member_token>`
- **Response 200 OK**: List of outbound query records.

---

### 3.5 Feishu Bot Binding & Webhook

#### 3.5.1 Get Current Feishu Binding
- **Route**: `GET /api/v1/feishu/binding`
- **Headers**: `Authorization: Bearer <member_token>`
- **Response 200 OK**:
```json
{
  "bound": true,
  "app_id": "cli_aa17a38637f8dbb7",
  "webhook_url": "https://hub.empeirion.cn/api/v1/feishu/webhook/mem_01J8ZHANGSAN"
}
```

#### 3.5.2 Set Feishu Binding Credentials
- **Route**: `POST /api/v1/feishu/binding`
- **Headers**: `Authorization: Bearer <member_token>`
- **Request Body**:
```json
{
  "app_id": "cli_aa17a38637f8dbb7",
  "app_secret": "sec_xxxxxxxxxxxxxxxxxxxxxx",
  "verification_token": "ver_yyyyyyyyyyyyyyyyyy",
  "encrypt_key": "enc_zzzzzzzzzzzzzzzzzz"
}
```
- **Response 200 OK**:
```json
{
  "success": true,
  "webhook_url": "https://hub.empeirion.cn/api/v1/feishu/webhook/mem_01J8ZHANGSAN"
}
```

#### 3.5.3 Delete Feishu Binding
- **Route**: `DELETE /api/v1/feishu/binding`
- **Headers**: `Authorization: Bearer <member_token>`
- **Response 204 No Content**

---

## 4. Feishu Webhook Contract

### 4.1 URL Verification Challenge
When configuring the Feishu event subscription URL, Feishu sends a challenge POST:
```json
{
  "challenge": "ajls384kjsdf85423",
  "token": "ver_yyyyyyyyyyyyyyyyyy",
  "type": "url_verification"
}
```
If encrypted, Hub decrypts using AES-CBC-256 with SHA-256 of `encrypt_key`.  
Hub returns:
```json
{
  "challenge": "ajls384kjsdf85423"
}
```

### 4.2 Signature Verification
Hub verifies the `X-Lark-Signature` header:
```
signature = SHA256(timestamp + nonce + encrypt_key + raw_body)
```

### 4.3 Event Dispatch: `im.message.receive_v1`
When a user sends a message to Zhang San's personal bot:
1. Hub receives the event payload:
```json
{
  "schema": "2.0",
  "header": {
    "event_id": "evt_01J8EVT001",
    "event_type": "im.message.receive_v1",
    "create_time": "1726828800000"
  },
  "event": {
    "sender": {
      "sender_id": {
        "open_id": "ou_62c7d721..."
      },
      "sender_type": "user"
    },
    "message": {
      "message_id": "om_01J8MSG001",
      "chat_id": "oc_65f32e69...",
      "chat_type": "p2p",
      "message_type": "text",
      "content": "{\"text\":\"现在登录模块进展如何？\"}"
    }
  }
}
```
2. Hub parses message text, creates query targeting member `mem_01J8ZHANGSAN` with `asker_name: "Feishu user (ou_62c7...)"`, `asker_type: "feishu"`.
3. Hub responds with HTTP 200 `{}` immediately to acknowledge the event.
4. When the daemon returns the answer, Hub retrieves a `tenant_access_token` and calls Feishu IM reply API:
```http
POST /open-apis/im/v1/messages/om_01J8MSG001/reply HTTP/1.1
Host: open.feishu.cn
Authorization: Bearer t-xxxxxxxxxxxx
Content-Type: application/json; charset=utf-8

{
  "content": "{\"text\":\"[TalkIntent 自动回答]\\n张三目前正在 feature/auth-v2 分支...\"}",
  "msg_type": "text"
}
```
5. If the mock Feishu server is used in tests (`FEISHU_API_BASE`), Hub directs calls to the mock server instead of `open.feishu.cn`.
