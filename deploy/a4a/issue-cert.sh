#!/usr/bin/env bash
# Issue TLS certificate for talkintent.empeirion.cn using AsterGate Private CA.
# Safety rules:
# - ca.key is strictly read by openssl as a signing key; never copied, printed, or moved.
# - Generated server private key is stored at /opt/talkintent/tls/talkintent.key with 0600 mode.
# - Output certificate is saved at /opt/talkintent/tls/talkintent.crt.
set -euo pipefail

CA_CRT="/home/ubuntu/astergate-deploy/certs/ca.crt"
CA_KEY="/home/ubuntu/astergate-deploy/certs/ca.key"
OUT_DIR="/opt/talkintent/tls"
DOMAIN="talkintent.empeirion.cn"
VALID_DAYS=825

echo "[issue-cert] Creating TLS directory at ${OUT_DIR}..."
mkdir -p "${OUT_DIR}"
chmod 0750 "${OUT_DIR}"

TMP_DIR="$(mktemp -d /tmp/ti-cert-XXXXXX)"
trap 'rm -rf "${TMP_DIR}"' EXIT

CSR="${TMP_DIR}/${DOMAIN}.csr"
EXT="${TMP_DIR}/openssl-san.cnf"

cat > "${EXT}" <<EOF
basicConstraints = critical, CA:FALSE
keyUsage = critical, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = DNS:${DOMAIN}
EOF

echo "[issue-cert] Generating 2048-bit RSA private key and CSR for ${DOMAIN}..."
openssl req -new -newkey rsa:2048 -nodes \
    -keyout "${OUT_DIR}/talkintent.key" \
    -out "${CSR}" \
    -subj "/CN=${DOMAIN}/O=TalkIntent"

chmod 0600 "${OUT_DIR}/talkintent.key"

echo "[issue-cert] Signing server certificate using CA key (${VALID_DAYS} days)..."
openssl x509 -req \
    -in "${CSR}" \
    -CA "${CA_CRT}" \
    -CAkey "${CA_KEY}" \
    -CAcreateserial \
    -out "${OUT_DIR}/talkintent.crt" \
    -days "${VALID_DAYS}" \
    -sha256 \
    -extfile "${EXT}"

chmod 0644 "${OUT_DIR}/talkintent.crt"

echo "[issue-cert] Verifying certificate against CA..."
openssl verify -CAfile "${CA_CRT}" "${OUT_DIR}/talkintent.crt"
echo "[issue-cert] Certificate successfully issued to ${OUT_DIR}/talkintent.crt"
