package life

// RunningActivity describes an activity that has already started. Occupancy is
// the physical resource (for example "seat" or "path") it still holds.
//
// A not-yet-started activity offered to the R09/R10 planning layer is
// described by the same fields, so it reuses this shape instead of a second
// activity type. RequiresMovement is only meaningful for such a candidate: it
// records that admitting the activity would move the Actor away from its
// current place.
type RunningActivity struct {
	ID        string
	Occupancy string
	// RequiresMovement is whether starting this activity leaves the current
	// place. Presence-required running work keeps the Actor in place.
	RequiresMovement bool
	Rule             *InterruptionRule
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
	// StopState is the posture the Actor ends in when the stop is allowed. It
	// comes from the activity's own interruption rule, so a consumer never has
	// to read the owner mapping to act on the decision. It stays empty whenever
	// the stop was refused, which grants no position change.
	StopState string
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
		StopState:        activity.Rule.StopState,
	}
	plan.Actions = append(plan.Actions, activity.Rule.Cleanup...)
	return plan
}
