package life

import (
	"errors"
	"fmt"
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
	// ErrNoCandidate reports that no activity may be selected now: the
	// Assessment grants nothing, or every admitted activity would move the
	// Actor. It carries the WORLD reason and admits nothing on its own.
	ErrNoCandidate = errors.New("life selects no activity candidate")
	// ErrActivityNotDefined reports that an activity has no WORLD owner
	// definition, so no posture, occupancy, or interruption rule may be
	// inferred for it. It is never treated as a permitted activity.
	ErrActivityNotDefined = errors.New("life planning requires a registered activity definition")
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

// SelectCandidate is how WORLD's own catalog reaches the R10 controller (D-28):
// it returns the single activity the controller may start now, taken from the
// owner mapping in activity-ID order, never from a caller-supplied definition.
// It selects at most one candidate and mutates nothing — no Event, no State
// change, no Command Receipt, and no movement.
//
// A candidate whose start would move the Actor is never selected. Selecting it
// would plan a move the controller cannot execute and would read as a claim
// that a place exists or is reachable, and measured movement plus spatial
// readiness stay D-17/D-18 work. ExecutableActivityIDs keeps publishing the
// wider candidate set for the later controller that owns movement.
func SelectCandidate(assessment Assessment) (Candidate, RunningActivity, error) {
	if err := validateAssessment(assessment); err != nil {
		return Candidate{}, RunningActivity{}, err
	}
	if !assessment.NewActivityAllowed {
		// The Assessment already names why: missing policy, an expired policy
		// with no registered fallback, or nothing registered at all.
		return Candidate{}, RunningActivity{}, fmt.Errorf("%w: %s", ErrNoCandidate, assessment.BlockedReason)
	}
	for _, definition := range DefaultActivityDefinitions() {
		admitted := admit(assessment, definition)
		if !admitted.Allowed || definition.RequiresMovement {
			continue
		}
		return admitted, definition, nil
	}
	return Candidate{}, RunningActivity{}, fmt.Errorf("%w: no_activity_selectable", ErrNoCandidate)
}

// StopPlanning evaluates WORLD's owner mapping for one activity the Actor is
// already running, using only its ID: posture, logical occupancy, and the
// interruption rule always come from the owner table, never from the caller.
// Like the rest of this layer it selects nothing and changes nothing — it does
// not stop, start, or move an Actor, cancel CORE work, or write WORLD State.
// An ID outside the owner mapping is rejected instead of guessed at.
func StopPlanning(assessment Assessment, activityID string) (Continuation, error) {
	if err := validateAssessment(assessment); err != nil {
		return Continuation{}, err
	}
	if activityID == "" {
		return Continuation{}, ErrActivityRequired
	}
	for _, definition := range activityDefinitions {
		if definition.ID != activityID {
			continue
		}
		if definition.Rule != nil {
			rule := *definition.Rule
			definition.Rule = &rule
		}
		return PlanContinuation(assessment, definition)
	}
	return Continuation{}, fmt.Errorf("%w: %q", ErrActivityNotDefined, activityID)
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

// validateAssessment rejects an Assessment that Assess could not have produced,
// so no plan may be built on meaning this layer cannot explain. An undefined
// presentation kind would silently read as "no presence requirement" and admit
// a moving candidate, and the permission flag always comes from the permitted
// set with its reason: separating them would open a permission with no permitted
// activity or reject a candidate without naming why (D-09, D-10, D-12).
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
	switch assessment.WorkPresentation.Kind {
	case WorkNone, WorkQueued, WorkWaiting, WorkRemote, WorkPresence:
	default:
		return ErrInvalidAssessment
	}
	if assessment.NewActivityAllowed != (len(assessment.PermittedActivityIDs) > 0) {
		return ErrInvalidAssessment
	}
	if assessment.NewActivityAllowed == (assessment.BlockedReason != "") {
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
