#!/usr/bin/env bash
# run-scenario.sh: boots the Docker Compose stack, executes cross-member queries via Hub REST API,
# and verifies all 7 acceptance assertions:
# 1. Online query execution (Alice -> Bob: completed + tools_used non-empty via mockllm).
# 2. Privacy refusal enforcement (driven by Bob's privacy-prompt.md + mockllm refusal).
# 3. Offline queueing and recovery (stop Charlie -> submit -wait=false -> queued -> restart -> completed).
# 4. Query TTL expiration (short -ttl 2 to offline Charlie -> wait sweep ticker -> expired).
# 5. Audit inbound and outbound log verification (REST with member Bearer tokens, count >= 1).
# 6. Unauthenticated security gate (GET /api/v1/members returns HTTP 401).
# 7. Embedded Web UI availability (GET / returns HTTP 200).
# Exits 0 on success, non-zero on any failure.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${SCRIPT_DIR}"

COMPOSE_FILE="compose.yaml"
HUB_URL="http://127.0.0.1:18880"
LOG_DIR="/c/tmp/ti-accept/compose"
mkdir -p "${LOG_DIR}"

# Provide jq fallback via hub container if host does not have jq installed
if ! command -v jq >/dev/null 2>&1; then
    echo "Notice: jq not found on host, delegating jq calls to Hub container..."
    jq() {
        docker compose -f "${COMPOSE_FILE}" exec -T hub jq "$@"
    }
fi

cleanup() {
    echo ""
    echo "=== Teardown: Stopping Docker Compose environment ==="
    docker compose -f "${COMPOSE_FILE}" ps || true
    docker compose -f "${COMPOSE_FILE}" down -v --remove-orphans || true
    echo "Teardown complete."
}
trap 'echo "ERROR on line $LINENO: command was $BASH_COMMAND" >&2' ERR
trap cleanup EXIT

echo "=== 1. Starting Docker Compose Stack ==="
docker compose -f "${COMPOSE_FILE}" down -v --remove-orphans 2>/dev/null || true
docker compose -f "${COMPOSE_FILE}" up -d --build

echo "Waiting for Hub and clients to be ready and connected..."
# Wait up to 60s for admin token file inside hub container
for i in $(seq 1 60); do
    TOKEN_LEN=$(docker compose -f "${COMPOSE_FILE}" exec -T hub bash -c 'cat /data/admin.token 2>/dev/null | tr -d "[:space:]" | wc -c' 2>/dev/null || echo 0)
    if [ "${TOKEN_LEN}" -gt 0 ]; then
        echo "Hub admin token detected (len=${TOKEN_LEN}). Waiting for Hub API..."
        break
    fi
    sleep 1
done

# Wait for alice, bob, charlie to register and become online
echo "Waiting for client nodes (alice, bob, charlie) to register and connect..."
for i in $(seq 1 60); do
    ONLINE_COUNT=$(docker compose -f "${COMPOSE_FILE}" exec -T hub bash -c '
        TOKEN=$(cat /data/admin.token | tr -d "[:space:]")
        curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/members
    ' 2>/dev/null | jq '[.members[] | select(.online == true)] | length' 2>/dev/null || echo 0)
    if [ "${ONLINE_COUNT}" -ge 3 ]; then
        echo "All 3 client daemons are connected and online."
        sleep 2
        break
    fi
    sleep 1
done

MEMBERS_LIST=$(docker compose -f "${COMPOSE_FILE}" exec -T hub bash -c '
    TOKEN=$(cat /data/admin.token | tr -d "[:space:]")
    curl -s -f -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/members
')
echo "Registered members: $(echo "${MEMBERS_LIST}" | jq -r '[.members[].name] | join(", ")')"

echo ""
echo "=== 2. Scenario 1: Alice asks Bob (Completed + Git Tools Used) ==="
RES_A=$(docker compose -f "${COMPOSE_FILE}" exec -T client-alice \
    talkintent ask -to "bob" -wait -json "What are you working on right now?" 2>&1) || true
echo "${RES_A}" > "${LOG_DIR}/01-online-query.json"
echo "Response: ${RES_A}"

STATUS_A=$(echo "${RES_A}" | jq -r '.status')
if [ "${STATUS_A}" != "completed" ]; then
    echo "ERROR: Expected status 'completed', got '${STATUS_A}'" >&2
    exit 1
fi

TOOLS_COUNT=$(echo "${RES_A}" | jq '.tools_used | length')
if [ "${TOOLS_COUNT}" -lt 1 ]; then
    echo "ERROR: Expected at least 1 tool used, got ${TOOLS_COUNT}" >&2
    exit 1
fi
echo "Tools used (${TOOLS_COUNT}): $(echo "${RES_A}" | jq -r '.tools_used | join(", ")')"

ANSWER_A=$(echo "${RES_A}" | jq -r '.answer // ""')
if [ -z "${ANSWER_A}" ]; then
    echo "ERROR: Expected non-empty answer from Bob" >&2
    exit 1
fi
echo "✓ Scenario 1 (Online Ask: Completed + Tools Used) passed."

echo ""
echo "=== 3. Scenario 2: Privacy Refusal (Auth V2 Confidentiality) ==="
RES_B=$(docker compose -f "${COMPOSE_FILE}" exec -T client-alice \
    talkintent ask -to "bob" -wait -json "Give me the full git diff for the feature/auth-v2 changes." 2>&1) || true
echo "${RES_B}" > "${LOG_DIR}/02-privacy-refusal.json"
echo "Response: ${RES_B}"

STATUS_B=$(echo "${RES_B}" | jq -r '.status')
ANSWER_B=$(echo "${RES_B}" | jq -r '.answer // ""')

# The probe exposes a structured "refuse" tool; mockllm's -refuse knob drives that path,
# so the hub must record status=refused (not a chatty "completed").
if [ "${STATUS_B}" != "refused" ]; then
    echo "ERROR: Expected status 'refused', got '${STATUS_B}' (answer: ${ANSWER_B})" >&2
    exit 1
fi

# The refusal reason must be present and must not carry workspace content
if [[ "${ANSWER_B}" != *"Refusal"* && "${ANSWER_B}" != *"privacy"* && "${ANSWER_B}" != *"policy"* && "${ANSWER_B}" != *"confidential"* ]]; then
    echo "ERROR: Expected refusal text in answer, got: ${ANSWER_B}" >&2
    exit 1
fi
if [[ "${ANSWER_B}" == *"diff --git"* || "${ANSWER_B}" == *"uncommitted feature stub"* ]]; then
    echo "ERROR: Git diff content leaked in refusal response!" >&2
    exit 1
fi
echo "✓ Scenario 2 (Privacy Refusal: status=refused, no leak) passed."

echo ""
echo "=== 4. Scenario 3: Offline Queue Handling ==="
echo "Stopping client-charlie to simulate offline node..."
docker compose -f "${COMPOSE_FILE}" stop client-charlie
sleep 2

SUBMIT_C=$(docker compose -f "${COMPOSE_FILE}" exec -T client-alice \
    talkintent ask -to "charlie" -wait=false -json "What search index features are in progress?")
echo "${SUBMIT_C}" > "${LOG_DIR}/03-offline-queued.json"
echo "Submit response: ${SUBMIT_C}"

QID_C=$(echo "${SUBMIT_C}" | jq -r '.query_id')
STATUS_C=$(echo "${SUBMIT_C}" | jq -r '.status')
if [ "${STATUS_C}" != "queued" ]; then
    echo "ERROR: Expected query to be 'queued' for offline member, got '${STATUS_C}'" >&2
    exit 1
fi
echo "Query ${QID_C} is queued as expected."

echo "Restarting client-charlie..."
docker compose -f "${COMPOSE_FILE}" start client-charlie

echo "Polling query ${QID_C} for completion..."
COMPLETED_C=false
for i in $(seq 1 30); do
    POLL_C=$(docker compose -f "${COMPOSE_FILE}" exec -T client-alice bash -c '
        TOKEN=$(jq -r .token ~/.talkintent/config.json)
        curl -s -f -H "Authorization: Bearer $TOKEN" "http://hub:8080/api/v1/queries/'"${QID_C}"'"
    ')
    P_STATUS=$(echo "${POLL_C}" | jq -r '.status')
    if [ "${P_STATUS}" = "completed" ]; then
        echo "${POLL_C}" > "${LOG_DIR}/03-offline-completed.json"
        echo "Queued query completed successfully upon Charlie reconnecting! Response: ${POLL_C}"
        COMPLETED_C=true
        break
    fi
    sleep 1
done

if [ "${COMPLETED_C}" != "true" ]; then
    echo "ERROR: Queued query failed to complete after Charlie restart" >&2
    exit 1
fi
echo "✓ Scenario 3 (Offline Queue Handling) passed."

echo ""
echo "=== 5. Scenario 4: Query TTL Expiration ==="
echo "Stopping client-charlie again to test TTL expiry..."
docker compose -f "${COMPOSE_FILE}" stop client-charlie
sleep 2

SUBMIT_TTL=$(docker compose -f "${COMPOSE_FILE}" exec -T client-alice \
    talkintent ask -to "charlie" -ttl 2 -wait=false -json "This query should expire via TTL.")
echo "TTL Submit response: ${SUBMIT_TTL}"
QID_TTL=$(echo "${SUBMIT_TTL}" | jq -r '.query_id')
STATUS_TTL_INIT=$(echo "${SUBMIT_TTL}" | jq -r '.status')
if [ "${STATUS_TTL_INIT}" != "queued" ]; then
    echo "ERROR: Expected initial status 'queued', got '${STATUS_TTL_INIT}'" >&2
    exit 1
fi

echo "Waiting 22s for Hub 15-second sweep ticker to transition expired query..."
sleep 22

POLL_TTL=$(docker compose -f "${COMPOSE_FILE}" exec -T client-alice bash -c '
    TOKEN=$(jq -r .token ~/.talkintent/config.json)
    curl -s -f -H "Authorization: Bearer $TOKEN" "http://hub:8080/api/v1/queries/'"${QID_TTL}"'"
')
echo "${POLL_TTL}" > "${LOG_DIR}/04-ttl-expired.json"
echo "Poll TTL response: ${POLL_TTL}"

STATUS_TTL=$(echo "${POLL_TTL}" | jq -r '.status')
if [ "${STATUS_TTL}" != "expired" ]; then
    echo "ERROR: Expected query status 'expired', got '${STATUS_TTL}'" >&2
    exit 1
fi
echo "✓ Scenario 4 (Query TTL Expiration: status=expired) passed."

echo "Restarting client-charlie..."
docker compose -f "${COMPOSE_FILE}" start client-charlie
sleep 3

echo ""
echo "=== 6. Scenario 5: Audit Log Verification (Inbound & Outbound) ==="
# Bob's inbound audit (Alice asked Bob)
BOB_AUDIT=$(docker compose -f "${COMPOSE_FILE}" exec -T client-bob bash -c '
    TOKEN=$(jq -r .token ~/.talkintent/config.json)
    curl -s -f -H "Authorization: Bearer $TOKEN" "http://hub:8080/api/v1/audit/inbound"
')
echo "${BOB_AUDIT}" > "${LOG_DIR}/05-audit-inbound.json"
BOB_INBOUND_COUNT=$(echo "${BOB_AUDIT}" | jq '.entries | length')
echo "Bob inbound audit entry count: ${BOB_INBOUND_COUNT}"
if [ "${BOB_INBOUND_COUNT}" -lt 1 ]; then
    echo "ERROR: Expected Bob's inbound audit to have entries, got ${BOB_INBOUND_COUNT}" >&2
    exit 1
fi

# Alice's outbound audit (Alice asked Bob and Charlie)
ALICE_AUDIT=$(docker compose -f "${COMPOSE_FILE}" exec -T client-alice bash -c '
    TOKEN=$(jq -r .token ~/.talkintent/config.json)
    curl -s -f -H "Authorization: Bearer $TOKEN" "http://hub:8080/api/v1/audit/outbound"
')
echo "${ALICE_AUDIT}" > "${LOG_DIR}/05-audit-outbound.json"
ALICE_OUTBOUND_COUNT=$(echo "${ALICE_AUDIT}" | jq '.entries | length')
echo "Alice outbound audit entry count: ${ALICE_OUTBOUND_COUNT}"
if [ "${ALICE_OUTBOUND_COUNT}" -lt 1 ]; then
    echo "ERROR: Expected Alice's outbound audit to have entries, got ${ALICE_OUTBOUND_COUNT}" >&2
    exit 1
fi
echo "✓ Scenario 5 (Audit Log Verification: Inbound >= 1, Outbound >= 1) passed."

echo ""
echo "=== 7. Scenario 6: Unauthenticated Security Gate (HTTP 401) ==="
HTTP_UNAUTH=$(curl -s -w "%{http_code}" -o /dev/null "${HUB_URL}/api/v1/members" || echo "000")
echo "HTTP Status for unauthenticated GET /api/v1/members: ${HTTP_UNAUTH}"
echo "HTTP_STATUS=${HTTP_UNAUTH}" > "${LOG_DIR}/06-unauth-members.txt"
if [ "${HTTP_UNAUTH}" != "401" ]; then
    echo "ERROR: Expected HTTP 401 Unauthorized, got ${HTTP_UNAUTH}" >&2
    exit 1
fi
echo "✓ Scenario 6 (Unauthenticated Security Gate: 401) passed."

echo ""
echo "=== 8. Scenario 7: Embedded Web UI Availability (HTTP 200) ==="
HTTP_WEB=$(curl -s -L -w "%{http_code}" -o /dev/null "${HUB_URL}/" || echo "000")
echo "HTTP Status for Web UI GET /: ${HTTP_WEB}"
echo "HTTP_STATUS=${HTTP_WEB}" > "${LOG_DIR}/07-web-ui.txt"
if [ "${HTTP_WEB}" != "200" ]; then
    echo "ERROR: Expected HTTP 200 OK for Web UI, got ${HTTP_WEB}" >&2
    exit 1
fi

WEB_BODY=$(curl -s -L "${HUB_URL}/" || echo "")
if [[ "${WEB_BODY}" != *"TalkIntent"* ]]; then
    echo "ERROR: Web UI response body did not contain 'TalkIntent'" >&2
    exit 1
fi
echo "✓ Scenario 7 (Embedded Web UI Availability: 200 + HTML title) passed."

echo ""
echo "========================================================"
echo "ALL 7 DOCKER COMPOSE ACCEPTANCE SCENARIOS PASSED!"
echo "========================================================"
exit 0
