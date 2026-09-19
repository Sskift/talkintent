#!/usr/bin/env bash
# run-multiuser.sh: Multi-user validation suite for Docker-less Linux hosts (e.g. StarPub).
# Endpoints are simulated by distinct Linux system users: ti-alice, ti-bob, ti-carol.
# Builds the linux/amd64 binary, starts the Hub as invoking user, seeds git repos,
# configures ~/.talkintent/config.json with LLM credentials (sourced inside user shell, never echoing values),
# pairs each daemon via Hub admin invites, starts daemons under sudo -u with nohup + PID files,
# runs scenarios A through G, and verifies assertions.
#
# Usage: ./run-multiuser.sh [--port PORT] [--llm-env PATH] [--log-dir PATH]
set -euo pipefail

export PATH="$HOME/.local/bin:$HOME/.local/go/bin:/usr/local/go/bin:$PATH"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

HUB_PORT="18801"
LLM_ENV_FILE="/home/skift/.config/qa-assistant/astergate.env"
CA_FILE_SRC="/home/skift/.config/talkintent/astergate-leaf.pem"
TLS_SNI="aster.empeirion.cn"
PID_DIR="/tmp/talkintent-multiuser-pids"
HUB_DATA_DIR="/tmp/talkintent-multiuser-hub-data"
BIN_DIR="/tmp/talkintent-multiuser-bin"
LOG_DIR=""

while [ $# -gt 0 ]; do
    case "$1" in
        --port)
            HUB_PORT="$2"
            shift 2
            ;;
        --llm-env)
            LLM_ENV_FILE="$2"
            shift 2
            ;;
        --ca-file)
            CA_FILE_SRC="$2"
            shift 2
            ;;
        --log-dir)
            LOG_DIR="$2"
            shift 2
            ;;
        *)
            echo "Unknown argument: $1" >&2
            echo "Usage: $0 [--port PORT] [--llm-env PATH] [--log-dir PATH]" >&2
            exit 1
            ;;
    esac
done

cleanup() {
    local exit_code=$?
    echo ""
    echo "=== Cleaning Up Multi-User Processes (exit code: ${exit_code}) ==="

    if [ -n "${LOG_DIR}" ] && [ -d "${PID_DIR}" ]; then
        mkdir -p "${LOG_DIR}"
        cp -r "${PID_DIR}"/* "${LOG_DIR}/" 2>/dev/null || true
    fi

    "${SCRIPT_DIR}/stop.sh" "${PID_DIR}" || true
    exit "${exit_code}"
}
trap cleanup EXIT

# 0. Pre-clean previous run artifacts
echo "=== 0. Preparing Workspace & Artifact Directories ==="
"${SCRIPT_DIR}/stop.sh" "${PID_DIR}" 2>/dev/null || true
rm -rf "${HUB_DATA_DIR}" "${PID_DIR}"
mkdir -p "${BIN_DIR}" "${PID_DIR}" "${HUB_DATA_DIR}"
chmod 1777 "${PID_DIR}"
chmod 755 "${BIN_DIR}"

echo ""
echo "=== 1. Building Linux/amd64 talkintent binary ==="
BIN="${BIN_DIR}/talkintent"
(
    cd "${REPO_ROOT}"
    echo "Compiling talkintent single binary..."
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "${BIN}" ./cmd/talkintent
    chmod 755 "${BIN}"
)
echo "Binary ready: ${BIN} ($("${BIN}" version 2>/dev/null || echo 'built'))"

echo ""
echo "=== 2. Verifying Linux Users (ti-alice, ti-bob, ti-carol) ==="
for u in ti-alice ti-bob ti-carol; do
    if ! id "${u}" >/dev/null 2>&1; then
        echo "ERROR: User ${u} does not exist. Run provision-users.sh first." >&2
        exit 1
    fi
    # Clean previous user talkintent data & workspaces
    sudo -u "${u}" -H rm -rf "/home/${u}/.talkintent" "/home/${u}/work"
    sudo -u "${u}" -H mkdir -p "/home/${u}/.talkintent" "/home/${u}/work"
    sudo chmod 700 "/home/${u}/.talkintent" "/home/${u}/work"
done

echo ""
echo "=== 3. Starting TalkIntent Hub Server ==="
HUB_URL="http://127.0.0.1:${HUB_PORT}"
nohup "${BIN}" hub -addr "127.0.0.1:${HUB_PORT}" -data-dir "${HUB_DATA_DIR}" > "${PID_DIR}/hub.log" 2>&1 &
echo $! > "${PID_DIR}/hub.pid"
echo "Hub process started with PID $(cat "${PID_DIR}/hub.pid")"

# Wait for Hub to generate admin token
TOKEN_FILE="${HUB_DATA_DIR}/admin.token"
echo "Waiting for Hub admin token at ${TOKEN_FILE}..."
ADMIN_TOKEN=""
for i in $(seq 1 40); do
    if [ -f "${TOKEN_FILE}" ]; then
        ADMIN_TOKEN=$(cat "${TOKEN_FILE}" | tr -d '[:space:]')
        if [ -n "${ADMIN_TOKEN}" ]; then
            break
        fi
    fi
    sleep 0.25
done

if [ -z "${ADMIN_TOKEN}" ]; then
    echo "ERROR: Failed to acquire Hub admin token from ${TOKEN_FILE}" >&2
    exit 1
fi
echo "Hub is listening at ${HUB_URL}. Admin token ready (len=${#ADMIN_TOKEN})."

# Helper to generate invite
create_invite() {
    local target="$1"
    local resp
    resp=$(curl -s -f -X POST "${HUB_URL}/api/v1/admin/invites" \
        -H "Authorization: Bearer ${ADMIN_TOKEN}" \
        -H "Content-Type: application/json" \
        -d "{\"target_name\": \"${target}\", \"expires_in_hours\": 24}")
    echo "${resp}" | jq -r '.code // .invite_code'
}

echo ""
echo "=== 4. Setting Up Developer Workspaces & Daemons ==="

setup_user_endpoint() {
    local username="$1"       # e.g. alice
    local login="ti-${username}"
    local branch="$2"
    local privacy_rule="$3"

    echo "--- Configuring ${login} ---"
    local user_home
    user_home=$(getent passwd "${login}" | cut -d: -f6)
    local work_dir="${user_home}/work/demo"

    # Seed Git repository under user ownership
    sudo -u "${login}" -H bash -c "
        set -euo pipefail
        mkdir -p '${work_dir}'
        cd '${work_dir}'
        if [ ! -d .git ]; then
            git config --global user.name '${username}'
            git config --global user.email '${username}@example.com'
            git config --global init.defaultBranch main
            git init
            echo '# ${username} demo repository' > README.md
            echo 'package main' > main.go
            git add .
            git commit -m 'initial commit'
            git checkout -b '${branch}'
            echo '// uncommitted feature work by ${username}' >> main.go
            echo 'scratch data for ${username}' > '${username}_work.txt'
        fi

        mkdir -p '${work_dir}/.talkintent'
        if [ -n '${privacy_rule}' ]; then
            echo '${privacy_rule}' > '${work_dir}/.talkintent/privacy-prompt.md'
        fi
    "

    # Generate invite from Hub
    local invite_code
    invite_code=$(create_invite "${username}")
    echo "Generated invite for ${username}"

    # Pair user and register workspace
    sudo -u "${login}" -H bash -c "
        set -euo pipefail
        '${BIN}' pair -hub '${HUB_URL}' -code '${invite_code}' -name '${username}-box'
        '${BIN}' workspace add '${work_dir}' -name 'demo'
    "

    # Distribute leaf cert if present
    if [ -f "${CA_FILE_SRC}" ]; then
        sudo install -m 644 -o "${login}" -g "${login}" "${CA_FILE_SRC}" "${user_home}/.talkintent/astergate-leaf.pem"
    fi

    # Configure LLM settings inside target user's shell without echoing secrets
    if [ -n "${LLM_ENV_FILE}" ] && [ -f "${LLM_ENV_FILE}" ]; then
        sudo install -m 600 -o "${login}" -g "${login}" "${LLM_ENV_FILE}" "${user_home}/.talkintent/llm.env"
        sudo -u "${login}" -H bash -c "
            set -euo pipefail
            set -a
            # Source env file inside user shell; credentials never echoed to stdout
            source '${user_home}/.talkintent/llm.env'
            set +a
            rm -f '${user_home}/.talkintent/llm.env'

            BASE_URL=\"\${ANTHROPIC_BASE_URL:-\${TALKINTENT_LLM_URL:-}}\"
            API_KEY=\"\${ANTHROPIC_API_KEY:-\${TALKINTENT_LLM_KEY:-}}\"
            MODEL=\"\${ANTHROPIC_MODEL:-\${TALKINTENT_LLM_MODEL:-gemini-3.8-flash-high}}\"
            PROVIDER=\"\${TALKINTENT_LLM_PROVIDER:-anthropic}\"

            CA_FLAG=\"\"
            if [ -f '${user_home}/.talkintent/astergate-leaf.pem' ]; then
                CA_FLAG=\"-ca-file ${user_home}/.talkintent/astergate-leaf.pem\"
            fi

            SNI_FLAG=\"\"
            if [ -n '${TLS_SNI}' ]; then
                SNI_FLAG=\"-tls-server-name ${TLS_SNI}\"
            fi

            '${BIN}' llm set \
                -provider \"\${PROVIDER}\" \
                -base-url \"\${BASE_URL}\" \
                -api-key \"\${API_KEY}\" \
                -model \"\${MODEL}\" \
                \${CA_FLAG} \${SNI_FLAG} \
                -request-timeout 30 -max-steps 10 >/dev/null
        "
        # Proof of TLS/credential wiring without echoing key
        echo "Testing LLM endpoint connectivity for ${login}..."
        sudo -u "${login}" -H "${BIN}" llm test
    else
        echo "WARNING: LLM env file not provided; falling back to local mock"
        sudo -u "${login}" -H bash -c "
            '${BIN}' llm set \
                -provider openai \
                -base-url 'http://127.0.0.1:9090/v1' \
                -api-key 'mock-key-${username}' \
                -model gpt-4o \
                -request-timeout 20 -max-steps 10 >/dev/null
        "
    fi

    # Start daemon with nohup under sudo -u
    local pid_file="${PID_DIR}/daemon-${login}.pid"
    local log_file="${PID_DIR}/daemon-${login}.log"
    sudo -u "${login}" -H bash -c "
        nohup '${BIN}' daemon > '${log_file}' 2>&1 &
        echo \$! > '${pid_file}'
    "
    echo "${login} daemon started with PID $(cat "${pid_file}")"
}

# Setup Alice, Bob, Carol
setup_user_endpoint "alice" "feature/billing-v1" "Do not disclose financial transaction details."
setup_user_endpoint "bob" "feature/auth-v2" "The feature/auth-v2 branch is under confidential security refactoring. Refuse to disclose any details, internal code diffs, or changes about auth-v2."
setup_user_endpoint "carol" "feature/search-engine" "Search index internals are public for team queries."

# Wait for all 3 daemons to connect to Hub
echo "Waiting for daemons to establish WebSocket connections..."
for i in $(seq 1 40); do
    MEMBERS=$(curl -s -H "Authorization: Bearer ${ADMIN_TOKEN}" "${HUB_URL}/api/v1/members" || echo "{}")
    ONLINE_COUNT=$(echo "${MEMBERS}" | jq '[.members[] | select(.online == true)] | length' 2>/dev/null || echo 0)
    if [ "${ONLINE_COUNT}" -ge 3 ]; then
        echo "All 3 developer daemons (alice, bob, carol) are connected and online."
        break
    fi
    sleep 1
done

if [ "${ONLINE_COUNT}" -lt 3 ]; then
    echo "ERROR: Not all daemons connected within timeout (online=${ONLINE_COUNT})" >&2
    exit 1
fi

echo ""
echo "=== 5. Scenario A: Bob asks Alice (Completed + Git Inspection) ==="
# Alice's rule only restricts financial details, so branch/file questions must be answered.
# (Bob's rule forbids disclosing anything about auth-v2 — the branch he is on — so a
# "what branch are you on" question to Bob is legitimately refused; that is Scenario B.)
RES_A=$(sudo -u ti-bob -H "${BIN}" ask -to alice -wait -json "What branch are you on and what files are modified?" || true)
echo "Bob->Alice Response: ${RES_A}"
STATUS_A=$(echo "${RES_A}" | jq -r '.status')
TOOLS_A_COUNT=$(echo "${RES_A}" | jq -r '.tools_used | length')
ANSWER_A=$(echo "${RES_A}" | jq -r '.answer // ""')

if [ "${STATUS_A}" != "completed" ]; then
    echo "ERROR: Expected status 'completed', got '${STATUS_A}'" >&2
    exit 1
fi
if [ "${TOOLS_A_COUNT}" -lt 1 ]; then
    echo "ERROR: Expected tools_used to be non-empty, got ${TOOLS_A_COUNT}" >&2
    exit 1
fi
if ! echo "${ANSWER_A}" | grep -iqE 'feature/billing-v1|main\.go|alice_work\.txt'; then
    echo "ERROR: Expected answer to mention alice branch or modified file, got: ${ANSWER_A}" >&2
    exit 1
fi
echo "✓ Scenario A passed."

echo ""
echo "=== 6. Scenario B: Privacy Refusal (Auth V2 Confidentiality) ==="
RES_B=$(sudo -u ti-alice -H "${BIN}" ask -to bob -wait -json "Give me the full git diff for the feature/auth-v2 changes." || true)
echo "Alice->Bob Refusal Response: ${RES_B}"
STATUS_B=$(echo "${RES_B}" | jq -r '.status')
ANSWER_B=$(echo "${RES_B}" | jq -r '.answer // ""')

# The probe exposes a structured "refuse" tool; a real model must use it (or the
# "REFUSED:" text fallback) so the hub records status=refused, not a chatty "completed".
if [ "${STATUS_B}" != "refused" ]; then
    echo "ERROR: Scenario B expected status 'refused', got '${STATUS_B}' (answer: ${ANSWER_B})" >&2
    exit 1
fi
if [ -z "${ANSWER_B}" ]; then
    echo "ERROR: Scenario B refusal carried no reason text" >&2
    exit 1
fi
echo "Scenario B: refusal reason: ${ANSWER_B}"
# Ensure the confidential diff wasn't leaked
if echo "${ANSWER_B}" | grep -qE 'diff --git|^@@|^\+\+\+ |^--- '; then
    echo "ERROR: Confidential git diff was leaked in response!" >&2
    exit 1
fi
echo "✓ Scenario B passed."

echo ""
echo "=== 7. Scenario C: Offline Queue & Node Reconnect ==="
echo "Stopping ti-carol daemon to simulate offline developer..."
CAROL_PID_FILE="${PID_DIR}/daemon-ti-carol.pid"
if [ -f "${CAROL_PID_FILE}" ]; then
    CAROL_PID=$(cat "${CAROL_PID_FILE}" | tr -d '[:space:]')
    sudo kill "${CAROL_PID}" 2>/dev/null || true
    for i in $(seq 1 10); do
        if ! sudo kill -0 "${CAROL_PID}" 2>/dev/null; then
            break
        fi
        sleep 0.2
    done
    rm -f "${CAROL_PID_FILE}"
fi

# Wait for Hub to register Carol as offline
echo "Waiting for Hub to detect Carol as offline..."
for i in $(seq 1 20); do
    MEMBERS=$(curl -s -H "Authorization: Bearer ${ADMIN_TOKEN}" "${HUB_URL}/api/v1/members" || echo "{}")
    CAROL_ONLINE=$(echo "${MEMBERS}" | jq -r '.members[] | select(.name == "carol") | .online' 2>/dev/null || echo "false")
    if [ "${CAROL_ONLINE}" = "false" ]; then
        echo "Hub confirmed Carol is offline."
        break
    fi
    sleep 0.5
done

# Submit query to Carol without waiting
SUBMIT_C=$(sudo -u ti-alice -H "${BIN}" ask -to carol -wait=false -json "What search features are you working on?" || true)
echo "Submit response: ${SUBMIT_C}"
QID_C=$(echo "${SUBMIT_C}" | jq -r '.query_id')
STATUS_C=$(echo "${SUBMIT_C}" | jq -r '.status')
if [ "${STATUS_C}" != "queued" ]; then
    echo "ERROR: Expected query to Carol to be 'queued', got '${STATUS_C}'" >&2
    exit 1
fi
echo "Query ${QID_C} successfully placed in offline queue."

echo "Restarting ti-carol daemon..."
sudo -u ti-carol -H bash -c "
    nohup '${BIN}' daemon > '${PID_DIR}/daemon-ti-carol.log' 2>&1 &
    echo \$! > '${CAROL_PID_FILE}'
"

echo "Polling query ${QID_C} for completion..."
ALICE_TOKEN=$(sudo cat /home/ti-alice/.talkintent/config.json | jq -r '.token')
COMPLETED_C=false
for i in $(seq 1 40); do
    POLL_C=$(curl -s -H "Authorization: Bearer ${ALICE_TOKEN}" "${HUB_URL}/api/v1/queries/${QID_C}")
    P_STAT=$(echo "${POLL_C}" | jq -r '.status')
    if [ "${P_STAT}" = "completed" ]; then
        echo "Queued query completed successfully upon Carol reconnecting!"
        COMPLETED_C=true
        break
    fi
    sleep 1
done

if [ "${COMPLETED_C}" != "true" ]; then
    echo "ERROR: Queued query failed to complete after Carol reconnected (last status: ${P_STAT})" >&2
    exit 1
fi
echo "✓ Scenario C passed."

echo ""
echo "=== 8. Scenario D: TTL Expiry on Offline Node ==="
echo "Stopping ti-carol daemon again to test TTL expiration..."
if [ -f "${CAROL_PID_FILE}" ]; then
    CAROL_PID=$(cat "${CAROL_PID_FILE}" | tr -d '[:space:]')
    sudo kill "${CAROL_PID}" 2>/dev/null || true
    for i in $(seq 1 10); do
        if ! sudo kill -0 "${CAROL_PID}" 2>/dev/null; then
            break
        fi
        sleep 0.2
    done
    rm -f "${CAROL_PID_FILE}"
fi

# Wait for Hub to register Carol as offline
echo "Waiting for Hub to detect Carol as offline..."
for i in $(seq 1 20); do
    MEMBERS=$(curl -s -H "Authorization: Bearer ${ADMIN_TOKEN}" "${HUB_URL}/api/v1/members" || echo "{}")
    CAROL_ONLINE=$(echo "${MEMBERS}" | jq -r '.members[] | select(.name == "carol") | .online' 2>/dev/null || echo "false")
    if [ "${CAROL_ONLINE}" = "false" ]; then
        echo "Hub confirmed Carol is offline."
        break
    fi
    sleep 0.5
done

# Submit short TTL query (5s) to offline Carol
SUBMIT_D=$(sudo -u ti-alice -H "${BIN}" ask -to carol -ttl 5 -wait=false -json "Query that will expire quickly" || true)
echo "Submit TTL response: ${SUBMIT_D}"
QID_D=$(echo "${SUBMIT_D}" | jq -r '.query_id')
STATUS_D=$(echo "${SUBMIT_D}" | jq -r '.status')
if [ "${STATUS_D}" != "queued" ]; then
    echo "ERROR: Expected query ${QID_D} to be 'queued', got '${STATUS_D}'" >&2
    exit 1
fi

echo "Waiting for Hub 15s sweep ticker to expire query ${QID_D} (sleeping 22s)..."
sleep 22

POLL_D=$(curl -s -H "Authorization: Bearer ${ALICE_TOKEN}" "${HUB_URL}/api/v1/queries/${QID_D}")
P_STAT_D=$(echo "${POLL_D}" | jq -r '.status')
echo "Query ${QID_D} status after TTL window: ${P_STAT_D}"
if [ "${P_STAT_D}" != "expired" ]; then
    echo "ERROR: Expected query to be 'expired', got '${P_STAT_D}'" >&2
    exit 1
fi
echo "✓ Scenario D passed."

echo ""
echo "=== 9. Scenario E: Inbound & Outbound Audit Verification ==="
BOB_TOKEN=$(sudo cat /home/ti-bob/.talkintent/config.json | jq -r '.token')
BOB_INBOUND=$(curl -s "${HUB_URL}/api/v1/audit/inbound" -H "Authorization: Bearer ${BOB_TOKEN}")
INBOUND_COUNT=$(echo "${BOB_INBOUND}" | jq '.entries | length')
if [ "${INBOUND_COUNT}" -lt 1 ]; then
    echo "ERROR: Expected Bob's inbound audit trail to have entries, got ${INBOUND_COUNT}" >&2
    exit 1
fi
echo "Bob's inbound audit record count: ${INBOUND_COUNT}"

ALICE_OUTBOUND=$(curl -s "${HUB_URL}/api/v1/audit/outbound" -H "Authorization: Bearer ${ALICE_TOKEN}")
OUTBOUND_COUNT=$(echo "${ALICE_OUTBOUND}" | jq '.entries | length')
if [ "${OUTBOUND_COUNT}" -lt 1 ]; then
    echo "ERROR: Expected Alice's outbound audit trail to have entries, got ${OUTBOUND_COUNT}" >&2
    exit 1
fi
echo "Alice's outbound audit record count: ${OUTBOUND_COUNT}"
echo "✓ Scenario E passed."

echo ""
echo "=== 10. Scenario F: Unauthenticated REST Access Rejected (401) ==="
HTTP_CODE_F=$(curl -s -o /dev/null -w "%{http_code}" "${HUB_URL}/api/v1/members")
echo "Unauthenticated GET /api/v1/members -> HTTP ${HTTP_CODE_F}"
if [ "${HTTP_CODE_F}" != "401" ]; then
    echo "ERROR: Expected HTTP 401, got ${HTTP_CODE_F}" >&2
    exit 1
fi
echo "✓ Scenario F passed."

echo ""
echo "=== 11. Scenario G: Members Directory Online Count ==="
echo "Restarting ti-carol daemon to restore full team mesh..."
sudo -u ti-carol -H bash -c "
    nohup '${BIN}' daemon > '${PID_DIR}/daemon-ti-carol.log' 2>&1 &
    echo \$! > '${CAROL_PID_FILE}'
"
for i in $(seq 1 30); do
    MEMBERS_G=$(sudo -u ti-alice -H "${BIN}" members -json 2>/dev/null || echo "{}")
    ONLINE_COUNT_G=$(echo "${MEMBERS_G}" | jq '[.members[] | select(.online == true)] | length' 2>/dev/null || echo 0)
    if [ "${ONLINE_COUNT_G}" -ge 3 ]; then
        echo "All 3 members reported online in 'talkintent members'!"
        break
    fi
    sleep 1
done

if [ "${ONLINE_COUNT_G}" -lt 3 ]; then
    echo "ERROR: Expected 3 online members in 'talkintent members', got ${ONLINE_COUNT_G}" >&2
    exit 1
fi
echo "✓ Scenario G passed."

echo ""
echo "=========================================="
echo "ALL STARPUB MULTI-USER SCENARIOS PASSED!"
echo "=========================================="
