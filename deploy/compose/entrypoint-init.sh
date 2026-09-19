#!/usr/bin/env bash
# Init service to create onboarding invite codes for alice, bob, charlie
set -euo pipefail

HUB_URL="${TALKINTENT_HUB_URL:-http://hub:8080}"
INVITE_DIR="/shared/invites"
TOKEN_FILE="/shared/hub-data/admin.token"

mkdir -p "${INVITE_DIR}"
echo "[init] Waiting for Hub admin token at ${TOKEN_FILE}..."

while [ ! -f "${TOKEN_FILE}" ]; do
    sleep 0.5
done

ADMIN_TOKEN=$(cat "${TOKEN_FILE}" | tr -d '[:space:]')
echo "[init] Admin token detected. Waiting for Hub API..."

until curl -s -f -H "Authorization: Bearer ${ADMIN_TOKEN}" "${HUB_URL}/api/v1/members" >/dev/null 2>&1; do
    sleep 0.5
done

echo "[init] Hub API ready. Generating invite codes..."

for name in alice bob charlie; do
    resp=$(curl -s -f -X POST "${HUB_URL}/api/v1/admin/invites" \
        -H "Authorization: Bearer ${ADMIN_TOKEN}" \
        -H "Content-Type: application/json" \
        -d "{\"target_name\": \"${name}\", \"expires_in_hours\": 24}")
    code=$(echo "${resp}" | jq -r '.code // .invite_code')
    if [ -z "${code}" ] || [ "${code}" = "null" ]; then
        echo "[init] Failed to generate invite for ${name}: ${resp}" >&2
        exit 1
    fi
    echo "${code}" > "${INVITE_DIR}/${name}.invite"
    echo "[init] Created invite for ${name}"
done

echo "[init] All invites generated successfully."
