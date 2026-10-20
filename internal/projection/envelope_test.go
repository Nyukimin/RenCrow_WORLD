package projection

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func workSetEnvelope(epoch string, revision uint64, activeWork string) []byte {
	if activeWork == "" {
		activeWork = `[]`
	}
	return []byte(`{
		"contract_version": "world.contracts/v1",
		"record_type": "CoreProjectionEnvelope",
		"world_id": "rencrow-home",
		"timeline_id": "timeline-1",
		"timeline_epoch": "timeline-epoch-1",
		"actor_instance_id": "core-instance-mio",
		"projection_kind": "full_actor_work_set",
		"payload": {
			"contract_version": "rencrow.world.work-set/v2",
			"world_id": "rencrow-home",
			"actor_id": "mio",
			"source_owner": "CORE",
			"source_epoch": "` + epoch + `",
			"source_revision": ` + jsonNumber(revision) + `,
			"valid_until": "2026-10-21T00:00:00Z",
			"active_work": ` + activeWork + `
		}
	}`)
}

func jsonNumber(v uint64) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func validWorkItem(state string) string {
	run := ""
	if state == "running" {
		run = `"run_id": "run-1",`
	}
	return `{
		"task_id": "task-1",
		` + run + `
		"execution_state": "` + state + `",
		"work_class": "assigned_work",
		"requires_house_presence": true,
		"classification_reason_code": "assigned"
	}`
}

func decodeTestWorkSet(t *testing.T, raw []byte) Envelope {
	t.Helper()
	env, err := Decode(raw, "rencrow-home", "timeline-1", "timeline-epoch-1")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return env
}

func TestDecodeWorkSetAndCanonicalDigest(t *testing.T) {
	raw := workSetEnvelope("core-epoch-1", 10, `[`+validWorkItem("running")+`]`)
	env := decodeTestWorkSet(t, raw)
	if env.ActorID != "mio" || env.ActorInstanceID != "core-instance-mio" ||
		env.Kind != KindWorkSet || env.SourceEpoch != "core-epoch-1" || env.SourceRevision != 10 {
		t.Fatalf("decoded envelope = %+v", env)
	}
	if env.Digest == "" || len(env.Digest) != 64 {
		t.Fatalf("digest = %q", env.Digest)
	}
	if got, want := env.ValidUntil.UTC(), time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC); got != want {
		t.Fatalf("valid_until = %s, want %s", got, want)
	}

	// Object key order is not semantic. The same payload must produce the same
	// canonical digest even when CORE serializes its fields in another order.
	reordered := []byte(`{
		"projection_kind": "full_actor_work_set",
		"actor_instance_id": "core-instance-mio",
		"timeline_epoch": "timeline-epoch-1",
		"timeline_id": "timeline-1",
		"world_id": "rencrow-home",
		"record_type": "CoreProjectionEnvelope",
		"contract_version": "world.contracts/v1",
		"payload": {
			"active_work": [` + validWorkItem("running") + `],
			"valid_until": "2026-10-21T00:00:00Z",
			"source_revision": 10,
			"source_epoch": "core-epoch-1",
			"source_owner": "CORE",
			"actor_id": "mio",
			"world_id": "rencrow-home",
			"contract_version": "rencrow.world.work-set/v2"
		}
	}`)
	other := decodeTestWorkSet(t, reordered)
	if other.Digest != env.Digest {
		t.Fatalf("digest depends on object key order: %s != %s", other.Digest, env.Digest)
	}
}

func TestDecodeWorkSetAcceptsEmptySetAndQueuedWithoutRun(t *testing.T) {
	env := decodeTestWorkSet(t, workSetEnvelope("core-epoch-1", 1, `[]`))
	if env.SourceRevision != 1 {
		t.Fatalf("revision = %d", env.SourceRevision)
	}
	queued := decodeTestWorkSet(t, workSetEnvelope("core-epoch-1", 2, `[`+validWorkItem("queued")+`]`))
	if queued.SourceRevision != 2 {
		t.Fatalf("queued revision = %d", queued.SourceRevision)
	}
}

func TestDecodeRejectsInvalidWorkSets(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
	}{
		{"running without run", workSetEnvelope("core-epoch-1", 1, `[`+validWorkItem("running")+`]`)},
		{"terminal state", workSetEnvelope("core-epoch-1", 1, `[{
			"task_id":"task-1","execution_state":"completed","work_class":"assigned_work",
			"requires_house_presence":true,"classification_reason_code":"assigned"}]`)},
		{"unknown work item field", workSetEnvelope("core-epoch-1", 1, `[{
			"task_id":"task-1","execution_state":"queued","work_class":"assigned_work",
			"requires_house_presence":true,"classification_reason_code":"assigned","extra":true}]`)},
		{"missing active_work", []byte(`{
			"contract_version":"world.contracts/v1","record_type":"CoreProjectionEnvelope",
			"world_id":"rencrow-home","timeline_id":"timeline-1","timeline_epoch":"timeline-epoch-1",
			"actor_instance_id":"core-instance-mio","projection_kind":"full_actor_work_set",
			"payload":{"contract_version":"rencrow.world.work-set/v2","world_id":"rencrow-home",
			"actor_id":"mio","source_owner":"CORE","source_epoch":"core-epoch-1","source_revision":1,
			"valid_until":"2026-10-21T00:00:00Z"}}`)},
		{"zero revision", workSetEnvelope("core-epoch-1", 0, `[]`)},
		{"wrong work set world", []byte(`{
			"contract_version":"world.contracts/v1","record_type":"CoreProjectionEnvelope",
			"world_id":"rencrow-home","timeline_id":"timeline-1","timeline_epoch":"timeline-epoch-1",
			"actor_instance_id":"core-instance-mio","projection_kind":"full_actor_work_set",
			"payload":{"contract_version":"rencrow.world.work-set/v2","world_id":"other",
			"actor_id":"mio","source_owner":"CORE","source_epoch":"core-epoch-1","source_revision":1,
			"valid_until":"2026-10-21T00:00:00Z","active_work":[]}}`)},
	}
	// The first case names a run-less running item on purpose.
	cases[0].raw = []byte(strings.Replace(string(workSetEnvelope("core-epoch-1", 1, `[`+validWorkItem("running")+`]`)), `"run_id": "run-1",`, "", 1))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Decode(tc.raw, "rencrow-home", "timeline-1", "timeline-epoch-1"); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestDecodeLifePolicy(t *testing.T) {
	raw := []byte(`{
		"contract_version":"world.contracts/v1","record_type":"CoreProjectionEnvelope",
		"world_id":"rencrow-home","timeline_id":"timeline-1","timeline_epoch":"timeline-epoch-1",
		"actor_instance_id":"core-instance-mio","projection_kind":"life_policy",
		"payload":{
			"contract_version":"world.life-policy/v1","world_id":"rencrow-home","actor_id":"mio",
			"source_owner":"CORE","source_epoch":"core-epoch-1","source_revision":3,
			"valid_until":"2026-10-21T00:00:00Z","persona_revision":"persona-7",
			"allowed_non_cognitive_activity_ids":["walk","read","idle","rest"],
			"expired_fallback_activity_ids":["idle","rest"],
			"condition_profile_ref":"condition-1"
		}
	}`)
	env, err := Decode(raw, "rencrow-home", "timeline-1", "timeline-epoch-1")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if env.Kind != KindLifePolicy || env.SourceRevision != 3 {
		t.Fatalf("envelope = %+v", env)
	}

	duplicate := []byte(strings.Replace(string(raw), `["walk","read","idle","rest"]`, `["walk","walk"]`, 1))
	if _, err := Decode(duplicate, "rencrow-home", "timeline-1", "timeline-epoch-1"); err == nil {
		t.Fatal("duplicate activity id accepted")
	}

	outside := []byte(strings.Replace(string(raw), `["idle","rest"]`, `["idle","outside"]`, 1))
	if _, err := Decode(outside, "rencrow-home", "timeline-1", "timeline-epoch-1"); err == nil {
		t.Fatal("fallback outside allowed activities accepted")
	}
}

func TestDecodeRejectsDuplicateObjectKeysAndTrailingValues(t *testing.T) {
	raw := workSetEnvelope("core-epoch-1", 1, `[]`)
	dup := []byte(strings.Replace(string(raw), `"source_revision": 1`, `"source_revision": 1, "source_revision": 2`, 1))
	if _, err := Decode(dup, "rencrow-home", "timeline-1", "timeline-epoch-1"); err == nil {
		t.Fatal("duplicate key accepted")
	}
	trailing := append(append([]byte(nil), raw...), []byte(` {}`)...)
	if _, err := Decode(trailing, "rencrow-home", "timeline-1", "timeline-epoch-1"); err == nil {
		t.Fatal("trailing value accepted")
	}
}

func TestDecodeRejectsWrongDestination(t *testing.T) {
	raw := workSetEnvelope("core-epoch-1", 1, `[]`)
	for _, tc := range []struct{ world, timeline, epoch string }{
		{"other", "timeline-1", "timeline-epoch-1"},
		{"rencrow-home", "other", "timeline-epoch-1"},
		{"rencrow-home", "timeline-1", "other"},
	} {
		if _, err := Decode(raw, tc.world, tc.timeline, tc.epoch); err == nil {
			t.Fatalf("accepted wrong destination %+v", tc)
		}
	}
}

func TestDecodeRejectsUnknownEnvelopeKind(t *testing.T) {
	raw := []byte(strings.Replace(string(workSetEnvelope("core-epoch-1", 1, `[]`)), `"full_actor_work_set"`, `"memory_dump"`, 1))
	if _, err := Decode(raw, "rencrow-home", "timeline-1", "timeline-epoch-1"); err == nil {
		t.Fatal("unknown projection kind accepted")
	}
}
