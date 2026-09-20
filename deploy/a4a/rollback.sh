#!/usr/bin/env bash
# Rollback script for TalkIntent Hub deployment on A4A.
# Stops talkintent container, removes nginx config, reloads nginx, leaves existing services untouched.
set -euo pipefail

BACKUP_DIR="/root/talkintent-deploy-20260920-2330"
APP_DIR="/opt/talkintent/app"
NGINX_CONF="/etc/nginx/conf.d/talkintent.conf"

echo "[rollback] Stopping and removing talkintent container..."
if [ -d "${APP_DIR}" ] && [ -f "${APP_DIR}/compose.yaml" ]; then
    (cd "${APP_DIR}" && docker compose down 2>/dev/null || true)
fi
docker rm -f talkintent-hub 2>/dev/null || true
docker rmi talkintent-hub:latest 2>/dev/null || true

echo "[rollback] Removing talkintent nginx configuration..."
if [ -f "${NGINX_CONF}" ]; then
    rm -f "${NGINX_CONF}"
fi

echo "[rollback] Testing nginx configuration..."
nginx -t

echo "[rollback] Reloading nginx..."
systemctl reload nginx

echo "[rollback] Checking AsterGate endpoints..."
curl -sk -o /dev/null -w "aster: %{http_code}\n" https://127.0.0.1:44444/ -H "Host: aster-admin.empeirion.cn" || true

echo "[rollback] Rollback finished successfully."
