// Package store is the WORLD R06 durable EventStore foundation.
//
// It keeps a single logical writer over SQLite WAL. A Command commits its ordered
// Events, State projection, reservations, and Receipt in one transaction, or none
// of them. The Event log is authoritative; State is a deterministic projection and
// can be replayed from a Snapshot plus saved per-transaction deltas without running
// a handler, LLM, Tool, or current rule again.
//
// R05 inspect remains a separate read-only command and does not use this package.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const (
	MaxCommandEvents  = 128
	MaxCommandBytes   = 256 * 1024
	MaxPageEvents     = 500
	DefaultPageEvents = 500
)

var (
	ErrIdempotencyConflict = errors.New("idempotency key reused with another body")
	ErrLimitExceeded       = errors.New("command exceeds the R06 limit")
	ErrTimelineNotFound    = errors.New("timeline scope not found")
	ErrInvalidCommand      = errors.New("invalid command")
	ErrSeqOutOfRange       = errors.New("sequence is beyond the timeline")
	ErrTimelineExists      = errors.New("timeline scope already exists")
)

// transactionHook is the crash-injection seam used only by the package tests.
// Production callers never set it.
var transactionHook func(point string)

// Scope is the exact WorldID, TimelineID, and TimelineEpoch of every record.
type Scope struct {
	WorldID       string
	TimelineID    string
	TimelineEpoch string
}

func (s Scope) valid() bool {
	for _, v := range []string{s.WorldID, s.TimelineID, s.TimelineEpoch} {
		if v == "" || !utf8.ValidString(v) || strings.TrimSpace(v) != v {
			return false
		}
	}
	return true
}

type ActorState struct {
	RoomID     string `json:"room_id"`
	ActivityID string `json:"activity_id,omitempty"`
}

type ActivityState struct {
	ID      string `json:"id"`
	ActorID string `json:"actor_id"`
	Status  string `json:"status"`
}

type ResourceState struct {
	Capacity int64 `json:"capacity"`
	Used     int64 `json:"used"`
}

type Reservation struct {
	ResourceID string `json:"resource_id"`
	ActorID    string `json:"actor_id"`
	ActivityID string `json:"activity_id"`
	Quantity   int64  `json:"quantity"`
}

// State is the canonical projection. Derived clock and freshness values are not
// stored here, so replay does not depend on wall-clock time.
type State struct {
	Actors       map[string]ActorState    `json:"actors"`
	Activities   map[string]ActivityState `json:"activities"`
	Resources    map[string]ResourceState `json:"resources"`
	Reservations []Reservation            `json:"reservations"`
}

type EventDraft struct {
	Kind    string
	Payload json.RawMessage
}

type Outcome struct {
	State   State
	Events  []EventDraft
	Reserve []Reservation
	Release []Reservation
	Consume []Reservation
}

// Handler is a registered, typed, deterministic world handler. It never reads a
// clock, random source, network, or LLM.
type Handler func(state State, payload json.RawMessage) (Outcome, error)

type Command struct {
	Scope            Scope
	ClientID         string
	IdempotencyKey   string
	ExpectedStateSeq *uint64
	Action           string
	Payload          json.RawMessage
	RecordedAt       time.Time
}

type Receipt struct {
	ID            string
	Status        string
	Reason        string
	TransactionID string
	EventIDs      []string
	FirstSeq      uint64
	LastSeq       uint64
	StateSeq      uint64
	BodyDigest    string
	RecordedAt    time.Time
}

type Result struct {
	Receipt   Receipt
	Duplicate bool
}

type Event struct {
	Seq           uint64
	TransactionID string
	EventID       string
	Kind          string
	Payload       json.RawMessage
	RecordedAt    time.Time
}

type Page struct {
	Events  []Event
	NextSeq uint64
	Done    bool
}

type Store struct {
	db       *sql.DB
	handlers map[string]Handler
}

func Open(path string) (*Store, error) {
	abs, err := filepathAbs(path)
	if err != nil {
		return nil, err
	}
	dsn := "file:" + filepathToSlash(abs) +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One logical writer: serializes Command transactions and keeps the WAL
	// recovery boundary simple. Reads share the same connection.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, handlers: map[string]Handler{}}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Register(action string, h Handler) {
	if h == nil {
		delete(s.handlers, action)
		return
	}
	s.handlers[action] = h
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS timeline (
			world_id TEXT NOT NULL, timeline_id TEXT NOT NULL, timeline_epoch TEXT NOT NULL,
			event_seq INTEGER NOT NULL, state_seq INTEGER NOT NULL,
			state_json BLOB NOT NULL, state_hash TEXT NOT NULL,
			PRIMARY KEY (world_id, timeline_id, timeline_epoch))`,
		`CREATE TABLE IF NOT EXISTS snapshot (
			world_id TEXT NOT NULL, timeline_id TEXT NOT NULL, timeline_epoch TEXT NOT NULL,
			seq INTEGER NOT NULL, state_json BLOB NOT NULL, state_hash TEXT NOT NULL,
			PRIMARY KEY (world_id, timeline_id, timeline_epoch, seq))`,
		`CREATE TABLE IF NOT EXISTS txn (
			world_id TEXT NOT NULL, timeline_id TEXT NOT NULL, timeline_epoch TEXT NOT NULL,
			txn_id TEXT NOT NULL, first_seq INTEGER NOT NULL, last_seq INTEGER NOT NULL,
			receipt_id TEXT NOT NULL, delta_json BLOB NOT NULL, state_hash TEXT NOT NULL,
			recorded_at TEXT NOT NULL,
			PRIMARY KEY (world_id, timeline_id, timeline_epoch, txn_id),
			UNIQUE (world_id, timeline_id, timeline_epoch, first_seq))`,
		`CREATE TABLE IF NOT EXISTS event (
			world_id TEXT NOT NULL, timeline_id TEXT NOT NULL, timeline_epoch TEXT NOT NULL,
			seq INTEGER NOT NULL, txn_id TEXT NOT NULL, event_id TEXT NOT NULL, kind TEXT NOT NULL,
			payload BLOB NOT NULL, recorded_at TEXT NOT NULL,
			PRIMARY KEY (world_id, timeline_id, timeline_epoch, seq),
			UNIQUE (world_id, timeline_id, timeline_epoch, event_id))`,
		`CREATE TABLE IF NOT EXISTS receipt (
			receipt_id TEXT PRIMARY KEY,
			world_id TEXT NOT NULL, timeline_id TEXT NOT NULL, timeline_epoch TEXT NOT NULL,
			client_id TEXT NOT NULL, idem_key TEXT NOT NULL, digest TEXT NOT NULL,
			status TEXT NOT NULL, reason TEXT NOT NULL, txn_id TEXT NOT NULL,
			event_ids TEXT NOT NULL, first_seq INTEGER, last_seq INTEGER, state_seq INTEGER,
			recorded_at TEXT NOT NULL,
			UNIQUE (world_id, timeline_id, timeline_epoch, client_id, idem_key))`,
		`CREATE TABLE IF NOT EXISTS resource (
			world_id TEXT NOT NULL, timeline_id TEXT NOT NULL, timeline_epoch TEXT NOT NULL,
			resource_id TEXT NOT NULL, capacity INTEGER NOT NULL, used INTEGER NOT NULL,
			PRIMARY KEY (world_id, timeline_id, timeline_epoch, resource_id))`,
		`CREATE TABLE IF NOT EXISTS reservation (
			world_id TEXT NOT NULL, timeline_id TEXT NOT NULL, timeline_epoch TEXT NOT NULL,
			resource_id TEXT NOT NULL, actor_id TEXT NOT NULL, activity_id TEXT NOT NULL,
			quantity INTEGER NOT NULL,
			PRIMARY KEY (world_id, timeline_id, timeline_epoch, resource_id, actor_id, activity_id))`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateTimeline(scope Scope, initial State, resources map[string]int64) error {
	if !scope.valid() {
		return ErrInvalidCommand
	}
	state := normalizeState(initial)
	for id, capacity := range resources {
		if id == "" || capacity < 0 {
			return ErrInvalidCommand
		}
		state.Resources[id] = ResourceState{Capacity: capacity, Used: state.Resources[id].Used}
	}
	state.Reservations = nil
	hash, raw, err := stateHash(state)
	if err != nil {
		return err
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO timeline
		(world_id, timeline_id, timeline_epoch, event_seq, state_seq, state_json, state_hash)
		VALUES (?, ?, ?, 0, 0, ?, ?)`, scope.WorldID, scope.TimelineID, scope.TimelineEpoch, raw, hash); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrTimelineExists
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO snapshot
		(world_id, timeline_id, timeline_epoch, seq, state_json, state_hash) VALUES (?, ?, ?, 0, ?, ?)`,
		scope.WorldID, scope.TimelineID, scope.TimelineEpoch, raw, hash); err != nil {
		return err
	}
	for id, r := range state.Resources {
		if _, err := tx.ExecContext(ctx, `INSERT INTO resource
			(world_id, timeline_id, timeline_epoch, resource_id, capacity, used) VALUES (?, ?, ?, ?, ?, ?)`,
			scope.WorldID, scope.TimelineID, scope.TimelineEpoch, id, r.Capacity, r.Used); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Append(cmd Command) (Result, error) {
	if !cmd.Scope.valid() || cmd.ClientID == "" || cmd.IdempotencyKey == "" ||
		strings.TrimSpace(cmd.ClientID) != cmd.ClientID || strings.TrimSpace(cmd.IdempotencyKey) != cmd.IdempotencyKey ||
		cmd.Action == "" || !utf8.Valid(cmd.Payload) {
		return Result{}, ErrInvalidCommand
	}
	if len(cmd.Payload) == 0 {
		cmd.Payload = json.RawMessage(`{}`)
	}
	if len(cmd.Payload) > MaxCommandBytes {
		return Result{}, ErrLimitExceeded
	}
	digest, err := commandDigest(cmd)
	if err != nil {
		return Result{}, ErrInvalidCommand
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()

	if existing, ok, err := lookupReceipt(ctx, tx, cmd, digest); err != nil {
		return Result{}, err
	} else if ok {
		return Result{Receipt: existing, Duplicate: true}, nil
	}

	state, stateSeq, _, err := readTimeline(ctx, tx, cmd.Scope)
	if err != nil {
		return Result{}, err
	}

	reject := func(reason string) (Result, error) {
		r := Receipt{
			ID: newID("rcp"), Status: "rejected", Reason: reason,
			EventIDs: []string{}, StateSeq: stateSeq, BodyDigest: digest, RecordedAt: cmd.RecordedAt,
		}
		if err := insertReceipt(ctx, tx, cmd, r); err != nil {
			return Result{}, err
		}
		if err := tx.Commit(); err != nil {
			return Result{}, err
		}
		return Result{Receipt: r}, nil
	}

	if cmd.ExpectedStateSeq != nil && *cmd.ExpectedStateSeq != stateSeq {
		return reject("state_conflict")
	}
	handler, ok := s.handlers[cmd.Action]
	if !ok {
		return reject("unknown_action")
	}
	before := cloneState(state)
	outcome, err := handler(state, cmd.Payload)
	if err != nil {
		return reject("handler_error")
	}
	if len(outcome.Events) > MaxCommandEvents {
		return Result{}, ErrLimitExceeded
	}
	size := 0
	for _, ev := range outcome.Events {
		if ev.Kind == "" || !utf8.Valid([]byte(ev.Kind)) {
			return Result{}, ErrInvalidCommand
		}
		if len(ev.Payload) == 0 {
			ev.Payload = json.RawMessage(`{}`)
		}
		if !json.Valid(ev.Payload) {
			return Result{}, ErrInvalidCommand
		}
		size += len(ev.Kind) + len(ev.Payload)
	}
	if size > MaxCommandBytes {
		return Result{}, ErrLimitExceeded
	}

	after, reservations, reason, err := applyReservations(before, outcome)
	if err != nil {
		return Result{}, err
	}
	if reason != "" {
		return reject(reason)
	}
	after.Reservations = reservations
	afterHash, afterRaw, err := stateHash(after)
	if err != nil {
		return Result{}, err
	}
	delta, err := diffState(before, after)
	if err != nil {
		return Result{}, err
	}

	first := stateSeq + 1
	last := stateSeq + uint64(len(outcome.Events))
	txnID := newID("txn")
	receiptID := newID("rcp")
	eventIDs := make([]string, 0, len(outcome.Events))
	for i, ev := range outcome.Events {
		seq := first + uint64(i)
		eventID := newID("evt")
		eventIDs = append(eventIDs, eventID)
		if _, err := tx.ExecContext(ctx, `INSERT INTO event
			(world_id, timeline_id, timeline_epoch, seq, txn_id, event_id, kind, payload, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cmd.Scope.WorldID, cmd.Scope.TimelineID, cmd.Scope.TimelineEpoch, seq, txnID, eventID,
			ev.Kind, []byte(ev.Payload), cmd.RecordedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return Result{}, err
		}
	}
	hook("events")

	if _, err := tx.ExecContext(ctx, `UPDATE timeline SET event_seq = ?, state_seq = ?, state_json = ?, state_hash = ?
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ?`,
		last, last, afterRaw, afterHash,
		cmd.Scope.WorldID, cmd.Scope.TimelineID, cmd.Scope.TimelineEpoch); err != nil {
		return Result{}, err
	}
	if err := writeReservations(ctx, tx, cmd.Scope, state.Reservations, reservations); err != nil {
		return Result{}, err
	}
	for id, r := range after.Resources {
		if _, err := tx.ExecContext(ctx, `UPDATE resource SET capacity = ?, used = ?
			WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ? AND resource_id = ?`,
			r.Capacity, r.Used,
			cmd.Scope.WorldID, cmd.Scope.TimelineID, cmd.Scope.TimelineEpoch, id); err != nil {
			return Result{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO txn
		(world_id, timeline_id, timeline_epoch, txn_id, first_seq, last_seq, receipt_id, delta_json, state_hash, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cmd.Scope.WorldID, cmd.Scope.TimelineID, cmd.Scope.TimelineEpoch, txnID, first, last, receiptID,
		delta, afterHash, cmd.RecordedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return Result{}, err
	}
	hook("state")

	receipt := Receipt{
		ID: receiptID, Status: "committed", TransactionID: txnID, EventIDs: eventIDs,
		FirstSeq: first, LastSeq: last, StateSeq: last, BodyDigest: digest, RecordedAt: cmd.RecordedAt,
	}
	if err := insertReceipt(ctx, tx, cmd, receipt); err != nil {
		return Result{}, err
	}
	hook("receipt")
	hook("commit")
	if err := tx.Commit(); err != nil {
		return Result{}, err
	}
	return Result{Receipt: receipt}, nil
}

func (s *Store) Snapshot(scope Scope) (State, uint64, string, error) {
	ctx := context.Background()
	state, seq, hash, err := readTimeline(ctx, s.db, scope)
	if err != nil {
		return State{}, 0, "", err
	}
	return state, seq, hash, nil
}

func (s *Store) Replay(scope Scope, uptoSeq uint64) (State, string, uint64, error) {
	ctx := context.Background()
	_, liveSeq, _, err := readTimeline(ctx, s.db, scope)
	if err != nil {
		return State{}, "", 0, err
	}
	if uptoSeq > liveSeq {
		return State{}, "", 0, ErrSeqOutOfRange
	}
	var snapSeq uint64
	var raw []byte
	var snapHash string
	row := s.db.QueryRowContext(ctx, `SELECT seq, state_json, state_hash FROM snapshot
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ? AND seq <= ?
		ORDER BY seq DESC LIMIT 1`,
		scope.WorldID, scope.TimelineID, scope.TimelineEpoch, uptoSeq)
	if err := row.Scan(&snapSeq, &raw, &snapHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return State{}, "", 0, ErrTimelineNotFound
		}
		return State{}, "", 0, err
	}
	state, err := decodeState(raw)
	if err != nil {
		return State{}, "", 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT last_seq, delta_json FROM txn
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ? AND last_seq > ? AND last_seq <= ?
		ORDER BY first_seq`, scope.WorldID, scope.TimelineID, scope.TimelineEpoch, snapSeq, uptoSeq)
	if err != nil {
		return State{}, "", 0, err
	}
	defer rows.Close()
	applied := snapSeq
	for rows.Next() {
		var last uint64
		var deltaRaw []byte
		if err := rows.Scan(&last, &deltaRaw); err != nil {
			return State{}, "", 0, err
		}
		if err := applyDelta(&state, deltaRaw); err != nil {
			return State{}, "", 0, err
		}
		applied = last
	}
	if err := rows.Err(); err != nil {
		return State{}, "", 0, err
	}
	hash, _, err := stateHash(state)
	if err != nil {
		return State{}, "", 0, err
	}
	if applied == snapSeq {
		hash = snapHash
	}
	return state, hash, applied, nil
}

func (s *Store) Page(scope Scope, afterSeq uint64, maxEvents, maxBytes int) (Page, error) {
	if maxEvents <= 0 || maxEvents > MaxPageEvents {
		maxEvents = MaxPageEvents
	}
	if maxBytes <= 0 {
		maxBytes = 2 * 1024 * 1024
	}
	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx, `SELECT first_seq, last_seq FROM txn
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ? AND first_seq > ?
		ORDER BY first_seq`, scope.WorldID, scope.TimelineID, scope.TimelineEpoch, afterSeq)
	if err != nil {
		return Page{}, err
	}
	type span struct{ first, last uint64 }
	var spans []span
	for rows.Next() {
		var sp span
		if err := rows.Scan(&sp.first, &sp.last); err != nil {
			rows.Close()
			return Page{}, err
		}
		spans = append(spans, sp)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Page{}, err
	}
	rows.Close()
	if len(spans) == 0 {
		return Page{Done: true, NextSeq: afterSeq}, nil
	}
	var chosen []span
	count, bytes := 0, 0
	for _, sp := range spans {
		n := int(sp.last - sp.first + 1)
		sz, err := s.eventBytes(ctx, scope, sp.first, sp.last)
		if err != nil {
			return Page{}, err
		}
		if len(chosen) > 0 && (count+n > maxEvents || bytes+sz > maxBytes) {
			break
		}
		chosen = append(chosen, sp)
		count += n
		bytes += sz
	}
	page := Page{NextSeq: afterSeq}
	for _, sp := range chosen {
		rows, err := s.db.QueryContext(ctx, `SELECT seq, txn_id, event_id, kind, payload, recorded_at FROM event
			WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ? AND seq >= ? AND seq <= ? ORDER BY seq`,
			scope.WorldID, scope.TimelineID, scope.TimelineEpoch, sp.first, sp.last)
		if err != nil {
			return Page{}, err
		}
		for rows.Next() {
			var ev Event
			var payload []byte
			var recorded string
			if err := rows.Scan(&ev.Seq, &ev.TransactionID, &ev.EventID, &ev.Kind, &payload, &recorded); err != nil {
				rows.Close()
				return Page{}, err
			}
			ev.Payload = append(json.RawMessage(nil), payload...)
			ev.RecordedAt, _ = time.Parse(time.RFC3339Nano, recorded)
			page.Events = append(page.Events, ev)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return Page{}, err
		}
		rows.Close()
		page.NextSeq = sp.last
	}
	page.Done = page.NextSeq >= spans[len(spans)-1].last
	return page, nil
}

func (s *Store) eventBytes(ctx context.Context, scope Scope, first, last uint64) (int, error) {
	var total sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT SUM(LENGTH(kind) + LENGTH(payload) + 64) FROM event
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ? AND seq >= ? AND seq <= ?`,
		scope.WorldID, scope.TimelineID, scope.TimelineEpoch, first, last).Scan(&total)
	if err != nil {
		return 0, err
	}
	return int(total.Int64), nil
}

func readTimeline(ctx context.Context, q queryer, scope Scope) (State, uint64, string, error) {
	var raw []byte
	var seq uint64
	var hash string
	err := q.QueryRowContext(ctx, `SELECT state_json, state_seq, state_hash FROM timeline
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ?`,
		scope.WorldID, scope.TimelineID, scope.TimelineEpoch).Scan(&raw, &seq, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, 0, "", ErrTimelineNotFound
	}
	if err != nil {
		return State{}, 0, "", err
	}
	state, err := decodeState(raw)
	if err != nil {
		return State{}, 0, "", err
	}
	return state, seq, hash, nil
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func lookupReceipt(ctx context.Context, tx *sql.Tx, cmd Command, digest string) (Receipt, bool, error) {
	var r Receipt
	var eventIDs string
	var first, last, stateSeq sql.NullInt64
	var recorded string
	err := tx.QueryRowContext(ctx, `SELECT receipt_id, status, reason, txn_id, event_ids, first_seq, last_seq, state_seq, digest, recorded_at
		FROM receipt WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ? AND client_id = ? AND idem_key = ?`,
		cmd.Scope.WorldID, cmd.Scope.TimelineID, cmd.Scope.TimelineEpoch, cmd.ClientID, cmd.IdempotencyKey).
		Scan(&r.ID, &r.Status, &r.Reason, &r.TransactionID, &eventIDs, &first, &last, &stateSeq, &r.BodyDigest, &recorded)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, false, nil
	}
	if err != nil {
		return Receipt{}, false, err
	}
	if r.BodyDigest != digest {
		return Receipt{}, false, ErrIdempotencyConflict
	}
	if err := json.Unmarshal([]byte(eventIDs), &r.EventIDs); err != nil {
		return Receipt{}, false, err
	}
	r.FirstSeq = uint64(first.Int64)
	r.LastSeq = uint64(last.Int64)
	r.StateSeq = uint64(stateSeq.Int64)
	r.RecordedAt, _ = time.Parse(time.RFC3339Nano, recorded)
	return r, true, nil
}

func insertReceipt(ctx context.Context, tx *sql.Tx, cmd Command, r Receipt) error {
	eventIDs, err := json.Marshal(r.EventIDs)
	if err != nil {
		return err
	}
	var first, last, stateSeq any
	if r.Status == "committed" {
		first, last, stateSeq = r.FirstSeq, r.LastSeq, r.StateSeq
	} else {
		first, last, stateSeq = nil, nil, r.StateSeq
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO receipt
		(receipt_id, world_id, timeline_id, timeline_epoch, client_id, idem_key, digest, status, reason, txn_id, event_ids, first_seq, last_seq, state_seq, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, cmd.Scope.WorldID, cmd.Scope.TimelineID, cmd.Scope.TimelineEpoch, cmd.ClientID, cmd.IdempotencyKey,
		r.BodyDigest, r.Status, r.Reason, r.TransactionID, string(eventIDs), first, last, stateSeq,
		cmd.RecordedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func applyReservations(before State, out Outcome) (State, []Reservation, string, error) {
	after := normalizeState(out.State)
	held := map[reservationKey]int64{}
	for _, r := range before.Reservations {
		held[reservationKey{r.ResourceID, r.ActorID, r.ActivityID}] += r.Quantity
	}
	reservedByResource := map[string]int64{}
	for k, q := range held {
		reservedByResource[k.resource] += q
	}
	used := map[string]int64{}
	capacity := map[string]int64{}
	for id, r := range before.Resources {
		used[id] = r.Used
		capacity[id] = r.Capacity
	}
	add := func(list []Reservation, delta int64, consume bool) string {
		for _, r := range list {
			if r.ResourceID == "" || r.ActorID == "" || r.ActivityID == "" || r.Quantity <= 0 {
				return "invalid_reservation"
			}
			if _, ok := capacity[r.ResourceID]; !ok {
				return "unknown_resource"
			}
			key := reservationKey{r.ResourceID, r.ActorID, r.ActivityID}
			switch {
			case delta > 0:
				if used[r.ResourceID]+reservedByResource[r.ResourceID]+r.Quantity > capacity[r.ResourceID] {
					return "resource_conflict"
				}
				held[key] += r.Quantity
				reservedByResource[r.ResourceID] += r.Quantity
			default:
				if held[key] < r.Quantity {
					return "reservation_missing"
				}
				held[key] -= r.Quantity
				reservedByResource[r.ResourceID] -= r.Quantity
				if consume {
					used[r.ResourceID] += r.Quantity
				}
			}
		}
		return ""
	}
	if reason := add(out.Reserve, 1, false); reason != "" {
		return State{}, nil, reason, nil
	}
	if reason := add(out.Release, -1, false); reason != "" {
		return State{}, nil, reason, nil
	}
	if reason := add(out.Consume, -1, true); reason != "" {
		return State{}, nil, reason, nil
	}
	for id, r := range after.Resources {
		if u, ok := used[id]; ok {
			r.Used = u
		}
		if c, ok := capacity[id]; ok {
			r.Capacity = c
		}
		after.Resources[id] = r
	}
	for id, u := range used {
		if _, ok := after.Resources[id]; !ok {
			after.Resources[id] = ResourceState{Capacity: capacity[id], Used: u}
		} else if after.Resources[id].Used != u {
			r := after.Resources[id]
			r.Used = u
			after.Resources[id] = r
		}
	}
	var reservations []Reservation
	for k, q := range held {
		if q > 0 {
			reservations = append(reservations, Reservation{
				ResourceID: k.resource, ActorID: k.actor, ActivityID: k.activity, Quantity: q,
			})
		}
	}
	sortReservations(reservations)
	return after, reservations, "", nil
}

type reservationKey struct {
	resource string
	actor    string
	activity string
}

func writeReservations(ctx context.Context, tx *sql.Tx, scope Scope, before, after []Reservation) error {
	beforeSet := map[reservationKey]int64{}
	for _, r := range before {
		beforeSet[reservationKey{r.ResourceID, r.ActorID, r.ActivityID}] = r.Quantity
	}
	afterSet := map[reservationKey]int64{}
	for _, r := range after {
		afterSet[reservationKey{r.ResourceID, r.ActorID, r.ActivityID}] = r.Quantity
	}
	for k, q := range afterSet {
		if beforeSet[k] == q {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO reservation
			(world_id, timeline_id, timeline_epoch, resource_id, actor_id, activity_id, quantity)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(world_id, timeline_id, timeline_epoch, resource_id, actor_id, activity_id)
			DO UPDATE SET quantity = excluded.quantity`,
			scope.WorldID, scope.TimelineID, scope.TimelineEpoch, k.resource, k.actor, k.activity, q); err != nil {
			return err
		}
	}
	for k := range beforeSet {
		if _, ok := afterSet[k]; ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM reservation
			WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ? AND resource_id = ? AND actor_id = ? AND activity_id = ?`,
			scope.WorldID, scope.TimelineID, scope.TimelineEpoch, k.resource, k.actor, k.activity); err != nil {
			return err
		}
	}
	return nil
}

// delta is the saved State change of one transaction. A nil map value deletes the
// key; Reservations replaces the whole sorted list.
type delta struct {
	Actors       map[string]*ActorState    `json:"actors,omitempty"`
	Activities   map[string]*ActivityState `json:"activities,omitempty"`
	Resources    map[string]*ResourceState `json:"resources,omitempty"`
	Reservations *[]Reservation            `json:"reservations,omitempty"`
}

func diffState(before, after State) ([]byte, error) {
	d := delta{
		Actors:     map[string]*ActorState{},
		Activities: map[string]*ActivityState{},
		Resources:  map[string]*ResourceState{},
	}
	for k, v := range after.Actors {
		if prev, ok := before.Actors[k]; !ok || prev != v {
			vv := v
			d.Actors[k] = &vv
		}
	}
	for k := range before.Actors {
		if _, ok := after.Actors[k]; !ok {
			d.Actors[k] = nil
		}
	}
	for k, v := range after.Activities {
		if prev, ok := before.Activities[k]; !ok || prev != v {
			vv := v
			d.Activities[k] = &vv
		}
	}
	for k := range before.Activities {
		if _, ok := after.Activities[k]; !ok {
			d.Activities[k] = nil
		}
	}
	for k, v := range after.Resources {
		if prev, ok := before.Resources[k]; !ok || prev != v {
			vv := v
			d.Resources[k] = &vv
		}
	}
	for k := range before.Resources {
		if _, ok := after.Resources[k]; !ok {
			d.Resources[k] = nil
		}
	}
	res := append([]Reservation(nil), after.Reservations...)
	d.Reservations = &res
	return json.Marshal(d)
}

func applyDelta(state *State, raw []byte) error {
	var d delta
	if err := json.Unmarshal(raw, &d); err != nil {
		return err
	}
	state.Actors = applyMap(state.Actors, d.Actors)
	state.Activities = applyMap(state.Activities, d.Activities)
	state.Resources = applyMap(state.Resources, d.Resources)
	if d.Reservations != nil {
		state.Reservations = append([]Reservation(nil), (*d.Reservations)...)
		sortReservations(state.Reservations)
	}
	return nil
}

func applyMap[T any](base map[string]T, changes map[string]*T) map[string]T {
	if base == nil {
		base = map[string]T{}
	}
	for k, v := range changes {
		if v == nil {
			delete(base, k)
			continue
		}
		base[k] = *v
	}
	return base
}

func cloneState(s State) State {
	out := State{
		Actors:       make(map[string]ActorState, len(s.Actors)),
		Activities:   make(map[string]ActivityState, len(s.Activities)),
		Resources:    make(map[string]ResourceState, len(s.Resources)),
		Reservations: append([]Reservation(nil), s.Reservations...),
	}
	for k, v := range s.Actors {
		out.Actors[k] = v
	}
	for k, v := range s.Activities {
		out.Activities[k] = v
	}
	for k, v := range s.Resources {
		out.Resources[k] = v
	}
	return out
}

func normalizeState(s State) State {
	if s.Actors == nil {
		s.Actors = map[string]ActorState{}
	}
	if s.Activities == nil {
		s.Activities = map[string]ActivityState{}
	}
	if s.Resources == nil {
		s.Resources = map[string]ResourceState{}
	}
	s.Reservations = append([]Reservation(nil), s.Reservations...)
	sortReservations(s.Reservations)
	return s
}

func sortReservations(rs []Reservation) {
	sort.Slice(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.ResourceID != b.ResourceID {
			return a.ResourceID < b.ResourceID
		}
		if a.ActorID != b.ActorID {
			return a.ActorID < b.ActorID
		}
		return a.ActivityID < b.ActivityID
	})
}

func stateHash(s State) (string, []byte, error) {
	raw, err := json.Marshal(normalizeState(s))
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), raw, nil
}

func decodeState(raw []byte) (State, error) {
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return State{}, err
	}
	return normalizeState(s), nil
}

func commandDigest(cmd Command) (string, error) {
	var payload any
	dec := json.NewDecoder(strings.NewReader(string(cmd.Payload)))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return "", err
	}
	var expected any
	if cmd.ExpectedStateSeq != nil {
		expected = *cmd.ExpectedStateSeq
	}
	raw, err := json.Marshal(struct {
		Action   string `json:"action"`
		Payload  any    `json:"payload"`
		Expected any    `json:"expected_state_seq"`
	}{cmd.Action, payload, expected})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func newID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b)
}

func hook(point string) {
	if transactionHook != nil {
		transactionHook(point)
	}
}

func filepathAbs(path string) (string, error) {
	if path == "" {
		return "", ErrInvalidCommand
	}
	return filepath.Abs(path)
}

func filepathToSlash(path string) string {
	p := strings.ReplaceAll(path, `\`, `/`)
	if len(p) >= 2 && p[1] == ':' {
		return p
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}
