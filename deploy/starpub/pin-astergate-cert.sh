#!/usr/bin/env bash
# Pin the AsterGate leaf certificate as a trust anchor for the probe LLM client.
# The gateway serves only its leaf (issuer "AsterGate Private CA" is not distributed),
# so we fetch the leaf over TLS and write it as a CA file. Go's x509 accepts a leaf that
# is itself in the root pool; this is safer than insecure_skip_verify for a fixed endpoint.
# usage: pin-astergate-cert.sh HOST:PORT OUT.pem
set -euo pipefail
hp=${1:?host:port}; out=${2:?out.pem}
tmp=$(mktemp)
echo | openssl s_client -connect "$hp" -showcerts 2>/dev/null | awk '/BEGIN CERT/,/END CERT/' > "$tmp"
[ -s "$tmp" ] || { echo "no certificate received from $hp" >&2; exit 1; }
awk '/BEGIN CERT/{c++} c==1' "$tmp" > "$out"
rm -f "$tmp"
echo "pinned: $(openssl x509 -in "$out" -noout -subject -enddate | tr '\n' ' ')"
