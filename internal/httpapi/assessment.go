package httpapi

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/idmap"
	"github.com/Nyukimin/RenCrow_WORLD/internal/life"
	"github.com/Nyukimin/RenCrow_WORLD/internal/projection"
	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

type assessmentBody struct {
	SchemaVersion        int                            `json:"schema_version"`
	WorldID              string                         `json:"world_id"`
	TimelineID           string                         `json:"timeline_id"`
	TimelineEpoch        string                         `json:"timeline_epoch"`
	ActorID              string                         `json:"actor_id"`
	AssessedAt           time.Time                      `json:"assessed_at"`
	WorkSetState         string                         `json:"work_set_state"`
	PolicyState          string                         `json:"policy_state"`
	WorkPresentation     assessmentWorkPresentationBody `json:"work_presentation"`
	PermittedActivityIDs []string                       `json:"permitted_activity_ids"`
	NewActivityAllowed   bool                           `json:"new_activity_allowed"`
	BlockedReason        string                         `json:"blocked_reason,omitempty"`
	WorkSuccess          bool                           `json:"work_success"`
}

type assessmentWorkPresentationBody struct {
	Kind            string   `json:"kind"`
	PresenceTaskIDs []string `json:"presence_task_ids"`
	RunningTaskIDs  []string `json:"running_task_ids"`
}

// handleAssessment exposes the R09 meaning layer for one operator-authorized
// CORE Actor. It is a read-only projection of the accepted WorkSet/LifePolicy
// heads: it does not select an activity, move an Actor, or write WORLD State.
func (s *Server) handleAssessment(w http.ResponseWriter, r *http.Request) {
	actorID, err := idmap.Decode(r.PathValue("actor"))
	if err != nil || actorID == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_actor_id", "the actor path is not a valid transport ID")
		return
	}
	if !s.coreProjectionActor(actorID) {
		writeAPIError(w, http.StatusForbidden, "actor_not_bound", "the actor has no authorized CORE instance mapping")
		return
	}
	if _, _, _, err := s.store.Snapshot(s.cfg.Scope); err != nil {
		if errors.Is(err, store.ErrTimelineNotFound) {
			writeAPIError(w, http.StatusServiceUnavailable, "not_ready", "the timeline is not available")
			return
		}
		writeAPIError(w, http.StatusServiceUnavailable, "store_unavailable", "the store is unavailable")
		return
	}

	workSet, err := s.loadWorkSet(actorID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "projection_unreadable", "the stored WorkSet projection is unreadable")
		return
	}
	policy, err := s.loadLifePolicy(actorID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "projection_unreadable", "the stored LifePolicy projection is unreadable")
		return
	}
	assessedAt := nowUTC()
	assessment, err := life.Assess(workSet, policy, assessedAt, life.DefaultCatalog())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "assessment_unavailable", "the life assessment is unavailable")
		return
	}

	permitted := append([]string(nil), assessment.PermittedActivityIDs...)
	if permitted == nil {
		permitted = []string{}
	}
	sort.Strings(permitted)
	presence := append([]string(nil), assessment.WorkPresentation.PresenceTaskIDs...)
	if presence == nil {
		presence = []string{}
	}
	running := append([]string(nil), assessment.WorkPresentation.RunningTaskIDs...)
	if running == nil {
		running = []string{}
	}
	writeJSON(w, http.StatusOK, assessmentBody{
		SchemaVersion: SchemaVersion,
		WorldID:       s.cfg.Scope.WorldID,
		TimelineID:    s.cfg.Scope.TimelineID,
		TimelineEpoch: s.cfg.Scope.TimelineEpoch,
		ActorID:       actorID,
		AssessedAt:    assessedAt,
		WorkSetState:  string(assessment.WorkSetState),
		PolicyState:   string(assessment.PolicyState),
		WorkPresentation: assessmentWorkPresentationBody{
			Kind:            string(assessment.WorkPresentation.Kind),
			PresenceTaskIDs: presence,
			RunningTaskIDs:  running,
		},
		PermittedActivityIDs: permitted,
		NewActivityAllowed:   assessment.NewActivityAllowed,
		BlockedReason:        assessment.BlockedReason,
		WorkSuccess:          assessment.WorkSuccess,
	})
}

func (s *Server) coreProjectionActor(actorID string) bool {
	for _, binding := range s.cfg.CoreProjectionActors {
		if binding.ActorID == actorID {
			return true
		}
	}
	return false
}

func (s *Server) loadWorkSet(actorID string) (*projection.WorkSet, error) {
	head, err := s.store.ProjectionHead(s.cfg.Scope, actorID, projection.KindWorkSet)
	if errors.Is(err, store.ErrProjectionNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	workSet, err := projection.ParseWorkSet(head.Payload)
	if err != nil {
		return nil, err
	}
	return &workSet, nil
}

func (s *Server) loadLifePolicy(actorID string) (*projection.LifePolicy, error) {
	head, err := s.store.ProjectionHead(s.cfg.Scope, actorID, projection.KindLifePolicy)
	if errors.Is(err, store.ErrProjectionNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	policy, err := projection.ParseLifePolicy(head.Payload)
	if err != nil {
		return nil, err
	}
	return &policy, nil
}
