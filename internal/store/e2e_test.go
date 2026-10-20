package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestE2ELifecycle exercises the same on-disk store across a Close/Open cycle:
// commit, idempotent retry, restart, snapshot, page, and replay. It is the
// process-level end-to-end scenario used by scripts/e2e-r06.sh.
func TestE2ELifecycle(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "e2e-world.db")
	scope := testScope()

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	registerTestHandlers(s)
	if err := s.CreateTimeline(scope, State{Actors: map[string]ActorState{
		"mio": {RoomID: "1f-living"}, "shiro": {RoomID: "2f-room"},
	}}, map[string]int64{"chair": 1, "drink": 1}); err != nil {
		t.Fatalf("create: %v", err)
	}
	start := testCommand(scope, "e2e-start", "activity_start", map[string]any{
		"activity_id": "act-e2e", "actor_id": "mio",
		"resources": []map[string]any{{"resource_id": "chair", "quantity": 1}, {"resource_id": "drink", "quantity": 1}},
	})
	first, err := s.Append(start)
	if err != nil || first.Receipt.Status != "committed" {
		t.Fatalf("start: %v %+v", err, first.Receipt)
	}
	if again, err := s.Append(start); err != nil || !again.Duplicate || again.Receipt.ID != first.Receipt.ID {
		t.Fatalf("idempotent retry: %v %+v", err, again.Receipt)
	}
	move := testCommand(scope, "e2e-move", "move", map[string]any{"actor_id": "shiro", "room_id": "1f-living"})
	if _, err := s.Append(move); err != nil {
		t.Fatalf("move: %v", err)
	}
	_, liveSeq, liveHash, err := s.Snapshot(scope)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen the same file: WAL recovery must expose exactly the committed state.
	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	registerTestHandlers(reopened)
	state, seq, hash, err := reopened.Snapshot(scope)
	if err != nil {
		t.Fatalf("snapshot after reopen: %v", err)
	}
	if seq != liveSeq || hash != liveHash {
		t.Fatalf("reopen changed state: seq=%d/%d hash=%s/%s", seq, liveSeq, hash, liveHash)
	}
	if state.Actors["mio"].ActivityID != "act-e2e" || state.Actors["shiro"].RoomID != "1f-living" {
		t.Fatalf("state after reopen = %+v", state)
	}
	again, err := reopened.Append(start)
	if err != nil || !again.Duplicate || again.Receipt.ID != first.Receipt.ID {
		t.Fatalf("retry after reopen: %v %+v", err, again.Receipt)
	}
	page, err := reopened.Page(scope, 0, 500, 2*1024*1024)
	if err != nil || len(page.Events) != 4 || !page.Done {
		t.Fatalf("page: %v events=%d done=%v", err, len(page.Events), page.Done)
	}
	replayed, replayHash, replaySeq, err := reopened.Replay(scope, liveSeq)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replayHash != liveHash || replaySeq != liveSeq {
		t.Fatalf("replay hash=%s seq=%d want %s %d", replayHash, replaySeq, liveHash, liveSeq)
	}
	live, _, _, _ := reopened.Snapshot(scope)
	lb, _ := json.Marshal(live)
	rb, _ := json.Marshal(replayed)
	if string(lb) != string(rb) {
		t.Fatalf("replay state differs\nlive=%s\nreplay=%s", lb, rb)
	}
	// The database file (and its WAL) exist on disk, and the recorded time is the
	// explicit command time rather than wall-clock time.
	if info, err := os.Stat(dbPath); err != nil || info.Size() == 0 {
		t.Fatalf("db file: %v", err)
	}
	if got := first.Receipt.RecordedAt.UTC(); got != time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("recorded_at = %s", got)
	}
}
