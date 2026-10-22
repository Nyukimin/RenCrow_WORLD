package life

import (
	"errors"
	"sort"
)

// This file is the deterministic D-07/D-09/D-11/D-12 planning layer on top of
// an Assessment: whether an already-running activity may keep running, which
// activities may be published as candidates, and whether one candidate is
// admitted. It decides with logical occupancy names only; it never moves an
// Actor, never reads measured geometry, and never creates an Event, State
// change, or Command Receipt. Movement, furniture occupancy, and spatial
// readiness stay in R10/R11 (D-17/D-18).
const (
	reasonPermitted              = "permitted"
	reasonPolicyMissing          = "policy_missing"
	reasonPolicyExpiredFallback  = "policy_expired_fallback"
	reasonActivityNotPermitted   = "activity_not_permitted"
	reasonNoActivityDefinition   = "no_activity_definition"
	reasonPresenceRequired       = "presence_required"
	reasonPresenceHoldsOccupancy = "presence_work_holds_occupancy"
	reasonAdmitted               = "admitted"
)

type ContinuationAction string

const (
	// ContinuationContinue keeps the running activity as it is.
	ContinuationContinue ContinuationAction = "continue"
	// ContinuationStop stops the running activity through its interruption
	// rule. Stopping is a WORLD event and never cancels, fails, or completes
	// CORE work.
	ContinuationStop ContinuationAction = "stop"
	// ContinuationHold keeps the Actor where it is because no safe stop exists.
	// A missing or non-interruptible rule never grants a position change.
	ContinuationHold ContinuationAction = "hold"
)

// Continuation is the decision for one already-running activity.
type Continuation struct {
	Action ContinuationAction
	Reason string
	// Stop is the planned safe stop. It stays zero unless Action is
	// ContinuationStop.
	Stop StopPlan
	// RetainOccupancyReason explains why Stop.ReleaseOccupancy was refused even
	// though the rule allowed it. Activity cancellation and physical occupancy
	// release are separate operations (D-11).
	RetainOccupancyReason string
	// ClearWorkPresentation is true only for a fresh empty WorkSet. An expired
	// WorkSet keeps the work presentation because the assignment is unknown,
	// and neither state implies work success (D-07, D-08).
	ClearWorkPresentation bool
}

// Candidate is the admission result for one requested activity.
type Candidate struct {
	Allowed    bool
	ActivityID string
	Reason     string
}

var (
	ErrInvalidAssessment = errors.New("life planning requires an Assess result")
	ErrActivityRequired  = errors.New("life planning requires an activity ID")
)

// PlanContinuation decides whether a running non-cognitive activity keeps
// running, stops safely, or holds its position. The live permission is the
// Assessment's permitted set: a fresh policy that dropped the ID is an
// explicit revocation and is not rescued by an expired fallback, while an
// expired policy keeps only its registered fallback subset. Stopping always
// goes through the activity's own interruption rule.
func PlanContinuation(assessment Assessment, running RunningActivity) (Continuation, error) {
	if err := validateAssessment(assessment); err != nil {
		return Continuation{}, err
	}
	if running.ID == "" {
		return Continuation{}, ErrActivityRequired
	}
	clear := assessment.WorkSetState == WorkSetEmpty

	action, reason := ContinuationContinue, reasonPermitted
	switch {
	case assessment.PolicyState == PolicyMissing:
		action, reason = ContinuationStop, reasonPolicyMissing
	case !contains(assessment.PermittedActivityIDs, running.ID):
		action, reason = ContinuationStop, reasonActivityNotPermitted
	case assessment.PolicyState == PolicyExpired:
		reason = reasonPolicyExpiredFallback
	}
	if action == ContinuationContinue {
		return Continuation{Action: action, Reason: reason, ClearWorkPresentation: clear}, nil
	}

	plan := PlanStop(running, reason)
	if !plan.Allowed {
		return Continuation{Action: ContinuationHold, Reason: plan.BlockedReason, ClearWorkPresentation: clear}, nil
	}
	decision := Continuation{Action: action, Reason: reason, Stop: plan, ClearWorkPresentation: clear}
	// Remaining presence-required work still occupies the physical resource, so
	// stopping this activity must not release it.
	if plan.ReleaseOccupancy && requiresPresence(assessment) {
		decision.Stop.ReleaseOccupancy = false
		decision.RetainOccupancyReason = reasonPresenceHoldsOccupancy
	}
	return decision, nil
}

// ExecutableActivityIDs publishes the activities a controller may plan now:
// the current permitted set, restricted to activities with a defined
// interruption rule, and excluding any that would move the Actor away while
// presence-required work runs. The result is sorted and never includes an
// activity that has no definition (D-11).
func ExecutableActivityIDs(assessment Assessment, definitions []RunningActivity) ([]string, error) {
	if err := validateAssessment(assessment); err != nil {
		return nil, err
	}
	if !assessment.NewActivityAllowed {
		return nil, nil
	}
	ids := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		if admitted := admit(assessment, definition); admitted.Allowed {
			ids = append(ids, admitted.ActivityID)
		}
	}
	sort.Strings(ids)
	return unique(ids), nil
}

// AdmitCandidate admits one requested activity for planning. It selects
// nothing, starts nothing, and plans no movement: a later single life
// controller consumes this decision and still owns every movement and
// occupancy decision under its own rules.
func AdmitCandidate(assessment Assessment, candidate RunningActivity) (Candidate, error) {
	if err := validateAssessment(assessment); err != nil {
		return Candidate{}, err
	}
	if candidate.ID == "" {
		return Candidate{}, ErrActivityRequired
	}
	return admit(assessment, candidate), nil
}

func admit(assessment Assessment, activity RunningActivity) Candidate {
	rejected := func(reason string) Candidate {
		return Candidate{Reason: reason}
	}
	if !assessment.NewActivityAllowed {
		// The Assessment already carries the explicit reason (missing policy,
		// expired policy without fallback, or nothing registered).
		return rejected(assessment.BlockedReason)
	}
	if !contains(assessment.PermittedActivityIDs, activity.ID) {
		return rejected(reasonActivityNotPermitted)
	}
	if !isDefined(activity) {
		return rejected(reasonNoActivityDefinition)
	}
	if activity.RequiresMovement && requiresPresence(assessment) {
		return rejected(reasonPresenceRequired)
	}
	return Candidate{Allowed: true, ActivityID: activity.ID, Reason: reasonAdmitted}
}

// isDefined reports whether the activity declares where it can stop. An
// activity without that definition is never executable.
func isDefined(activity RunningActivity) bool {
	return activity.Rule != nil && activity.Rule.StopState != ""
}

// requiresPresence reports whether running work still needs the Actor in the
// HOUSE. One body and one movement controller means presence-required work
// dominates, and an expired WorkSet never contributes a requirement.
func requiresPresence(assessment Assessment) bool {
	return assessment.WorkPresentation.Kind == WorkPresence
}

func validateAssessment(assessment Assessment) error {
	switch assessment.WorkSetState {
	case WorkSetMissing, WorkSetExpired, WorkSetEmpty, WorkSetAssigned:
	default:
		return ErrInvalidAssessment
	}
	switch assessment.PolicyState {
	case PolicyMissing, PolicyFresh, PolicyExpired:
	default:
		return ErrInvalidAssessment
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
