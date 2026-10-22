package life

import (
	"errors"
	"testing"
)

// These tests are the D-07/D-09/D-11/D-12 logic layer: expiry judgement,
// activity candidate selection, WorkSet priority, cancellation, and stale
// handling. They use only logical occupancy names ("seat", "study_seat");
// measured geometry, room graphs, and spatial readiness are D-17/D-18 work and
// are never implied here.

func interruptible(stopState string, releaseOccupancy bool) *InterruptionRule {
	return &InterruptionRule{
		CanInterrupt:     true,
		StopState:        stopState,
		ReleaseOccupancy: releaseOccupancy,
	}
}

func activity(id, occupancy string, rule *InterruptionRule) RunningActivity {
	return RunningActivity{ID: id, Occupancy: occupancy, Rule: rule}
}

// movingActivity is a candidate whose start would move the Actor away from the
// place running work needs it. Presence-required work therefore refuses it.
func movingActivity(id, occupancy string, rule *InterruptionRule) RunningActivity {
	candidate := activity(id, occupancy, rule)
	candidate.RequiresMovement = true
	return candidate
}

func TestPlanContinuationKeepsPermittedActivity(t *testing.T) {
	got := AssessForTest(t, nil, policy(future(), []string{"rest"}, nil), DefaultCatalog())
	plan, err := PlanContinuation(got, activity("rest", "study_seat", interruptible("lying", true)))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	if plan.Action != ContinuationContinue || plan.Reason != "permitted" {
		t.Fatalf("permitted activity = %+v", plan)
	}
	if plan.Stop.Allowed || plan.ClearWorkPresentation {
		t.Fatalf("permitted activity changed stop or presentation: %+v", plan)
	}
}

func TestPlanContinuationStopsRevokedActivityAndHoldsPresenceOccupancy(t *testing.T) {
	got := AssessForTest(t, workSet(future(), item("running", true)), policy(future(), []string{"rest"}, nil), DefaultCatalog())
	if got.WorkPresentation.Kind != WorkPresence {
		t.Fatalf("presence presentation expected: %+v", got.WorkPresentation)
	}
	plan, err := PlanContinuation(got, activity("read", "study_seat", interruptible("standing", true)))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	if plan.Action != ContinuationStop || plan.Reason != "activity_not_permitted" {
		t.Fatalf("revoked activity = %+v", plan)
	}
	if !plan.Stop.Allowed {
		t.Fatalf("safe stop expected: %+v", plan.Stop)
	}
	// The seat is still held by presence-required work, so stopping the read
	// activity must not release it (D-11, D-12).
	if plan.Stop.ReleaseOccupancy || plan.RetainOccupancyReason != "presence_work_holds_occupancy" {
		t.Fatalf("presence occupancy released: %+v", plan)
	}
}

func TestPlanContinuationStopsActivityOutsideFallbackWhenPolicyExpired(t *testing.T) {
	assessed := AssessForTest(t, nil, policy(past(), []string{"read", "rest"}, []string{"rest"}), DefaultCatalog())
	if assessed.PolicyState != PolicyExpired {
		t.Fatalf("expired policy expected: %+v", assessed)
	}
	plan, err := PlanContinuation(assessed, activity("read", "study_seat", interruptible("standing", true)))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	// An expired fallback never rescues an activity the fallback does not name.
	if plan.Action != ContinuationStop || plan.Reason != "activity_not_permitted" {
		t.Fatalf("outside fallback = %+v", plan)
	}
	if !plan.Stop.ReleaseOccupancy || plan.RetainOccupancyReason != "" {
		t.Fatalf("occupancy release without presence work = %+v", plan)
	}
}

func TestPlanContinuationKeepsFallbackActivityWhenPolicyExpired(t *testing.T) {
	got := AssessForTest(t, nil, policy(past(), []string{"read", "rest"}, []string{"rest"}), DefaultCatalog())
	plan, err := PlanContinuation(got, activity("rest", "study_seat", interruptible("lying", true)))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	if plan.Action != ContinuationContinue || plan.Reason != "policy_expired_fallback" {
		t.Fatalf("expired fallback = %+v", plan)
	}
}

func TestPlanContinuationFreshPolicyIsNotOverriddenByExpiredFallback(t *testing.T) {
	got := AssessForTest(t, nil, policy(future(), []string{"rest"}, []string{"read"}), DefaultCatalog())
	plan, err := PlanContinuation(got, activity("read", "", interruptible("standing", false)))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	// Explicit revocation under a fresh policy is not an expiry fallback case.
	if plan.Action != ContinuationStop || plan.Reason != "activity_not_permitted" {
		t.Fatalf("fresh revocation = %+v", plan)
	}
}

func TestPlanContinuationMissingPolicyStopsSafely(t *testing.T) {
	got := AssessForTest(t, workSet(future()), nil, DefaultCatalog())
	plan, err := PlanContinuation(got, activity("rest", "study_seat", interruptible("lying", true)))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	if plan.Action != ContinuationStop || plan.Reason != "policy_missing" {
		t.Fatalf("missing policy = %+v", plan)
	}
	if !plan.Stop.Allowed {
		t.Fatalf("post-processing stop must stay available: %+v", plan.Stop)
	}
}

func TestPlanContinuationWithoutInterruptionRuleHoldsPosition(t *testing.T) {
	got := AssessForTest(t, workSet(future()), nil, DefaultCatalog())
	plan, err := PlanContinuation(got, activity("rest", "study_seat", nil))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	if plan.Action != ContinuationHold || plan.Reason != "no_interruption_rule" {
		t.Fatalf("no rule = %+v", plan)
	}
	if plan.Stop.Allowed || plan.Stop.ReleaseOccupancy {
		t.Fatalf("hold mutated occupancy: %+v", plan.Stop)
	}

	got = AssessForTest(t, workSet(future()), nil, DefaultCatalog())
	plan, err = PlanContinuation(got, activity("sleep", "bed", &InterruptionRule{CanInterrupt: false, StopState: "lying"}))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	if plan.Action != ContinuationHold || plan.Reason != "not_interruptible" {
		t.Fatalf("non-interruptible = %+v", plan)
	}
}

func TestPlanContinuationExpiredWorkSetDoesNotClearPresentation(t *testing.T) {
	got := AssessForTest(t, workSet(past(), item("running", true)), policy(future(), []string{"rest"}, nil), DefaultCatalog())
	if got.WorkSetState != WorkSetExpired {
		t.Fatalf("expired WorkSet expected: %+v", got)
	}
	plan, err := PlanContinuation(got, activity("rest", "study_seat", interruptible("lying", true)))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	// Unknown work is neither completion nor "no assigned work", so it neither
	// stops a permitted activity nor releases the work presentation.
	if plan.Action != ContinuationContinue || plan.ClearWorkPresentation {
		t.Fatalf("expired WorkSet = %+v", plan)
	}
	if got.WorkSuccess {
		t.Fatal("expired WorkSet implied work success")
	}
}

func TestPlanContinuationFreshEmptyWorkSetClearsPresentationOnly(t *testing.T) {
	got := AssessForTest(t, workSet(future()), policy(future(), []string{"rest"}, nil), DefaultCatalog())
	plan, err := PlanContinuation(got, activity("rest", "study_seat", interruptible("lying", true)))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	if plan.Action != ContinuationContinue || !plan.ClearWorkPresentation {
		t.Fatalf("fresh empty WorkSet = %+v", plan)
	}
	if got.WorkSuccess {
		t.Fatal("empty WorkSet implied work success")
	}
}

func TestPlanContinuationRejectsIncompleteInput(t *testing.T) {
	if _, err := PlanContinuation(Assessment{}, activity("rest", "", interruptible("lying", false))); !errors.Is(err, ErrInvalidAssessment) {
		t.Fatalf("zero assessment error = %v", err)
	}
	got := AssessForTest(t, nil, policy(future(), []string{"rest"}, nil), DefaultCatalog())
	if _, err := PlanContinuation(got, RunningActivity{}); !errors.Is(err, ErrActivityRequired) {
		t.Fatalf("missing activity error = %v", err)
	}
	if _, err := AdmitCandidate(got, RunningActivity{}); !errors.Is(err, ErrActivityRequired) {
		t.Fatalf("missing candidate error = %v", err)
	}
	if _, err := ExecutableActivityIDs(got, nil); err != nil {
		t.Fatalf("ExecutableActivityIDs: %v", err)
	}
}

func TestExecutableActivityIDsPublishesOnlyDefinedPermittedActivities(t *testing.T) {
	got := AssessForTest(t, nil, policy(future(), []string{"walk", "read", "rest", "unknown_activity"}, nil), DefaultCatalog())
	definitions := []RunningActivity{
		movingActivity("walk", "path", interruptible("standing", true)),
		activity("rest", "bed", nil),
		activity("read", "study_seat", interruptible("standing", true)),
		activity("sit", "chair", interruptible("sitting", false)),
	}
	ids, err := ExecutableActivityIDs(got, definitions)
	if err != nil {
		t.Fatalf("ExecutableActivityIDs: %v", err)
	}
	// Undefined activities and IDs outside the current permission are never
	// published, and the result is deterministic.
	if len(ids) != 2 || ids[0] != "read" || ids[1] != "walk" {
		t.Fatalf("executable ids = %v", ids)
	}

	// Presence-required work keeps the Actor in place, so a movement activity
	// is not offered while that work runs.
	presence := AssessForTest(t, workSet(future(), item("running", true)), policy(future(), []string{"walk", "read"}, nil), DefaultCatalog())
	ids, err = ExecutableActivityIDs(presence, definitions)
	if err != nil {
		t.Fatalf("ExecutableActivityIDs presence: %v", err)
	}
	if len(ids) != 1 || ids[0] != "read" {
		t.Fatalf("presence executable ids = %v", ids)
	}

	// Unknown work must not remove the permitted non-cognitive options.
	expired := AssessForTest(t, workSet(past(), item("running", true)), policy(future(), []string{"walk", "read"}, nil), DefaultCatalog())
	ids, err = ExecutableActivityIDs(expired, definitions)
	if err != nil {
		t.Fatalf("ExecutableActivityIDs expired: %v", err)
	}
	if len(ids) != 2 || ids[0] != "read" || ids[1] != "walk" {
		t.Fatalf("expired WorkSet removed life options: %v", ids)
	}

	blocked := AssessForTest(t, nil, nil, DefaultCatalog())
	ids, err = ExecutableActivityIDs(blocked, definitions)
	if err != nil {
		t.Fatalf("ExecutableActivityIDs blocked: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("blocked policy published activities: %v", ids)
	}
	if blocked.BlockedReason != "policy_missing" {
		t.Fatalf("blocked reason = %q", blocked.BlockedReason)
	}
}

func TestAdmitCandidateAdmitsDefinedPermittedActivity(t *testing.T) {
	got := AssessForTest(t, workSet(future()), policy(future(), []string{"rest"}, nil), DefaultCatalog())
	admitted, err := AdmitCandidate(got, activity("rest", "study_seat", interruptible("lying", true)))
	if err != nil {
		t.Fatalf("AdmitCandidate: %v", err)
	}
	if !admitted.Allowed || admitted.ActivityID != "rest" || admitted.Reason != "admitted" {
		t.Fatalf("admitted = %+v", admitted)
	}
}

func TestAdmitCandidateRejectsUndefinedOrUnpermittedActivity(t *testing.T) {
	got := AssessForTest(t, workSet(future()), policy(future(), []string{"rest"}, nil), DefaultCatalog())
	admitted, err := AdmitCandidate(got, activity("rest", "study_seat", nil))
	if err != nil {
		t.Fatalf("AdmitCandidate: %v", err)
	}
	if admitted.Allowed || admitted.Reason != "no_activity_definition" {
		t.Fatalf("undefined activity = %+v", admitted)
	}

	admitted, err = AdmitCandidate(got, activity("walk", "path", interruptible("standing", true)))
	if err != nil {
		t.Fatalf("AdmitCandidate: %v", err)
	}
	if admitted.Allowed || admitted.Reason != "activity_not_permitted" {
		t.Fatalf("unpermitted activity = %+v", admitted)
	}

	blocked := AssessForTest(t, nil, policy(past(), []string{"rest"}, nil), DefaultCatalog())
	admitted, err = AdmitCandidate(blocked, activity("rest", "study_seat", interruptible("lying", true)))
	if err != nil {
		t.Fatalf("AdmitCandidate: %v", err)
	}
	if admitted.Allowed || admitted.Reason != "policy_expired_no_fallback" {
		t.Fatalf("expired policy without fallback = %+v", admitted)
	}
}

func TestAdmitCandidateKeepsPresenceWorkInPlace(t *testing.T) {
	presence := AssessForTest(t, workSet(future(), item("running", true)), policy(future(), []string{"walk"}, nil), DefaultCatalog())
	moving := movingActivity("walk", "path", interruptible("standing", true))
	admitted, err := AdmitCandidate(presence, moving)
	if err != nil {
		t.Fatalf("AdmitCandidate: %v", err)
	}
	if admitted.Allowed || admitted.Reason != "presence_required" {
		t.Fatalf("movement during presence work = %+v", admitted)
	}

	stationary := activity("walk", "path", interruptible("standing", true))
	admitted, err = AdmitCandidate(presence, stationary)
	if err != nil {
		t.Fatalf("AdmitCandidate stationary: %v", err)
	}
	if !admitted.Allowed {
		t.Fatalf("activity without movement refused: %+v", admitted)
	}

	// An expired WorkSet is unknown, so it must not be read as a presence
	// requirement either way.
	expired := AssessForTest(t, workSet(past(), item("running", true)), policy(future(), []string{"walk"}, nil), DefaultCatalog())
	admitted, err = AdmitCandidate(expired, moving)
	if err != nil {
		t.Fatalf("AdmitCandidate expired: %v", err)
	}
	if !admitted.Allowed {
		t.Fatalf("expired WorkSet blocked a permitted activity: %+v", admitted)
	}
}

// TestStopPlanningUsesOwnerDefinition stops a running activity that a fresh
// policy no longer permits, using WORLD's owner mapping for the posture and the
// occupancy rule. The caller supplies only the activity ID: occupancy, rule, and
// posture always come from the owner table (D-11).
func TestStopPlanningUsesOwnerDefinition(t *testing.T) {
	got := AssessForTest(t, workSet(future(), item("running", true)), policy(future(), []string{"idle"}, nil), DefaultCatalog())

	decision, err := StopPlanning(got, "read")
	if err != nil {
		t.Fatalf("StopPlanning: %v", err)
	}
	if decision.Action != ContinuationStop || decision.Reason != reasonActivityNotPermitted {
		t.Fatalf("revoked activity = %+v", decision)
	}
	if decision.Stop.StopState != "standing" {
		t.Fatalf("stop posture = %+v", decision.Stop)
	}
	if len(decision.Stop.Actions) != 1 || decision.Stop.Actions[0] != "stop:read" {
		t.Fatalf("stop actions = %v", decision.Stop.Actions)
	}
	if decision.Stop.ReleaseOccupancy {
		t.Fatalf("owner table grants no occupancy release yet: %+v", decision.Stop)
	}
	if decision.ClearWorkPresentation {
		t.Fatalf("assigned WorkSet must not be cleared by a stop: %+v", decision)
	}
}

// TestStopPlanningHoldsNonInterruptibleOwnerActivity pins that "sleep" defines
// no safe interruption point, so a revoked permission holds the Actor in bed
// instead of granting a position change.
func TestStopPlanningHoldsNonInterruptibleOwnerActivity(t *testing.T) {
	got := AssessForTest(t, nil, nil, DefaultCatalog())

	decision, err := StopPlanning(got, "sleep")
	if err != nil {
		t.Fatalf("StopPlanning: %v", err)
	}
	if decision.Action != ContinuationHold || decision.Reason != "not_interruptible" {
		t.Fatalf("sleep with a missing policy = %+v", decision)
	}
	if decision.Stop.Allowed || decision.Stop.StopState != "" || len(decision.Stop.Actions) != 0 {
		t.Fatalf("held activity planned a stop: %+v", decision.Stop)
	}
}

// TestStopPlanningRejectsUnregisteredActivity pins that an ID outside WORLD's
// owner mapping and an empty ID are rejected instead of evaluated with a
// guessed posture or occupancy.
func TestStopPlanningRejectsUnregisteredActivity(t *testing.T) {
	got := AssessForTest(t, nil, policy(future(), []string{"idle"}, nil), DefaultCatalog())

	if _, err := StopPlanning(got, "walk_the_dog"); !errors.Is(err, ErrActivityNotDefined) {
		t.Fatalf("unregistered activity error = %v", err)
	}
	if _, err := StopPlanning(got, ""); !errors.Is(err, ErrActivityRequired) {
		t.Fatalf("empty activity error = %v", err)
	}
	if _, err := StopPlanning(Assessment{}, "read"); !errors.Is(err, ErrInvalidAssessment) {
		t.Fatalf("invalid assessment error = %v", err)
	}
}
