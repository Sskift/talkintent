# TalkIntent Wire Protocol Specification

Version: 1.1.0  
Status: Protocol Specification (Aligned with Implementation)  
Base Path: `/api/v1`  
WebSocket Path: `/ws/daemon`  

---

## 1. Protocol Architecture & Common Conventions

### 1.1 Transport Channels
TalkIntent defines two primary transport channels:
1. **Outbound WebSocket (`/ws/daemon`)**: Established by background client daemons (`talkintent daemon`) to the central Hub. Used for persistent bidirectional signaling, heartbeats, task dispatch, cancellation, and answer delivery. Survives NAT and firewalls.
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
| `version` | string | Protocol version. Fixed to `"v1"`. Both Hub and Daemon reject major version mismatches. |
| `type` | string | Message type discriminator. |
| `id` | string | Unique message identifier (UUIDv4 or ULID). Used for tracing and request-response matching. |
| `timestamp` | int64 | Epoch millisecond timestamp of message generation. |
| `payload` | object | Type-specific payload body. |

#### Compatibility Rules
- **Forward Compatibility**: Receivers must ignore unknown JSON properties in both envelope and payload.
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
| `FORBIDDEN` | 403 | Token lacks required permission (e.g. non-admin calling admin endpoint or unauthorized query detail lookup). |
| `INVALID_ARGUMENT` | 400 | Malformed JSON body or invalid parameter values. |
| `AMBIGUOUS_TARGET` | 400 | Target name matches multiple members. Candidates returned in details (deduplicated by member ID). |
| `NOT_FOUND` | 404 | Target member or query ID not found. |
| `CONFLICT` | 409 | Resource already exists (e.g. duplicate member name). |
| `RATE_LIMITED` | 429 | Query submission rate limit exceeded (60 QPM, 10 burst). |
| `INTERNAL_ERROR` | 500 | Unexpected server error. |

---

## 2. WebSocket Protocol (`/ws/daemon`)

### 2.1 Handshake & Authentication
The client daemon initiates an HTTP Upgrade request:
```http
GET /ws/daemon HTTP/1.1
Host: hub.talkintent.internal:8080
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
- **Frame Read Limit**: Both Hub and client daemon configure `conn.SetReadLimit(2 * 1024 * 1024)` (2 MB) immediately upon upgrade/dial to avoid frame limit aborts on large diff summaries.
- **Multi-Device Sessions & Session Takeover**: The Hub tracks sessions in `memberSessions[memberID][sessionID]`. When a member reconnects from the same machine, the Hub performs session takeover: the old connection is terminated with WebSocket close code `StatusPolicyViolation` (1008), and the new session takes over routing.
- **Heartbeat & Read Deadlines**: Daemons send `heartbeat_ping` every 20 seconds. The Hub enforces a read deadline of 50 seconds (2.5x heartbeat interval). If no frame arrives within 50s, the Hub terminates the half-open socket. Daemons expect `heartbeat_pong` within 10 seconds; timeout triggers exponential backoff reconnection.
- **Anti-Hijacking Check**: When receiving `query_response`, the Hub verifies that `existingQuery.TargetMemberID == sess.memberID`. Spoofed responses from other members are rejected and logged.

Upon upgrade, the client daemon **MUST** immediately send `daemon_hello`.

---

### 2.2 Client -> Hub Message Types

#### 2.2.1 `daemon_hello`
Announces daemon readiness, machine details, concurrency limits, and configured workspaces.

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
| `session_id` | string | Optional client-proposed session identifier. |
| `member_id` | string | Authenticated member ID. |
| `client_version` | string | Semantic version of the running daemon. |
| `machine_name` | string | Hostname or machine label. |
| `os` / `arch` | string | Operating system and CPU architecture. |
| `max_concurrency`| int | Maximum parallel probe runs allowed on this node (default: 2). |
| `workspaces` | array | List of active workspace definitions known to daemon. |

#### 2.2.2 `heartbeat_ping`
Sent periodically by the daemon (every 20s) to maintain the WebSocket connection and report probe load.
```json
{
  "version": "v1",
  "type": "heartbeat_ping",
  "id": "msg_01J8PING001",
  "timestamp": 1726828820000,
  "payload": {
    "active_probe_count": 1
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
| `status` | string | Execution outcome: `"completed"` (or `"success"` which Hub normalizes to `"completed"`), `"refused"`, `"error"`, `"timeout"`. |
| `answer` | string | Final synthesized response text (capped at 16 KB; truncated with ` [truncated by TalkIntent daemon]`). Empty on fatal error. |
| `tools_used` | array[string] | Sorted list of tool names invoked. Tool arguments and outputs are NOT included. |
| `duration_ms` | int64 | Total execution time in milliseconds. |
| `token_usage` | object | Total prompt and completion token counts. |
| `error_message`| string | Present when status is `"error"` or `"timeout"`. Sanitized by redactor. |

**Privacy Refusal Semantics & Control Tool (`refuse`)**:
When a query touches areas restricted by a member's sovereign natural-language privacy rules (`privacy-prompt.md`), the probe agent signals this via a structured control tool `refuse(reason string)` rather than text sniffing. The probe exposes `refuse` in its tool definitions across both OpenAI and Anthropic dialects. When the LLM invokes `refuse`, reasoning halts immediately, returning `status: "refused"` with the sanitized, redacted reason in `answer`. The `tools_used` array records only tools that actually ran in the workspace prior to refusal. For models unable to call tools, a plain-text fallback marked with a leading `REFUSED:` prefix is also honored.

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

#### 2.3.4 `query_cancel`
Dispatched by Hub if the asker cancels the query or HTTP long-poll client disconnects before completion.
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
*Client daemon cancels the running probe's `context.CancelFunc` from its `activeQueries` map.*

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
  "machine_name": "wangwu-workstation"
}
```
- **Response 200 OK**:
```json
{
  "member_id": "mem_01J8WANGWU",
  "member_name": "王五",
  "token": "tok_sec_01J8WANGWU_987654321",
  "hub_url": "http://hub.talkintent.internal:8080"
}
```

---

### 3.2 Member Directory

#### 3.2.1 List Members
Retrieves the registered member directory and live online/offline presence status.
- **Route**: `GET /api/v1/members`
- **Headers**: `Authorization: Bearer <member_token>`
- **Response 200 OK**:
```json
{
  "members": [
    {
      "id": "mem_01J8ZHANGSAN",
      "name": "张三",
      "aliases": ["zhangsan", "三哥"],
      "online": true,
      "last_seen_at": 1726828820000
    },
    {
      "id": "mem_01J8LISI",
      "name": "李四",
      "aliases": ["lisi"],
      "online": false,
      "last_seen_at": 1726825200000
    }
  ]
}
```

---

### 3.3 Query Execution & Polling

#### 3.3.1 Submit Query
Submits a query targeting a teammate's dev workspace.
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
  "wait": true,
  "idempotency_key": "idemp_12345"
}
```

##### Success Response (Target Online & Completed via Wait) — 200 OK:
```json
{
  "query_id": "qry_01J8QRY999",
  "status": "completed",
  "asker_id": "mem_01J8LISI",
  "asker_name": "李四",
  "target_member_id": "mem_01J8ZHANGSAN",
  "target_member_name": "张三",
  "query": "问一下张三现在登录模块重构得怎么样了",
  "answer": "张三目前正在 feature/auth-v2 分支重构 JWT 验证中间件...",
  "tools_used": ["git_status", "git_diff", "read_file"],
  "duration_ms": 4210,
  "created_at": 1726828805000,
  "completed_at": 1726828809210
}
```

##### Success Response (Target Offline & Queued) — 202 Accepted:
```json
{
  "query_id": "qry_01J8QRY999",
  "status": "queued",
  "target_member_id": "mem_01J8ZHANGSAN",
  "target_member_name": "张三",
  "queue_position": 1,
  "ttl_expires_at": 1726915205000,
  "created_at": 1726828805000
}
```

##### Ambiguity Error (Multiple Matches) — 400 Bad Request / 300 Multiple Choices:
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
- **Headers**: `Authorization: Bearer <member_token>` *(Enforces F9: Caller must be Asker, Target, or Admin)*
- **Query Parameter `wait`**: Optional duration string (e.g. `30s`, `15s`).

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
  "answer": "张三目前正在 feature/auth-v2 分支重构 JWT 验证中间件...",
  "tools_used": ["git_status", "git_diff", "read_file"],
  "duration_ms": 4210,
  "ttl_expires_at": 1726915205000,
  "target_workspace": "",
  "origin": "cli",
  "created_at": 1726828805000,
  "completed_at": 1726828809210
}
```

---

### 3.4 Audit Trails

#### 3.4.1 Inbound Audit Log
Shows who queried the authenticated member's dev workspace, what was asked, and what was answered.
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
Shows queries initiated by the authenticated member.
- **Route**: `GET /api/v1/audit/outbound?limit=50&offset=0`
- **Headers**: `Authorization: Bearer <member_token>`
- **Response 200 OK**: List of outbound query records.

---

### 3.5 Feishu Bot Binding & Long Connection

#### 3.5.1 Get Current Feishu Binding
- **Route**: `GET /api/v1/feishu/binding`
- **Headers**: `Authorization: Bearer <member_token>`
- **Response 200 OK**:
```json
{
  "bound": true,
  "app_id": "cli_aa17a38637f8dbb7",
  "status": "connected",
  "connected_at": "2026-09-20T13:05:42+08:00",
  "reconnects": 0
}
```
- `status`: `connecting` | `connected` | `disconnected` | `error` | `stopped`. `disconnected` is also reported when the Hub holds a binding but no client is running for it.
- `error` (omitted when empty): the most recent connection error, e.g. `feishu client error (code 10003): invalid app_secret` or `reconnect limit reached`. `app_secret` is redacted before the message is stored, so it can never appear here.
- `connected_at` (RFC3339, omitted when not connected): when the current WebSocket session was established.
- `reconnects`: cumulative count of successful re-connections since the client started (`0` for the first connection).

`app_secret` is never returned by any route.

#### 3.5.2 Set Feishu Binding Credentials
- **Route**: `POST /api/v1/feishu/binding`
- **Headers**: `Authorization: Bearer <member_token>`
- **Request Body**:
```json
{
  "app_id": "cli_aa17a38637f8dbb7",
  "app_secret": "sec_xxxxxxxxxxxxxxxxxxxxxx",
  "base_url": "https://open.feishu.cn"
}
```
*`app_secret` is stored encrypted at rest using AES-GCM-256. `base_url` is optional and defaults to `https://open.feishu.cn`.*
- **Response 200 OK**:
```json
{
  "bound": true,
  "app_id": "cli_aa17a38637f8dbb7",
  "status": "connecting"
}
```

#### 3.5.3 Delete Feishu Binding
- **Route**: `DELETE /api/v1/feishu/binding`
- **Headers**: `Authorization: Bearer <member_token>`
- **Response 204 No Content**

---

## 4. Feishu Long Connection WebSocket Specification

TalkIntent interacts with Feishu Open Platform exclusively through native Long Connection (WebSocket) mode using the PBBP2 (Protocol Buffers 2) framing protocol. No public IP, domain, reverse proxy, or webhook endpoint is required.

### 4.1 Endpoint Discovery
Before dialing the WebSocket stream, the client obtains a dynamic endpoint and runtime connection parameters:
- **Route**: `POST /callback/ws/endpoint`
- **Headers**:
  - `Content-Type: application/json`
  - `locale: zh`
- **Request Body** (credentials travel only in the body, mirroring the official SDK):
```json
{
  "AppID": "cli_aa17a38637f8dbb7",
  "AppSecret": "sec_xxxxxxxxxxxxxxxxxxxxxx"
}
```
- **Response 200 OK**:
```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "url": "wss://<feishu-gateway-host>/callback/ws/connect?service_id=...",
    "ClientConfig": {
      "ReconnectCount": -1,
      "ReconnectInterval": 120,
      "ReconnectNonce": 30,
      "PingInterval": 120
    }
  }
}
```
*Error Handling*:
- HTTP non-200, network errors and timeouts: retryable (`ServerError`), scheduled for reconnection with backoff.
- Body `code == 1` (system busy) or `code == 1000040343`: retryable (`ServerError`).
- Any other non-zero `code` (e.g. invalid `app_id`/`app_secret`): fatal (`ClientError`) — the client stops and the binding status becomes `error` with the Feishu message attached.
- `code == 0` with an empty `url`: treated as retryable.

### 4.2 Wire Framing (PBBP2)
All frames over the WebSocket stream are encoded in Protocol Buffers 2 wire format (field tags with standard LEB128 varint and length-delimited byte slices):

| Field Tag | Field Name | Wire Type | Description |
|---|---|---|---|
| 1 | `seq_id` | Varint (uint64) | Monotonically increasing sequence identifier |
| 2 | `log_id` | Varint (uint64) | Tracing log ID |
| 3 | `service` | Varint (int32) | Feishu service identifier |
| 4 | `method` | Varint (int32) | `0` = Control (Ping/Pong), `1` = Data (Event/Response) |
| 5 | `headers` | Length-delimited | Repeated key-value string pairs (Tag 1 = key, Tag 2 = value) |
| 6 | `payload_encoding` | Length-delimited | Payload encoding (e.g. `gzip` or empty) |
| 7 | `payload_type` | Length-delimited | MIME type or format of payload |
| 8 | `payload` | Length-delimited | Raw bytes of message or event payload |
| 9 | `log_id_new` | Length-delimited | String representation of log ID for distributed tracing |

### 4.3 Heartbeat (Ping/Pong)
- **Client Ping**: Every `PingInterval` seconds (configured via `ClientConfig`, default 120s), the client writes a Frame with:
  - `Method = 0`
  - Headers: `type: ping`
- **Server Pong**: Server replies with a Frame with `Method = 0` and headers `type: pong`. The pong frame payload contains an updated `ClientConfig` JSON, which the client parses to update heartbeat and reconnection parameters.

### 4.4 Event Ingestion & Fragment Reassembly
Feishu pushes events as data frames:
- `Method = 1`
- Header `type = event` (any data frame with non-event type is dropped)
- Frames include partitioning headers: `message_id`, `sum`, and `seq`.

#### Fragment Reassembly Rules:
1. If `sum <= 1`, the payload is complete and processed immediately.
2. If `sum > 1024` or `seq < 0` or `seq >= sum`, the frame is dropped to prevent memory exhaustion or out-of-bounds panics.
3. Multi-part fragments are reassembled in an in-memory buffer indexed by `message_id` with a sliding 5-second TTL.
4. If a fragment arrives with a different `sum` than previously recorded for that `message_id`, or `seq >= len(parts)`, the fragment is discarded.
5. Once all `sum` parts are received, they are concatenated and dispatched to the event handler.

### 4.5 Response Acknowledgment & Header Echo
For every complete `type = event` message received, the client replies with an acknowledgment frame (the same shape the official SDK produces):
- `Method = 1`
- Preserves `SeqID`, `LogID`, `LogIDNew`, `Service`, `PayloadEncoding`, and `PayloadType` from the incoming request frame.
- Echoes **all** request headers (`type`, `message_id`, `sum`, `seq`, `trace_id`, ...) and appends:
  - `biz_rt`: Processing duration in milliseconds as a string (e.g. `"12"`).
- Response payload (JSON):
```json
{"code":200,"headers":null,"data":null}
```
`code` is `200` when the event handler succeeded and `500` when it returned an error. For fragmented messages (`sum > 1`) the handler runs once and **exactly one** acknowledgment is sent — after the fragment that completes reassembly — echoing that fragment's `SeqID`, `LogID` and headers. Fragments that do not complete the message are buffered without a reply, mirroring the official SDK.

### 4.6 Reconnection & Resilience
1. **Backoff**: the first retry after a failure waits a random `[0, ReconnectNonce)` seconds (jitter, so a fleet of clients does not reconnect in lock-step); every subsequent retry waits `ReconnectInterval` seconds. A successful connection resets the attempt counter. Minimum wait is 100 ms.
2. **Bounds**: attempts are bounded by `ClientConfig.ReconnectCount` when it is `>= 0`; once reached the client enters the `error` state (`reconnect limit reached`) and stops. `ReconnectCount < 0` (Feishu's default) retries indefinitely.
3. **Context Lifecycle**: endpoint discovery and the WebSocket dial are bound to the client root context, so `Stop()` aborts an in-flight connect immediately instead of waiting for the 30 s / 20 s timeouts.
4. **Shared Sockets**: acknowledgment and ping writes use detached 5 s contexts and are serialised under a mutex; a write is skipped if the connection it targets is no longer the active one.
5. **Pong-driven config**: `ClientConfig` values delivered in pong payloads override the discovery-time values on the fly (`ReconnectCount` may be updated to any non-zero value; intervals only to positive values).

### 4.7 Asynchronous Query Dispatch & IM Reply
1. Upon reassembly and verification, `im.message.receive_v1` event payloads are passed to `Handler.ProcessEvent`.
2. Bot/app senders and non-text messages are ignored to prevent infinite message loops.
3. Messages are deduplicated using `Deduplicator` against both `event_id` and `message_id`.
4. The query is dispatched to the target member's workspace daemon. If the member is offline, an immediate acknowledgment reply is sent to Feishu informing the user that the query is queued.
5. When the query completes, the Hub fetches a `tenant_access_token` and calls the Feishu IM reply API:
```http
POST /open-apis/im/v1/messages/{message_id}/reply HTTP/1.1
Host: open.feishu.cn
Authorization: Bearer t-xxxxxxxxxxxx
Content-Type: application/json; charset=utf-8

{
  "content": "{\"text\":\"[TalkIntent 自动回答]\\n张三目前正在 feature/auth-v2 分支重构 JWT 校验器\"}",
  "msg_type": "text"
}
```

---

## 5. Known Gaps and Protocol Discrepancies

The following discrepancies between specification and code have been identified and are documented here as intentional or frozen behaviors:

1. **`protocol.AuditLogEntry` lacks `error_message`**:
   - In `internal/protocol/rest.go`, `AuditLogEntry` includes `status` but omits `error_message`. However, `store.AuditLogEntryDetailed` and the underlying `events.jsonl` store record the full error message. Callers requiring the exact error description can query `GET /api/v1/queries/{id}`.
2. **WebSocket Status Taxonomy Normalization**:
   - Daemons may transmit `status: "success"` or `status: "completed"` in `query_response`. The Hub's `handleDaemonEnvelope` automatically normalizes `success` or empty status to `protocol.QueryStatusCompleted` before saving to store.
3. **Idempotency Key Persistence**:
   - `idempotency_key` is cached in memory on the Hub with a 1-hour expiration window. It is not written to `events.jsonl`; an abrupt Hub restart resets the idempotency window.
4. **Candidate Member Deduplication**:
   - `ResolveTargetMember` in `internal/store/store.go` automatically deduplicates matching candidate entries by `ID` before evaluating ambiguous target conditions.
