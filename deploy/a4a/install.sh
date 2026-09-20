#!/usr/bin/env bash
# TalkIntent Hub deployment script for A4A production host.
# Fully automated, idempotent, and adheres to all production safety red lines.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKUP_DIR="/root/talkintent-deploy-20260920-2330"
OPT_TI="/opt/talkintent"
APP_DIR="${OPT_TI}/app"
DATA_DIR="${OPT_TI}/data"
TLS_DIR="${OPT_TI}/tls"
NGINX_CONF_SRC="${SCRIPT_DIR}/talkintent.conf"
NGINX_CONF_DST="/etc/nginx/conf.d/talkintent.conf"
CA_CRT="/home/ubuntu/astergate-deploy/certs/ca.crt"

echo "=== [Step 0] Environment verification ==="
if [ "$(id -u)" -ne 0 ]; then
    echo "ERROR: Must run as root." >&2
    exit 1
fi

echo "=== [Step 1] Creating backup directory and archiving nginx ==="
mkdir -p "${BACKUP_DIR}"
chmod 0700 "${BACKUP_DIR}"

if [ ! -f "${BACKUP_DIR}/nginx-pre-deploy.tar.gz" ]; then
    echo "Archiving /etc/nginx to ${BACKUP_DIR}/nginx-pre-deploy.tar.gz..."
    tar -czf "${BACKUP_DIR}/nginx-pre-deploy.tar.gz" -C /etc nginx
else
    echo "Existing nginx backup found at ${BACKUP_DIR}/nginx-pre-deploy.tar.gz."
fi

echo "Installing rollback script to ${BACKUP_DIR}/rollback.sh..."
cp "${SCRIPT_DIR}/rollback.sh" "${BACKUP_DIR}/rollback.sh"
chmod 0700 "${BACKUP_DIR}/rollback.sh"

echo "=== [Step 2] Pre-deploy AsterGate health check ==="
aster_pre=$(curl -sk -o /dev/null -w "%{http_code}" --resolve aster.empeirion.cn:44444:127.0.0.1 https://aster.empeirion.cn:44444/v1/models)
aster_admin_pre=$(curl -sk -o /dev/null -w "%{http_code}" --resolve aster-admin.empeirion.cn:44444:127.0.0.1 https://aster-admin.empeirion.cn:44444/)
echo "Pre-check aster /v1/models: ${aster_pre} (expected 401)"
echo "Pre-check aster-admin: ${aster_admin_pre} (expected 200)"

echo "=== [Step 3] Preparing directory structure ==="
mkdir -p "${APP_DIR}" "${DATA_DIR}" "${TLS_DIR}"
chmod 0755 "${OPT_TI}"
chmod 0750 "${APP_DIR}" "${TLS_DIR}"
chmod 0700 "${DATA_DIR}"
chown -R 10001:10001 "${DATA_DIR}"

echo "=== [Step 4] Issuing TLS Certificate for talkintent.empeirion.cn ==="
bash "${SCRIPT_DIR}/issue-cert.sh"

echo "=== [Step 5] Preparing Docker build context ==="
cp "${SCRIPT_DIR}/Dockerfile" "${APP_DIR}/Dockerfile"
cp "${SCRIPT_DIR}/compose.yaml" "${APP_DIR}/compose.yaml"
if [ -f "${SCRIPT_DIR}/talkintent" ]; then
    cp "${SCRIPT_DIR}/talkintent" "${APP_DIR}/talkintent"
fi
chmod 0755 "${APP_DIR}/talkintent"

# Create minimal passwd & group for non-root runtime user 10001:10001
cat > "${APP_DIR}/passwd" <<'EOF'
talkintent:x:10001:10001:talkintent:/data:/bin/false
EOF
cat > "${APP_DIR}/group" <<'EOF'
talkintent:x:10001:
EOF

# Build combined CA bundle (system root CAs + AsterGate private CA)
cat /etc/ssl/certs/ca-certificates.crt "${CA_CRT}" > "${APP_DIR}/ca-certificates.crt"

echo "=== [Step 6] Building Docker image (FROM scratch, zero pull) ==="
(cd "${APP_DIR}" && docker build -t talkintent-hub:latest .)

echo "=== [Step 7] Launching Docker container ==="
(cd "${APP_DIR}" && docker compose up -d)

echo "Waiting for Hub to listen on 127.0.0.1:18800..."
ready=0
for i in {1..30}; do
    if curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:18800/web | grep -q "302"; then
        ready=1
        echo "Hub service is up and responding."
        break
    fi
    sleep 1
done
if [ "${ready}" -ne 1 ]; then
    echo "ERROR: Hub container failed to start on 127.0.0.1:18800" >&2
    (cd "${APP_DIR}" && docker compose logs)
    exit 1
fi

echo "=== [Step 8] Configuring Nginx reverse proxy ==="
cp "${NGINX_CONF_SRC}" "${NGINX_CONF_DST}"
echo "Testing nginx configuration..."
nginx -t
echo "Reloading nginx (systemctl reload nginx)..."
systemctl reload nginx

echo "=== [Step 9] Post-deploy verification ==="
echo "Testing HTTP 301 redirect on port 80..."
http_code=$(curl -s -o /dev/null -w "%{http_code}" -H "Host: talkintent.empeirion.cn" http://127.0.0.1/)
echo "Port 80 redirect status: ${http_code} (expected 301)"

echo "Testing HTTPS endpoint on port 443 with AsterGate CA..."
ti_https_code=$(curl -s -o /dev/null -w "%{http_code}" --cacert "${CA_CRT}" --resolve talkintent.empeirion.cn:443:127.0.0.1 https://talkintent.empeirion.cn/web)
echo "HTTPS /web status: ${ti_https_code} (expected 200)"

ti_body_check=$(curl -s --cacert "${CA_CRT}" --resolve talkintent.empeirion.cn:443:127.0.0.1 https://talkintent.empeirion.cn/web | grep -o "TalkIntent" | head -n 1)
echo "HTTPS /web body keyword: '${ti_body_check}' (expected 'TalkIntent')"

echo "=== [Step 10] Checking AsterGate endpoints after nginx reload ==="
aster_post=$(curl -sk -o /dev/null -w "%{http_code}" --resolve aster.empeirion.cn:44444:127.0.0.1 https://aster.empeirion.cn:44444/v1/models)
aster_admin_post=$(curl -sk -o /dev/null -w "%{http_code}" --resolve aster-admin.empeirion.cn:44444:127.0.0.1 https://aster-admin.empeirion.cn:44444/)
echo "Post-check aster /v1/models: ${aster_post} (expected 401)"
echo "Post-check aster-admin: ${aster_admin_post} (expected 200)"

echo "=== [Step 11] Checking admin token generation ==="
if [ -f "${DATA_DIR}/admin.token" ]; then
    token_len=$(wc -c < "${DATA_DIR}/admin.token" | tr -d ' ')
    echo "Admin token exists at ${DATA_DIR}/admin.token"
    echo "Admin token length: ${token_len} bytes (secret content strictly not logged)"
else
    echo "ERROR: ${DATA_DIR}/admin.token was not generated" >&2
    exit 1
fi

echo "=== Installation complete ==="
