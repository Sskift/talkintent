# TalkIntent Acceptance Test Specification & Checklists

Version: 1.0.0  
Target: TalkIntent v1.1.0  
Scope: Verification across 3 Deployment Topologies  

---

## 1. Overview & Verification Matrix

TalkIntent is verified across three standard deployment topologies:
1. **Topology 1: Local Docker Compose (Automated CI / Regression)**: Full multi-node mesh on a single machine with a Mock LLM server.
2. **Topology 2: Multi-User Linux Host (StarPub Environment)**: Single Linux VM with multiple Unix users simulating isolated developer workstations, connected to a real LLM endpoint with private CA.
3. **Topology 3: Cross-Machine (Remote Linux Hub + Windows/macOS Laptops)**: Production configuration with developer laptops behind NAT connecting outbound to a cloud Hub.

| Acceptance Criterion | Topology 1 (Compose) | Topology 2 (StarPub) | Topology 3 (Cross-Machine) |
|---|:---:|:---:|:---:|
| Hub Bootstrap & Admin Auth | Verified | Verified | Verified |
| Member Invite & Pairing Flow | Verified | Verified | Verified |
| Multi-Turn Tool Inspection Loop | Verified (Mock LLM) | Verified (Real LLM: AsterGate `gemini-3.8-flash-high`) | Verified (Mock LLM) |
| Natural-Language Target Resolution | NOT YET VERIFIED (topology acceptance suites use explicit `-to`; covered by unit tests in `internal/cli`) | NOT YET VERIFIED (topology acceptance suites use explicit `-to`; covered by unit tests in `internal/cli`) | NOT YET VERIFIED (topology acceptance suites use explicit `-to`; covered by unit tests in `internal/cli`) |
| Sovereign Privacy Guardrail Enforcement | Verified (Mock LLM refusal) | Verified (Real LLM prompt guardrail) | Verified (Mock LLM prompt guardrail) |
| Physical Sandbox & Hard Denylists | Verified (Unit/Integration tests) | Verified (Unit/Integration tests) | Verified (Unit/Integration tests) |
| Offline Queueing & Reconnection Recovery | Verified | Verified | Verified |
| Audit Trail Persistence (JSONL) | Verified (REST /audit/inbound & /outbound) | Verified (REST /audit/inbound & /outbound) | NOT YET VERIFIED (audit endpoints not tested in xmach suite; verified in Topologies 1 & 2) |
| Web UI Dashboard & Token Hash Auth | Verified (GET / -> HTTP 200 console title) | NOT YET VERIFIED (StarPub automated suite runs headless via CLI/curl) | NOT YET VERIFIED (Cross-Machine automated suite runs headless via CLI/curl) |
| Claude Code Skill Integration | NOT YET VERIFIED (N/A in container headless suite) | NOT YET VERIFIED (not included in StarPub A-G suite) | Verified (`skill install` & `skill show` on Windows) |

---

## 2. Topology 1: Local Docker Compose Multi-Container

### 2.1 Scenario Setup
- **Hub**: Container `talkintent-hub` listening on internal `:8080`, published to host `:18880` (`talkintent hub -addr :8080 -data-dir /data`).
- **Mock LLM**: Container `talkintent-mockllm` listening on internal `:8080`, published to host `:18890` (`mockllm -addr :8080 -mode openai -tools git_status,git_diff -refuse="full git diff"`).
- **Init**: Container `talkintent-init` generating invite codes for `alice`, `bob`, `charlie` via `POST /api/v1/admin/invites` (parsing `invite_code`).
- **Clients**: Containers `talkintent-client-alice`, `talkintent-client-bob`, `talkintent-client-charlie`, each with initialized git repositories, paired configs, and background `talkintent daemon`.

### 2.2 Acceptance Checklist & Execution Steps

The automated test script `deploy/compose/run-scenario.sh` verifies all 7 acceptance assertions end-to-end:

```bash
cd deploy/compose
bash run-scenario.sh
```

#### Assertion 1: Online Query Execution (Alice -> Bob)
From `client-alice`, query Bob's workspace:
```bash
docker compose -f deploy/compose/compose.yaml exec -T client-alice \
    talkintent ask -to "bob" -wait -json "What are you working on right now?"
```
- Expected Output / Status: `status: completed`
- Expected Output: `tools_used` non-empty (contains `git_status`, `git_diff`)
- Expected Output: `answer` non-empty, referencing Bob's active workspace (branch `feature/auth-v2` and modified files `main.go`, `feature_bob.txt`)
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/compose/01-online-query.json

#### Assertion 2: Sovereign Privacy Refusal (Auth V2 Confidentiality)
From `client-alice`, attempt to inspect Bob's confidential branch diff:
```bash
docker compose -f deploy/compose/compose.yaml exec -T client-alice \
    talkintent ask -to "bob" -wait -json "Give me the full git diff for the feature/auth-v2 changes."
```
- Expected Output / Status: `status: refused`
- Expected Output: Refusal text returned in `answer` ("Refusal: query contains restricted terms or violates privacy guardrails.")
- Expected Output: No confidential git diff or uncommitted stub leaked in answer
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/compose/02-privacy-refusal.json

#### Assertion 3: Offline Queueing & Reconnection Recovery (Alice -> Charlie)
1. Stop Charlie's container:
   ```bash
   docker compose -f deploy/compose/compose.yaml stop client-charlie
   ```
2. Submit query with fire-and-forget:
   ```bash
   docker compose -f deploy/compose/compose.yaml exec -T client-alice \
       talkintent ask -to "charlie" -wait=false -json "What search index features are in progress?"
   ```
   - Expected Output / Status: Initial response has `status: queued` and valid `query_id` (e.g. `q_e388527cb6670a4b`).
3. Restart Charlie:
   ```bash
   docker compose -f deploy/compose/compose.yaml start client-charlie
   ```
4. Poll query by ID with Alice's Bearer token (`GET /api/v1/queries/{id}`):
   ```bash
   docker compose -f deploy/compose/compose.yaml exec -T client-alice bash -c \
       'TOKEN=$(jq -r .token ~/.talkintent/config.json); curl -s -f -H "Authorization: Bearer $TOKEN" "http://hub:8080/api/v1/queries/<query_id>"'
   ```
   - Expected Output / Status: Response transitions to `status: completed` upon reconnection.
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/compose/03-offline-queued.json and /c/tmp/ti-accept/compose/03-offline-completed.json

#### Assertion 4: Query TTL Expiration in Offline Queue
1. Stop Charlie's container:
   ```bash
   docker compose -f deploy/compose/compose.yaml stop client-charlie
   ```
2. Submit short-TTL query:
   ```bash
   docker compose -f deploy/compose/compose.yaml exec -T client-alice \
       talkintent ask -to "charlie" -ttl 2 -wait=false -json "This query should expire via TTL."
   ```
   - Expected Output / Status: Initial response has `status: queued`.
3. Wait 22 seconds (allowing the Hub's 15-second background sweep ticker to trigger `SweepExpiredQueries`).
4. Poll query by ID:
   ```bash
   docker compose -f deploy/compose/compose.yaml exec -T client-alice bash -c \
       'TOKEN=$(jq -r .token ~/.talkintent/config.json); curl -s -f -H "Authorization: Bearer $TOKEN" "http://hub:8080/api/v1/queries/<query_id>"'
   ```
   - Expected Output / Status: Response transitions to `status: expired` with `error_message: "query expired in offline queue"`.
5. Restart Charlie:
   ```bash
   docker compose -f deploy/compose/compose.yaml start client-charlie
   ```
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/compose/04-ttl-expired.json

#### Assertion 5: Audit Trail Verification (Inbound & Outbound)
Using member Bearer tokens extracted from local `~/.talkintent/config.json`:
- Bob's Inbound Audit:
  ```bash
  docker compose -f deploy/compose/compose.yaml exec -T client-bob bash -c \
      'TOKEN=$(jq -r .token ~/.talkintent/config.json); curl -s -f -H "Authorization: Bearer $TOKEN" "http://hub:8080/api/v1/audit/inbound"'
  ```
  - Expected Output: `total >= 1` (verified: 2 entries).
- Alice's Outbound Audit:
  ```bash
  docker compose -f deploy/compose/compose.yaml exec -T client-alice bash -c \
      'TOKEN=$(jq -r .token ~/.talkintent/config.json); curl -s -f -H "Authorization: Bearer $TOKEN" "http://hub:8080/api/v1/audit/outbound"'
  ```
  - Expected Output: `total >= 1` (verified: 4 entries).
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/compose/05-audit-inbound.json and /c/tmp/ti-accept/compose/05-audit-outbound.json

#### Assertion 6: Unauthenticated Security Gate (/api/v1/members)
Unauthenticated request to Hub members endpoint:
```bash
curl -s -w "%{http_code}" -o /dev/null "http://127.0.0.1:18880/api/v1/members"
```
- Expected Output / Status: Returns HTTP 401 Unauthorized (`HTTP_STATUS=401`).
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/compose/06-unauth-members.txt

#### Assertion 7: Embedded Web UI Availability (/)
Request to Hub Web UI endpoint:
```bash
curl -s -L -w "%{http_code}" -o /dev/null "http://127.0.0.1:18880/"
```
- Expected Output / Status: Redirects to `/web/` and returns HTTP 200 OK (`HTTP_STATUS=200`).
- Expected Output: Response HTML contains `<title>TalkIntent 研发协同感知控制台</title>`.
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/compose/07-web-ui.txt

#### Teardown and Clean Exit (Compose)
```bash
docker compose -f deploy/compose/compose.yaml down -v --remove-orphans
```
- Expected Output: Teardown complete. All containers and anonymous volumes stopped and purged cleanly; zero compose containers remain.
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/compose/run-scenario.log

---

## 3. Topology 2: Multi-User Linux Host (StarPub Environment)

### 3.1 Scenario Setup
- **Environment**: Single Linux host/container (`starpub-docker`, Ubuntu 22.04 / Debian 12, 72 cores) without nested Docker capability.
- **User Isolation**: Independent unprivileged Linux users `ti-alice` (UID 1001), `ti-bob` (UID 1002), `ti-carol` (UID 1003). Each user possesses an isolated home directory (`0700`) with independent `~/.talkintent/config.json` (`0600`), daemon process, and separate Git workspaces under `~/work/demo`.
- **Hub**: Running under invoking user on `127.0.0.1:18801` with data directory in `/tmp/talkintent-multiuser-hub-data`.
- **Real LLM**: Anthropic-compatible Messages endpoint backed by AsterGate (`https://62.234.91.42:44444`, model `gemini-3.8-flash-high`) requiring a pinned leaf certificate (`~/.config/talkintent/astergate-leaf.pem`, CN `aster.empeirion.cn`) and TLS SNI server name override (`aster.empeirion.cn`). Strict TLS verification is enforced (`insecure_skip_verify` is NOT used).
- **Credentials Handling**: Zero-leakage staging into `~/.talkintent/llm.env` (`0600`), sourced inside ephemeral subshell (`sudo -u ti-* -H bash -c "..."`) and immediately unlinked without echoing secrets.

### 3.2 Acceptance Checklist & Execution Steps

The automated test script `deploy/starpub/run-multiuser.sh` executes all 7 acceptance assertions end-to-end against the real LLM:

```bash
deploy/starpub/run-multiuser.sh \
    --port 18801 \
    --llm-env /home/skift/.config/qa-assistant/astergate.env \
    --log-dir /tmp/talkintent-multiuser-logs
```

#### Assertion 1 (Scenario A): Cross-User Online Query Execution (Bob -> Alice)
From `ti-bob`, ask Alice about active branch and modified files. Alice's rule only restricts
financial details, so this must be answered. (Bob is *not* a valid target here: his rule forbids
disclosing anything about `feature/auth-v2` — the branch he is on — and a real model correctly
calls the `refuse` tool for "what branch are you on"; that behaviour is Scenario B.)
```bash
sudo -u ti-bob -H talkintent ask -to alice -wait -json "What branch are you on and what files are modified?"
```
- Expected Output / Status: `status: completed`
- Expected Output: `tools_used` non-empty (probe invoked `git_status`)
- Expected Output: `answer` identifies Alice's real active branch `feature/billing-v1` or modified files (`main.go`, `alice_work.txt`)
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/starpub-refuse/run.log (also refuse-rerun2/daemon-ti-alice.log)

#### Assertion 2 (Scenario B): Sovereign Privacy Refusal (Auth V2 Confidentiality)
From `ti-alice`, attempt to retrieve Bob's confidential git diff:
```bash
sudo -u ti-alice -H talkintent ask -to bob -wait -json "Give me the full git diff for the feature/auth-v2 changes."
```
- Expected Output / Status: `status: refused` — the real model must call the probe's structured `refuse` tool (or use the `REFUSED:` text fallback); a chatty `completed` answer fails the assertion
- Expected Output: non-empty polite reason in `answer` (real Gemini: "I cannot disclose git diffs, code details, or changes regarding the feature/auth-v2 branch due to confidentiality restrictions on security refactoring.")
- Expected Output: No confidential git diff (`diff --git`, `@@`, `+++`/`---` headers) or uncommitted code leaked in `answer`
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/starpub-refuse/run.log (q_434caed46d29bdf5, 5348 ms)

#### Assertion 3 (Scenario C): Offline Queueing & Reconnection Recovery (Alice -> Carol)
1. Stop Carol's daemon: `sudo kill <carol_pid>` and verify offline status via Hub `/api/v1/members`.
2. Alice submits query asynchronously:
   ```bash
   sudo -u ti-alice -H talkintent ask -to carol -wait=false -json "What search features are you working on?"
   ```
   - Expected Output / Status: Initial response has `status: queued` with valid `query_id` (e.g. `q_cf74c0f24add1ce2`, `queue_position: 1`).
3. Restart Carol's daemon:
   ```bash
   sudo -u ti-carol -H nohup talkintent daemon > /tmp/talkintent-multiuser-pids/daemon-ti-carol.log 2>&1 &
   ```
4. Poll query by ID with Alice's Bearer token (`GET /api/v1/queries/{id}`):
   ```bash
   curl -s -H "Authorization: Bearer ${ALICE_TOKEN}" "http://127.0.0.1:18801/api/v1/queries/<query_id>"
   ```
   - Expected Output / Status: Query transitions to `status: completed` upon Carol reconnecting.
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/starpub/run.log (also daemon-ti-carol.log)

#### Assertion 4 (Scenario D): Query TTL Expiration
1. Stop Carol's daemon and verify offline status via Hub `/api/v1/members`.
2. Alice submits short-TTL query:
   ```bash
   sudo -u ti-alice -H talkintent ask -to carol -ttl 5 -wait=false -json "Query that will expire quickly"
   ```
   - Expected Output / Status: Initial response has `status: queued` (e.g. `q_6644a19e8ecb9ece`).
3. Wait 22 seconds (allowing the Hub's 15-second background sweep ticker to trigger `SweepExpiredQueries`).
4. Poll query by ID:
   ```bash
   curl -s -H "Authorization: Bearer ${ALICE_TOKEN}" "http://127.0.0.1:18801/api/v1/queries/<query_id>"
   ```
   - Expected Output / Status: Query transitions to `status: expired`.
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/starpub/run.log (also hub.log)

#### Assertion 5 (Scenario E): Audit Trail Verification (Inbound & Outbound)
Using member Bearer tokens extracted from local `~/.talkintent/config.json`:
- Bob's Inbound Audit:
  ```bash
  curl -s "http://127.0.0.1:18801/api/v1/audit/inbound" -H "Authorization: Bearer ${BOB_TOKEN}"
  ```
  - Expected Output: `entries` count >= 1 (verified: 2 records).
- Alice's Outbound Audit:
  ```bash
  curl -s "http://127.0.0.1:18801/api/v1/audit/outbound" -H "Authorization: Bearer ${ALICE_TOKEN}"
  ```
  - Expected Output: `entries` count >= 1 (verified: 4 records).
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/starpub/run.log

#### Assertion 6 (Scenario F): Unauthenticated Security Gate
Request to Hub members endpoint without authorization:
```bash
curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:18801/api/v1/members"
```
- Expected Output / Status: Returns HTTP 401 Unauthorized (`HTTP 401`).
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/starpub/run.log

#### Assertion 7 (Scenario G): Members Directory Online Count
Restart Carol's daemon to restore complete team mesh, then check member directory:
```bash
sudo -u ti-alice -H talkintent members -json
```
- Expected Output / Status: All 3 members (`alice`, `bob`, `carol`) reported online (`online == true`).
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/starpub/run.log

#### Teardown & Process Cleanup
Invoked automatically via `stop.sh` on script EXIT trap:
```bash
deploy/starpub/stop.sh /tmp/talkintent-multiuser-pids
```
- Expected Output: Terminates all per-user daemons (`ti-alice`, `ti-bob`, `ti-carol`) and Hub with SIGTERM, falling back to SIGKILL; exit code 0.
- Asserts zero lingering processes for `skift` and `ti-*` users, and frees port 18801.
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/starpub/run.log

#### Raw Evidence Artifacts
All run evidence is captured and archived under `/c/tmp/ti-accept/starpub/`:
- `run.log`: Full end-to-end execution transcript with zero exit code.
- `hub.log`: Hub lifecycle, invite issuance, WebSocket sessions, sweep ticker events.
- `daemon-ti-alice.log`: Alice daemon logs.
- `daemon-ti-bob.log`: Bob daemon logs showing tool invocation and privacy guardrail enforcement.
- `daemon-ti-carol.log`: Carol daemon logs showing disconnect, reconnect, and queued query draining.

---

## 4. Topology 3: Cross-Machine Deployment (Linux Hub + Windows Client via SSH Tunnel)

### 4.1 Scenario Setup
- **Central Hub**: Linux Server (`starpub-docker`) running `talkintent hub` on `127.0.0.1:18820` (isolated under `~/work/talkintent-tmp/xmach/hub-data`).
- **Remote Member**: `remote-dev` on Linux Server (`starpub-docker`), paired with Hub and running local daemon + mock LLM (`127.0.0.1:18821`).
- **Windows Member**: `win-laptop` on Windows workstation running `bin/talkintent.exe daemon` + `bin/mockllm.exe` (`127.0.0.1:18822`).
- **Secure Mesh Tunnel**: Encrypted SSH local port forward (`ssh -N -L 18820:127.0.0.1:18820 starpub-docker`) mapping Windows client loopback directly to the remote Hub.
- **Reproducible Automation**: Scripts under `deploy/xmachine/`:
  - `deploy/xmachine/run-xmachine.sh`: Fully automated end-to-end acceptance runner verifying all 6 scenarios.
  - `deploy/xmachine/stop.sh`: Clean teardown script terminating all local and remote background processes and releasing ports.
  - `deploy/xmachine/README.md`: Architecture guide, port allocation rules (`18820-18822`), and manual verification instructions.
- **Raw Evidence Logs**: All logs saved to `/c/tmp/ti-accept/xmach/`.

### 4.2 Acceptance Criteria & Execution Results

#### Scenario 1: Cross-Machine Pairing & Mutual Discovery
- **Action**:
  Windows client pairs over the SSH tunnel to the remote Hub:
  ```bash
  talkintent.exe pair -hub http://127.0.0.1:18820 -code <WIN_INVITE_CODE> -name win-laptop
  ```
  Remote member pairs locally on the host:
  ```bash
  talkintent pair -hub http://127.0.0.1:18820 -code <REMOTE_INVITE_CODE> -name remote-dev
  ```
  Both start daemons and query member list:
  ```bash
  # From Windows:
  talkintent.exe members -json
  # From Remote:
  talkintent members -json
  ```
- Expected Output / Status:
  - Remote client sees `win-laptop` with `online: true`
  - Windows client sees `remote-dev` with `online: true`
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/xmach/05-invite-pair.log, /c/tmp/ti-accept/xmach/06-members-discovery.log

#### Scenario 2: Bidirectional Real-Workspace Queries with Live Tools
- **Action**:
  Remote queries Windows client:
  ```bash
  talkintent ask -to win-laptop -q "win-laptop 目前在哪个分支改了什么" -timeout 60 -json
  ```
  Windows queries Remote client:
  ```bash
  talkintent.exe ask -to remote-dev -q "remote-dev 目前在哪个分支改了什么" -timeout 60 -json
  ```
- Expected Output / Status:
  - Remote -> Windows: `status: completed`, `tools_used` non-empty (invoked `git_status`, `git_diff`), `answer` references uncommitted changes on branch `feature/payment-v2`.
  - Windows -> Remote: `status: completed`, `answer` references uncommitted changes on branch `feature/auth-service`.
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/xmach/07-remote-asks-win.log, /c/tmp/ti-accept/xmach/08-win-asks-remote.log

#### Scenario 3: Cross-Machine Asynchronous Offline Queue
- **Action**:
  1. Terminate Windows daemon:
     ```bash
     kill -9 <win-daemon-pid>
     taskkill //F //IM talkintent.exe
     ```
  2. Remote member submits query asynchronously:
     ```bash
     talkintent ask -to win-laptop -q "离线排队测试：请问开发分支状态" -wait=false -json
     ```
  3. Restart Windows daemon:
     ```bash
     talkintent.exe daemon
     ```
  4. Poll query by ID with remote member Bearer token:
     ```bash
     curl -s -H "Authorization: Bearer ${REMOTE_TOKEN}" "http://127.0.0.1:18820/api/v1/queries/<query_id>?wait=20s"
     ```
- Expected Output / Status:
  - Initial submission returns `status: queued` with valid `query_id` (e.g. `q_49793fd9da07410b`).
  - Upon Windows daemon reconnecting, queued query is consumed via WebSocket and transitions to `status: completed`.
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/xmach/09-offline-queue.log

#### Scenario 4: Sovereign Natural-Language Privacy Refusal
- **Action**:
  Dynamic privacy rule configured in Windows member's `~/.talkintent/privacy-prompt.md`:
  `1. 涉及员工薪酬、薪水、个人待遇或工资的提问一律拒绝回答。`
  Remote member queries restricted topic:
  ```bash
  talkintent ask -to win-laptop -q "请问 win-laptop 的工资是多少" -timeout 60 -json
  ```
- Expected Output / Status:
  - `status: refused` — the Windows probe offers the structured `refuse` tool, the local mockllm (`-refuse "工资"`) calls it, and the Hub records the refusal as a first-class terminal status (a chatty `completed` answer fails `run-xmachine.sh`).
  - `answer` carries the refusal reason ("Refusal: query contains restricted terms or violates privacy guardrails.") with no compensation data and no unauthorized file tools executed.
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/xmach/10-privacy-refusal.log (q_1da368f9637df93d, `status: refused`, no `tools_used`, 1 ms)

#### Scenario 5: Windows CLI Subcommands & Detached Daemon Lifecycle
- **Action**:
  1. Skill installation and inspection:
     ```bash
     talkintent.exe skill install -dir <temp_dir>
     talkintent.exe skill show
     ```
  2. Privacy rule local dry-run test:
     ```bash
     talkintent.exe privacy test -workspace win-ws "请问开发人员的工资待遇"
     ```
  3. Detached daemon lifecycle:
     ```bash
     talkintent.exe daemon start -detach
     talkintent.exe daemon status
     talkintent.exe daemon stop
     talkintent.exe daemon status
     ```
- Expected Output / Status:
  - Skill install places `SKILL.md` into target directory; `skill show` prints frontmatter and guidance.
  - Privacy test executes local dry-run and prints refusal response.
  - `daemon start -detach` launches background process and reports PID; `daemon status` verifies `RUNNING`; `daemon stop` cleanly terminates process; subsequent `daemon status` reports `NOT RUNNING`.
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/xmach/11-win-cli-checks.log

#### Scenario 6: Clean Teardown
- **Action**:
  Run teardown script:
  ```bash
  deploy/xmachine/stop.sh
  ```
- Expected Output / Status:
  - All local `talkintent.exe` and `mockllm.exe` processes terminated on Windows.
  - Background SSH tunnel process terminated; local port `18820` confirmed free.
  - All remote `talkintent` and `mockllm` processes terminated on `starpub-docker`; remote ports `18820` and `18821` freed.
- Last verified: 2026-09-20 — evidence: /c/tmp/ti-accept/xmach/12-cleanup.log

---

## 5. Verification Gate Pass Criteria

Before releasing or deploying TalkIntent, verify that:
1. `go build ./...` compiles cleanly with zero warnings.
2. `go vet ./...` reports zero issues.
3. `go test ./...` passes all unit, integration, and sandbox tests with 100% success rate across all packages:
   - `internal/config`: 0600 permission checks, serialization, private CA fields.
   - `internal/store`: append-only JSONL replay, AES-GCM-256 credential encryption, token salt hashing, candidate deduplication, compaction.
   - `internal/hub`: authentication, multi-device sessions, anti-hijacking, rate limiting, status normalization.
   - `internal/probe/tools`: physical symlink sandbox escapes, Windows drive volume normalization, 64 KB tool cap, hard denylists (`.git/config`, `*.pem`, `*.key`, `*aws/credentials*`, `*.ssh/*`).
   - `internal/probe`: system prompt baseline guardrails, indirect prompt injection defense, 16 KB answer truncation, regex redactor.
   - `internal/feishu`: AES decryption IV derivation (`keyHash[:16]`), constant-time signature verification with 300s freshness window.
   - `internal/cli`: natural language target extraction, candidate disambiguation table, daemon process management.
   - `web`: embedded asset integrity and SPA MIME types.
