package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrBackupExists        = errors.New("backup destination already exists")
	ErrBackupInvalid       = errors.New("backup is invalid")
	ErrRestoreTargetExists = errors.New("restore target timeline already exists")
	ErrEpochSuperseded     = errors.New("timeline epoch is superseded")
)

// BackupOptions describes an isolated, verified R06 backup.
type BackupOptions struct {
	Path           string
	SourceRevision string
	CreatedAt      time.Time
}

// BackupManifest is written next to a backup as JSON. SHA256 covers the exact
// backup database bytes; StateHash covers the canonical State at StateSeq.
type BackupManifest struct {
	SchemaVersion  int       `json:"schema_version"`
	Format         string    `json:"format"`
	Scope          Scope     `json:"scope"`
	EventSeq       uint64    `json:"event_seq"`
	StateSeq       uint64    `json:"state_seq"`
	StateHash      string    `json:"state_hash"`
	SHA256         string    `json:"sha256"`
	SizeBytes      int64     `json:"size_bytes"`
	CreatedAt      time.Time `json:"created_at"`
	SourceRevision string    `json:"source_revision,omitempty"`
}

// RestoreOptions restores a verified backup into a new TimelineEpoch. OldScope
// must be the backup's exact scope; it is marked superseded and kept for audit.
type RestoreOptions struct {
	OldScope    Scope
	NewEpoch    string
	CompletedAt time.Time
}

// RestoreRecord records the new epoch and the omitted sequence interval. The
// omitted interval is never fabricated as Events.
type RestoreRecord struct {
	Scope           Scope
	BackupStateSeq  uint64
	StateHash       string
	OmittedFrom     uint64
	OmittedTo       uint64
	SupersededEpoch string
}

// RecoveryGap records a sequence interval that cannot be reconstructed.
type RecoveryGap struct {
	FromSeq    uint64
	ToSeq      uint64
	Reason     string
	RecordedAt time.Time
}

// BackupManifestPath is the sidecar path for one backup database.
func BackupManifestPath(path string) string { return path + ".manifest.json" }

// Backup creates a standalone copy with VACUUM INTO, hashes it, writes its
// manifest atomically, and verifies the result before returning. The live
// database file is never copied directly.
func (s *Store) Backup(scope Scope, opts BackupOptions) (BackupManifest, error) {
	if !scope.valid() || opts.Path == "" || opts.CreatedAt.IsZero() {
		return BackupManifest{}, ErrInvalidCommand
	}
	if opts.SourceRevision != "" && !validExact(opts.SourceRevision, 256) {
		return BackupManifest{}, ErrInvalidCommand
	}
	abs, err := filepath.Abs(opts.Path)
	if err != nil {
		return BackupManifest{}, err
	}
	if abs == s.path {
		return BackupManifest{}, ErrInvalidCommand
	}
	if _, err := os.Stat(abs); err == nil {
		return BackupManifest{}, ErrBackupExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return BackupManifest{}, err
	}
	if _, err := os.Stat(BackupManifestPath(abs)); err == nil {
		return BackupManifest{}, ErrBackupExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return BackupManifest{}, err
	}
	info, err := os.Stat(filepath.Dir(abs))
	if err != nil || !info.IsDir() {
		return BackupManifest{}, ErrInvalidCommand
	}

	_, stateSeq, stateHash, err := s.Snapshot(scope)
	if err != nil {
		return BackupManifest{}, err
	}
	if _, err := s.db.ExecContext(context.Background(), `VACUUM INTO ?`, abs); err != nil {
		return BackupManifest{}, err
	}
	// The Store has one logical writer, so this is a quiescent check as well as
	// a guard against a concurrent connection.
	_, afterSeq, afterHash, err := s.Snapshot(scope)
	if err != nil || afterSeq != stateSeq || afterHash != stateHash {
		_ = os.Remove(abs)
		return BackupManifest{}, errors.New("state changed during backup")
	}
	sum, size, err := fileDigest(abs)
	if err != nil {
		return BackupManifest{}, err
	}
	manifest := BackupManifest{
		SchemaVersion:  1,
		Format:         "sqlite-vacuum-into",
		Scope:          scope,
		EventSeq:       stateSeq,
		StateSeq:       stateSeq,
		StateHash:      stateHash,
		SHA256:         sum,
		SizeBytes:      size,
		CreatedAt:      opts.CreatedAt.UTC(),
		SourceRevision: opts.SourceRevision,
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return BackupManifest{}, err
	}
	manifestPath := BackupManifestPath(abs)
	tmp := manifestPath + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return BackupManifest{}, err
	}
	if err := os.Rename(tmp, manifestPath); err != nil {
		return BackupManifest{}, err
	}
	if _, err := VerifyBackup(abs); err != nil {
		_ = os.Remove(manifestPath)
		_ = os.Remove(abs)
		return BackupManifest{}, err
	}
	return manifest, nil
}

// VerifyBackup checks the manifest, file hash, SQLite integrity, and the
// Timeline row before a restore is allowed.
func VerifyBackup(path string) (BackupManifest, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return BackupManifest{}, invalidBackup(err)
	}
	var manifest BackupManifest
	raw, err := os.ReadFile(BackupManifestPath(abs))
	if err != nil {
		return BackupManifest{}, invalidBackup(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return BackupManifest{}, invalidBackup(err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return BackupManifest{}, invalidBackup(errors.New("manifest has trailing data"))
	}
	if manifest.SchemaVersion != 1 || manifest.Format != "sqlite-vacuum-into" || !manifest.Scope.Valid() {
		return BackupManifest{}, invalidBackup(errors.New("manifest header is invalid"))
	}
	info, err := os.Stat(abs)
	if err != nil {
		return BackupManifest{}, invalidBackup(err)
	}
	if info.Size() != manifest.SizeBytes {
		return BackupManifest{}, invalidBackup(errors.New("size mismatch"))
	}
	sum, _, err := fileDigest(abs)
	if err != nil {
		return BackupManifest{}, invalidBackup(err)
	}
	if sum != manifest.SHA256 {
		return BackupManifest{}, invalidBackup(errors.New("sha256 mismatch"))
	}
	db, err := sql.Open("sqlite", readOnlyDSN(abs))
	if err != nil {
		return BackupManifest{}, invalidBackup(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var check string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		return BackupManifest{}, invalidBackup(errors.New("integrity_check failed"))
	}
	var eventSeq, stateSeq uint64
	var stateHash string
	err = db.QueryRow(`SELECT event_seq, state_seq, state_hash FROM timeline
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ?`,
		manifest.Scope.WorldID, manifest.Scope.TimelineID, manifest.Scope.TimelineEpoch).
		Scan(&eventSeq, &stateSeq, &stateHash)
	if err != nil {
		return BackupManifest{}, invalidBackup(err)
	}
	if eventSeq != manifest.EventSeq || stateSeq != manifest.StateSeq || stateHash != manifest.StateHash {
		return BackupManifest{}, invalidBackup(errors.New("manifest does not match the backup state"))
	}
	return manifest, nil
}

// Restore creates NewEpoch at the backup State/sequence, marks OldScope
// superseded, and records the omitted interval. It is one transaction.
func (s *Store) Restore(backupPath string, opts RestoreOptions) (RestoreRecord, error) {
	if !opts.OldScope.valid() || !validExact(opts.NewEpoch, 256) || opts.CompletedAt.IsZero() {
		return RestoreRecord{}, ErrInvalidCommand
	}
	abs, err := filepath.Abs(backupPath)
	if err != nil || abs == s.path {
		return RestoreRecord{}, ErrInvalidCommand
	}
	manifest, err := VerifyBackup(abs)
	if err != nil {
		return RestoreRecord{}, err
	}
	if opts.OldScope != manifest.Scope || opts.NewEpoch == opts.OldScope.TimelineEpoch {
		return RestoreRecord{}, ErrInvalidCommand
	}
	state, raw, err := readBackupState(abs, manifest)
	if err != nil {
		return RestoreRecord{}, err
	}
	next := Scope{WorldID: manifest.Scope.WorldID, TimelineID: manifest.Scope.TimelineID, TimelineEpoch: opts.NewEpoch}

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RestoreRecord{}, err
	}
	defer tx.Rollback()

	var oldSeq uint64
	var supersededBy string
	err = tx.QueryRowContext(ctx, `SELECT state_seq, superseded_by FROM timeline
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ?`,
		opts.OldScope.WorldID, opts.OldScope.TimelineID, opts.OldScope.TimelineEpoch).Scan(&oldSeq, &supersededBy)
	if errors.Is(err, sql.ErrNoRows) {
		return RestoreRecord{}, ErrTimelineNotFound
	}
	if err != nil {
		return RestoreRecord{}, err
	}
	if supersededBy != "" {
		return RestoreRecord{}, ErrEpochSuperseded
	}
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM timeline WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ?`,
		next.WorldID, next.TimelineID, next.TimelineEpoch).Scan(&exists)
	if err == nil {
		return RestoreRecord{}, ErrRestoreTargetExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RestoreRecord{}, err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO timeline
		(world_id, timeline_id, timeline_epoch, event_seq, state_seq, state_json, state_hash, reconciled, superseded_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, '')`,
		next.WorldID, next.TimelineID, next.TimelineEpoch, manifest.EventSeq, manifest.StateSeq, raw, manifest.StateHash); err != nil {
		return RestoreRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO snapshot
		(world_id, timeline_id, timeline_epoch, seq, state_json, state_hash) VALUES (?, ?, ?, ?, ?, ?)`,
		next.WorldID, next.TimelineID, next.TimelineEpoch, manifest.StateSeq, raw, manifest.StateHash); err != nil {
		return RestoreRecord{}, err
	}
	for id, r := range state.Resources {
		if _, err := tx.ExecContext(ctx, `INSERT INTO resource
			(world_id, timeline_id, timeline_epoch, resource_id, capacity, used) VALUES (?, ?, ?, ?, ?, ?)`,
			next.WorldID, next.TimelineID, next.TimelineEpoch, id, r.Capacity, r.Used); err != nil {
			return RestoreRecord{}, err
		}
	}
	if err := writeReservations(ctx, tx, next, nil, state.Reservations); err != nil {
		return RestoreRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE timeline SET superseded_by = ?
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ?`,
		next.TimelineEpoch, opts.OldScope.WorldID, opts.OldScope.TimelineID, opts.OldScope.TimelineEpoch); err != nil {
		return RestoreRecord{}, err
	}
	record := RestoreRecord{
		Scope:           next,
		BackupStateSeq:  manifest.StateSeq,
		StateHash:       manifest.StateHash,
		SupersededEpoch: opts.OldScope.TimelineEpoch,
	}
	if oldSeq > manifest.StateSeq {
		record.OmittedFrom = manifest.StateSeq + 1
		record.OmittedTo = oldSeq
		if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_gap
			(world_id, timeline_id, timeline_epoch, from_seq, to_seq, reason, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			next.WorldID, next.TimelineID, next.TimelineEpoch, record.OmittedFrom, record.OmittedTo,
			"restore", opts.CompletedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return RestoreRecord{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return RestoreRecord{}, err
	}
	return record, nil
}

// CompleteReconcile opens the new epoch for new Commands after CORE full
// reconcile. It is idempotent for the same epoch.
func (s *Store) CompleteReconcile(scope Scope, sourceRevision string, at time.Time) error {
	if !scope.valid() || !validExact(sourceRevision, 256) || at.IsZero() {
		return ErrInvalidCommand
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	reconciled, supersededBy, err := readEpochStatus(ctx, tx, scope)
	if err != nil {
		return err
	}
	if supersededBy != "" {
		return ErrEpochSuperseded
	}
	if reconciled {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO epoch_reconcile
		(world_id, timeline_id, timeline_epoch, source_revision, completed_at) VALUES (?, ?, ?, ?, ?)`,
		scope.WorldID, scope.TimelineID, scope.TimelineEpoch, sourceRevision, at.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE timeline SET reconciled = 1
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ?`,
		scope.WorldID, scope.TimelineID, scope.TimelineEpoch); err != nil {
		return err
	}
	return tx.Commit()
}

// EpochStatus reports whether an epoch is open for new Commands.
func (s *Store) EpochStatus(scope Scope) (reconciled bool, supersededBy string, err error) {
	if !scope.valid() {
		return false, "", ErrInvalidCommand
	}
	return readEpochStatus(context.Background(), s.db, scope)
}

// RecoveryGaps returns the omitted intervals recorded in an epoch.
func (s *Store) RecoveryGaps(scope Scope) ([]RecoveryGap, error) {
	if !scope.valid() {
		return nil, ErrInvalidCommand
	}
	rows, err := s.db.Query(`SELECT from_seq, to_seq, reason, recorded_at FROM recovery_gap
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ? ORDER BY from_seq`,
		scope.WorldID, scope.TimelineID, scope.TimelineEpoch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var gaps []RecoveryGap
	for rows.Next() {
		var g RecoveryGap
		var recorded string
		if err := rows.Scan(&g.FromSeq, &g.ToSeq, &g.Reason, &recorded); err != nil {
			return nil, err
		}
		g.RecordedAt, _ = time.Parse(time.RFC3339Nano, recorded)
		gaps = append(gaps, g)
	}
	return gaps, rows.Err()
}

func readEpochStatus(ctx context.Context, q queryer, scope Scope) (bool, string, error) {
	var reconciled int
	var superseded string
	err := q.QueryRowContext(ctx, `SELECT reconciled, superseded_by FROM timeline
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ?`,
		scope.WorldID, scope.TimelineID, scope.TimelineEpoch).Scan(&reconciled, &superseded)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", ErrTimelineNotFound
	}
	if err != nil {
		return false, "", err
	}
	return reconciled == 1, superseded, nil
}

func readBackupState(path string, manifest BackupManifest) (State, []byte, error) {
	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return State{}, nil, invalidBackup(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var eventSeq, stateSeq uint64
	var raw []byte
	var hash string
	err = db.QueryRow(`SELECT event_seq, state_seq, state_json, state_hash FROM timeline
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ?`,
		manifest.Scope.WorldID, manifest.Scope.TimelineID, manifest.Scope.TimelineEpoch).
		Scan(&eventSeq, &stateSeq, &raw, &hash)
	if err != nil {
		return State{}, nil, invalidBackup(err)
	}
	if eventSeq != manifest.EventSeq || stateSeq != manifest.StateSeq || hash != manifest.StateHash {
		return State{}, nil, invalidBackup(errors.New("backup row changed"))
	}
	state, err := decodeState(raw)
	if err != nil {
		return State{}, nil, invalidBackup(err)
	}
	computed, normalized, err := stateHash(state)
	if err != nil || computed != manifest.StateHash {
		return State{}, nil, invalidBackup(errors.New("state hash mismatch"))
	}
	return state, normalized, nil
}

func fileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func readOnlyDSN(path string) string {
	abs, _ := filepath.Abs(path)
	return "file:" + filepathToSlash(abs) + "?mode=ro"
}

func invalidBackup(err error) error {
	return fmt.Errorf("%w: %v", ErrBackupInvalid, err)
}

func validExact(value string, max int) bool {
	if value == "" || len(value) > max || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
