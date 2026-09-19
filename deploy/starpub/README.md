# TalkIntent StarPub Multi-User Host Deployment

This deployment suite validates TalkIntent on a bare Linux host (such as a remote dev server or cloud instance) where Docker containerization is impossible (e.g. nested containers prohibited inside existing container environments like StarPub).

## Why Multi-User on Bare Host?

In environments like StarPub (which itself runs inside a Docker container), nested Docker-in-Docker or running additional container engines is restricted or unfeasible. TalkIntent solves multi-node testing on bare Linux by leveraging standard Unix user isolation:
- Separate developer workspaces and configs under `/home/ti-<user>` (e.g. `ti-alice`, `ti-bob`, `ti-carol`).
- Separate local daemons running under `sudo -u ti-<user>`.
- File system permissions (`0700` home directories, `0600` config files) enforce physical privacy and token isolation.

## Architecture

- **TalkIntent Hub**: Central Hub server run by the invoking user on `127.0.0.1:18801` (or custom `--port` in range 18800-18819).
- **Simulated Developers**:
  - `ti-alice` (UID 1001): Feature developer on `feature/billing-v1`.
  - `ti-bob` (UID 1002): Security refactoring developer on `feature/auth-v2` with strict privacy prompt blocking disclosures.
  - `ti-carol` (UID 1003): Search engine developer on `feature/search-engine` used to validate offline queuing and TTL expiry.
- **LLM Connectivity**: Real LLM (Anthropic-compatible AsterGate endpoint) secured by private CA leaf certificate (`astergate-leaf.pem`) and SNI hostname (`aster.empeirion.cn`) without disabling TLS verification (`insecure_skip_verify` is NOT used).

## Secure Credential & TLS Handling

To prevent credentials from leaking into environment dumps, process tables, command-line arguments, or shared log files:
1. **Isolated Subshell Sourcing**: The credentials file (`.env`) is staged into the target user's home directory as `~/.talkintent/llm.env` with strict permissions (`0600` owned by `ti-*`).
2. **Immediate Consumption & Deletion**: Inside `sudo -u ti-<user> -H bash -c "..."`, the environment file is sourced into the ephemeral subshell and immediately unlinked (`rm -f`) before configuring the daemon via `talkintent llm set`.
3. **Zero Secrets in Logs**: Subshell standard output does not echo API keys or secrets. Only key presence and lengths are displayed during diagnostics.
4. **Private CA Distribution**: The leaf CA certificate is staged into each target user's `~/.talkintent/astergate-leaf.pem` with mode `0644`. `talkintent llm set` is called with `-ca-file` and `-tls-server-name` to ensure strict TLS verification against self-hosted gateway endpoints dialing by IP.

## Scripts Overview

### 1. `deploy/starpub/smoke-llm.sh`
Performs credential-safe endpoint connectivity verification using `talkintent llm test`.
- Accepts `--env <path>` or `--config <path>`.
- Sources the environment in an isolated subshell with a temporary config file (`0700` temp dir).
- Masks API keys (displaying only length) and tests private CA + SNI dial parameters.

### 2. `deploy/starpub/run-multiuser.sh`
End-to-end automation suite executing all acceptance scenarios:
- **Compilation**: Compiles static `linux/amd64` `talkintent` binary into `/tmp/talkintent-multiuser-bin/talkintent`.
- **User Provisioning & Git Seeding**: Prepares isolated Git repositories and `.talkintent/privacy-prompt.md` rules for `ti-alice`, `ti-bob`, and `ti-carol`.
- **Hub Launch**: Starts Hub on dedicated port (default `18801`), tracks PID in `/tmp/talkintent-multiuser-pids/hub.pid`, and retrieves auto-generated `admin.token`.
- **Endpoint Pairing & LLM Configuration**: Generates admin invites via REST API, pairs each user endpoint, configures LLM with private CA and SNI, and tests connectivity.
- **Daemon Lifecycle**: Starts daemons under `sudo -u ti-<user>` with `nohup` and PID file tracking.
- **Scenario Assertions**:
  - **Scenario A (Cross-User Ask)**: Bob asks Alice about branch and modified files -> asserts `status: completed`, non-empty `tools_used`, and response mentions Alice's real branch (`feature/billing-v1`) or modified files (`main.go`, `alice_work.txt`). (Alice's rule only restricts financial details; Bob's rule covers the very branch he is on, so he is not a valid "answer normally" target — a real model legitimately refuses.)
  - **Scenario B (Privacy Refusal)**: Alice asks Bob for confidential `feature/auth-v2` diffs -> asserts `status: refused` (the model must call the structured `refuse` tool or emit the `REFUSED:` fallback; a chatty `completed` answer fails), a non-empty refusal reason, and no git diff leaked in the reason.
  - **Scenario C (Offline Queue & Node Reconnect)**: Stops Carol's daemon, Alice asks Carol asynchronously (`-wait=false`) -> asserts `status: queued`, restarts Carol's daemon -> asserts query drains to `completed`.
  - **Scenario D (TTL Expiry)**: Stops Carol's daemon, Alice asks Carol with `-ttl 5 -wait=false` -> asserts `status: queued`, waits for Hub 15s sweep ticker (sleep 22s) -> asserts query transitions to `expired`.
  - **Scenario E (Audit Verification)**: Queries Hub `/api/v1/audit/inbound` and `/api/v1/audit/outbound` with member Bearer tokens -> asserts entry counts >= 1.
  - **Scenario F (Unauthenticated REST Access)**: Queries `/api/v1/members` without authorization -> asserts HTTP 401.
  - **Scenario G (Members Directory)**: Runs `talkintent members -json` as a `ti-*` user -> asserts 3 online members.
- **Automated Teardown**: Invokes `stop.sh` upon exit to guarantee no orphaned processes or ports.

### 3. `deploy/starpub/stop.sh`
Terminates all running processes:
- Reads PID files from `/tmp/talkintent-multiuser-pids/`.
- Sends SIGTERM with grace period, falling back to SIGKILL if unresponsive.
- Runs `sudo pkill -u ti-<user> -f talkintent` to ensure clean process tables.

## Execution Example

```bash
# Test endpoint
deploy/starpub/smoke-llm.sh --env /home/skift/.config/qa-assistant/astergate.env

# Run full acceptance suite
deploy/starpub/run-multiuser.sh \
    --port 18801 \
    --llm-env /home/skift/.config/qa-assistant/astergate.env \
    --log-dir /tmp/talkintent-multiuser-logs
```
