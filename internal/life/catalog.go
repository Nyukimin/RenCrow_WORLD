package life

import "sort"

// registeredNonCognitiveActivityIDs is WORLD's registration of CORE's
// non-cognitive LifePolicy vocabulary. CORE owns the IDs and their meaning;
// WORLD registers the concrete posture, motion, and occupancy mapping for each
// ID. Adding or removing an ID is a cross-repo contract change, not a local
// preference. Unknown IDs are never granted.
var registeredNonCognitiveActivityIDs = []string{
	"change_clothes",
	"exercise",
	"idle",
	"read",
	"rest",
	"sit",
	"sleep",
	"walk",
}

// RegisteredNonCognitiveActivityIDs returns a sorted copy of the registered
// activity IDs.
func RegisteredNonCognitiveActivityIDs() []string {
	out := append([]string(nil), registeredNonCognitiveActivityIDs...)
	sort.Strings(out)
	return out
}

// DefaultCatalog is the production non-cognitive activity catalog registered
// by WORLD. An empty or nil catalog cannot grant activity.
func DefaultCatalog() Catalog {
	catalog := make(StaticCatalog, len(registeredNonCognitiveActivityIDs))
	for _, id := range registeredNonCognitiveActivityIDs {
		catalog[id] = true
	}
	return catalog
}

// activityDefinitions is WORLD's owner mapping of CORE's registered
// non-cognitive IDs onto their logical posture, motion, and occupancy meaning
// (D-11, D-26). Occupancy names are logical only: they are not anchor IDs,
// room graph nodes, coordinates, or reservations, and admitting an activity
// never claims that a place exists or is reachable. Only the later R10/R11
// controller turns one into movement or an occupancy claim, and that needs the
// D-17/D-18 geometry evidence that does not exist yet.
//
// ReleaseOccupancy is false for every entry. D-11 forbids releasing a physical
// resource while the body may still be on it, and WORLD has no measured
// movement or occupancy route, so no activity may grant a release yet.
// Cleanup stays empty for the same reason: there is no occupancy or reservation
// to unwind. Adding a release, a cleanup step, or an occupancy name that
// implies measured space requires that evidence and a review of this table.
var activityDefinitions = []RunningActivity{
	// Stopping leaves the Actor standing in the room, so the room stays held.
	{ID: "change_clothes", Occupancy: "private_room", RequiresMovement: true, Rule: &InterruptionRule{CanInterrupt: true, StopState: "standing"}},
	{ID: "exercise", Occupancy: "exercise_mat", RequiresMovement: true, Rule: &InterruptionRule{CanInterrupt: true, StopState: "standing"}},
	// Idle relocates nothing and holds nothing, so it stays available while
	// presence-required work runs.
	{ID: "idle", RequiresMovement: false, Rule: &InterruptionRule{CanInterrupt: true, StopState: "standing"}},
	{ID: "read", Occupancy: "study_seat", RequiresMovement: true, Rule: &InterruptionRule{CanInterrupt: true, StopState: "standing"}},
	{ID: "rest", Occupancy: "sofa", RequiresMovement: true, Rule: &InterruptionRule{CanInterrupt: true, StopState: "sitting"}},
	{ID: "sit", Occupancy: "chair", RequiresMovement: true, Rule: &InterruptionRule{CanInterrupt: true, StopState: "standing"}},
	// Sleep defines no safe interruption point, so a lost permission holds the
	// Actor in bed instead of moving it.
	{ID: "sleep", Occupancy: "bed", RequiresMovement: true, Rule: &InterruptionRule{CanInterrupt: false, StopState: "sitting"}},
	{ID: "walk", Occupancy: "path", RequiresMovement: true, Rule: &InterruptionRule{CanInterrupt: true, StopState: "standing"}},
}

// DefaultActivityDefinitions returns WORLD's owner mapping for the registered
// vocabulary, ordered by activity ID. Callers receive copies, so a controller
// cannot mutate the owner mapping.
func DefaultActivityDefinitions() []RunningActivity {
	out := make([]RunningActivity, 0, len(activityDefinitions))
	for _, definition := range activityDefinitions {
		if definition.Rule != nil {
			rule := *definition.Rule
			definition.Rule = &rule
		}
		out = append(out, definition)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
