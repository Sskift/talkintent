#!/usr/bin/env bash
# stop.sh: Tears down all processes spawned by run-multiuser.sh (Hub and per-user daemons).
# Usage: ./stop.sh [PID_DIR]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PID_DIR="${1:-/tmp/talkintent-multiuser-pids}"

echo "=== Tearing Down TalkIntent Multi-User Deployment ==="

stop_pid() {
    local pid_file="$1"
    local name="$2"
    if [ -f "${pid_file}" ]; then
        local pid
        pid=$(cat "${pid_file}" | tr -d '[:space:]')
        if [ -n "${pid}" ]; then
            echo "Stopping ${name} (PID ${pid})..."
            sudo kill "${pid}" 2>/dev/null || true
            for i in $(seq 1 10); do
                if ! sudo kill -0 "${pid}" 2>/dev/null; then
                    break
                fi
                sleep 0.5
            done
            if sudo kill -0 "${pid}" 2>/dev/null; then
                echo "Force killing ${name} (PID ${pid})..."
                sudo kill -9 "${pid}" 2>/dev/null || true
            fi
        fi
        rm -f "${pid_file}"
    fi
}

# Stop per-user daemons
for u in alice bob carol; do
    stop_pid "${PID_DIR}/daemon-ti-${u}.pid" "daemon for ti-${u}"
done

# Stop Hub
stop_pid "${PID_DIR}/hub.pid" "Hub server"

# Extra safety: sweep lingering talkintent processes for test users
for u in ti-alice ti-bob ti-carol; do
    if id "${u}" >/dev/null 2>&1; then
        sudo pkill -u "${u}" -f "talkintent" 2>/dev/null || true
    fi
done

echo "Teardown complete."
