package life

import (
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/projection"
)

var assessAt = time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC)

func future() time.Time { return assessAt.Add(time.Hour) }
func past() time.Time   { return assessAt.Add(-time.Hour) }

func workSet(validUntil time.Time, items ...projection.WorkItem) *projection.WorkSet {
	return &projection.WorkSet{
		WorldID: "rencrow-home", ActorID: "mio",
		SourceEpoch: "core-epoch-1", SourceRevision: 1,
		ValidUntil: validUntil, ActiveWork: items,
	}
}

func policy(validUntil time.Time, allowed, fallback []string) *projection.LifePolicy {
	return &projection.LifePolicy{
		WorldID: "rencrow-home", ActorID: "mio",
		SourceEpoch: "core-epoch-1", SourceRevision: 1,
		ValidUntil: validUntil, PersonaRevision: "persona-1", ConditionProfileRef: "condition-1",
		AllowedNonCognitiveActivityIDs: allowed, ExpiredFallbackActivityIDs: fallback,
	}
}

func item(state string, presence bool) projection.WorkItem {
	runID := ""
	if state == "running" {
		runID = "run-1"
	}
	return projection.WorkItem{
		TaskID: "task-" + state, RunID: runID,
		ExecutionState: state, WorkClass: "assigned_work", RequiresHousePresence: presence,
		ClassificationReasonCode: "assigned",
	}
}

func AssessForTest(t *testing.T, ws *projection.WorkSet, lp *projection.LifePolicy, catalog Catalog) Assessment {
	t.Helper()
	got, err := Assess(ws, lp, assessAt, catalog)
	if err != nil {
		t.Fatalf("Assess: %v", err)
	}
	return got
}

func TestWorkSetFreshEmptyIsNotSuccess(t *testing.T) {
	got := AssessForTest(t, workSet(future()), policy(future(), []string{"rest"}, nil), StaticCatalog{"rest": true})
	if got.WorkSetState != WorkSetEmpty || got.WorkPresentation.Kind != WorkNone {
		t.Fatalf("empty set = %+v", got)
	}
	if got.WorkSuccess {
		t.Fatal("empty WorkSet was treated as success")
	}
}

func TestWorkSetExpiredIsUnknownAndDoesNotMove(t *testing.T) {
	got := AssessForTest(t, workSet(past(), item("running", true)), policy(future(), []string{"rest"}, nil), StaticCatalog{"rest": true})
	if got.WorkSetState != WorkSetExpired || got.WorkPresentation.Kind != WorkNone {
		t.Fatalf("expired set = %+v", got)
	}
	if got.WorkSuccess {
		t.Fatal("expired WorkSet implied success")
	}
}

func TestWorkPresentationFollowsRunAndPresence(t *testing.T) {
	cases := []struct {
		name string
		ws   *projection.WorkSet
		want WorkPresentationKind
	}{
		{"queued only", workSet(future(), item("queued", true)), WorkQueued},
		{"waiting only", workSet(future(), item("waiting", true)), WorkWaiting},
		{"running no presence", workSet(future(), item("running", false)), WorkRemote},
		{"running presence", workSet(future(), item("running", true)), WorkPresence},
		{"queued plus running presence", workSet(future(), item("queued", true), item("running", true)), WorkPresence},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AssessForTest(t, tc.ws, policy(future(), []string{"rest"}, nil), StaticCatalog{"rest": true})
			if got.WorkPresentation.Kind != tc.want {
				t.Fatalf("presentation = %s, want %s", got.WorkPresentation.Kind, tc.want)
			}
		})
	}
}

func TestPolicyFreshUsesAllowedActivities(t *testing.T) {
	got := AssessForTest(t, nil, policy(future(), []string{"walk", "rest"}, []string{"rest"}), StaticCatalog{"walk": true, "rest": true})
	if got.PolicyState != PolicyFresh || !got.NewActivityAllowed {
		t.Fatalf("fresh policy = %+v", got)
	}
	if len(got.PermittedActivityIDs) != 2 {
		t.Fatalf("permitted = %v", got.PermittedActivityIDs)
	}
}

// TestWorkPresentationPublishesQueuedAndWaitingTaskIDs keeps D-13 observable:
// queued work and waiting/blocked work are published by task ID so a consumer
// can see why the Actor stays in place. Publishing them never claims movement:
// queued work alone does not move, and waiting/blocked work starts nothing new.
func TestWorkPresentationPublishesQueuedAndWaitingTaskIDs(t *testing.T) {
	cases := []struct {
		name         string
		ws           *projection.WorkSet
		wantKind     WorkPresentationKind
		wantQueued   []string
		wantWaiting  []string
		wantRunning  []string
		wantPresence []string
	}{
		{"queued only", workSet(future(), item("queued", true)), WorkQueued, []string{"task-queued"}, nil, nil, nil},
		{"waiting only", workSet(future(), item("waiting", true)), WorkWaiting, nil, []string{"task-waiting"}, nil, nil},
		{"blocked only", workSet(future(), item("blocked", true)), WorkWaiting, nil, []string{"task-blocked"}, nil, nil},
		{"running no presence", workSet(future(), item("running", false)), WorkRemote, nil, nil, []string{"task-running"}, nil},
		{"mixed presence dominates", workSet(future(), item("queued", true), item("waiting", true), item("running", true)), WorkPresence,
			[]string{"task-queued"}, []string{"task-waiting"}, []string{"task-running"}, []string{"task-running"}},
		{"expired set publishes nothing", workSet(past(), item("queued", true), item("waiting", true)), WorkNone, nil, nil, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AssessForTest(t, tc.ws, policy(future(), []string{"rest"}, nil), StaticCatalog{"rest": true})
			p := got.WorkPresentation
			if p.Kind != tc.wantKind {
				t.Fatalf("kind = %s, want %s", p.Kind, tc.wantKind)
			}
			assertIDs(t, "queued", p.QueuedTaskIDs, tc.wantQueued)
			assertIDs(t, "waiting", p.WaitingTaskIDs, tc.wantWaiting)
			assertIDs(t, "running", p.RunningTaskIDs, tc.wantRunning)
			assertIDs(t, "presence", p.PresenceTaskIDs, tc.wantPresence)
		})
	}
}

func assertIDs(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s task IDs = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s task IDs = %v, want %v", label, got, want)
		}
	}
}

func TestPolicyExpiredUsesOnlyRegisteredFallback(t *testing.T) {
	got := AssessForTest(t, nil, policy(past(), []string{"walk", "rest"}, []string{"rest", "outside"}), StaticCatalog{"walk": true, "rest": true})
	if got.PolicyState != PolicyExpired || !got.NewActivityAllowed {
		t.Fatalf("expired policy = %+v", got)
	}
	if len(got.PermittedActivityIDs) != 1 || got.PermittedActivityIDs[0] != "rest" {
		t.Fatalf("fallback = %v", got.PermittedActivityIDs)
	}
}

func TestPolicyExpiredRejectsFallbackOutsideAllowedEvenInTypedInput(t *testing.T) {
	got := AssessForTest(t, nil, policy(past(), []string{"walk"}, []string{"rest"}), StaticCatalog{"walk": true, "rest": true})
	if got.NewActivityAllowed || len(got.PermittedActivityIDs) != 0 {
		t.Fatalf("typed fallback outside allowed = %+v", got)
	}
}

func TestPolicyExpiredWithoutFallbackBlocksNewActivity(t *testing.T) {
	got := AssessForTest(t, nil, policy(past(), []string{"walk"}, nil), StaticCatalog{"walk": true})
	if got.PolicyState != PolicyExpired || got.NewActivityAllowed || got.BlockedReason != "policy_expired_no_fallback" {
		t.Fatalf("blocked policy = %+v", got)
	}
}

func TestPolicyMissingDoesNotOpenActivity(t *testing.T) {
	got := AssessForTest(t, nil, nil, StaticCatalog{"rest": true})
	if got.PolicyState != PolicyMissing || got.NewActivityAllowed || got.BlockedReason != "policy_missing" {
		t.Fatalf("missing policy = %+v", got)
	}
}

func TestPolicyFiltersUnregisteredActivities(t *testing.T) {
	got := AssessForTest(t, nil, policy(future(), []string{"walk"}, nil), StaticCatalog{})
	if got.NewActivityAllowed || got.BlockedReason != "no_registered_activity" {
		t.Fatalf("unregistered policy = %+v", got)
	}
}

func TestAssessmentRejectsMissingCatalogAndZeroClock(t *testing.T) {
	if _, err := Assess(nil, nil, assessAt, nil); err == nil {
		t.Fatal("nil catalog accepted")
	}
	if _, err := Assess(nil, nil, time.Time{}, StaticCatalog{}); err == nil {
		t.Fatal("zero clock accepted")
	}
}
