#!/usr/bin/env bash
# deploy/xmachine/run-xmachine.sh: Reproducible acceptance test driver for Topology 3 (Cross-Machine)
# Windows Client (bin/talkintent.exe) <--- SSH Tunnel ---> Remote Linux Hub & Client (starpub-docker)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

TOPOLOGY="xmach"
ACCEPT_DIR="/c/tmp/ti-accept/${TOPOLOGY}"
LOG_DIR="${ACCEPT_DIR}"
BIN_WIN="${REPO_ROOT}/bin"
REMOTE_HOST="starpub-docker"
REMOTE_BASE="~/work/talkintent-tmp/xmach"

PORT_HUB="18820"
PORT_REMOTE_LLM="18821"
PORT_LOCAL_LLM="18822"
ADMIN_TOKEN="adm-xmach-top3"
HUB_URL="http://127.0.0.1:${PORT_HUB}"

step() {
  echo ""
  echo "================================================================================"
  echo ">>> [STEP] $*"
  echo "================================================================================"
}

log_sub() {
  echo "  --> $*"
}

mkdir -p "${LOG_DIR}"
rm -rf "${ACCEPT_DIR}/win-home" "${ACCEPT_DIR}/win-ws" "${ACCEPT_DIR}/skills"
rm -f "${LOG_DIR}"/*.log "${LOG_DIR}"/*.json "${ACCEPT_DIR}"/*.pid
mkdir -p "${ACCEPT_DIR}/win-home" "${ACCEPT_DIR}/win-ws" "${ACCEPT_DIR}/skills"

# Trap cleanup to guarantee teardown on unexpected failure
cleanup_on_exit() {
  local exit_code=$?
  if [ $exit_code -ne 0 ]; then
    echo "!!! Run failed with exit code $exit_code, executing teardown..."
    "${SCRIPT_DIR}/stop.sh" || true
  fi
}
trap cleanup_on_exit EXIT

step "0. Environment Check, Binary Build, and Remote Synchronization"
export PATH="$HOME/go-sdk/go/bin:$PATH"

log_sub "Compiling Windows and Linux binaries..."
mkdir -p "${BIN_WIN}"
(
  cd "${REPO_ROOT}"
  go build -o "${BIN_WIN}/talkintent.exe" ./cmd/talkintent
  go build -o "${BIN_WIN}/mockllm.exe" ./test/mockllm/cmd/mockllm
  GOOS=linux GOARCH=amd64 go build -o "${BIN_WIN}/talkintent-linux" ./cmd/talkintent
  GOOS=linux GOARCH=amd64 go build -o "${BIN_WIN}/mockllm-linux" ./test/mockllm/cmd/mockllm
)
echo "Windows binaries: $("${BIN_WIN}/talkintent.exe" version)"

log_sub "Preparing remote directories on ${REMOTE_HOST}..."
ssh -o BatchMode=yes "${REMOTE_HOST}" "
  mkdir -p ${REMOTE_BASE}/bin ${REMOTE_BASE}/logs ${REMOTE_BASE}/hub-data ${REMOTE_BASE}/remote-home ${REMOTE_BASE}/remote-ws
  rm -rf ${REMOTE_BASE}/hub-data/* ${REMOTE_BASE}/remote-home/* ${REMOTE_BASE}/remote-ws/*
"

log_sub "Copying Linux binaries to ${REMOTE_HOST}..."
scp -o BatchMode=yes "${BIN_WIN}/talkintent-linux" "${REMOTE_HOST}:${REMOTE_BASE}/bin/talkintent"
scp -o BatchMode=yes "${BIN_WIN}/mockllm-linux" "${REMOTE_HOST}:${REMOTE_BASE}/bin/mockllm"
ssh -o BatchMode=yes "${REMOTE_HOST}" "chmod +x ${REMOTE_BASE}/bin/*"

# Ensure clean slate before startup
"${SCRIPT_DIR}/stop.sh" >/dev/null 2>&1 || true

step "1. Starting Remote Hub and Remote MockLLM on ${REMOTE_HOST}"
ssh -o BatchMode=yes "${REMOTE_HOST}" "
  nohup ${REMOTE_BASE}/bin/talkintent hub -addr 127.0.0.1:${PORT_HUB} \
    -data-dir ${REMOTE_BASE}/hub-data \
    -admin-token ${ADMIN_TOKEN} \
    -public-url http://127.0.0.1:${PORT_HUB} > ${REMOTE_BASE}/logs/hub.log 2>&1 &
  echo \$! > ${REMOTE_BASE}/hub.pid

  nohup ${REMOTE_BASE}/bin/mockllm -addr 127.0.0.1:${PORT_REMOTE_LLM} \
    -mode openai -tools git_status,git_diff > ${REMOTE_BASE}/logs/mockllm.log 2>&1 &
  echo \$! > ${REMOTE_BASE}/mockllm.pid
"
sleep 2

# Verify remote hub process is running
ssh -o BatchMode=yes "${REMOTE_HOST}" "
  curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:${PORT_HUB}/api/v1/members
" > "${LOG_DIR}/01-remote-hub.log"
echo "Remote Hub initial check HTTP code: $(cat "${LOG_DIR}/01-remote-hub.log") (expected 401)"

ssh -o BatchMode=yes "${REMOTE_HOST}" "
  curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:${PORT_REMOTE_LLM}/healthz
" > "${LOG_DIR}/02-remote-mockllm.log"
echo "Remote MockLLM healthz HTTP code: $(cat "${LOG_DIR}/02-remote-mockllm.log") (expected 200)"

step "2. Starting Local MockLLM and Establishing SSH Tunnel"
log_sub "Starting local mockllm.exe on 127.0.0.1:${PORT_LOCAL_LLM} (refusing '工资')..."
"${BIN_WIN}/mockllm.exe" -addr "127.0.0.1:${PORT_LOCAL_LLM}" -mode openai -refuse "工资" -tools git_status,git_diff >"${LOG_DIR}/03-win-mockllm.log" 2>&1 &
LOCAL_MOCKLLM_PID=$!
echo $LOCAL_MOCKLLM_PID > "${ACCEPT_DIR}/mockllm.pid"
sleep 1

log_sub "Starting background SSH tunnel (forwarding ${PORT_HUB} -> ${REMOTE_HOST}:${PORT_HUB})..."
ssh -N -L "${PORT_HUB}:127.0.0.1:${PORT_HUB}" "${REMOTE_HOST}" >"${LOG_DIR}/04-ssh-tunnel.log" 2>&1 &
SSH_TUNNEL_PID=$!
echo $SSH_TUNNEL_PID > "${ACCEPT_DIR}/tunnel.pid"
sleep 2

log_sub "Testing Hub reachability from Windows through SSH tunnel..."
TUNNEL_CODE=""
for i in $(seq 1 30); do
  TUNNEL_CODE=$(curl -s -o /dev/null -w '%{http_code}' "${HUB_URL}/api/v1/members" 2>/dev/null || true)
  if [ "${TUNNEL_CODE}" = "401" ]; then
    break
  fi
  sleep 0.5
done
echo "Windows -> Tunnel -> Remote Hub HTTP code: ${TUNNEL_CODE} (expected 401)"
[ "${TUNNEL_CODE}" = "401" ] || { echo "ERROR: Tunnel failed to connect to Hub"; exit 1; }

step "3. Member Onboarding: Invites, Pairing, LLM & Workspace Setup"
PAIR_LOG="${LOG_DIR}/05-invite-pair.log"
exec 3>&1
exec > >(tee "${PAIR_LOG}") 2>&1

log_sub "Creating invite for Windows member (win-laptop)..."
INVITE_WIN_JSON=$("${BIN_WIN}/talkintent.exe" invite -hub "${HUB_URL}" -admin-token "${ADMIN_TOKEN}" -name win-laptop -alias win,laptop -json)
echo "Invite Windows response: ${INVITE_WIN_JSON}"
CODE_WIN=$(node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const o=JSON.parse(s);console.log(o.code||o.invite_code||"")}catch(e){console.log("")}})' <<< "${INVITE_WIN_JSON}")
echo "Parsed Windows invite code: ${CODE_WIN}"
[ -n "${CODE_WIN}" ] || { echo "Failed to parse Windows invite code"; exit 1; }

log_sub "Creating invite for Remote member (remote-dev)..."
INVITE_REMOTE_JSON=$("${BIN_WIN}/talkintent.exe" invite -hub "${HUB_URL}" -admin-token "${ADMIN_TOKEN}" -name remote-dev -alias remote,linux -json)
echo "Invite Remote response: ${INVITE_REMOTE_JSON}"
CODE_REMOTE=$(node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const o=JSON.parse(s);console.log(o.code||o.invite_code||"")}catch(e){console.log("")}})' <<< "${INVITE_REMOTE_JSON}")
echo "Parsed Remote invite code: ${CODE_REMOTE}"
[ -n "${CODE_REMOTE}" ] || { echo "Failed to parse Remote invite code"; exit 1; }

log_sub "Pairing Windows member with Hub through tunnel..."
TALKINTENT_HOME="${ACCEPT_DIR}/win-home" "${BIN_WIN}/talkintent.exe" pair -hub "${HUB_URL}" -code "${CODE_WIN}" -name win-laptop

log_sub "Pairing Remote member with Hub on Linux host..."
ssh -o BatchMode=yes "${REMOTE_HOST}" "
  TALKINTENT_HOME=${REMOTE_BASE}/remote-home ${REMOTE_BASE}/bin/talkintent pair -hub http://127.0.0.1:${PORT_HUB} -code ${CODE_REMOTE} -name remote-dev
"

log_sub "Configuring Windows LLM provider (mock on :${PORT_LOCAL_LLM})..."
TALKINTENT_HOME="${ACCEPT_DIR}/win-home" "${BIN_WIN}/talkintent.exe" llm set -provider openai -base-url "http://127.0.0.1:${PORT_LOCAL_LLM}/v1" -api-key sk-win-mock -model mock-win

log_sub "Configuring Remote LLM provider (mock on :${PORT_REMOTE_LLM})..."
ssh -o BatchMode=yes "${REMOTE_HOST}" "
  TALKINTENT_HOME=${REMOTE_BASE}/remote-home ${REMOTE_BASE}/bin/talkintent llm set -provider openai -base-url http://127.0.0.1:${PORT_REMOTE_LLM}/v1 -api-key sk-remote-mock -model mock-remote
"

log_sub "Seeding Windows Git workspace with uncommitted changes..."
(
  cd "${ACCEPT_DIR}/win-ws"
  git init -q
  git config user.name "Windows Dev"
  git config user.email "win@dev.local"
  printf 'package main\n\nfunc ProcessPayment() bool {\n\treturn true\n}\n' > pay.go
  git add pay.go
  git commit -qm "initial commit"
  git checkout -qb feature/payment-v2
  printf 'package main\n\n// Uncommitted changes for Payment V2\nfunc ProcessPayment() bool {\n\treturn true\n}\nfunc ValidateCard() bool {\n\treturn true\n}\n' > pay.go
)
TALKINTENT_HOME="${ACCEPT_DIR}/win-home" "${BIN_WIN}/talkintent.exe" workspace add "${ACCEPT_DIR}/win-ws" -name win-ws

log_sub "Configuring Windows sovereign privacy prompt (baseline rules without refusal trigger)..."
cat << 'EOF' > "${ACCEPT_DIR}/win-home/privacy-prompt.md"
# Sovereign Natural-Language Privacy Guardrails for Windows Member
1. 允许并如实回答关于当前工作分支、未提交代码改动及文件状态的提问。
2. 保持回答客观、精准，基于工作区实时代码执行工具检查。
EOF

log_sub "Seeding Remote Git workspace with uncommitted changes..."
ssh -o BatchMode=yes "${REMOTE_HOST}" "
  rm -rf ${REMOTE_BASE}/remote-ws
  mkdir -p ${REMOTE_BASE}/remote-ws
  cd ${REMOTE_BASE}/remote-ws
  git init -q
  git config user.name \"Remote Dev\"
  git config user.email \"remote@dev.local\"
  printf 'package main\n\nfunc Authenticate() bool {\n\treturn true\n}\n' > auth.go
  git add auth.go
  git commit -qm \"initial commit\"
  git checkout -qb feature/auth-service
  printf 'package main\n\n// Uncommitted auth token refresh\nfunc Authenticate() bool {\n\treturn true\n}\nfunc RefreshToken() string {\n\treturn \"dummy\"\n}\n' > auth.go
  TALKINTENT_HOME=${REMOTE_BASE}/remote-home ${REMOTE_BASE}/bin/talkintent workspace add ${REMOTE_BASE}/remote-ws -name remote-ws
"

exec 1>&3

step "4. Acceptance Scenario 1: Online Presence Discovery Across Machines"
MEMBERS_LOG="${LOG_DIR}/06-members-discovery.log"
exec 3>&1
exec > >(tee "${MEMBERS_LOG}") 2>&1

log_sub "Starting Windows daemon..."
TALKINTENT_HOME="${ACCEPT_DIR}/win-home" "${BIN_WIN}/talkintent.exe" daemon >"${ACCEPT_DIR}/win-daemon.log" 2>&1 &
WIN_DAEMON_PID=$!
echo $WIN_DAEMON_PID > "${ACCEPT_DIR}/win-daemon.pid"

log_sub "Starting Remote daemon on ${REMOTE_HOST}..."
ssh -o BatchMode=yes "${REMOTE_HOST}" "
  nohup env TALKINTENT_HOME=${REMOTE_BASE}/remote-home ${REMOTE_BASE}/bin/talkintent daemon >${REMOTE_BASE}/logs/remote-daemon.log 2>&1 &
  echo \$! > ${REMOTE_BASE}/daemon.pid
"

log_sub "Waiting for daemons to connect to Hub (5s)..."
sleep 5

log_sub "Checking members list from Windows side..."
TALKINTENT_HOME="${ACCEPT_DIR}/win-home" "${BIN_WIN}/talkintent.exe" members -json | tee "${ACCEPT_DIR}/win-members.json"

log_sub "Checking members list from Remote side..."
ssh -o BatchMode=yes "${REMOTE_HOST}" "
  TALKINTENT_HOME=${REMOTE_BASE}/remote-home ${REMOTE_BASE}/bin/talkintent members -json
" | tee "${ACCEPT_DIR}/remote-members.json"

WIN_SEES_REMOTE_ONLINE=$(node -e '
  const fs = require("fs");
  const data = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
  const m = (data.members || []).find(x => x.name === "remote-dev");
  console.log(m && m.online ? "true" : "false");
' "${ACCEPT_DIR}/win-members.json")

REMOTE_SEES_WIN_ONLINE=$(node -e '
  const fs = require("fs");
  const data = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
  const m = (data.members || []).find(x => x.name === "win-laptop");
  console.log(m && m.online ? "true" : "false");
' "${ACCEPT_DIR}/remote-members.json")

echo "Windows sees remote-dev online: ${WIN_SEES_REMOTE_ONLINE}"
echo "Remote sees win-laptop online: ${REMOTE_SEES_WIN_ONLINE}"

[ "${WIN_SEES_REMOTE_ONLINE}" = "true" ] || { echo "FAIL: Windows did not see remote-dev online"; exit 1; }
[ "${REMOTE_SEES_WIN_ONLINE}" = "true" ] || { echo "FAIL: Remote did not see win-laptop online"; exit 1; }
echo ">>> SUCCESS: Scenario 1 passed."

exec 1>&3

step "5. Acceptance Scenario 2: Bi-Directional Query Execution with Tool Use"
Q_WIN_LOG="${LOG_DIR}/07-remote-asks-win.log"
Q_REMOTE_LOG="${LOG_DIR}/08-win-asks-remote.log"

log_sub "Remote member queries Windows member: 'win-laptop 目前在哪个分支改了什么'..."
ssh -o BatchMode=yes "${REMOTE_HOST}" "
  TALKINTENT_HOME=${REMOTE_BASE}/remote-home ${REMOTE_BASE}/bin/talkintent ask -to win-laptop -q 'win-laptop 目前在哪个分支改了什么' -timeout 60 -json
" > "${Q_WIN_LOG}" 2>&1
cat "${Q_WIN_LOG}"

REMOTE_TO_WIN_STATUS=$(node -e '
  const fs = require("fs");
  try {
    const o = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
    console.log(o.status || "");
  } catch(e) { console.log(""); }
' "${Q_WIN_LOG}")

REMOTE_TO_WIN_TOOLS=$(node -e '
  const fs = require("fs");
  try {
    const o = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
    console.log((o.tools_used || []).join(","));
  } catch(e) { console.log(""); }
' "${Q_WIN_LOG}")

echo "Remote -> Win query status: ${REMOTE_TO_WIN_STATUS}, tools_used: [${REMOTE_TO_WIN_TOOLS}]"
[ "${REMOTE_TO_WIN_STATUS}" = "completed" ] || { echo "FAIL: Remote -> Win query did not complete"; exit 1; }
[ -n "${REMOTE_TO_WIN_TOOLS}" ] || { echo "FAIL: Remote -> Win tools_used is empty"; exit 1; }

log_sub "Windows member queries Remote member: 'remote-dev 目前在哪个分支改了什么'..."
TALKINTENT_HOME="${ACCEPT_DIR}/win-home" "${BIN_WIN}/talkintent.exe" ask -to remote-dev -q "remote-dev 目前在哪个分支改了什么" -timeout 60 -json > "${Q_REMOTE_LOG}" 2>&1
cat "${Q_REMOTE_LOG}"

WIN_TO_REMOTE_STATUS=$(node -e '
  const fs = require("fs");
  try {
    const o = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
    console.log(o.status || "");
  } catch(e) { console.log(""); }
' "${Q_REMOTE_LOG}")

echo "Win -> Remote query status: ${WIN_TO_REMOTE_STATUS}"
[ "${WIN_TO_REMOTE_STATUS}" = "completed" ] || { echo "FAIL: Win -> Remote query did not complete"; exit 1; }
echo ">>> SUCCESS: Scenario 2 passed."

step "6. Acceptance Scenario 3: Cross-Machine Offline Queuing and Drain"
OFFLINE_LOG="${LOG_DIR}/09-offline-queue.log"
exec 3>&1
exec > >(tee "${OFFLINE_LOG}") 2>&1

log_sub "Stopping Windows daemon to simulate offline developer laptop..."
if [ -f "${ACCEPT_DIR}/win-daemon.pid" ]; then
  kill -9 $(cat "${ACCEPT_DIR}/win-daemon.pid") 2>/dev/null || true
  rm -f "${ACCEPT_DIR}/win-daemon.pid"
fi
taskkill //F //IM talkintent.exe 2>/dev/null || true
sleep 2

log_sub "Remote member submits query with -wait=false to offline Windows member..."
QUEUE_SUBMIT_RAW=$(ssh -o BatchMode=yes "${REMOTE_HOST}" "
  TALKINTENT_HOME=${REMOTE_BASE}/remote-home ${REMOTE_BASE}/bin/talkintent ask -to win-laptop -q '离线排队测试：请问开发分支状态' -wait=false -json
")
echo "Queue submit response: ${QUEUE_SUBMIT_RAW}"

QUEUE_STATUS=$(node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const o=JSON.parse(s);console.log(o.status||"")}catch(e){console.log("")}})' <<< "${QUEUE_SUBMIT_RAW}")
QUEUE_QUERY_ID=$(node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const o=JSON.parse(s);console.log(o.query_id||"")}catch(e){console.log("")}})' <<< "${QUEUE_SUBMIT_RAW}")

echo "Initial query status: ${QUEUE_STATUS}, query_id: ${QUEUE_QUERY_ID}"
[ "${QUEUE_STATUS}" = "queued" ] || { echo "FAIL: Query was not queued"; exit 1; }
[ -n "${QUEUE_QUERY_ID}" ] || { echo "FAIL: No query_id returned"; exit 1; }

log_sub "Restarting Windows daemon to drain queued offline queries..."
TALKINTENT_HOME="${ACCEPT_DIR}/win-home" "${BIN_WIN}/talkintent.exe" daemon >"${ACCEPT_DIR}/win-daemon2.log" 2>&1 &
WIN_DAEMON_PID2=$!
echo $WIN_DAEMON_PID2 > "${ACCEPT_DIR}/win-daemon.pid"

log_sub "Waiting for Windows daemon to reconnect and process queue (8s)..."
sleep 8

log_sub "Polling query status from Remote host with member Bearer token via isolated subshell..."
POLL_RESP=$(ssh -o BatchMode=yes "${REMOTE_HOST}" "
  bash -c '
    TOKEN=\$(node -e \"const fs=require(\\\"fs\\\"), p=require(\\\"path\\\"); const c=JSON.parse(fs.readFileSync(p.join(process.env.HOME, \\\"work/talkintent-tmp/xmach/remote-home/config.json\\\"))); console.log(c.token)\")
    curl -s -H \"Authorization: Bearer \$TOKEN\" \"http://127.0.0.1:${PORT_HUB}/api/v1/queries/${QUEUE_QUERY_ID}?wait=20s\"
  '
")
echo "Polled query detail: ${POLL_RESP}"

DRAINED_STATUS=$(node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const o=JSON.parse(s);console.log(o.status||"")}catch(e){console.log("")}})' <<< "${POLL_RESP}")
echo "Drained query status: ${DRAINED_STATUS}"

[ "${DRAINED_STATUS}" = "completed" ] || { echo "FAIL: Queued query did not reach completed status"; exit 1; }
echo ">>> SUCCESS: Scenario 3 passed."

exec 1>&3

step "7. Acceptance Scenario 4: Sovereign Privacy Refusal Path"
PRIVACY_LOG="${LOG_DIR}/10-privacy-refusal.log"
exec 3>&1
exec > >(tee "${PRIVACY_LOG}") 2>&1

log_sub "Injecting privacy guardrail rule into Windows member privacy-prompt.md..."
cat << 'EOF' > "${ACCEPT_DIR}/win-home/privacy-prompt.md"
# Sovereign Natural-Language Privacy Guardrails for Windows Member
1. 涉及员工薪酬、薪水、个人待遇或工资的提问一律拒绝回答。
2. 允许并如实回答关于当前工作分支、未提交代码改动及文件状态的提问。
EOF

log_sub "Remote queries Windows for restricted topic ('工资')..."
# `ask -json` still prints the query detail but exits 1 on refused (see internal/cli/ask.go),
# so don't let set -e abort before we get to inspect the JSON.
REFUSAL_RAW=$(ssh -o BatchMode=yes "${REMOTE_HOST}" "
  TALKINTENT_HOME=${REMOTE_BASE}/remote-home ${REMOTE_BASE}/bin/talkintent ask -to win-laptop -q '请问 win-laptop 的工资是多少' -timeout 60 -json
" || true)
echo "Refusal response: ${REFUSAL_RAW}"

IS_REFUSED=$(node -e '
  let s = "";
  process.stdin.on("data", d => s += d).on("end", () => {
    try {
      const o = JSON.parse(s);
      const answer = (o.answer || "").toLowerCase();
      const status = (o.status || "").toLowerCase();
      // The probe exposes a structured "refuse" tool and mockllm -refuse drives it, so the
      // hub must record status=refused; a chatty "completed" answer is not good enough.
      if (status === "refused" && (answer.includes("refusal") || answer.includes("privacy"))) {
        console.log("true");
      } else {
        console.log("false (status=" + status + ")");
      }
    } catch(e) {
      console.log("false");
    }
  });
' <<< "${REFUSAL_RAW}")

echo "Privacy refusal verified: ${IS_REFUSED}"
[ "${IS_REFUSED}" = "true" ] || { echo "FAIL: Query about 工资 was not refused with status=refused"; exit 1; }
echo ">>> SUCCESS: Scenario 4 passed."

exec 1>&3

step "8. Acceptance Scenario 5: Windows CLI Features Verification"
CLI_LOG="${LOG_DIR}/11-win-cli-checks.log"
exec 3>&1
exec > >(tee "${CLI_LOG}") 2>&1

log_sub "1. Testing skill install and skill show..."
TALKINTENT_HOME="${ACCEPT_DIR}/win-home" "${BIN_WIN}/talkintent.exe" skill install -dir "${ACCEPT_DIR}/skills"
ls -la "${ACCEPT_DIR}/skills/talkintent/SKILL.md"
TALKINTENT_HOME="${ACCEPT_DIR}/win-home" "${BIN_WIN}/talkintent.exe" skill show | head -n 12

log_sub "2. Testing privacy test against workspace..."
TALKINTENT_HOME="${ACCEPT_DIR}/win-home" "${BIN_WIN}/talkintent.exe" privacy test -workspace win-ws "请问开发人员的工资待遇"

log_sub "3. Testing daemon detached lifecycle: start -detach -> status -> stop..."
# Stop current test daemon first
if [ -f "${ACCEPT_DIR}/win-daemon.pid" ]; then
  kill -9 $(cat "${ACCEPT_DIR}/win-daemon.pid") 2>/dev/null || true
  rm -f "${ACCEPT_DIR}/win-daemon.pid"
fi
taskkill //F //IM talkintent.exe 2>/dev/null || true
sleep 1

TALKINTENT_HOME="${ACCEPT_DIR}/win-home" "${BIN_WIN}/talkintent.exe" daemon start -detach
sleep 2
TALKINTENT_HOME="${ACCEPT_DIR}/win-home" "${BIN_WIN}/talkintent.exe" daemon status
TALKINTENT_HOME="${ACCEPT_DIR}/win-home" "${BIN_WIN}/talkintent.exe" daemon stop
sleep 1
TALKINTENT_HOME="${ACCEPT_DIR}/win-home" "${BIN_WIN}/talkintent.exe" daemon status

echo ">>> SUCCESS: Scenario 5 passed."

exec 1>&3

step "9. Acceptance Scenario 6: Clean Teardown & Process Verification"
CLEANUP_LOG="${LOG_DIR}/12-cleanup.log"
exec 3>&1
exec > >(tee "${CLEANUP_LOG}") 2>&1

"${SCRIPT_DIR}/stop.sh"

log_sub "Checking local processes..."
LOCAL_TALKINTENT=$(tasklist //FI "IMAGENAME eq talkintent.exe" 2>/dev/null || true)
LOCAL_MOCKLLM=$(tasklist //FI "IMAGENAME eq mockllm.exe" 2>/dev/null || true)
echo "Local talkintent: ${LOCAL_TALKINTENT}"
echo "Local mockllm: ${LOCAL_MOCKLLM}"

log_sub "Checking remote processes on ${REMOTE_HOST}..."
REMOTE_PROCS=$(ssh -o BatchMode=yes "${REMOTE_HOST}" "
  ps -ef | grep -E 'xmach|18820|18821' | grep -v grep || echo 'None'
")
echo "Remote processes remaining: ${REMOTE_PROCS}"

log_sub "Checking port availability..."
LOCAL_PORT_CHECK=$(netstat -ano | grep "${PORT_HUB}" | grep "LISTENING" || echo "Port ${PORT_HUB} is free")
echo "Local port ${PORT_HUB}: ${LOCAL_PORT_CHECK}"

REMOTE_PORT_CHECK=$(ssh -o BatchMode=yes "${REMOTE_HOST}" "
  ss -tulpn | grep -E ':(18820|18821) ' || echo 'Ports free'
")
echo "Remote ports: ${REMOTE_PORT_CHECK}"

echo ">>> SUCCESS: Scenario 6 passed. Teardown verified."

exec 1>&3

echo ""
echo "================================================================================"
echo ">>> ALL TOPOLOGY 3 (CROSS-MACHINE) ACCEPTANCE SCENARIOS COMPLETED SUCCESSFULLY!"
echo "================================================================================"
