#!/usr/bin/env bash
# smoke-llm.sh: verifies whether the LLM endpoint configured for a user or env works
# with 'talkintent llm test'. Credentials are never echoed to stdout.
# Usage: ./smoke-llm.sh [--config PATH | --env PATH] [--bin PATH] [--ca-file PATH] [--tls-server-name NAME]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_PATH="${SCRIPT_DIR}/../../talkintent"
if [ ! -f "${BIN_PATH}" ]; then
    BIN_PATH="/tmp/talkintent-multiuser-bin/talkintent"
fi
if [ ! -f "${BIN_PATH}" ]; then
    BIN_PATH="$(command -v talkintent || true)"
fi

CONFIG_ARG=""
ENV_FILE=""
CA_FILE="/home/skift/.config/talkintent/astergate-leaf.pem"
TLS_SERVER_NAME="aster.empeirion.cn"

while [ $# -gt 0 ]; do
    case "$1" in
        --bin)
            BIN_PATH="$2"
            shift 2
            ;;
        --config)
            CONFIG_ARG="$2"
            shift 2
            ;;
        --env)
            ENV_FILE="$2"
            shift 2
            ;;
        --ca-file)
            CA_FILE="$2"
            shift 2
            ;;
        --tls-server-name)
            TLS_SERVER_NAME="$2"
            shift 2
            ;;
        *)
            echo "Unknown argument: $1" >&2
            echo "Usage: $0 [--config PATH | --env PATH] [--bin PATH] [--ca-file PATH] [--tls-server-name NAME]" >&2
            exit 1
            ;;
    esac
done

if [ -z "${BIN_PATH}" ] || [ ! -x "${BIN_PATH}" ]; then
    echo "ERROR: talkintent binary not found at ${BIN_PATH}. Run 'go build ./cmd/talkintent' first." >&2
    exit 1
fi

echo "=== TalkIntent LLM Smoke Test ==="
echo "Using binary: ${BIN_PATH}"

if [ -n "${ENV_FILE}" ]; then
    if [ ! -f "${ENV_FILE}" ]; then
        echo "ERROR: env file ${ENV_FILE} not found" >&2
        exit 1
    fi
    echo "Testing LLM endpoint from environment file (masking credentials)..."

    TMP_DIR=$(mktemp -d /tmp/talkintent-smoke-llm-XXXXXX)
    chmod 700 "${TMP_DIR}"
    trap 'rm -rf "${TMP_DIR}"' EXIT

    # Execute isolated subshell: configure temporary config.json and test
    (
        set -euo pipefail
        set -a
        # shellcheck disable=SC1090
        source "${ENV_FILE}"
        set +a

        BASE_URL="${ANTHROPIC_BASE_URL:-${TALKINTENT_LLM_URL:-}}"
        API_KEY="${ANTHROPIC_API_KEY:-${TALKINTENT_LLM_KEY:-}}"
        MODEL="${ANTHROPIC_MODEL:-${TALKINTENT_LLM_MODEL:-gemini-3.8-flash-high}}"
        PROVIDER="${TALKINTENT_LLM_PROVIDER:-anthropic}"

        CA_FLAG=""
        EFFECTIVE_CA="${TALKINTENT_LLM_CA_FILE:-${CA_FILE}}"
        if [ -n "${EFFECTIVE_CA}" ] && [ -f "${EFFECTIVE_CA}" ]; then
            CA_FLAG="-ca-file ${EFFECTIVE_CA}"
        fi

        SNI_FLAG=""
        EFFECTIVE_SNI="${TALKINTENT_LLM_TLS_SERVER_NAME:-${TLS_SERVER_NAME}}"
        if [ -n "${EFFECTIVE_SNI}" ]; then
            SNI_FLAG="-tls-server-name ${EFFECTIVE_SNI}"
        fi

        INSECURE_FLAG=""
        if [ "${TALKINTENT_LLM_INSECURE_SKIP_VERIFY:-false}" = "true" ]; then
            INSECURE_FLAG="-insecure-skip-verify"
        fi

        echo "Provider:  ${PROVIDER}"
        echo "Base URL:  ${BASE_URL}"
        echo "Model:     ${MODEL}"
        echo "Key len:   ${#API_KEY}"
        if [ -n "${CA_FLAG}" ]; then
            echo "CA File:   ${EFFECTIVE_CA}"
        fi
        if [ -n "${SNI_FLAG}" ]; then
            echo "SNI:       ${EFFECTIVE_SNI}"
        fi

        "${BIN_PATH}" llm set \
            -config "${TMP_DIR}/config.json" \
            -provider "${PROVIDER}" \
            -base-url "${BASE_URL}" \
            -api-key "${API_KEY}" \
            -model "${MODEL}" \
            ${CA_FLAG} ${SNI_FLAG} ${INSECURE_FLAG} \
            -request-timeout 30 >/dev/null

        "${BIN_PATH}" llm test -config "${TMP_DIR}/config.json"
    )
else
    CFG_FLAG=""
    if [ -n "${CONFIG_ARG}" ]; then
        CFG_FLAG="-config ${CONFIG_ARG}"
    fi
    echo "Testing LLM endpoint configured in local TalkIntent config..."
    "${BIN_PATH}" llm test ${CFG_FLAG}
fi

echo "✓ LLM smoke test passed successfully."
