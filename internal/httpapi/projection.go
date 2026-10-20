package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/idmap"
	"github.com/Nyukimin/RenCrow_WORLD/internal/projection"
	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

const maxProjectionBody = projection.MaxEnvelopeBytes + 4096

type projectionAckDTO struct {
	AckID           string     `json:"ack_id"`
	Status          string     `json:"status"`
	Reason          string     `json:"reason,omitempty"`
	ActorID         string     `json:"actor_id"`
	ActorInstanceID string     `json:"actor_instance_id"`
	ProjectionKind  string     `json:"projection_kind"`
	SourceEpoch     string     `json:"source_epoch"`
	SourceRevision  uint64     `json:"source_revision"`
	Digest          string     `json:"digest"`
	PublisherID     string     `json:"publisher_id"`
	CredentialID    string     `json:"credential_id"`
	ValidUntil      *time.Time `json:"valid_until,omitempty"`
	AcceptedAt      *time.Time `json:"accepted_at,omitempty"`
	RecordedAt      time.Time  `json:"recorded_at"`
}

type projectionHeadDTO struct {
	ActorID         string     `json:"actor_id"`
	ActorInstanceID string     `json:"actor_instance_id"`
	ProjectionKind  string     `json:"projection_kind"`
	SourceEpoch     string     `json:"source_epoch"`
	SourceRevision  uint64     `json:"source_revision"`
	Digest          string     `json:"digest"`
	ValidUntil      *time.Time `json:"valid_until,omitempty"`
	AcceptedAt      time.Time  `json:"accepted_at"`
}

type projectionHeadBody struct {
	SchemaVersion int               `json:"schema_version"`
	Projection    projectionHeadDTO `json:"projection"`
}

type projectionAckBody struct {
	SchemaVersion int              `json:"schema_version"`
	Projection    projectionAckDTO `json:"projection"`
}

func (s *Server) handleCoreProjection(w http.ResponseWriter, r *http.Request) {
	principal, apiErr := s.authenticate(r)
	if apiErr != nil {
		writeAPIError(w, apiErr.status, apiErr.code, apiErr.message)
		return
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" && !strings.HasPrefix(contentType, "application/json") {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "projections require application/json")
		return
	}
	rawKind := r.PathValue("kind")
	if rawKind != projection.KindWorkSet && rawKind != projection.KindLifePolicy {
		writeAPIError(w, http.StatusBadRequest, "unsupported_projection_kind", "the projection kind is not supported")
		return
	}
	actorID, err := idmap.Decode(r.PathValue("actor"))
	if err != nil || actorID == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_actor_id", "the actor path is not a valid transport ID")
		return
	}
	reconcile, ok := reconcileQuery(r)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "invalid_reconcile", "reconcile must be true or false")
		return
	}
	requiredScope := "projection"
	if reconcile {
		requiredScope = "projection_reconcile"
	}
	if !principal.allows(requiredScope) {
		writeAPIError(w, http.StatusForbidden, "forbidden", "the credential does not allow this projection operation")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxProjectionBody)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "projection_too_large", "the projection body exceeds the limit")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "invalid_projection", "the projection body could not be read")
		return
	}
	if len(raw) > projection.MaxEnvelopeBytes {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "projection_too_large", "the projection envelope exceeds the limit")
		return
	}
	env, err := projection.Decode(raw, s.cfg.Scope.WorldID, s.cfg.Scope.TimelineID, s.cfg.Scope.TimelineEpoch)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_projection", "the projection envelope or payload is invalid")
		return
	}
	if env.ActorID != actorID {
		writeAPIError(w, http.StatusBadRequest, "actor_mismatch", "the path actor does not match the projection payload")
		return
	}
	if env.Kind != rawKind {
		writeAPIError(w, http.StatusBadRequest, "projection_kind_mismatch", "the path kind does not match the projection envelope")
		return
	}

	p := store.Projection{
		Scope:           s.cfg.Scope,
		ActorID:         env.ActorID,
		ActorInstanceID: env.ActorInstanceID,
		Kind:            env.Kind,
		SourceEpoch:     env.SourceEpoch,
		SourceRevision:  env.SourceRevision,
		Payload:         env.Payload,
		PublisherID:     principal.publisherID,
		CredentialID:    principal.credentialID,
		ValidUntil:      env.ValidUntil,
		ReceivedAt:      nowUTC(),
	}
	var ack store.ProjectionAck
	if reconcile {
		ack, err = s.store.ReconcileProjection(p)
	} else {
		ack, err = s.store.ApplyProjection(p)
	}
	if err != nil {
		switch {
		case errors.Is(err, store.ErrProjectionActorUnbound):
			writeAPIError(w, http.StatusForbidden, "actor_not_bound", "the actor has no authorized CORE instance mapping")
		case errors.Is(err, store.ErrProjectionActorConflict):
			writeAPIError(w, http.StatusForbidden, "actor_mapping_conflict", "the actor instance does not match the configured mapping")
		case errors.Is(err, store.ErrEpochSuperseded):
			writeAPIError(w, http.StatusConflict, "epoch_superseded", "the destination timeline epoch is superseded")
		case errors.Is(err, store.ErrProjectionNotFound):
			writeAPIError(w, http.StatusConflict, "projection_not_found", "reconciliation requires an existing projection head")
		case errors.Is(err, store.ErrLimitExceeded):
			writeAPIError(w, http.StatusRequestEntityTooLarge, "projection_too_large", "the projection exceeds the configured limit")
		case errors.Is(err, store.ErrInvalidProjection):
			writeAPIError(w, http.StatusBadRequest, "invalid_projection", "the projection is invalid")
		case errors.Is(err, store.ErrTimelineNotFound):
			writeAPIError(w, http.StatusServiceUnavailable, "not_ready", "the timeline is not available")
		default:
			writeAPIError(w, http.StatusInternalServerError, "internal_error", "the projection could not be recorded")
		}
		return
	}

	status := http.StatusOK
	switch ack.Status {
	case store.ProjectionStaleRevision, store.ProjectionRevisionConflict,
		store.ProjectionEpochSuperseded, store.ProjectionEpochRequiresReconcile,
		store.ProjectionEpochUnchanged:
		status = http.StatusConflict
	}
	var acceptedAt *time.Time
	if !ack.AcceptedAt.IsZero() {
		value := ack.AcceptedAt
		acceptedAt = &value
	}
	writeJSON(w, status, projectionAckBody{
		SchemaVersion: SchemaVersion,
		Projection: projectionAckDTO{
			AckID: ack.ID, Status: ack.Status, Reason: ack.Reason,
			ActorID: ack.ActorID, ActorInstanceID: ack.ActorInstanceID, ProjectionKind: ack.Kind,
			SourceEpoch: ack.SourceEpoch, SourceRevision: ack.SourceRevision, Digest: ack.Digest,
			PublisherID: ack.PublisherID, CredentialID: ack.CredentialID,
			ValidUntil: timePtr(ack.ValidUntil),
			AcceptedAt: acceptedAt, RecordedAt: ack.RecordedAt,
		},
	})
}

func (s *Server) handleProjectionHead(w http.ResponseWriter, r *http.Request) {
	principal, apiErr := s.authenticate(r)
	if apiErr != nil {
		writeAPIError(w, apiErr.status, apiErr.code, apiErr.message)
		return
	}
	if !principal.allows("projection") && !principal.allows("projection_reconcile") {
		writeAPIError(w, http.StatusForbidden, "forbidden", "the credential does not allow projection reads")
		return
	}
	rawKind := r.PathValue("kind")
	if rawKind != projection.KindWorkSet && rawKind != projection.KindLifePolicy {
		writeAPIError(w, http.StatusBadRequest, "unsupported_projection_kind", "the projection kind is not supported")
		return
	}
	actorID, err := idmap.Decode(r.PathValue("actor"))
	if err != nil || actorID == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_actor_id", "the actor path is not a valid transport ID")
		return
	}
	head, err := s.store.ProjectionHead(s.cfg.Scope, actorID, rawKind)
	if errors.Is(err, store.ErrProjectionNotFound) {
		writeAPIError(w, http.StatusNotFound, "projection_not_found", "no accepted projection head exists")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "the projection head could not be read")
		return
	}
	writeJSON(w, http.StatusOK, projectionHeadBody{
		SchemaVersion: SchemaVersion,
		Projection: projectionHeadDTO{
			ActorID: head.ActorID, ActorInstanceID: head.ActorInstanceID, ProjectionKind: head.Kind,
			SourceEpoch: head.SourceEpoch, SourceRevision: head.SourceRevision, Digest: head.Digest,
			ValidUntil: timePtr(head.ValidUntil), AcceptedAt: head.AcceptedAt,
		},
	})
}

func timePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func reconcileQuery(r *http.Request) (bool, bool) {
	value := r.URL.Query().Get("reconcile")
	switch value {
	case "", "false":
		return false, true
	case "true":
		return true, true
	default:
		return false, false
	}
}
