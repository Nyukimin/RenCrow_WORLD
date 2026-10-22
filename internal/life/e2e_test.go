package life_test

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/life"
	"github.com/Nyukimin/RenCrow_WORLD/internal/projection"
	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

// TestE2EProjectionToLifeAssessment uses the real SQLite EventStore and the
// production projection parser. It verifies that stored heads can be evaluated
// without creating a WORLD Event, State change, or Command Receipt.
func TestE2EProjectionToLifeAssessment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "world.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	scope := store.Scope{WorldID: "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-1"}
	if err := s.CreateTimeline(scope, store.State{Actors: map[string]store.ActorState{"mio": {RoomID: "1f-living"}}}, nil); err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if err := s.BindProjectionActor(scope, "mio", "core-instance-mio", time.Now().UTC()); err != nil {
		t.Fatalf("bind: %v", err)
	}

	now := time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC)
	workPayload := json.RawMessage(`{
		"contract_version":"rencrow.world.work-set/v2","world_id":"rencrow-home","actor_id":"mio",
		"source_owner":"CORE","source_epoch":"core-epoch-1","source_revision":1,
		"valid_until":"2026-10-20T04:00:00Z","active_work":[
			{"task_id":"task-1","run_id":"run-1","execution_state":"running","work_class":"assigned_work","requires_house_presence":true,"classification_reason_code":"assigned"}
		]}`)
	policyPayload := json.RawMessage(`{
		"contract_version":"world.life-policy/v1","world_id":"rencrow-home","actor_id":"mio",
		"source_owner":"CORE","source_epoch":"core-epoch-1","source_revision":1,
		"valid_until":"2026-10-20T04:00:00Z","persona_revision":"persona-1",
		"allowed_non_cognitive_activity_ids":["walk","rest"],
		"expired_fallback_activity_ids":["rest"],"condition_profile_ref":"condition-1"}`)

	apply := func(kind string, revision uint64, validUntil time.Time, payload json.RawMessage) {
		t.Helper()
		_, err := s.ApplyProjection(store.Projection{
			Scope: scope, ActorID: "mio", ActorInstanceID: "core-instance-mio",
			Kind: kind, SourceEpoch: "core-epoch-1", SourceRevision: revision,
			Payload: payload, PublisherID: "core-publisher", CredentialID: "core",
			ValidUntil: validUntil, ReceivedAt: now,
		})
		if err != nil {
			t.Fatalf("apply %s: %v", kind, err)
		}
	}
	apply(store.ProjectionKindWorkSet, 1, now.Add(time.Hour), workPayload)
	apply(store.ProjectionKindLifePolicy, 1, now.Add(time.Hour), policyPayload)

	assess := func() life.Assessment {
		t.Helper()
		workHead, err := s.ProjectionHead(scope, "mio", store.ProjectionKindWorkSet)
		if err != nil {
			t.Fatalf("work head: %v", err)
		}
		policyHead, err := s.ProjectionHead(scope, "mio", store.ProjectionKindLifePolicy)
		if err != nil {
			t.Fatalf("policy head: %v", err)
		}
		work, err := projection.ParseWorkSet(workHead.Payload)
		if err != nil {
			t.Fatalf("parse work: %v", err)
		}
		policy, err := projection.ParseLifePolicy(policyHead.Payload)
		if err != nil {
			t.Fatalf("parse policy: %v", err)
		}
		got, err := life.Assess(&work, &policy, now, life.DefaultCatalog())
		if err != nil {
			t.Fatalf("assess: %v", err)
		}
		return got
	}

	got := assess()
	if got.WorkSetState != life.WorkSetAssigned || got.WorkPresentation.Kind != life.WorkPresence ||
		got.PolicyState != life.PolicyFresh || !got.NewActivityAllowed {
		t.Fatalf("fresh assessment = %+v", got)
	}

	// WORLD's owner mapping is the candidate source for these real heads.
	// Presence-required running work keeps the Actor in place, so neither of the
	// two permitted activities is offered or admitted, and a life activity the
	// fresh policy does not name stops through its own rule without releasing
	// the physical resource it holds.
	definitions := life.DefaultActivityDefinitions()
	if candidates, err := life.ExecutableActivityIDs(got, definitions); err != nil {
		t.Fatalf("executable ids: %v", err)
	} else if len(candidates) != 0 {
		t.Fatalf("presence work offered candidates: %v", candidates)
	}
	if admitted, err := life.AdmitCandidate(got, ownerActivity(t, definitions, "walk")); err != nil {
		t.Fatalf("admit walk: %v", err)
	} else if admitted.Allowed || admitted.Reason != "presence_required" {
		t.Fatalf("walk admission = %+v", admitted)
	}
	if plan, err := life.PlanContinuation(got, ownerActivity(t, definitions, "sit")); err != nil {
		t.Fatalf("continue sit: %v", err)
	} else if plan.Action != life.ContinuationStop || plan.Reason != "activity_not_permitted" ||
		!plan.Stop.Allowed || plan.Stop.ReleaseOccupancy {
		t.Fatalf("revoked sit plan = %+v", plan)
	}

	expiredPolicy := json.RawMessage(`{
		"contract_version":"world.life-policy/v1","world_id":"rencrow-home","actor_id":"mio",
		"source_owner":"CORE","source_epoch":"core-epoch-1","source_revision":2,
		"valid_until":"2026-10-20T02:00:00Z","persona_revision":"persona-1",
		"allowed_non_cognitive_activity_ids":["walk","rest"],
		"expired_fallback_activity_ids":["rest"],"condition_profile_ref":"condition-1"}`)
	apply(store.ProjectionKindLifePolicy, 2, now.Add(-time.Hour), expiredPolicy)
	got = assess()
	if got.PolicyState != life.PolicyExpired || !got.NewActivityAllowed ||
		len(got.PermittedActivityIDs) != 1 || got.PermittedActivityIDs[0] != "rest" {
		t.Fatalf("expired assessment = %+v", got)
	}

	// The expired fallback keeps only `rest`, which still needs movement, so
	// presence work keeps offering nothing while the fallback activity that is
	// already permitted keeps running.
	if candidates, err := life.ExecutableActivityIDs(got, definitions); err != nil {
		t.Fatalf("expired executable ids: %v", err)
	} else if len(candidates) != 0 {
		t.Fatalf("expired fallback offered candidates during presence work: %v", candidates)
	}
	if plan, err := life.PlanContinuation(got, ownerActivity(t, definitions, "rest")); err != nil {
		t.Fatalf("continue rest: %v", err)
	} else if plan.Action != life.ContinuationContinue || plan.Reason != "policy_expired_fallback" {
		t.Fatalf("fallback plan = %+v", plan)
	}

	plan := life.PlanStop(life.RunningActivity{
		ID: "activity-1", Occupancy: "seat",
		Rule: &life.InterruptionRule{CanInterrupt: true, StopState: "seated"},
	}, "policy_expired")
	if !plan.Allowed || plan.ReleaseOccupancy {
		t.Fatalf("stop plan = %+v", plan)
	}

	_, seq, _, err := s.Snapshot(scope)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if seq != 0 {
		t.Fatalf("projection evaluation changed state_seq to %d", seq)
	}
}

// ownerActivity selects one entry of WORLD's owner mapping and fails when the
// mapping does not name it, so the evidence cannot pass on a missing activity.
func ownerActivity(t *testing.T, definitions []life.RunningActivity, id string) life.RunningActivity {
	t.Helper()
	for _, definition := range definitions {
		if definition.ID == id {
			return definition
		}
	}
	t.Fatalf("owner activity mapping has no %q", id)
	return life.RunningActivity{}
}
