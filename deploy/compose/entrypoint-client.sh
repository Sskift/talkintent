#!/usr/bin/env bash
# Entrypoint for TalkIntent client containers in Docker Compose.
# Seeds git repository workspace + privacy rules, waits for invite, pairs with Hub,
# sets up LLM configuration, and starts the TalkIntent daemon.
set -euo pipefail

NAME="${MEMBER_NAME:-alice}"
MACHINE="${MEMBER_MACHINE:-machine-${NAME}}"
BRANCH="${GIT_BRANCH:-main}"
PRIVACY_RULE="${PRIVACY_RULE:-}"
INVITE_FILE="/shared/invites/${NAME}.invite"
HUB_URL="${TALKINTENT_HUB_URL:-http://hub:8080}"
LLM_BASE_URL="${LLM_BASE_URL:-http://mockllm:8080/v1}"
WORKSPACE_DIR="/home/talkintent/work/demo"

echo "[client-${NAME}] Starting client initialization..."

# 1. Seed Git Repository
mkdir -p "${WORKSPACE_DIR}"
cd "${WORKSPACE_DIR}"

if [ ! -d .git ]; then
    echo "[client-${NAME}] Seeding Git repository in ${WORKSPACE_DIR} on branch ${BRANCH}..."
    git config --global user.name "${NAME}"
    git config --global user.email "${NAME}@example.com"
    git config --global init.defaultBranch main

    git init
    echo "# ${NAME}'s Project" > README.md
    echo "package main" > main.go
    git add .
    git commit -m "initial commit"
    git checkout -b "${BRANCH}"

    # Create uncommitted changes
    echo "// Work in progress by ${NAME} on ${BRANCH}" >> main.go
    echo "uncommitted feature stub" > "feature_${NAME}.txt"

    # Privacy prompt
    if [ -n "${PRIVACY_RULE}" ]; then
        mkdir -p .talkintent
        echo "${PRIVACY_RULE}" > .talkintent/privacy-prompt.md
        echo "[client-${NAME}] Configured privacy rule in .talkintent/privacy-prompt.md"
    fi
fi

# Check if already paired and configured (e.g. after container restart)
if [ -f "/home/talkintent/.talkintent/config.json" ]; then
    echo "[client-${NAME}] Client already configured. Starting TalkIntent client daemon directly..."
    exec talkintent daemon
fi

# 2. Wait for Hub and Invite code from init job
echo "[client-${NAME}] Waiting for invite code at ${INVITE_FILE}..."
while [ ! -f "${INVITE_FILE}" ]; do
    sleep 0.5
done

INVITE_CODE=$(cat "${INVITE_FILE}" | tr -d '[:space:]')
echo "[client-${NAME}] Received invite code."

# 3. Pair with Hub
echo "[client-${NAME}] Pairing with Hub at ${HUB_URL}..."
talkintent pair -hub "${HUB_URL}" -code "${INVITE_CODE}" -name "${MACHINE}"

# 4. Register Workspace
talkintent workspace add "${WORKSPACE_DIR}" -name "demo"

# 5. Configure LLM
echo "[client-${NAME}] Configuring LLM endpoint at ${LLM_BASE_URL}..."
talkintent llm set \
    -provider openai \
    -base-url "${LLM_BASE_URL}" \
    -model gpt-4o \
    -api-key "mock-key-${NAME}" \
    -request-timeout 15 \
    -max-steps 10

# 6. Start Client Daemon in foreground
echo "[client-${NAME}] Starting TalkIntent client daemon..."
exec talkintent daemon
