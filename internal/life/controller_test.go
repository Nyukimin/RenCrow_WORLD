package life

import (
	"errors"
	"strings"
	"testing"
)

// These are the D-11/D-12 execution tests for the R10 controller logic: the
// controller consumes the decisions this package already produces and mutates
// only the logical ledger (activity, occupancy name, posture, receipts). It
// never changes Position — movement stays measured-geometry work (D-17) and
// is not implemented. The tests pin the Failure Knowledge: occupancy release
// is a separate capability that only runs after the stop action executed, a
// refused or malformed decision mutates nothing, and the controller never
// starts work it did not admit.

func newControllerSpace(position, activity, occupancy, state string) *Ledger {
	return &Ledger{
		Position:  position,
		Activity:  activity,
		Occupancy: occupancy,
		State:     state,
		stopped:   activity == "",
	}
}

func TestControllerStoppedActivityKeepsOccupancyAndPosition(t *testing.T) {
	assessed := AssessForTest(t, workSet(future(), item("running", true)), policy(future(), []string{"rest"}, nil), DefaultCatalog())
	decision, err := PlanContinuation(assessed, activity("read", "study_seat", interruptible("standing", true)))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	ledger := newControllerSpace("study_room", "read", "study_seat", "sitting")
	if err := ExecuteContinuation(ledger, decision); err != nil {
		t.Fatalf("ExecuteContinuation: %v", err)
	}
	if ledger.Activity != "" {
		t.Fatalf("revoked activity still running: %+v", ledger)
	}
	if ledger.State != "standing" {
		t.Fatalf("stop posture from owner rule not applied: %+v", ledger)
	}
	if ledger.Occupancy != "study_seat" {
		t.Fatalf("presence work lost its seat: %+v", ledger)
	}
	if ledger.Position != "study_room" {
		t.Fatalf("controller moved the Actor: %+v", ledger)
	}
	for _, action := range ledger.Actions {
		if strings.HasPrefix(action, "release:") {
			t.Fatalf("controller released occupancy inside the stop: %+v", ledger.Actions)
		}
	}
}

func TestControllerHoldAndContinueMutateNothing(t *testing.T) {
	assessed := AssessForTest(t, workSet(future(), item("running", false)), policy(future(), []string{"read"}, nil), DefaultCatalog())
	sleepRule := &InterruptionRule{CanInterrupt: false, StopState: "sitting"}
	ledger := newControllerSpace("bedroom", "sleep", "bed", "lying")
	hold, err := PlanContinuation(assessed, activity("sleep", "bed", sleepRule))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	if err := ExecuteContinuation(ledger, hold); err != nil {
		t.Fatalf("ExecuteContinuation hold: %v", err)
	}
	permitted, err := PlanContinuation(assessed, activity("read", "study_seat", interruptible("standing", true)))
	if err != nil {
		t.Fatalf("PlanContinuation continue: %v", err)
	}
	if err := ExecuteContinuation(ledger, permitted); err != nil {
		t.Fatalf("ExecuteContinuation continue: %v", err)
	}
	if ledger.Activity != "sleep" || ledger.Occupancy != "bed" || ledger.Position != "bedroom" || ledger.State != "lying" {
		t.Fatalf("hold/continue disturbed the ledger: %+v", ledger)
	}
}

func TestControllerRefusedOrUnruledStopMutatesNothing(t *testing.T) {
	ledger := newControllerSpace("study_room", "read", "study_seat", "sitting")
	refused := Continuation{
		Action: ContinuationStop,
		Stop:   StopPlan{Allowed: false, BlockedReason: "not_interruptible", Actions: []string{"stop:read"}, StopState: "standing"},
	}
	err := ExecuteContinuation(ledger, refused)
	if !errors.Is(err, ErrDecisionNotExecutable) {
		t.Fatalf("refused stop executed: err=%v ledger=%+v", err, ledger)
	}
	if ledger.Activity != "read" || ledger.State != "sitting" || len(ledger.Actions) != 0 {
		t.Fatalf("refused stop mutated the ledger: %+v", ledger)
	}
	noPosture := Continuation{Action: ContinuationStop, Stop: StopPlan{Allowed: true, Actions: []string{"stop:read"}}}
	err = ExecuteContinuation(ledger, noPosture)
	if !errors.Is(err, ErrDecisionNotExecutable) {
		t.Fatalf("stop without owner posture executed: err=%v", err)
	}
	if ledger.Activity != "read" || len(ledger.Actions) != 0 {
		t.Fatalf("posture-less stop mutated the ledger: %+v", ledger)
	}
}

func TestControllerReleaseOnlyAfterStopAction(t *testing.T) {
	assessed := AssessForTest(t, nil, policy(past(), []string{"read"}, []string{"rest"}), DefaultCatalog())
	decision, err := PlanContinuation(assessed, activity("read", "study_seat", interruptible("standing", true)))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	ledger := newControllerSpace("study_room", "read", "study_seat", "sitting")
	if err := ReleaseOccupancy(ledger); !errors.Is(err, ErrReleaseWithoutStop) {
		t.Fatalf("release before stop accepted: err=%v ledger=%+v", err, ledger)
	}
	if ledger.Occupancy != "study_seat" || len(ledger.Actions) != 0 {
		t.Fatalf("premature release mutated the ledger: %+v", ledger)
	}
	if err := ExecuteContinuation(ledger, decision); err != nil {
		t.Fatalf("ExecuteContinuation: %v", err)
	}
	if !decision.Stop.ReleaseOccupancy {
		t.Fatalf("release-authorized stop expected: %+v", decision)
	}
	if err := ReleaseOccupancy(ledger); err != nil {
		t.Fatalf("ReleaseOccupancy after stop: %v", err)
	}
	stopAt, releaseAt := actionIndex(ledger.Actions, "stop:read"), actionIndex(ledger.Actions, "release:study_seat")
	if stopAt < 0 || releaseAt < 0 || stopAt >= releaseAt {
		t.Fatalf("release did not follow the stop action: %+v", ledger.Actions)
	}
	if ledger.Occupancy != "" || ledger.Position != "study_room" {
		t.Fatalf("release left occupancy or moved the Actor: %+v", ledger)
	}
	if err := ReleaseOccupancy(ledger); !errors.Is(err, ErrReleaseWithoutStop) {
		t.Fatalf("second release accepted: err=%v", err)
	}
}

func TestControllerAdmissionStartsOnlyAdmittedIdleWork(t *testing.T) {
	assessed := AssessForTest(t, workSet(future()), policy(future(), []string{"rest"}, nil), DefaultCatalog())
	admitted, err := AdmitCandidate(assessed, activity("rest", "study_seat", interruptible("standing", true)))
	if err != nil {
		t.Fatalf("AdmitCandidate: %v", err)
	}
	if !admitted.Allowed {
		t.Fatalf("permitted candidate must be admitted: %+v", admitted)
	}
	ledger := newControllerSpace("study_room", "", "", "standing")
	if err := ExecuteAdmission(ledger, admitted, activity("rest", "study_seat", interruptible("standing", true))); err != nil {
		t.Fatalf("ExecuteAdmission: %v", err)
	}
	if ledger.Activity != "rest" || ledger.Occupancy != "study_seat" || ledger.Position != "study_room" {
		t.Fatalf("admitted start wrong: %+v", ledger)
	}
	if ledger.Actions[0] != "start:rest" {
		t.Fatalf("start receipt missing: %+v", ledger.Actions)
	}
	if err := ExecuteAdmission(ledger, admitted, activity("rest", "study_seat", interruptible("standing", true))); !errors.Is(err, ErrBusyLedger) {
		t.Fatalf("second start on busy ledger accepted: err=%v", err)
	}
}

func TestControllerRefusesRefusedAndMovingCandidates(t *testing.T) {
	assessed := AssessForTest(t, workSet(future()), policy(future(), []string{"rest"}, nil), DefaultCatalog())
	refused, err := AdmitCandidate(assessed, activity("walk", "path", interruptible("standing", false)))
	if err != nil {
		t.Fatalf("AdmitCandidate: %v", err)
	}
	ledger := newControllerSpace("study_room", "", "", "standing")
	if err := ExecuteAdmission(ledger, refused, activity("walk", "path", interruptible("standing", false))); !errors.Is(err, ErrCandidateNotAdmitted) {
		t.Fatalf("refused candidate started: err=%v ledger=%+v", err, ledger)
	}
	if ledger.Activity != "" || len(ledger.Actions) != 0 {
		t.Fatalf("refused candidate mutated the ledger: %+v", ledger)
	}
	policyWithWalk := AssessForTest(t, workSet(future()), policy(future(), []string{"rest", "walk"}, nil), DefaultCatalog())
	moving := movingActivity("rest", "study_seat", interruptible("standing", true))
	movingAdmitted, err := AdmitCandidate(policyWithWalk, moving)
	if err != nil {
		t.Fatalf("AdmitCandidate moving: %v", err)
	}
	if !movingAdmitted.Allowed {
		t.Fatalf("presence-free admission should allow the candidate: %+v", movingAdmitted)
	}
	if err := ExecuteAdmission(ledger, movingAdmitted, moving); !errors.Is(err, ErrMovementNotImplemented) {
		t.Fatalf("movement-requiring start accepted: err=%v ledger=%+v", err, ledger)
	}
	if ledger.Activity != "" || ledger.Occupancy != "" || ledger.Position != "study_room" || len(ledger.Actions) != 0 {
		t.Fatalf("movement start mutated the ledger: %+v", ledger)
	}
}

func TestControllerRejectsMismatchedAdmission(t *testing.T) {
	assessed := AssessForTest(t, workSet(future()), policy(future(), []string{"rest"}, nil), DefaultCatalog())
	admitted, err := AdmitCandidate(assessed, activity("rest", "study_seat", interruptible("standing", true)))
	if err != nil {
		t.Fatalf("AdmitCandidate: %v", err)
	}
	ledger := newControllerSpace("study_room", "", "", "standing")
	if err := ExecuteAdmission(ledger, admitted, activity("read", "study_seat", interruptible("standing", true))); !errors.Is(err, ErrCandidateNotAdmitted) {
		t.Fatalf("mismatched activity accepted: err=%v", err)
	}
	if ledger.Activity != "" || len(ledger.Actions) != 0 {
		t.Fatalf("mismatched admission mutated the ledger: %+v", ledger)
	}
}

func TestControllerRefusesOccupancyOverwriteOnStart(t *testing.T) {
	assessed := AssessForTest(t, workSet(future()), policy(future(), []string{"rest"}, nil), DefaultCatalog())
	admitted, err := AdmitCandidate(assessed, activity("rest", "study_seat", interruptible("standing", true)))
	if err != nil {
		t.Fatalf("AdmitCandidate: %v", err)
	}
	// A stop kept the bed occupancy (D-11); starting study_seat work must not
	// silently overwrite the held name — release is the separate capability.
	ledger := newControllerSpace("study_room", "", "bed", "standing")
	if err := ExecuteAdmission(ledger, admitted, activity("rest", "study_seat", interruptible("standing", true))); !errors.Is(err, ErrOccupancyConflict) {
		t.Fatalf("conflicting start accepted: err=%v ledger=%+v", err, ledger)
	}
	if ledger.Activity != "" || ledger.Occupancy != "bed" || len(ledger.Actions) != 0 {
		t.Fatalf("conflicting start mutated the ledger: %+v", ledger)
	}
}

func actionIndex(actions []string, want string) int {
	for i, action := range actions {
		if action == want {
			return i
		}
	}
	return -1
}
