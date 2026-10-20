package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var backupAt = time.Date(2026, 10, 20, 1, 2, 3, 0, time.UTC)

func TestOpenMigratesLegacyTimelineGateColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", "file:"+filepathToSlash(path))
	if err != nil {
		t.Fatalf("legacy open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE timeline (
		world_id TEXT NOT NULL, timeline_id TEXT NOT NULL, timeline_epoch TEXT NOT NULL,
		event_seq INTEGER NOT NULL, state_seq INTEGER NOT NULL,
		state_json BLOB NOT NULL, state_hash TEXT NOT NULL,
		PRIMARY KEY (world_id, timeline_id, timeline_epoch))`); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("migrate open: %v", err)
	}
	defer s.Close()
	registerTestHandlers(s)
	scope := testScope()
	if err := s.CreateTimeline(scope, State{}, map[string]int64{}); err != nil {
		t.Fatalf("create after migration: %v", err)
	}
	reconciled, superseded, err := s.EpochStatus(scope)
	if err != nil || !reconciled || superseded != "" {
		t.Fatalf("status after migration: %v %q %v", reconciled, superseded, err)
	}
}

func TestBackupCreatesVerifiedManifest(t *testing.T) {
	s, scope := openTestStore(t)
	if _, err := s.Append(testCommand(scope, "b1", "emit", map[string]any{"count": 2})); err != nil {
		t.Fatalf("append: %v", err)
	}
	path := filepath.Join(t.TempDir(), "backup.db")
	manifest, err := s.Backup(scope, BackupOptions{Path: path, SourceRevision: "rev-test", CreatedAt: backupAt})
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if manifest.Scope != scope || manifest.StateSeq != 2 || manifest.EventSeq != 2 {
		t.Fatalf("manifest = %+v", manifest)
	}
	if manifest.StateHash == "" || manifest.SHA256 == "" || manifest.SizeBytes == 0 || manifest.SourceRevision != "rev-test" {
		t.Fatalf("manifest fields = %+v", manifest)
	}
	if manifest.CreatedAt.UTC() != backupAt {
		t.Fatalf("created_at = %s", manifest.CreatedAt)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != manifest.SizeBytes {
		t.Fatalf("backup file: %v", err)
	}
	if _, err := os.Stat(BackupManifestPath(path)); err != nil {
		t.Fatalf("manifest file: %v", err)
	}
	verified, err := VerifyBackup(path)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verified != manifest {
		t.Fatalf("verified = %+v, want %+v", verified, manifest)
	}
	if _, err := s.Backup(scope, BackupOptions{Path: path, CreatedAt: backupAt}); !errors.Is(err, ErrBackupExists) {
		t.Fatalf("second backup err = %v", err)
	}
}

func TestVerifyBackupRejectsTampering(t *testing.T) {
	s, scope := openTestStore(t)
	if _, err := s.Append(testCommand(scope, "t1", "emit", map[string]any{"count": 1})); err != nil {
		t.Fatalf("append: %v", err)
	}
	path := filepath.Join(t.TempDir(), "backup.db")
	if _, err := s.Backup(scope, BackupOptions{Path: path, CreatedAt: backupAt}); err != nil {
		t.Fatalf("backup: %v", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.Write([]byte("tamper")); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Close()
	if _, err := VerifyBackup(path); !errors.Is(err, ErrBackupInvalid) {
		t.Fatalf("tampered verify = %v", err)
	}

	clean := filepath.Join(t.TempDir(), "clean.db")
	if _, err := s.Backup(scope, BackupOptions{Path: clean, CreatedAt: backupAt}); err != nil {
		t.Fatalf("clean backup: %v", err)
	}
	if err := os.Remove(BackupManifestPath(clean)); err != nil {
		t.Fatalf("remove manifest: %v", err)
	}
	if _, err := VerifyBackup(clean); !errors.Is(err, ErrBackupInvalid) {
		t.Fatalf("missing manifest verify = %v", err)
	}

	corrupt := filepath.Join(t.TempDir(), "corrupt.db")
	if _, err := s.Backup(scope, BackupOptions{Path: corrupt, CreatedAt: backupAt}); err != nil {
		t.Fatalf("corrupt backup: %v", err)
	}
	if err := os.WriteFile(BackupManifestPath(corrupt), []byte(`{"schema_version":1`), 0o600); err != nil {
		t.Fatalf("corrupt manifest: %v", err)
	}
	if _, err := VerifyBackup(corrupt); !errors.Is(err, ErrBackupInvalid) {
		t.Fatalf("corrupt manifest verify = %v", err)
	}
}

func TestRestoreCreatesEpochAndRejectsOldCommands(t *testing.T) {
	s, scope := openTestStore(t)
	if _, err := s.Append(testCommand(scope, "e1", "emit", map[string]any{"count": 2})); err != nil {
		t.Fatalf("append: %v", err)
	}
	path := filepath.Join(t.TempDir(), "backup.db")
	if _, err := s.Backup(scope, BackupOptions{Path: path, SourceRevision: "rev-1", CreatedAt: backupAt}); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if _, err := s.Append(testCommand(scope, "e2", "emit", map[string]any{"count": 1})); err != nil {
		t.Fatalf("append after backup: %v", err)
	}

	next := Scope{WorldID: scope.WorldID, TimelineID: scope.TimelineID, TimelineEpoch: "epoch-2"}
	record, err := s.Restore(path, RestoreOptions{OldScope: scope, NewEpoch: next.TimelineEpoch, CompletedAt: backupAt})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if record.Scope != next || record.BackupStateSeq != 2 || record.OmittedFrom != 3 || record.OmittedTo != 3 {
		t.Fatalf("restore record = %+v", record)
	}
	state, seq, hash, err := s.Snapshot(next)
	if err != nil {
		t.Fatalf("snapshot new epoch: %v", err)
	}
	if seq != 2 || hash != record.StateHash || len(state.Actors) == 0 {
		t.Fatalf("new epoch snapshot seq=%d hash=%s state=%+v", seq, hash, state)
	}
	reconciled, superseded, err := s.EpochStatus(next)
	if err != nil || reconciled || superseded != "" {
		t.Fatalf("new epoch status: reconciled=%v superseded=%q err=%v", reconciled, superseded, err)
	}
	reconciled, superseded, err = s.EpochStatus(scope)
	if err != nil || !reconciled || superseded != next.TimelineEpoch {
		t.Fatalf("old epoch status: reconciled=%v superseded=%q err=%v", reconciled, superseded, err)
	}

	oldAppend, err := s.Append(testCommand(scope, "old-new-key", "move", map[string]any{"actor_id": "mio", "room_id": "2f-room"}))
	if err != nil {
		t.Fatalf("old append: %v", err)
	}
	if oldAppend.Receipt.Status != "rejected" || oldAppend.Receipt.Reason != "epoch_superseded" {
		t.Fatalf("old append receipt = %+v", oldAppend.Receipt)
	}
	if _, oldSeq, _, _ := s.Snapshot(scope); oldSeq != 3 {
		t.Fatalf("old epoch changed: %d", oldSeq)
	}

	blocked, err := s.Append(testCommand(next, "new-1", "move", map[string]any{"actor_id": "mio", "room_id": "2f-room"}))
	if err != nil {
		t.Fatalf("new epoch append: %v", err)
	}
	if blocked.Receipt.Status != "rejected" || blocked.Receipt.Reason != "epoch_not_reconciled" {
		t.Fatalf("unreconciled receipt = %+v", blocked.Receipt)
	}

	if err := s.CompleteReconcile(next, "core-full-1", backupAt.Add(time.Minute)); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if err := s.CompleteReconcile(next, "core-full-1", backupAt.Add(time.Minute)); err != nil {
		t.Fatalf("idempotent reconcile: %v", err)
	}
	if err := s.CompleteReconcile(next, "", backupAt); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("empty reconcile revision = %v", err)
	}
	expected := uint64(2)
	cmd := testCommand(next, "new-2", "move", map[string]any{"actor_id": "mio", "room_id": "2f-room"})
	cmd.ExpectedStateSeq = &expected
	committed, err := s.Append(cmd)
	if err != nil || committed.Receipt.Status != "committed" || committed.Receipt.StateSeq != 3 {
		t.Fatalf("new committed: %v %+v", err, committed.Receipt)
	}
	_, seq, hash, _ = s.Snapshot(next)
	if seq != 3 {
		t.Fatalf("new epoch seq = %d", seq)
	}
	_, replayHash, replaySeq, err := s.Replay(next, seq)
	if err != nil || replayHash != hash || replaySeq != seq {
		t.Fatalf("replay: err=%v hash=%s/%s seq=%d/%d", err, replayHash, hash, replaySeq, seq)
	}
	gaps, err := s.RecoveryGaps(next)
	if err != nil || len(gaps) != 1 || gaps[0].FromSeq != 3 || gaps[0].ToSeq != 3 || gaps[0].Reason != "restore" {
		t.Fatalf("recovery gaps = %+v err=%v", gaps, err)
	}

	// The idempotency namespace is epoch-scoped: an E1 key can be reused in E2,
	// and an E1 Receipt is not visible through the E2 scope.
	reused := testCommand(next, "old-new-key", "move", map[string]any{"actor_id": "shiro", "room_id": "1f-living"})
	if _, err := s.Append(reused); err != nil {
		t.Fatalf("reused key in new epoch: %v", err)
	}
	if _, err := s.GetReceipt(next, oldAppend.Receipt.ID); !errors.Is(err, ErrReceiptNotFound) {
		t.Fatalf("old receipt visible in new epoch: %v", err)
	}
	if _, err := s.Restore(path, RestoreOptions{OldScope: scope, NewEpoch: "epoch-3", CompletedAt: backupAt}); !errors.Is(err, ErrEpochSuperseded) {
		t.Fatalf("restore from superseded epoch = %v", err)
	}
}

func TestRestoreRejectsInvalidTargets(t *testing.T) {
	s, scope := openTestStore(t)
	if _, err := s.Append(testCommand(scope, "i1", "emit", map[string]any{"count": 1})); err != nil {
		t.Fatalf("append: %v", err)
	}
	path := filepath.Join(t.TempDir(), "backup.db")
	if _, err := s.Backup(scope, BackupOptions{Path: path, CreatedAt: backupAt}); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if _, err := s.Restore(path, RestoreOptions{OldScope: scope, NewEpoch: scope.TimelineEpoch, CompletedAt: backupAt}); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("same epoch restore = %v", err)
	}
	next := Scope{WorldID: scope.WorldID, TimelineID: scope.TimelineID, TimelineEpoch: "epoch-2"}
	if err := s.CreateTimeline(next, State{}, map[string]int64{}); err != nil {
		t.Fatalf("create target: %v", err)
	}
	if _, err := s.Restore(path, RestoreOptions{OldScope: scope, NewEpoch: next.TimelineEpoch, CompletedAt: backupAt}); !errors.Is(err, ErrRestoreTargetExists) {
		t.Fatalf("existing target restore = %v", err)
	}
	if _, err := s.Restore(filepath.Join(t.TempDir(), "missing.db"), RestoreOptions{OldScope: scope, NewEpoch: "epoch-3", CompletedAt: backupAt}); !errors.Is(err, ErrBackupInvalid) {
		t.Fatalf("missing backup restore = %v", err)
	}

	if _, err := s.Restore(path, RestoreOptions{OldScope: Scope{WorldID: "other", TimelineID: "timeline-1", TimelineEpoch: "epoch-1"}, NewEpoch: "epoch-4", CompletedAt: backupAt}); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("scope-mismatched restore = %v", err)
	}
}

func TestE2EBackupRestoreAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "world.db")
	backupPath := filepath.Join(t.TempDir(), "backup.db")
	scope := testScope()
	next := Scope{WorldID: scope.WorldID, TimelineID: scope.TimelineID, TimelineEpoch: "epoch-restored"}

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	registerTestHandlers(s)
	if err := s.CreateTimeline(scope, State{Actors: map[string]ActorState{"mio": {RoomID: "1f-living"}}}, map[string]int64{"chair": 1}); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, key := range []string{"a1", "a2"} {
		if _, err := s.Append(testCommand(scope, key, "emit", map[string]any{"count": 2})); err != nil {
			t.Fatalf("append %s: %v", key, err)
		}
	}
	if _, err := s.Backup(scope, BackupOptions{Path: backupPath, SourceRevision: "rev-e2e", CreatedAt: backupAt}); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if _, err := s.Append(testCommand(scope, "a3", "emit", map[string]any{"count": 1})); err != nil {
		t.Fatalf("post-backup append: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	registerTestHandlers(reopened)
	if _, err := reopened.Restore(backupPath, RestoreOptions{OldScope: scope, NewEpoch: next.TimelineEpoch, CompletedAt: backupAt}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if err := reopened.CompleteReconcile(next, "core-full-e2e", backupAt); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	appended, err := reopened.Append(testCommand(next, "n1", "move", map[string]any{"actor_id": "mio", "room_id": "2f-room"}))
	if err != nil || appended.Receipt.Status != "committed" {
		t.Fatalf("append new epoch: %v %+v", err, appended.Receipt)
	}
	state, seq, hash, err := reopened.Snapshot(next)
	if err != nil || seq != 5 {
		t.Fatalf("snapshot seq=%d err=%v", seq, err)
	}
	if state.Actors["mio"].RoomID != "2f-room" {
		t.Fatalf("state = %+v", state)
	}
	if _, replayHash, _, err := reopened.Replay(next, seq); err != nil || replayHash != hash {
		t.Fatalf("replay err=%v hash=%s/%s", err, replayHash, hash)
	}
	manifest, err := VerifyBackup(backupPath)
	if err != nil || manifest.StateSeq != 4 || manifest.SourceRevision != "rev-e2e" {
		t.Fatalf("manifest = %+v err=%v", manifest, err)
	}

	// The manifest is plain JSON and round-trips without hidden fields.
	raw, err := os.ReadFile(BackupManifestPath(backupPath))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded["schema_version"].(float64) != 1 {
		t.Fatalf("manifest json = %s err=%v", raw, err)
	}
}
