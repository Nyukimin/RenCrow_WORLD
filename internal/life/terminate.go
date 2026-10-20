package life

// RunningActivity describes an activity that has already started. Occupancy is
// the physical resource (for example "seat" or "path") it still holds.
type RunningActivity struct {
	ID        string
	Occupancy string
	Rule      *InterruptionRule
}

// InterruptionRule is required before an activity can be stopped. The rule
// defines the stop state, bounded cleanup, and whether physical occupancy may
// be released as part of the stop.
type InterruptionRule struct {
	CanInterrupt     bool
	StopState        string
	Cleanup          []string
	ReleaseOccupancy bool
}

type StopPlan struct {
	Allowed          bool
	BlockedReason    string
	Actions          []string
	ReleaseOccupancy bool
}

// PlanStop separates cancelling an activity from releasing the physical
// occupancy it holds. A missing rule never grants permission to continue or to
// mutate position/occupancy.
func PlanStop(activity RunningActivity, trigger string) StopPlan {
	if activity.ID == "" {
		return StopPlan{BlockedReason: "invalid_activity"}
	}
	if activity.Rule == nil {
		return StopPlan{BlockedReason: "no_interruption_rule"}
	}
	if !activity.Rule.CanInterrupt {
		return StopPlan{BlockedReason: "not_interruptible"}
	}
	plan := StopPlan{
		Allowed:          true,
		Actions:          []string{"stop:" + activity.ID},
		ReleaseOccupancy: activity.Rule.ReleaseOccupancy && activity.Occupancy != "",
	}
	plan.Actions = append(plan.Actions, activity.Rule.Cleanup...)
	return plan
}
