package life

import (
	"strings"
	"testing"
)

// These are the D-18 “単体試験 using a provisional spatial interface” for the
// stop/cancellation rules. The provisional space below records only logical
// place and resource names and is driven exclusively by the plans this package
// produces. It has no coordinates, rooms, graph edges, or movement operation,
// so nothing here can be promoted to measured geometry (D-17), and position
// changes stay R10 work. The tests pin the Failure Knowledge of D-11/D-12:
// cancelling an activity and releasing physical occupancy are separate
// operations, a stop never discontinuously changes position, and a reservation
// is never released before its stop action ran.

type provisionalSpace struct {
	position  string
	activity  string
	occupancy string
	log       []string
}

func newProvisionalSpace(position, running, occupancy string) *provisionalSpace {
	return &provisionalSpace{position: position, activity: running, occupancy: occupancy}
}

// apply executes one Continuation exactly as a consumer may: the activity is
// cancelled only through the stop action, and occupancy is released only
// through the separate ReleaseOccupancy capability after that action ran.
func (s *provisionalSpace) apply(decision Continuation) {
	switch decision.Action {
	case ContinuationContinue:
		s.log = append(s.log, "continue")
	case ContinuationHold:
		s.log = append(s.log, "hold")
	case ContinuationStop:
		s.log = append(s.log, decision.Stop.Actions...)
		s.activity = ""
		if decision.Stop.ReleaseOccupancy && s.occupancy != "" {
			s.log = append(s.log, "release:"+s.occupancy)
			s.occupancy = ""
		}
	}
}

func (s *provisionalSpace) indexOf(entry string) int {
	for i, item := range s.log {
		if item == entry {
			return i
		}
	}
	return -1
}

func TestProvisionalSpaceStoppedReadKeepsSeatOccupied(t *testing.T) {
	assessed := AssessForTest(t, workSet(future(), item("running", true)), policy(future(), []string{"rest"}, nil), DefaultCatalog())
	space := newProvisionalSpace("study_room", "read", "study_seat")
	decision, err := PlanContinuation(assessed, activity("read", "study_seat", interruptible("standing", true)))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	space.apply(decision)
	if space.activity != "" {
		t.Fatalf("revoked activity still running: %+v", space)
	}
	// The read activity is cancelled, but presence-required work still sits
	// in the seat, so the physical occupancy survives the cancellation (D-11).
	if space.occupancy != "study_seat" {
		t.Fatalf("presence work lost its seat: %+v", space)
	}
	if space.position != "study_room" {
		t.Fatalf("stop moved the Actor: %+v", space)
	}
}

func TestProvisionalSpaceWalkStopKeepsPathReservationAndPosition(t *testing.T) {
	assessed := AssessForTest(t, workSet(future(), item("running", false)), policy(future(), []string{"read"}, nil), DefaultCatalog())
	space := newProvisionalSpace("corridor", "walk", "path")
	decision, err := PlanContinuation(assessed, activity("walk", "path", interruptible("standing", false)))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	space.apply(decision)
	if space.activity != "" {
		t.Fatalf("revoked walk still running: %+v", space)
	}
	// Stopping mid-walk must not drop the path reservation up front and must
	// not place the Actor anywhere else (no discontinuous position change).
	if space.occupancy != "path" {
		t.Fatalf("path reservation released by stop: %+v", space)
	}
	if space.position != "corridor" {
		t.Fatalf("stop moved the Actor mid-walk: %+v", space)
	}
	if strings.HasPrefix(strings.Join(space.log, ","), "release:") {
		t.Fatalf("release ran before stop: %+v", space.log)
	}
}

func TestProvisionalSpaceSleepRevocationHoldsActorInBed(t *testing.T) {
	assessed := AssessForTest(t, workSet(future(), item("running", false)), policy(future(), []string{"read"}, nil), DefaultCatalog())
	sleepRule := &InterruptionRule{CanInterrupt: false, StopState: "sitting"}
	space := newProvisionalSpace("bedroom", "sleep", "bed")
	decision, err := PlanContinuation(assessed, activity("sleep", "bed", sleepRule))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	space.apply(decision)
	if decision.Action != ContinuationHold || space.activity != "sleep" || space.occupancy != "bed" || space.position != "bedroom" {
		t.Fatalf("non-interruptible activity was disturbed: decision=%+v space=%+v", decision, space)
	}
}

func TestProvisionalSpaceReleaseOnlyFollowsStopAction(t *testing.T) {
	assessed := AssessForTest(t, nil, policy(past(), []string{"read"}, []string{"rest"}), DefaultCatalog())
	space := newProvisionalSpace("study_room", "read", "study_seat")
	decision, err := PlanContinuation(assessed, activity("read", "study_seat", interruptible("standing", true)))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	if !decision.Stop.ReleaseOccupancy {
		t.Fatalf("release-authorized stop expected without presence work: %+v", decision)
	}
	// Release is a separate capability, never an action smuggled into the
	// stop/cleanup sequence.
	for _, action := range decision.Stop.Actions {
		if strings.HasPrefix(action, "release:") {
			t.Fatalf("release embedded in stop actions: %+v", decision.Stop.Actions)
		}
	}
	space.apply(decision)
	stopAt, releaseAt := space.indexOf("stop:read"), space.indexOf("release:study_seat")
	if stopAt < 0 || releaseAt < 0 || stopAt >= releaseAt {
		t.Fatalf("release did not follow the stop action: %+v", space.log)
	}
	if space.position != "study_room" {
		t.Fatalf("releasing the seat moved the Actor: %+v", space)
	}
}

func TestProvisionalSpaceMissingRuleHoldsEverything(t *testing.T) {
	assessed := AssessForTest(t, workSet(future()), nil, DefaultCatalog())
	space := newProvisionalSpace("study_room", "rest", "study_seat")
	decision, err := PlanContinuation(assessed, activity("rest", "study_seat", nil))
	if err != nil {
		t.Fatalf("PlanContinuation: %v", err)
	}
	space.apply(decision)
	if decision.Action != ContinuationHold || space.activity != "rest" || space.occupancy != "study_seat" || space.position != "study_room" {
		t.Fatalf("missing rule disturbed position or occupancy: decision=%+v space=%+v", decision, space)
	}
}
