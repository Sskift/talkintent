#!/usr/bin/env bash
# Provision N simulated "developer endpoints" on a Docker-less Linux host as separate
# Linux users (ti-alice, ti-bob, ti-carol). Each gets a home dir, a git workspace with
# uncommitted changes, and a natural-language privacy prompt. Idempotent.
# usage: sudo ./provision-users.sh [alice bob carol]
set -euo pipefail
users=("$@"); [ ${#users[@]} -gt 0 ] || users=(alice bob carol)
for u in "${users[@]}"; do
  login="ti-$u"
  if ! id "$login" >/dev/null 2>&1; then
    useradd -m -s /bin/bash "$login"
    echo "created $login"
  fi
  home=$(getent passwd "$login" | cut -d: -f6)
  install -d -o "$login" -g "$login" -m 700 "$home/.talkintent" "$home/work"
done
echo "provisioned: ${users[*]/#/ti-}"
