#!/usr/bin/env bash
# R08 end-to-end check: real binary, real SQLite file, authenticated CORE
# projection inbox. Verifies source revision ordering, digest idempotency and
# conflict, independent LifePolicy column, restart persistence, epoch
# reconciliation, old-epoch rejection, actor binding, and no Command effect.
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
WRITE_TOKEN="e2e-r08-write-token"
READ_TOKEN="e2e-r08-read-token"
CORE_TOKEN="e2e-r08-core-token"
CORE_RECONCILE_TOKEN="e2e-r08-core-reconcile-token"
WRITE_HASH="$(printf '%s' "$WRITE_TOKEN" | sha256sum | cut -d' ' -f1)"
READ_HASH="$(printf '%s' "$READ_TOKEN" | sha256sum | cut -d' ' -f1)"
CORE_HASH="$(printf '%s' "$CORE_TOKEN" | sha256sum | cut -d' ' -f1)"
CORE_RECONCILE_HASH="$(printf '%s' "$CORE_RECONCILE_TOKEN" | sha256sum | cut -d' ' -f1)"
BASE="http://127.0.0.1:${PORT}"
SCOPE_PATH="/v1/worlds/rencrow-home/timelines/timeline-1"
PROJECTION_PATH="$SCOPE_PATH/core-projections"

STORE_PATH="$(host_path "$TMP/world.db")"
CONFIG_PATH="$(host_path "$TMP/config.json")"
cat >"$CONFIG_PATH" <<EOF
{
  "listen": "127.0.0.1:${PORT}",
  "store_path": "${STORE_PATH}",
  "scope": {"world_id": "rencrow-home", "timeline_id": "timeline-1", "timeline_epoch": "epoch-e2e"},
  "tokens": [
    {"id": "writer", "sha256": "${WRITE_HASH}", "scopes": ["read", "write", "stream"]},
    {"id": "reader", "sha256": "${READ_HASH}", "scopes": ["read"]},
    {"id": "core", "publisher_id": "core-publisher", "sha256": "${CORE_HASH}", "scopes": ["projection"]},
    {"id": "core-reconcile", "publisher_id": "core-publisher", "sha256": "${CORE_RECONCILE_HASH}", "scopes": ["projection_reconcile"]}
  ],
  "core_projection_actors": [{"actor_id": "mio", "actor_instance_id": "core-instance-mio"}],
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

stop_server() {
  if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
    kill -TERM "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  PID=""
}

json_get() {
  "$PYTHON" -c 'import json,sys; v=json.load(sys.stdin); print(eval(sys.argv[1], {"v": v}))' "$1"
}

make_work_set() {
  local out="$1" epoch="$2" rev="$3" active="$4" actor="${5:-mio}"
  "$PYTHON" - "$out" "$epoch" "$rev" "$active" "$actor" <<'PY'
import json, sys
out, epoch, rev, active, actor = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4], sys.argv[5]
instance = "core-instance-" + actor
if active == "running":
    work = [{"task_id": "task-1", "run_id": "run-1", "execution_state": "running",
             "work_class": "assigned_work", "requires_house_presence": True,
             "classification_reason_code": "assigned"}]
elif active == "queued":
    work = [{"task_id": "task-2", "execution_state": "queued",
             "work_class": "assigned_work", "requires_house_presence": False,
             "classification_reason_code": "assigned"}]
elif active == "other":
    work = [{"task_id": "task-9", "execution_state": "queued",
             "work_class": "assigned_work", "requires_house_presence": False,
             "classification_reason_code": "assigned"}]
elif active == "bad_running":
    work = [{"task_id": "task-1", "execution_state": "running",
             "work_class": "assigned_work", "requires_house_presence": True,
             "classification_reason_code": "assigned"}]
else:
    work = []
body = {
    "contract_version": "world.contracts/v1",
    "record_type": "CoreProjectionEnvelope",
    "world_id": "rencrow-home",
    "timeline_id": "timeline-1",
    "timeline_epoch": "epoch-e2e",
    "actor_instance_id": instance,
    "projection_kind": "full_actor_work_set",
    "payload": {
        "contract_version": "rencrow.world.work-set/v2",
        "world_id": "rencrow-home",
        "actor_id": actor,
        "source_owner": "CORE",
        "source_epoch": epoch,
        "source_revision": rev,
        "valid_until": "2099-01-01T00:00:00Z",
        "active_work": work,
    },
}
open(out, "w", encoding="utf-8").write(json.dumps(body))
PY
}

make_life_policy() {
  local out="$1" epoch="$2" rev="$3"
  "$PYTHON" - "$out" "$epoch" "$rev" <<'PY'
import json, sys
out, epoch, rev = sys.argv[1], sys.argv[2], int(sys.argv[3])
body = {
    "contract_version": "world.contracts/v1",
    "record_type": "CoreProjectionEnvelope",
    "world_id": "rencrow-home",
    "timeline_id": "timeline-1",
    "timeline_epoch": "epoch-e2e",
    "actor_instance_id": "core-instance-mio",
    "projection_kind": "life_policy",
    "payload": {
        "contract_version": "world.life-policy/v1",
        "world_id": "rencrow-home",
        "actor_id": "mio",
        "source_owner": "CORE",
        "source_epoch": epoch,
        "source_revision": rev,
        "valid_until": "2099-01-01T00:00:00Z",
        "persona_revision": "persona-1",
        "allowed_non_cognitive_activity_ids": ["walk", "rest"],
        "expired_fallback_activity_ids": ["rest"],
        "condition_profile_ref": "condition-1",
    },
}
open(out, "w", encoding="utf-8").write(json.dumps(body))
PY
}

put_projection() {
  local path="$1" body="$2" out="$3" suffix="${4:-}" token="${5:-$CORE_TOKEN}"
  curl -sS -o "$out" -w '%{http_code}' -X PUT \
    -H "Authorization: Bearer $token" \
    -H 'Content-Type: application/json' \
    --data-binary @"$body" "$BASE$path$suffix"
}

echo "== readiness and no projection effect =="
start_server
READY_BODY="$(curl -sS -H "Authorization: Bearer $READ_TOKEN" "$BASE/health/ready")"
[ "$(printf '%s' "$READY_BODY" | json_get 'v["logical_ready"]')" = "True" ]
[ "$(printf '%s' "$READY_BODY" | json_get 'v["spatial_ready"]')" = "False" ]
[ "$(curl -sS -H "Authorization: Bearer $READ_TOKEN" "$BASE$SCOPE_PATH/snapshot" | json_get 'v["state_seq"]')" = "0" ]

echo "== source revision ordering and digest =="
WS10="$(host_path "$TMP/ws10.json")"
WS9="$(host_path "$TMP/ws9.json")"
WS10_CONFLICT="$(host_path "$TMP/ws10-conflict.json")"
WS11="$(host_path "$TMP/ws11.json")"
WS_QUEUED="$(host_path "$TMP/ws-queued.json")"
WS_BAD="$(host_path "$TMP/ws-bad.json")"
WS_KURO="$(host_path "$TMP/ws-kuro.json")"
LP1="$(host_path "$TMP/lp1.json")"
make_work_set "$WS10" core-epoch-1 10 running
make_work_set "$WS9" core-epoch-1 9 queued
make_work_set "$WS10_CONFLICT" core-epoch-1 10 other
make_work_set "$WS11" core-epoch-1 11 queued
make_work_set "$WS_QUEUED" core-epoch-1 12 queued
make_work_set "$WS_BAD" core-epoch-1 13 bad_running
make_work_set "$WS_KURO" core-epoch-1 1 queued kuro
make_life_policy "$LP1" core-epoch-1 1

CODE="$(put_projection "$PROJECTION_PATH/mio/full_actor_work_set" "$WS10" "$TMP/ack.json")"
[ "$CODE" = "200" ]
[ "$(json_get 'v["projection"]["status"]' <"$TMP/ack.json")" = "accepted" ]
[ "$(json_get 'v["projection"]["publisher_id"]' <"$TMP/ack.json")" = "core-publisher" ]
[ "$(json_get 'v["projection"]["credential_id"]' <"$TMP/ack.json")" = "core" ]

CODE="$(curl -sS -o "$TMP/head.json" -w '%{http_code}' -H "Authorization: Bearer $CORE_TOKEN" "$BASE$PROJECTION_PATH/mio/full_actor_work_set")"
[ "$CODE" = "200" ]
[ "$(json_get 'v["projection"]["source_revision"]' <"$TMP/head.json")" = "10" ]
[ "$(json_get 'v["projection"]["digest"]' <"$TMP/head.json")" != "" ]

CODE="$(put_projection "$PROJECTION_PATH/mio/full_actor_work_set" "$WS9" "$TMP/ack.json")"
[ "$CODE" = "409" ]
[ "$(json_get 'v["projection"]["status"]' <"$TMP/ack.json")" = "stale_revision" ]

CODE="$(put_projection "$PROJECTION_PATH/mio/full_actor_work_set" "$WS10" "$TMP/ack.json")"
[ "$CODE" = "200" ]
[ "$(json_get 'v["projection"]["status"]' <"$TMP/ack.json")" = "duplicate" ]

CODE="$(put_projection "$PROJECTION_PATH/mio/full_actor_work_set" "$WS10_CONFLICT" "$TMP/ack.json")"
[ "$CODE" = "409" ]
[ "$(json_get 'v["projection"]["status"]' <"$TMP/ack.json")" = "revision_conflict" ]

CODE="$(put_projection "$PROJECTION_PATH/mio/full_actor_work_set" "$WS11" "$TMP/ack.json")"
[ "$CODE" = "200" ]
CODE="$(put_projection "$PROJECTION_PATH/mio/full_actor_work_set" "$WS_QUEUED" "$TMP/ack.json")"
[ "$CODE" = "200" ]

CODE="$(put_projection "$PROJECTION_PATH/mio/life_policy" "$LP1" "$TMP/ack.json")"
[ "$CODE" = "200" ]
[ "$(json_get 'v["projection"]["status"]' <"$TMP/ack.json")" = "accepted" ]

echo "== invalid and unauthorized =="
CODE="$(put_projection "$PROJECTION_PATH/mio/full_actor_work_set" "$WS_BAD" "$TMP/ack.json")"
[ "$CODE" = "400" ]
grep -q '"code":"invalid_projection"' "$TMP/ack.json"
CODE="$(put_projection "$PROJECTION_PATH/kuro/full_actor_work_set" "$WS_KURO" "$TMP/ack.json")"
[ "$CODE" = "403" ]
grep -q '"code":"actor_not_bound"' "$TMP/ack.json"
CODE="$(curl -s -o "$TMP/ack.json" -w '%{http_code}' -X PUT -H "Authorization: Bearer $WRITE_TOKEN" -H 'Content-Type: application/json' --data-binary @"$WS_QUEUED" "$BASE$PROJECTION_PATH/mio/full_actor_work_set")"
[ "$CODE" = "403" ]
[ "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $READ_TOKEN" "$BASE$PROJECTION_PATH/mio/full_actor_work_set")" = "403" ]

echo "== projections do not create Commands or Events =="
[ "$(curl -sS -H "Authorization: Bearer $READ_TOKEN" "$BASE$SCOPE_PATH/snapshot" | json_get 'v["state_seq"]')" = "0" ]
[ "$(curl -sS -H "Authorization: Bearer $READ_TOKEN" "$BASE$SCOPE_PATH/events?after_seq=0" | json_get 'len(v["events"])')" = "0" ]

echo "== restart preserves head and revision =="
stop_server
start_server
CODE="$(put_projection "$PROJECTION_PATH/mio/full_actor_work_set" "$WS_QUEUED" "$TMP/ack.json")"
[ "$CODE" = "200" ]
[ "$(json_get 'v["projection"]["status"]' <"$TMP/ack.json")" = "duplicate" ]
make_work_set "$WS11" core-epoch-1 14 running
CODE="$(put_projection "$PROJECTION_PATH/mio/full_actor_work_set" "$WS11" "$TMP/ack.json")"
[ "$CODE" = "200" ]
[ "$(json_get 'v["projection"]["status"]' <"$TMP/ack.json")" = "accepted" ]

echo "== epoch reconciliation =="
WS_E2="$(host_path "$TMP/ws-e2.json")"
WS_E1_LATE="$(host_path "$TMP/ws-e1-late.json")"
make_work_set "$WS_E2" core-epoch-2 1 running
make_work_set "$WS_E1_LATE" core-epoch-1 999 running
CODE="$(put_projection "$PROJECTION_PATH/mio/full_actor_work_set" "$WS_E2" "$TMP/ack.json")"
[ "$CODE" = "409" ]
[ "$(json_get 'v["projection"]["status"]' <"$TMP/ack.json")" = "epoch_requires_reconcile" ]
CODE="$(put_projection "$PROJECTION_PATH/mio/full_actor_work_set" "$WS_E2" "$TMP/ack.json" '?reconcile=true')"
[ "$CODE" = "403" ]
CODE="$(put_projection "$PROJECTION_PATH/mio/full_actor_work_set" "$WS_E2" "$TMP/ack.json" '' "$CORE_RECONCILE_TOKEN")"
[ "$CODE" = "403" ]
CODE="$(put_projection "$PROJECTION_PATH/mio/full_actor_work_set" "$WS_E2" "$TMP/ack.json" '?reconcile=true' "$CORE_RECONCILE_TOKEN")"
[ "$CODE" = "200" ]
[ "$(json_get 'v["projection"]["status"]' <"$TMP/ack.json")" = "accepted" ]
CODE="$(put_projection "$PROJECTION_PATH/mio/full_actor_work_set" "$WS_E1_LATE" "$TMP/ack.json")"
[ "$CODE" = "409" ]
[ "$(json_get 'v["projection"]["status"]' <"$TMP/ack.json")" = "epoch_superseded" ]

echo "== checked reconcile route =="
RECONCILE_URL="$BASE$PROJECTION_PATH/reconcile"
RECONCILE_BODY='{"schema_version":1,"timeline_epoch":"epoch-e2e","source_revision":"e2e-reconcile"}'
CODE="$(curl -s -o "$TMP/reconcile.json" -w '%{http_code}' -X POST -H "Authorization: Bearer $CORE_TOKEN" -H 'Content-Type: application/json' --data "$RECONCILE_BODY" "$RECONCILE_URL")"
[ "$CODE" = "403" ]
CODE="$(curl -s -o "$TMP/reconcile.json" -w '%{http_code}' -X POST -H "Authorization: Bearer $CORE_RECONCILE_TOKEN" -H 'Content-Type: application/json' --data "$RECONCILE_BODY" "$RECONCILE_URL")"
[ "$CODE" = "200" ]
[ "$(json_get 'v["status"]' <"$TMP/reconcile.json")" = "reconciled" ]
[ "$(json_get 'v["required_projections"]' <"$TMP/reconcile.json")" = "2" ]

echo "== token secrecy =="
if grep -qE 'e2e-r08-(write|read|core|core-reconcile)-token' "$TMP/stdout.log" "$TMP/stderr.log"; then
  echo "token leaked to logs" >&2
  exit 1
fi

echo "R08 E2E PASS"
