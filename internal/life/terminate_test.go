package life

import "testing"

func TestPlanStopRequiresAnInterruptionRule(t *testing.T) {
	plan := PlanStop(RunningActivity{ID: "activity-1"}, "policy_expired")
	if plan.Allowed || plan.BlockedReason != "no_interruption_rule" {
		t.Fatalf("missing rule = %+v", plan)
	}
}

func TestPlanStopSeparatesActivityCancellationFromOccupancyRelease(t *testing.T) {
	plan := PlanStop(RunningActivity{
		ID: "activity-1", Occupancy: "seat",
		Rule: &InterruptionRule{CanInterrupt: true, StopState: "seated", Cleanup: []string{"close_book"}},
	}, "policy_expired")
	if !plan.Allowed {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.ReleaseOccupancy {
		t.Fatal("occupancy released without an explicit rule")
	}
	if len(plan.Actions) != 2 || plan.Actions[0] != "stop:activity-1" || plan.Actions[1] != "close_book" {
		t.Fatalf("actions = %v", plan.Actions)
	}

	plan = PlanStop(RunningActivity{
		ID: "activity-1", Occupancy: "seat",
		Rule: &InterruptionRule{CanInterrupt: true, StopState: "standing", ReleaseOccupancy: true},
	}, "policy_expired")
	if !plan.ReleaseOccupancy {
		t.Fatal("explicit occupancy release was not planned")
	}
}

func TestPlanStopRejectsNonInterruptibleActivity(t *testing.T) {
	plan := PlanStop(RunningActivity{
		ID: "activity-1", Rule: &InterruptionRule{CanInterrupt: false},
	}, "policy_expired")
	if plan.Allowed || plan.BlockedReason != "not_interruptible" {
		t.Fatalf("non-interruptible = %+v", plan)
	}
}
