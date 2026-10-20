#!/usr/bin/env bash
# R06 end-to-end check: real on-disk SQLite file, close/reopen WAL recovery,
# idempotent retry, paging, replay, and process-exit crash injection.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "== go test ./... =="
go test ./... -count=1

echo "== go vet ./... =="
go vet ./...

echo "== R06 on-disk lifecycle + crash injection =="
go test ./internal/store -count=1 -v \
  -run 'TestE2ELifecycle|TestCrashRecovery|TestReplayHashIsDeterministic|TestAtomicCommitAndReceipt'

echo "== R05 inspect regression =="
go test ./cmd/rencrow-world ./internal/input ./internal/world -count=1 -v >/dev/null

echo "E2E PASS"
