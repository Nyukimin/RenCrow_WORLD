package life

import (
	"sort"
	"testing"
)

func TestDefaultCatalogRegistersCOREActivityVocabulary(t *testing.T) {
	want := []string{
		"change_clothes", "exercise", "idle", "read",
		"rest", "sit", "sleep", "walk",
	}
	got := RegisteredNonCognitiveActivityIDs()
	if len(got) != len(want) {
		t.Fatalf("activity IDs = %v, want %v", got, want)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("activity IDs are not sorted: %v", got)
	}
	catalog := DefaultCatalog()
	for _, id := range want {
		if got, want := catalog.IsRegisteredNonCognitive(id), true; got != want {
			t.Fatalf("IsRegisteredNonCognitive(%q) = %v, want %v", id, got, want)
		}
	}
	if catalog.IsRegisteredNonCognitive("stand") {
		t.Fatal("unregistered activity was granted")
	}
}

func TestAssessAcceptsRegisteredLifePolicyVocabulary(t *testing.T) {
	allowed := RegisteredNonCognitiveActivityIDs()
	got := AssessForTest(t, nil, policy(future(), allowed, []string{"rest"}), DefaultCatalog())
	if got.PolicyState != PolicyFresh || !got.NewActivityAllowed {
		t.Fatalf("policy = %+v", got)
	}
	if len(got.PermittedActivityIDs) != len(allowed) {
		t.Fatalf("permitted = %v, want %v", got.PermittedActivityIDs, allowed)
	}
}

// The owner mapping below is WORLD's: CORE owns the eight non-cognitive IDs and
// WORLD owns their logical posture, motion, and occupancy meaning (D-11, D-26).
// Occupancy names are logical only. They are not anchor IDs, room graph nodes,
// coordinates, or reservations, and admitting an activity never claims that a
// place exists or is reachable: that judgement belongs to the later R10/R11
// controller and to the D-17/D-18 geometry evidence, which does not exist yet.

func TestDefaultActivityDefinitionsCoverEveryRegisteredIDExactlyOnce(t *testing.T) {
	definitions := DefaultActivityDefinitions()
	want := RegisteredNonCognitiveActivityIDs()
	if len(definitions) != len(want) {
		t.Fatalf("definitions = %d, want %d", len(definitions), len(want))
	}
	catalog := DefaultCatalog()
	counts := make(map[string]int, len(definitions))
	ids := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		if !catalog.IsRegisteredNonCognitive(definition.ID) {
			t.Fatalf("definition for unregistered activity %q", definition.ID)
		}
		if definition.ID == "" {
			t.Fatal("definition without an activity ID")
		}
		counts[definition.ID]++
		ids = append(ids, definition.ID)
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("definitions are not ordered by ID: %v", ids)
	}
	for _, id := range want {
		if counts[id] != 1 {
			t.Fatalf("activity %q has %d definitions, want 1", id, counts[id])
		}
	}

	// Callers receive copies: a controller may not mutate the owner mapping.
	definitions[0].ID = "mutated"
	definitions[0].Occupancy = "mutated"
	definitions[0].Rule.CanInterrupt = false
	definitions[0].Rule.StopState = "mutated"
	fresh := DefaultActivityDefinitions()
	if fresh[0].ID != want[0] || fresh[0].Rule == nil || fresh[0].Rule.StopState == "mutated" {
		t.Fatalf("owner mapping was shared with a caller: %+v", fresh[0])
	}
}

func TestDefaultActivityDefinitionsDefineAStopAndNeverReleaseOccupancy(t *testing.T) {
	// D-11 requires an interruption point and a stop state per activity, and
	// forbids releasing physical occupancy while the body may still be on the
	// resource. WORLD has no measured movement or occupancy route yet, so no
	// owner definition may grant a release.
	for _, definition := range DefaultActivityDefinitions() {
		if definition.Rule == nil || definition.Rule.StopState == "" {
			t.Fatalf("%q has no defined stop state", definition.ID)
		}
		if definition.Rule.ReleaseOccupancy {
			t.Fatalf("%q grants occupancy release without measured movement", definition.ID)
		}
		if stop := PlanStop(definition, "policy_missing"); stop.Allowed && stop.ReleaseOccupancy {
			t.Fatalf("%q released occupancy through a stop plan: %+v", definition.ID, stop)
		}
	}
}

func TestDefaultActivityDefinitionsPublishEveryPermittedRegisteredActivity(t *testing.T) {
	got := AssessForTest(t, nil, policy(future(), RegisteredNonCognitiveActivityIDs(), nil), DefaultCatalog())
	ids, err := ExecutableActivityIDs(got, DefaultActivityDefinitions())
	if err != nil {
		t.Fatalf("ExecutableActivityIDs: %v", err)
	}
	want := RegisteredNonCognitiveActivityIDs()
	if len(ids) != len(want) {
		t.Fatalf("published ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("published ids = %v, want %v", ids, want)
		}
	}
}

func TestDefaultActivityDefinitionsKeepPresenceWorkInPlace(t *testing.T) {
	presence := AssessForTest(t, workSet(future(), item("running", true)), policy(future(), RegisteredNonCognitiveActivityIDs(), nil), DefaultCatalog())
	ids, err := ExecutableActivityIDs(presence, DefaultActivityDefinitions())
	if err != nil {
		t.Fatalf("ExecutableActivityIDs presence: %v", err)
	}
	if len(ids) != 1 || ids[0] != "idle" {
		t.Fatalf("presence candidates = %v, want only idle", ids)
	}

	// Unknown work is not a presence requirement, so it must not shrink the
	// permitted options either.
	expired := AssessForTest(t, workSet(past(), item("running", true)), policy(future(), RegisteredNonCognitiveActivityIDs(), nil), DefaultCatalog())
	ids, err = ExecutableActivityIDs(expired, DefaultActivityDefinitions())
	if err != nil {
		t.Fatalf("ExecutableActivityIDs expired: %v", err)
	}
	if want := RegisteredNonCognitiveActivityIDs(); len(ids) != len(want) {
		t.Fatalf("expired WorkSet shrank candidates to %v", ids)
	}
}

func TestDefaultActivityDefinitionsStopSafelyOrHoldWhenPermissionIsLost(t *testing.T) {
	revoked := AssessForTest(t, workSet(future()), nil, DefaultCatalog())
	for _, definition := range DefaultActivityDefinitions() {
		plan, err := PlanContinuation(revoked, definition)
		if err != nil {
			t.Fatalf("PlanContinuation(%q): %v", definition.ID, err)
		}
		if definition.ID == "sleep" {
			// Sleep defines no safe interruption point, so the Actor stays in
			// bed instead of being moved by a lost permission.
			if plan.Action != ContinuationHold || plan.Reason != "not_interruptible" {
				t.Fatalf("sleep plan = %+v", plan)
			}
			if plan.Stop.Allowed || plan.Stop.ReleaseOccupancy {
				t.Fatalf("sleep stop granted: %+v", plan.Stop)
			}
			continue
		}
		if plan.Action != ContinuationStop || plan.Reason != "policy_missing" || !plan.Stop.Allowed {
			t.Fatalf("%q plan = %+v", definition.ID, plan)
		}
		if plan.Stop.ReleaseOccupancy || plan.RetainOccupancyReason != "" {
			t.Fatalf("%q changed occupancy: %+v", definition.ID, plan)
		}
	}
}

func TestAdmitCandidateAdmitsEveryRegisteredOwnerDefinition(t *testing.T) {
	got := AssessForTest(t, nil, policy(future(), RegisteredNonCognitiveActivityIDs(), nil), DefaultCatalog())
	for _, definition := range DefaultActivityDefinitions() {
		admitted, err := AdmitCandidate(got, definition)
		if err != nil {
			t.Fatalf("AdmitCandidate(%q): %v", definition.ID, err)
		}
		if !admitted.Allowed || admitted.ActivityID != definition.ID || admitted.Reason != "admitted" {
			t.Fatalf("%q admission = %+v", definition.ID, admitted)
		}
	}
}
