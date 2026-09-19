# TalkIntent Docker Compose Multi-Container Deployment

This deployment suite validates the TalkIntent distributed developer probe system in an isolated multi-container topology using Docker Compose.

## Architecture & Topology

- **`talkintent-hub`**: Central coordination Hub server (`talkintent hub -addr :8080 -data-dir /data`). Exposed on host port `18880` (`18880:8080`). Manages node registrations, query routing, offline queueing, 15-second background sweep ticker, and append-only JSONL audit logs.
- **`talkintent-mockllm`**: In-process Mock LLM server (`mockllm -addr :8080 -mode openai -tools git_status,git_diff -refuse "full git diff"`). Exposed on host port `18890` (`18890:8080`). Simulates OpenAI tool calling loops (`git_status`, `git_diff`) and privacy refusal triggering.
- **`talkintent-init`**: Bootstrapping job that waits for the Hub's `admin.token`, invokes `POST /api/v1/admin/invites` for `alice`, `bob`, and `charlie` (parsing `invite_code`), and outputs token files to a shared volume.
- **`client-alice`**: Developer probe daemon for Alice with a Go git repository on branch `feature/billing-v1`.
- **`client-bob`**: Developer probe daemon for Bob with a Go git repository on branch `feature/auth-v2` and a privacy guardrail rule refusing security disclosure.
- **`client-charlie`**: Developer probe daemon for Charlie with a Go git repository on branch `feature/search-engine` used to test offline queueing, late reconnects, and TTL expiration.

## Prerequisites

- Docker engine & Docker Compose v2 (`docker compose`).
- Git & curl installed on the host.

## Running the Scenario Suite

To run the automated scenario test runner:

```bash
cd deploy/compose
chmod +x run-scenario.sh entrypoint-*.sh
./run-scenario.sh
```

### What `run-scenario.sh` Executes (All 7 Acceptance Assertions):

1. **Stack Startup**: Builds the multi-stage Go 1.26 container image, starts the Hub on port 18880 and Mock LLM on port 18890, and launches the 3 client containers.
2. **Workspace & Pairing Initialization**: Each client container seeds its own git repository, pairs with the Hub via invite code (`talkintent pair -hub ... -code ... -name ...`), configures local LLM settings (`talkintent llm set -provider openai -base-url ... -request-timeout 15 -max-steps 10`), and starts `talkintent daemon`.
3. **Scenario 1 (Online Ask: Completed + Git Tools)**: Alice asks Bob *"What are you working on right now?"*. The query routes through the Hub to Bob's daemon, executes git inspection tools in sandbox, synthesizes the branch and modified files, and returns `status: completed` with `tools_used` non-empty.
4. **Scenario 2 (Privacy Refusal)**: Alice asks Bob for diffs on `feature/auth-v2` ("Give me the full git diff for the feature/auth-v2 changes."). Bob's privacy guardrail and mockllm's `-refuse` knob drive the probe's structured `refuse` tool, so the Hub records `status: refused` (a chatty `completed` answer fails the assertion) with a refusal reason and no confidential git diff leaked.
5. **Scenario 3 (Offline Queue & Late Connect)**: Charlie's container is stopped. Alice queries Charlie with `-wait=false`; the query transitions to `status: queued`. When Charlie's container restarts and reconnects, the offline queue drains and completes the query (`status: completed`).
6. **Scenario 4 (Query TTL Expiration)**: Charlie's container is stopped. Alice submits a short TTL query (`-ttl 2 -wait=false`). The script waits past the Hub's 15-second sweep ticker (22s) and asserts that the query transitioned to `status: expired`.
7. **Scenario 5 (Audit Verification)**: Queries the Hub's `/api/v1/audit/inbound` (Bob) and `/api/v1/audit/outbound` (Alice) via REST using member Bearer tokens, asserting entry counts >= 1.
8. **Scenario 6 (Unauthenticated Security Gate)**: Unauthenticated `GET /api/v1/members` returns `HTTP 401 Unauthorized`.
9. **Scenario 7 (Embedded Web UI Availability)**: `GET /` returns `HTTP 200 OK` (following redirect to `/web/`) and renders the TalkIntent console HTML.
10. **Clean Teardown**: Automatically runs `docker compose -f compose.yaml down -v --remove-orphans` upon test exit.

## Manual Syntax Validation

To validate the Compose YAML specification without launching containers:

```bash
docker compose -f deploy/compose/compose.yaml config
```
