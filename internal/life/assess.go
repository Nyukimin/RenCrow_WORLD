// Package life evaluates the validated CORE WorkSet and LifePolicy heads
// without moving an Actor or changing WORLD State. It is the deterministic R09
// meaning layer consumed by later R10 presentation/controllers.
package life

import (
	"errors"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/projection"
)

type WorkSetState string

const (
	WorkSetMissing  WorkSetState = "missing"
	WorkSetExpired  WorkSetState = "expired"
	WorkSetEmpty    WorkSetState = "empty"
	WorkSetAssigned WorkSetState = "assigned"
)

type PolicyState string

const (
	PolicyMissing PolicyState = "missing"
	PolicyFresh   PolicyState = "fresh"
	PolicyExpired PolicyState = "expired"
)

type WorkPresentationKind string

const (
	WorkNone     WorkPresentationKind = "none"
	WorkQueued   WorkPresentationKind = "queued"
	WorkWaiting  WorkPresentationKind = "waiting"
	WorkRemote   WorkPresentationKind = "remote"
	WorkPresence WorkPresentationKind = "presence"
)

// Catalog is the registered non-cognitive activity catalog. Production must
// supply the owner catalog; an empty or nil catalog cannot grant activity.
type Catalog interface {
	IsRegisteredNonCognitive(activityID string) bool
}

type StaticCatalog map[string]bool

func (c StaticCatalog) IsRegisteredNonCognitive(activityID string) bool { return c[activityID] }

type WorkPresentation struct {
	Kind            WorkPresentationKind
	PresenceTaskIDs []string
	RunningTaskIDs  []string
}

type Assessment struct {
	WorkSetState         WorkSetState
	PolicyState          PolicyState
	WorkPresentation     WorkPresentation
	PermittedActivityIDs []string
	NewActivityAllowed   bool
	BlockedReason        string
	// WorkSuccess is deliberately always false. Only a CORE work result can
	// establish success; an empty or expired WorkSet never does.
	WorkSuccess bool
}

var (
	ErrCatalogRequired = errors.New("life activity catalog is required")
	ErrInvalidClock    = errors.New("life assessment clock is required")
)

// Assess maps the current heads to their meaning. It does not choose a
// concrete activity, move an Actor, or create a WORLD Event/State change.
func Assess(workSet *projection.WorkSet, policy *projection.LifePolicy, now time.Time, catalog Catalog) (Assessment, error) {
	if now.IsZero() {
		return Assessment{}, ErrInvalidClock
	}
	if catalog == nil {
		return Assessment{}, ErrCatalogRequired
	}
	now = now.UTC()
	result := Assessment{
		WorkSetState:     assessWorkSetState(workSet, now),
		WorkPresentation: assessWorkPresentation(workSet, now),
		PolicyState:      assessPolicyState(policy, now),
	}
	switch result.PolicyState {
	case PolicyMissing:
		result.BlockedReason = "policy_missing"
	case PolicyExpired:
		allowed := make(map[string]bool, len(policy.AllowedNonCognitiveActivityIDs))
		for _, id := range policy.AllowedNonCognitiveActivityIDs {
			allowed[id] = true
		}
		for _, id := range unique(policy.ExpiredFallbackActivityIDs) {
			if allowed[id] && catalog.IsRegisteredNonCognitive(id) {
				result.PermittedActivityIDs = append(result.PermittedActivityIDs, id)
			}
		}
		if len(result.PermittedActivityIDs) == 0 {
			result.BlockedReason = "policy_expired_no_fallback"
		}
	case PolicyFresh:
		for _, id := range unique(policy.AllowedNonCognitiveActivityIDs) {
			if catalog.IsRegisteredNonCognitive(id) {
				result.PermittedActivityIDs = append(result.PermittedActivityIDs, id)
			}
		}
		if len(result.PermittedActivityIDs) == 0 {
			if len(policy.AllowedNonCognitiveActivityIDs) == 0 {
				result.BlockedReason = "no_allowed_activity"
			} else {
				result.BlockedReason = "no_registered_activity"
			}
		}
	}
	result.NewActivityAllowed = len(result.PermittedActivityIDs) > 0
	return result, nil
}

func assessWorkSetState(workSet *projection.WorkSet, now time.Time) WorkSetState {
	if workSet == nil {
		return WorkSetMissing
	}
	if !workSet.ValidUntil.After(now) {
		return WorkSetExpired
	}
	if len(workSet.ActiveWork) == 0 {
		return WorkSetEmpty
	}
	return WorkSetAssigned
}

func assessWorkPresentation(workSet *projection.WorkSet, now time.Time) WorkPresentation {
	state := assessWorkSetState(workSet, now)
	if state != WorkSetAssigned {
		return WorkPresentation{Kind: WorkNone}
	}
	result := WorkPresentation{Kind: WorkQueued}
	for _, item := range workSet.ActiveWork {
		switch item.ExecutionState {
		case "running":
			result.RunningTaskIDs = append(result.RunningTaskIDs, item.TaskID)
			if item.RequiresHousePresence {
				result.PresenceTaskIDs = append(result.PresenceTaskIDs, item.TaskID)
			}
		case "waiting", "blocked":
			if result.Kind == WorkQueued {
				result.Kind = WorkWaiting
			}
		}
	}
	// One body and one movement controller: a presence-requiring running task
	// dominates; otherwise running work stays where it is.
	if len(result.PresenceTaskIDs) > 0 {
		result.Kind = WorkPresence
	} else if len(result.RunningTaskIDs) > 0 {
		result.Kind = WorkRemote
	}
	return result
}

func assessPolicyState(policy *projection.LifePolicy, now time.Time) PolicyState {
	if policy == nil {
		return PolicyMissing
	}
	if !policy.ValidUntil.After(now) {
		return PolicyExpired
	}
	return PolicyFresh
}

func unique(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
