#!/usr/bin/env bash
# deploy/xmachine/stop.sh: Teardown script for Topology 3 (cross-machine)
# Cleans up local Windows processes, local SSH tunnel, and remote processes on starpub-docker.
set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ACCEPT_DIR="/c/tmp/ti-accept/xmach"
REMOTE_HOST="starpub-docker"
PORT_HUB="18820"
PORT_REMOTE_LLM="18821"
PORT_LOCAL_LLM="18822"

echo "=== [stop.sh] Tearing down Topology 3 (Cross-Machine) processes ==="

# 1. Kill local processes via PID files if present
echo "1. Terminating local processes via tracked PIDs..."
if [ -f "${ACCEPT_DIR}/tunnel.pid" ]; then
  TUNNEL_PID=$(cat "${ACCEPT_DIR}/tunnel.pid" 2>/dev/null || true)
  if [ -n "${TUNNEL_PID}" ]; then
    kill -9 "${TUNNEL_PID}" 2>/dev/null || true
  fi
  rm -f "${ACCEPT_DIR}/tunnel.pid"
fi

if [ -f "${ACCEPT_DIR}/mockllm.pid" ]; then
  MOCK_PID=$(cat "${ACCEPT_DIR}/mockllm.pid" 2>/dev/null || true)
  if [ -n "${MOCK_PID}" ]; then
    kill -9 "${MOCK_PID}" 2>/dev/null || true
  fi
  rm -f "${ACCEPT_DIR}/mockllm.pid"
fi

if [ -f "${ACCEPT_DIR}/win-daemon.pid" ]; then
  DAEMON_PID=$(cat "${ACCEPT_DIR}/win-daemon.pid" 2>/dev/null || true)
  if [ -n "${DAEMON_PID}" ]; then
    kill -9 "${DAEMON_PID}" 2>/dev/null || true
  fi
  rm -f "${ACCEPT_DIR}/win-daemon.pid"
fi

# 2. Terminate any lingering local listeners on port PORT_HUB
powershell.exe -NoProfile -Command "
  try {
    \$conns = Get-NetTCPConnection -LocalPort ${PORT_HUB} -State Listen -ErrorAction Stop
    foreach (\$c in \$conns) {
      if (\$c.OwningProcess -gt 0) {
        Stop-Process -Id \$c.OwningProcess -Force -ErrorAction SilentlyContinue
      }
    }
  } catch {}
  try {
    Get-CimInstance Win32_Process -Filter \"Name = 'ssh.exe'\" | Where-Object { \$_.CommandLine -like '*${PORT_HUB}*' } | ForEach-Object { Stop-Process -Id \$_.ProcessId -Force -ErrorAction SilentlyContinue }
  } catch {}
" 2>/dev/null || true

# 3. Terminate local talkintent and mockllm executables
echo "2. Terminating local talkintent.exe and mockllm.exe..."
taskkill //F //IM talkintent.exe 2>/dev/null || true
taskkill //F //IM mockllm.exe 2>/dev/null || true

# 4. Clean up remote host processes under ~/work/talkintent-tmp/xmach
echo "3. Terminating remote processes on ${REMOTE_HOST}..."
ssh -o BatchMode=yes "${REMOTE_HOST}" "
  # Kill via PID files if present
  for pf in ~/work/talkintent-tmp/xmach/*.pid ~/work/talkintent-tmp/xmach/*/*.pid; do
    if [ -f \"\$pf\" ]; then
      pid=\$(cat \"\$pf\" 2>/dev/null)
      if [ -n \"\$pid\" ]; then
        kill -9 \"\$pid\" 2>/dev/null || true
      fi
      rm -f \"\$pf\"
    fi
  done

  # Target specific xmach processes if any remain
  pkill -9 -f 'xmach.*talkintent' 2>/dev/null || true
  pkill -9 -f 'xmach.*mockllm' 2>/dev/null || true
  pkill -9 -f '127.0.0.1:${PORT_HUB}' 2>/dev/null || true
  pkill -9 -f '127.0.0.1:${PORT_REMOTE_LLM}' 2>/dev/null || true

  echo 'Remote processes terminated.'
" 2>/dev/null || true

sleep 1

# 5. Verification
echo "4. Verifying port availability..."
if netstat -ano | grep "${PORT_HUB}" | grep -q "LISTENING"; then
  echo "WARNING: Local port ${PORT_HUB} is still in LISTENING state:"
  netstat -ano | grep "${PORT_HUB}" | grep "LISTENING"
else
  echo "Local port ${PORT_HUB} is free."
fi

REMOTE_CHECK=$(ssh -o BatchMode=yes "${REMOTE_HOST}" "ss -tulpn | grep -E ':(18820|18821) ' || echo 'FREE'")
echo "Remote ports check: ${REMOTE_CHECK}"

echo "=== [stop.sh] Teardown complete. ==="
