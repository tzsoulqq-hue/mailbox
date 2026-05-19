#!/usr/bin/env bash
set -Eeuo pipefail

pids=()

cleanup() {
  local status=$?
  if ((${#pids[@]} > 0)); then
    kill "${pids[@]}" >/dev/null 2>&1 || true
    wait "${pids[@]}" >/dev/null 2>&1 || true
  fi
  exit "$status"
}
trap cleanup EXIT INT TERM

mailbox_pg_dsn=${MAILBOX_PG_DSN:-}
if [[ -z "$mailbox_pg_dsn" ]]; then
  echo "MAILBOX_PG_DSN is required" >&2
  exit 1
fi

outlook_addr=${MAILBOX_OUTLOOK_INTERNAL_ADDR:-127.0.0.1:50052}

(
  export LISTEN_ADDR="$outlook_addr"
  export PG_DSN="$mailbox_pg_dsn"
  exec /app/bin/outlook-mailbox
) &
pids+=("$!")

export LISTEN_ADDR=${MAILBOX_LISTEN_ADDR:-:50051}
export MAILBOX_PG_DSN="$mailbox_pg_dsn"
export MAILBOX_EMAIL_PROVIDER_ADDR="$outlook_addr"
exec /app/bin/mailbox
