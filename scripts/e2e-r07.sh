#!/usr/bin/env bash
# R07 end-to-end check: real binary, real SQLite file, explicit config, bearer
# auth, readiness, read, write, idempotency, SSE snapshot/live, restart.
set -euo pipefail
cd "$(dirname "$0")/.."

TMP="$(mktemp -d)"
PID=""
PYTHON="python3"
if ! "$PYTHON" -c 'import sys' >/dev/null 2>&1; then
  PYTHON="python"
fi
host_path() {
  if command -v cygpath >/dev/null 2>&1; then
    cygpath -m "$1"
  else
    printf '%s' "$1"
  fi
}
cleanup() {
  if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
    kill -TERM "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT

PORT="$("$PYTHON" - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)"
WRITE_TOKEN="e2e-write-token"
READ_TOKEN="e2e-read-token"
WRITE_HASH="$(printf '%s' "$WRITE_TOKEN" | sha256sum | cut -d' ' -f1)"
READ_HASH="$(printf '%s' "$READ_TOKEN" | sha256sum | cut -d' ' -f1)"
BASE="http://127.0.0.1:${PORT}"
SCOPE_PATH="/v1/worlds/rencrow-home/timelines/timeline-1"

STORE_PATH="$(host_path "$TMP/world.db")"
CONFIG_PATH="$(host_path "$TMP/config.json")"
cat >"$CONFIG_PATH" <<EOF
{
  "listen": "127.0.0.1:${PORT}",
  "store_path": "${STORE_PATH}",
  "scope": {"world_id": "rencrow-home", "timeline_id": "timeline-1", "timeline_epoch": "epoch-e2e"},
  "tokens": [
    {"id": "writer", "sha256": "${WRITE_HASH}", "scopes": ["read", "write", "stream"]},
    {"id": "reader", "sha256": "${READ_HASH}", "scopes": ["read"]}
  ],
  "page": {"max_events": 500, "max_bytes": 2097152},
  "sse": {"heartbeat_seconds": 2, "poll_millis": 50},
  "allow_probe_action": true,
  "initial_state": {"actors": {"mio": {"room_id": "1f-living"}}, "resources": {"chair": 1}}
}
EOF

echo "== build =="
BIN="$TMP/rencrow-world"
if [ "$(go env GOOS)" = "windows" ]; then
  BIN="$BIN.exe"
fi
go build -o "$BIN" ./cmd/rencrow-world

start_server() {
  "$BIN" serve --config "$CONFIG_PATH" >"$TMP/stdout.log" 2>"$TMP/stderr.log" &
  PID=$!
  for _ in $(seq 1 100); do
    code="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/health/live" || true)"
    if [ "$code" = "200" ]; then
      return 0
    fi
    sleep 0.1
  done
  echo "server did not become live" >&2
  cat "$TMP/stderr.log" >&2
  return 1
}

json_get() {
  "$PYTHON" -c 'import json,sys; v=json.load(sys.stdin); print(eval(sys.argv[1], {"v": v}))' "$1"
}

echo "== readiness and auth =="
start_server
[ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/health/live")" = "200" ]
[ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/health/ready")" = "401" ]
[ "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $READ_TOKEN" "$BASE/health/ready")" = "200" ]

echo "== snapshot and command =="
curl -sS -H "Authorization: Bearer $READ_TOKEN" "$BASE$SCOPE_PATH/snapshot" | grep -q '"state_seq":0'
CMD1='{"action":"debug.noop","payload":{"hello":"e2e"}}'
FIRST="$(curl -sS -X POST -H "Authorization: Bearer $WRITE_TOKEN" -H "Idempotency-Key: e2e-key-1" -H 'Content-Type: application/json' --data "$CMD1" "$BASE$SCOPE_PATH/commands")"
[ "$(printf '%s' "$FIRST" | json_get 'v["receipt"]["status"]')" = "committed" ]
RECEIPT="$(printf '%s' "$FIRST" | json_get 'v["receipt"]["receipt_id"]')"
AGAIN="$(curl -sS -X POST -H "Authorization: Bearer $WRITE_TOKEN" -H "Idempotency-Key: e2e-key-1" -H 'Content-Type: application/json' --data "$CMD1" "$BASE$SCOPE_PATH/commands")"
[ "$(printf '%s' "$AGAIN" | json_get 'v["duplicate"]')" = "True" ]
[ "$(printf '%s' "$AGAIN" | json_get 'v["receipt"]["receipt_id"]')" = "$RECEIPT" ]
CONFLICT_CODE="$(curl -s -o "$TMP/conflict.json" -w '%{http_code}' -X POST -H "Authorization: Bearer $WRITE_TOKEN" -H "Idempotency-Key: e2e-key-1" -H 'Content-Type: application/json' --data '{"action":"debug.noop","payload":{"hello":"other"}}' "$BASE$SCOPE_PATH/commands")"
[ "$CONFLICT_CODE" = "409" ]
grep -q '"code":"idempotency_conflict"' "$TMP/conflict.json"
[ "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $READ_TOKEN" -H "Idempotency-Key: e2e-key-2" -H 'Content-Type: application/json' --data "$CMD1" "$BASE$SCOPE_PATH/commands")" = "403" ]
[ "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $READ_TOKEN" "$BASE/v1/worlds/other/timelines/timeline-1/snapshot")" = "403" ]

echo "== reads =="
curl -sS -H "Authorization: Bearer $READ_TOKEN" "$BASE$SCOPE_PATH/snapshot" | grep -q '"state_seq":1'
curl -sS -H "Authorization: Bearer $READ_TOKEN" "$BASE$SCOPE_PATH/events?after_seq=0&limit=10" | grep -q '"kind":"debug.noop"'
curl -sS -H "Authorization: Bearer $READ_TOKEN" "$BASE$SCOPE_PATH/receipts/$RECEIPT" | grep -q "\"receipt_id\":\"$RECEIPT\""

echo "== SSE snapshot =="
timeout 5 curl -sN -H "Authorization: Bearer $WRITE_TOKEN" "$BASE$SCOPE_PATH/stream" >"$TMP/sse-snapshot.txt" || true
grep -q 'event: snapshot' "$TMP/sse-snapshot.txt"
grep -q '"state_seq":1' "$TMP/sse-snapshot.txt"

echo "== SSE live event after Last-Event-ID =="
timeout 6 curl -sN -H "Authorization: Bearer $WRITE_TOKEN" -H "Last-Event-ID: 1" "$BASE$SCOPE_PATH/stream" >"$TMP/sse-live.txt" &
SSE_PID=$!
sleep 0.5
curl -sS -X POST -H "Authorization: Bearer $WRITE_TOKEN" -H "Idempotency-Key: e2e-key-3" -H 'Content-Type: application/json' --data "$CMD1" "$BASE$SCOPE_PATH/commands" >/dev/null
wait "$SSE_PID" || true
grep -q 'event: world.event' "$TMP/sse-live.txt"
grep -q '"seq":2' "$TMP/sse-live.txt"
if grep -q 'event: snapshot' "$TMP/sse-live.txt"; then
  echo "unexpected snapshot on resume" >&2
  exit 1
fi

echo "== restart persistence =="
kill -TERM "$PID"
wait "$PID" || true
PID=""
start_server
curl -sS -H "Authorization: Bearer $READ_TOKEN" "$BASE$SCOPE_PATH/snapshot" | grep -q '"state_seq":2'

echo "== no secrets in logs =="
if grep -q "$WRITE_TOKEN" "$TMP/stderr.log" "$TMP/stdout.log"; then
  echo "token leaked to logs" >&2
  exit 1
fi

echo "E2E R07 PASS"
