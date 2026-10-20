package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	projectionpkg "github.com/Nyukimin/RenCrow_WORLD/internal/projection"
)

const MaxProjectionBytes = projectionpkg.MaxEnvelopeBytes

const (
	ProjectionKindWorkSet    = projectionpkg.KindWorkSet
	ProjectionKindLifePolicy = projectionpkg.KindLifePolicy

	ProjectionAccepted               = "accepted"
	ProjectionDuplicate              = "duplicate"
	ProjectionStaleRevision          = "stale_revision"
	ProjectionRevisionConflict       = "revision_conflict"
	ProjectionEpochSuperseded        = "epoch_superseded"
	ProjectionEpochRequiresReconcile = "epoch_requires_reconcile"
	ProjectionEpochUnchanged         = "epoch_unchanged"
)

var (
	ErrInvalidProjection       = errors.New("invalid projection")
	ErrProjectionNotFound      = errors.New("projection not found")
	ErrProjectionActorUnbound  = errors.New("projection actor is not bound")
	ErrProjectionActorConflict = errors.New("projection actor mapping conflict")
)

// Projection is a validated CORE projection input. It is deliberately separate
// from Command: no Command idempotency key, expected_state_seq, or Receipt is
// used, and a projection never creates a WORLD Event by itself.
type Projection struct {
	Scope           Scope
	ActorID         string
	ActorInstanceID string
	Kind            string
	SourceEpoch     string
	SourceRevision  uint64
	Payload         json.RawMessage
	ReceivedAt      time.Time
}

// ProjectionAck is the projection contract result. Status is one of the
// Projection* constants; semantic conflicts are acknowledgements, not Command
// Receipts.
type ProjectionAck struct {
	ID              string
	Status          string
	Reason          string
	ActorID         string
	ActorInstanceID string
	Kind            string
	SourceEpoch     string
	SourceRevision  uint64
	Digest          string
	AcceptedAt      time.Time
	RecordedAt      time.Time
}

// ProjectionHead is the latest accepted full projection for one destination,
// Actor, and projection kind.
type ProjectionHead struct {
	Scope           Scope
	ActorID         string
	ActorInstanceID string
	Kind            string
	SourceEpoch     string
	SourceRevision  uint64
	Digest          string
	Payload         json.RawMessage
	AcceptedAt      time.Time
}

type projectionHeadRow struct {
	actorInstanceID string
	sourceEpoch     string
	sourceRevision  uint64
	digest          string
	payload         []byte
	acceptedAt      time.Time
}

// BindProjectionActor records the operator/CORE-authorized ActorID to
// ActorInstanceID mapping for a World/Timeline. It is idempotent for the exact
// mapping and rejects a second mapping for either side.
func (s *Store) BindProjectionActor(scope Scope, actorID, actorInstanceID string, at time.Time) error {
	if !scope.valid() || !validExact(actorID, 256) || !validExact(actorInstanceID, 256) || at.IsZero() {
		return ErrInvalidProjection
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT actor_instance_id FROM projection_actor
		WHERE world_id = ? AND timeline_id = ? AND actor_id = ?`,
		scope.WorldID, scope.TimelineID, actorID).Scan(&existing)
	switch {
	case err == nil && existing == actorInstanceID:
		return tx.Commit()
	case err == nil:
		return ErrProjectionActorConflict
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO projection_actor
		(world_id, timeline_id, actor_id, actor_instance_id, bound_at) VALUES (?, ?, ?, ?, ?)`,
		scope.WorldID, scope.TimelineID, actorID, actorInstanceID, at.UTC().Format(time.RFC3339Nano)); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrProjectionActorConflict
		}
		return err
	}
	return tx.Commit()
}

// ApplyProjection accepts one normal full projection. A new source_epoch is
// rejected until ReconcileProjection authenticates the switch.
func (s *Store) ApplyProjection(p Projection) (ProjectionAck, error) {
	return s.applyProjection(p, false)
}

// ReconcileProjection is the authenticated full-projection epoch switch. It
// retires the current source_epoch for this destination/Actor/kind and rejects
// later projections from that retired epoch. It is idempotent for the current
// epoch (status epoch_unchanged).
func (s *Store) ReconcileProjection(p Projection) (ProjectionAck, error) {
	return s.applyProjection(p, true)
}

func (s *Store) applyProjection(p Projection, reconcile bool) (ProjectionAck, error) {
	if err := validateProjection(p); err != nil {
		return ProjectionAck{}, err
	}
	digest, err := projectionpkg.PayloadDigest(p.Payload)
	if err != nil {
		return ProjectionAck{}, ErrInvalidProjection
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectionAck{}, err
	}
	defer tx.Rollback()

	_, supersededBy, err := readEpochStatus(ctx, tx, p.Scope)
	if err != nil {
		return ProjectionAck{}, err
	}
	if supersededBy != "" {
		return ProjectionAck{}, ErrEpochSuperseded
	}
	boundInstance, err := lookupProjectionBinding(ctx, tx, p.Scope, p.ActorID)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectionAck{}, ErrProjectionActorUnbound
	}
	if err != nil {
		return ProjectionAck{}, err
	}
	if boundInstance != p.ActorInstanceID {
		return ProjectionAck{}, ErrProjectionActorConflict
	}

	head, found, err := lookupProjectionHead(ctx, tx, p.Scope, p.ActorID, p.Kind)
	if err != nil {
		return ProjectionAck{}, err
	}
	retired, err := projectionEpochRetired(ctx, tx, p.Scope, p.ActorID, p.Kind, p.SourceEpoch)
	if err != nil {
		return ProjectionAck{}, err
	}
	ack := ProjectionAck{
		ActorID: p.ActorID, ActorInstanceID: p.ActorInstanceID, Kind: p.Kind,
		SourceEpoch: p.SourceEpoch, SourceRevision: p.SourceRevision, Digest: digest,
		RecordedAt: p.ReceivedAt.UTC(),
	}
	reject := func(status, reason string) (ProjectionAck, error) {
		ack.Status, ack.Reason = status, reason
		if status == ProjectionDuplicate && found {
			ack.AcceptedAt = head.acceptedAt
		}
		ack.ID = newID("prj")
		if err := insertProjectionAck(ctx, tx, p, ack); err != nil {
			return ProjectionAck{}, err
		}
		if err := tx.Commit(); err != nil {
			return ProjectionAck{}, err
		}
		return ack, nil
	}

	if reconcile {
		if !found {
			return ProjectionAck{}, ErrProjectionNotFound
		}
		if head.sourceEpoch == p.SourceEpoch {
			return reject(ProjectionEpochUnchanged, "the source epoch is already current")
		}
		if retired {
			return reject(ProjectionEpochSuperseded, "the source epoch was already retired")
		}
	} else {
		switch {
		case !found:
			// The first projection establishes the current source epoch.
		case retired:
			return reject(ProjectionEpochSuperseded, "the source epoch was retired by reconciliation")
		case head.sourceEpoch != p.SourceEpoch:
			return reject(ProjectionEpochRequiresReconcile, "a source epoch change requires authenticated reconciliation")
		case p.SourceRevision < head.sourceRevision:
			return reject(ProjectionStaleRevision, "the source revision is older than the accepted head")
		case p.SourceRevision == head.sourceRevision && digest == head.digest:
			return reject(ProjectionDuplicate, "the same source revision and digest was already accepted")
		case p.SourceRevision == head.sourceRevision:
			return reject(ProjectionRevisionConflict, "the same source revision has a different payload digest")
		}
	}

	if reconcile {
		if _, err := tx.ExecContext(ctx, `INSERT INTO projection_epoch_history
			(world_id, timeline_id, timeline_epoch, actor_id, projection_kind, source_epoch, retired_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.Scope.WorldID, p.Scope.TimelineID, p.Scope.TimelineEpoch, p.ActorID, p.Kind,
			head.sourceEpoch, p.ReceivedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return ProjectionAck{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO projection_reconcile
			(world_id, timeline_id, timeline_epoch, actor_id, projection_kind, previous_epoch, current_epoch, source_revision, digest, reconciled_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.Scope.WorldID, p.Scope.TimelineID, p.Scope.TimelineEpoch, p.ActorID, p.Kind,
			head.sourceEpoch, p.SourceEpoch, p.SourceRevision, digest, p.ReceivedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return ProjectionAck{}, err
		}
	}
	if err := upsertProjectionHead(ctx, tx, p, digest); err != nil {
		return ProjectionAck{}, err
	}
	ack.ID = newID("prj")
	ack.Status = ProjectionAccepted
	ack.AcceptedAt = p.ReceivedAt.UTC()
	if err := insertProjectionAck(ctx, tx, p, ack); err != nil {
		return ProjectionAck{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProjectionAck{}, err
	}
	return ack, nil
}

// ProjectionHead returns the accepted head for one destination/Actor/kind.
func (s *Store) ProjectionHead(scope Scope, actorID, kind string) (ProjectionHead, error) {
	if !scope.valid() || !validExact(actorID, 256) || !validProjectionKind(kind) {
		return ProjectionHead{}, ErrInvalidProjection
	}
	head, found, err := lookupProjectionHead(context.Background(), s.db, scope, actorID, kind)
	if err != nil {
		return ProjectionHead{}, err
	}
	if !found {
		return ProjectionHead{}, ErrProjectionNotFound
	}
	return ProjectionHead{
		Scope: scope, ActorID: actorID, ActorInstanceID: head.actorInstanceID, Kind: kind,
		SourceEpoch: head.sourceEpoch, SourceRevision: head.sourceRevision, Digest: head.digest,
		Payload: append(json.RawMessage(nil), head.payload...), AcceptedAt: head.acceptedAt,
	}, nil
}

func validateProjection(p Projection) error {
	if !p.Scope.valid() || !validExact(p.ActorID, 256) || !validExact(p.ActorInstanceID, 256) ||
		!validProjectionKind(p.Kind) || !validExact(p.SourceEpoch, 256) ||
		p.SourceRevision < 1 || p.SourceRevision > projectionpkg.MaxSourceRevision || p.ReceivedAt.IsZero() {
		return ErrInvalidProjection
	}
	if len(p.Payload) == 0 || !utf8.Valid(p.Payload) {
		return ErrInvalidProjection
	}
	if len(p.Payload) > MaxProjectionBytes {
		return ErrLimitExceeded
	}
	if !json.Valid(p.Payload) || !strings.HasPrefix(strings.TrimSpace(string(p.Payload)), "{") {
		return ErrInvalidProjection
	}
	return nil
}

func validProjectionKind(kind string) bool {
	return kind == ProjectionKindWorkSet || kind == ProjectionKindLifePolicy
}

func lookupProjectionBinding(ctx context.Context, q queryer, scope Scope, actorID string) (string, error) {
	var instance string
	err := q.QueryRowContext(ctx, `SELECT actor_instance_id FROM projection_actor
		WHERE world_id = ? AND timeline_id = ? AND actor_id = ?`,
		scope.WorldID, scope.TimelineID, actorID).Scan(&instance)
	return instance, err
}

func lookupProjectionHead(ctx context.Context, q queryer, scope Scope, actorID, kind string) (projectionHeadRow, bool, error) {
	var h projectionHeadRow
	var accepted string
	var payload []byte
	err := q.QueryRowContext(ctx, `SELECT actor_instance_id, source_epoch, source_revision, digest, payload, accepted_at
		FROM projection_head WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ?
		AND actor_id = ? AND projection_kind = ?`,
		scope.WorldID, scope.TimelineID, scope.TimelineEpoch, actorID, kind).
		Scan(&h.actorInstanceID, &h.sourceEpoch, &h.sourceRevision, &h.digest, &payload, &accepted)
	if errors.Is(err, sql.ErrNoRows) {
		return projectionHeadRow{}, false, nil
	}
	if err != nil {
		return projectionHeadRow{}, false, err
	}
	h.payload = append([]byte(nil), payload...)
	h.acceptedAt, _ = time.Parse(time.RFC3339Nano, accepted)
	return h, true, nil
}

func projectionEpochRetired(ctx context.Context, q queryer, scope Scope, actorID, kind, sourceEpoch string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM projection_epoch_history
		WHERE world_id = ? AND timeline_id = ? AND timeline_epoch = ?
		AND actor_id = ? AND projection_kind = ? AND source_epoch = ?`,
		scope.WorldID, scope.TimelineID, scope.TimelineEpoch, actorID, kind, sourceEpoch).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func upsertProjectionHead(ctx context.Context, tx *sql.Tx, p Projection, digest string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO projection_head
		(world_id, timeline_id, timeline_epoch, actor_id, actor_instance_id, projection_kind,
		 source_epoch, source_revision, digest, payload, accepted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(world_id, timeline_id, timeline_epoch, actor_id, projection_kind)
		DO UPDATE SET actor_instance_id = excluded.actor_instance_id,
			source_epoch = excluded.source_epoch, source_revision = excluded.source_revision,
			digest = excluded.digest, payload = excluded.payload, accepted_at = excluded.accepted_at`,
		p.Scope.WorldID, p.Scope.TimelineID, p.Scope.TimelineEpoch, p.ActorID, p.ActorInstanceID,
		p.Kind, p.SourceEpoch, p.SourceRevision, digest, []byte(p.Payload),
		p.ReceivedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func insertProjectionAck(ctx context.Context, tx *sql.Tx, p Projection, ack ProjectionAck) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO projection_ack
		(ack_id, world_id, timeline_id, timeline_epoch, actor_id, actor_instance_id, projection_kind,
		 source_epoch, source_revision, digest, status, reason, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ack.ID, p.Scope.WorldID, p.Scope.TimelineID, p.Scope.TimelineEpoch, p.ActorID, p.ActorInstanceID,
		p.Kind, p.SourceEpoch, p.SourceRevision, ack.Digest, ack.Status, ack.Reason,
		p.ReceivedAt.UTC().Format(time.RFC3339Nano))
	return err
}
