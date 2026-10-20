package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/RenCrow_WORLD/internal/projection"
	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

func workSetPayload(epoch string, revision int, active []any) map[string]any {
	return map[string]any{
		"contract_version": "rencrow.world.work-set/v2",
		"world_id":         "rencrow-home",
		"actor_id":         "mio",
		"source_owner":     "CORE",
		"source_epoch":     epoch,
		"source_revision":  revision,
		"valid_until":      "2026-10-21T00:00:00Z",
		"active_work":      active,
	}
}

func lifePolicyPayload(epoch string, revision int) map[string]any {
	return map[string]any{
		"contract_version":                   "world.life-policy/v1",
		"world_id":                           "rencrow-home",
		"actor_id":                           "mio",
		"source_owner":                       "CORE",
		"source_epoch":                       epoch,
		"source_revision":                    revision,
		"valid_until":                        "2026-10-21T00:00:00Z",
		"persona_revision":                   "persona-1",
		"allowed_non_cognitive_activity_ids": []string{"walk", "rest"},
		"expired_fallback_activity_ids":      []string{"rest"},
		"condition_profile_ref":              "condition-1",
	}
}

func projectionEnvelope(t *testing.T, kind, actor, instance, epoch string, revision int, payload map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"contract_version":  "world.contracts/v1",
		"record_type":       "CoreProjectionEnvelope",
		"world_id":          "rencrow-home",
		"timeline_id":       "timeline-1",
		"timeline_epoch":    "epoch-1",
		"actor_instance_id": instance,
		"projection_kind":   kind,
		"payload":           payload,
	})
	if err != nil {
		t.Fatalf("marshal projection: %v", err)
	}
	return raw
}

func projectionURL(ts *httptest.Server, actor, kind string) string {
	return ts.URL + "/v1/worlds/rencrow-home/timelines/timeline-1/core-projections/" + actor + "/" + kind
}

func projectionAck(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	body := decodeMap(t, resp)
	ack, ok := body["projection"].(map[string]any)
	if !ok {
		t.Fatalf("missing projection ack: %v", body)
	}
	return ack
}

func TestCoreProjectionOrderingOverHTTP(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	url := projectionURL(ts, "mio", projection.KindWorkSet)
	item := []any{map[string]any{
		"task_id": "task-1", "run_id": "run-1", "execution_state": "running",
		"work_class": "assigned_work", "requires_house_presence": true,
		"classification_reason_code": "assigned",
	}}
	body10 := projectionEnvelope(t, projection.KindWorkSet, "mio", "core-instance-mio", "core-epoch-1", 10, workSetPayload("core-epoch-1", 10, item))

	resp := doRequest(t, http.MethodPut, url, projectionToken, "", body10)
	if ack := projectionAck(t, resp); resp.StatusCode != http.StatusOK || ack["status"] != store.ProjectionAccepted {
		t.Fatalf("rev10 = %d %v", resp.StatusCode, ack)
	} else if ack["publisher_id"] != "core-publisher" || ack["credential_id"] != "core" {
		t.Fatalf("ack identity = %v", ack)
	}
	resp = doRequest(t, http.MethodGet, url, projectionToken, "", nil)
	head := decodeMap(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("head = %d %v", resp.StatusCode, head)
	}
	hp := head["projection"].(map[string]any)
	if hp["source_revision"].(float64) != 10 || hp["digest"] == "" || hp["valid_until"] == nil {
		t.Fatalf("head body = %v", hp)
	}

	// Projection ordering is independent from Command idempotency: retries use
	// the same source epoch/revision/digest, not Idempotency-Key.
	resp = doRequest(t, http.MethodPut, url, projectionToken, "", body10)
	if ack := projectionAck(t, resp); resp.StatusCode != http.StatusOK || ack["status"] != store.ProjectionDuplicate {
		t.Fatalf("duplicate = %d %v", resp.StatusCode, ack)
	}

	stale := projectionEnvelope(t, projection.KindWorkSet, "mio", "core-instance-mio", "core-epoch-1", 9, workSetPayload("core-epoch-1", 9, item))
	resp = doRequest(t, http.MethodPut, url, projectionToken, "", stale)
	if ack := projectionAck(t, resp); resp.StatusCode != http.StatusConflict || ack["status"] != store.ProjectionStaleRevision {
		t.Fatalf("stale = %d %v", resp.StatusCode, ack)
	}

	conflict := projectionEnvelope(t, projection.KindWorkSet, "mio", "core-instance-mio", "core-epoch-1", 10, workSetPayload("core-epoch-1", 10, []any{}))
	resp = doRequest(t, http.MethodPut, url, projectionToken, "", conflict)
	if ack := projectionAck(t, resp); resp.StatusCode != http.StatusConflict || ack["status"] != store.ProjectionRevisionConflict {
		t.Fatalf("conflict = %d %v", resp.StatusCode, ack)
	}

	body11 := projectionEnvelope(t, projection.KindWorkSet, "mio", "core-instance-mio", "core-epoch-1", 11, workSetPayload("core-epoch-1", 11, []any{}))
	resp = doRequest(t, http.MethodPut, url, projectionToken, "", body11)
	if ack := projectionAck(t, resp); resp.StatusCode != http.StatusOK || ack["status"] != store.ProjectionAccepted {
		t.Fatalf("rev11 = %d %v", resp.StatusCode, ack)
	}

	// Another projection kind has its own revision sequence.
	policyURL := projectionURL(ts, "mio", projection.KindLifePolicy)
	policy := projectionEnvelope(t, projection.KindLifePolicy, "mio", "core-instance-mio", "core-epoch-1", 1, lifePolicyPayload("core-epoch-1", 1))
	resp = doRequest(t, http.MethodPut, policyURL, projectionToken, "", policy)
	if ack := projectionAck(t, resp); resp.StatusCode != http.StatusOK || ack["status"] != store.ProjectionAccepted {
		t.Fatalf("life policy = %d %v", resp.StatusCode, ack)
	}
}

func TestCoreProjectionReconcileAndBinding(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	url := projectionURL(ts, "mio", projection.KindWorkSet)
	old := projectionEnvelope(t, projection.KindWorkSet, "mio", "core-instance-mio", "core-epoch-1", 1, workSetPayload("core-epoch-1", 1, []any{}))
	if resp := doRequest(t, http.MethodPut, url, projectionToken, "", old); resp.StatusCode != http.StatusOK {
		t.Fatalf("old epoch = %d", resp.StatusCode)
	}
	next := projectionEnvelope(t, projection.KindWorkSet, "mio", "core-instance-mio", "core-epoch-2", 1, workSetPayload("core-epoch-2", 1, []any{}))
	resp := doRequest(t, http.MethodPut, url, projectionToken, "", next)
	if ack := projectionAck(t, resp); resp.StatusCode != http.StatusConflict || ack["status"] != store.ProjectionEpochRequiresReconcile {
		t.Fatalf("unapproved epoch = %d %v", resp.StatusCode, ack)
	}
	if resp := doRequest(t, http.MethodPut, url+"?reconcile=true", projectionToken, "", next); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("normal projection reconciled = %d", resp.StatusCode)
	}
	if resp := doRequest(t, http.MethodPut, url, projectionReconcileToken, "", next); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reconcile token normal projection = %d", resp.StatusCode)
	}

	resp = doRequest(t, http.MethodPut, url+"?reconcile=true", projectionReconcileToken, "", next)
	if ack := projectionAck(t, resp); resp.StatusCode != http.StatusOK || ack["status"] != store.ProjectionAccepted {
		t.Fatalf("reconcile = %d %v", resp.StatusCode, ack)
	}
	late := projectionEnvelope(t, projection.KindWorkSet, "mio", "core-instance-mio", "core-epoch-1", 999, workSetPayload("core-epoch-1", 999, []any{}))
	resp = doRequest(t, http.MethodPut, url, projectionToken, "", late)
	if ack := projectionAck(t, resp); resp.StatusCode != http.StatusConflict || ack["status"] != store.ProjectionEpochSuperseded {
		t.Fatalf("superseded = %d %v", resp.StatusCode, ack)
	}

	// The body actor and the path actor must be the same, so build the unbound
	// Actor payload directly instead of reusing mio's payload.
	unbound := projectionEnvelope(t, projection.KindWorkSet, "kuro", "core-instance-kuro", "core-epoch-2", 1, map[string]any{
		"contract_version": "rencrow.world.work-set/v2", "world_id": "rencrow-home", "actor_id": "kuro",
		"source_owner": "CORE", "source_epoch": "core-epoch-2", "source_revision": 1,
		"valid_until": "2026-10-21T00:00:00Z", "active_work": []any{},
	})
	resp = doRequest(t, http.MethodPut, projectionURL(ts, "kuro", projection.KindWorkSet), projectionToken, "", unbound)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unbound actor = %d", resp.StatusCode)
	}
	if body := decodeMap(t, resp); body["error"].(map[string]any)["code"] != "actor_not_bound" {
		t.Fatalf("unbound body = %v", body)
	}
}

func TestCoreProjectionRejectsBadRequestsAndScopes(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	url := projectionURL(ts, "mio", projection.KindWorkSet)
	valid := projectionEnvelope(t, projection.KindWorkSet, "mio", "core-instance-mio", "core-epoch-1", 1, workSetPayload("core-epoch-1", 1, []any{}))

	if resp := doRequest(t, http.MethodPut, url, writerToken, "", valid); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("writer projection = %d", resp.StatusCode)
	}
	if resp := doRequest(t, http.MethodPut, url, readerToken, "", valid); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reader projection = %d", resp.StatusCode)
	}
	if resp := doRequest(t, http.MethodPost, commandsURL(ts), projectionToken, "k", commandBody(t, "debug.noop", map[string]any{})); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("projection token command = %d", resp.StatusCode)
	}
	if resp := doRequest(t, http.MethodGet, url, readerToken, "", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("get projection with read token = %d", resp.StatusCode)
	}

	badKind := projectionEnvelope(t, projection.KindWorkSet, "mio", "core-instance-mio", "core-epoch-1", 1, workSetPayload("core-epoch-1", 1, []any{}))
	resp := doRequest(t, http.MethodPut, projectionURL(ts, "mio", projection.KindLifePolicy), projectionToken, "", badKind)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("kind mismatch = %d", resp.StatusCode)
	}

	runningNoRun := workSetPayload("core-epoch-1", 1, []any{map[string]any{
		"task_id": "task-1", "execution_state": "running", "work_class": "assigned_work",
		"requires_house_presence": true, "classification_reason_code": "assigned",
	}})
	bad := projectionEnvelope(t, projection.KindWorkSet, "mio", "core-instance-mio", "core-epoch-1", 1, runningNoRun)
	resp = doRequest(t, http.MethodPut, url, projectionToken, "", bad)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("running without run = %d", resp.StatusCode)
	}
	if body := decodeMap(t, resp); body["error"].(map[string]any)["code"] != "invalid_projection" {
		t.Fatalf("invalid body = %v", body)
	}

	wrongEpoch := projectionEnvelope(t, projection.KindWorkSet, "mio", "core-instance-mio", "core-epoch-1", 1, workSetPayload("core-epoch-1", 1, []any{}))
	// The configured destination epoch is authoritative; a body cannot choose
	// another epoch and have it inferred from source_epoch.
	var decoded map[string]any
	if err := json.Unmarshal(wrongEpoch, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["timeline_epoch"] = "epoch-2"
	raw, _ := json.Marshal(decoded)
	resp = doRequest(t, http.MethodPut, url, projectionToken, "", raw)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong destination epoch = %d", resp.StatusCode)
	}
}

func TestCoreProjectionSurvivesHTTPRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := baseConfig(t, dir)
	s1, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts1 := httptest.NewServer(s1.Handler())
	url := projectionURL(ts1, "mio", projection.KindWorkSet)
	body := projectionEnvelope(t, projection.KindWorkSet, "mio", "core-instance-mio", "core-epoch-1", 1, workSetPayload("core-epoch-1", 1, []any{}))
	if resp := doRequest(t, http.MethodPut, url, projectionToken, "", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("first = %d", resp.StatusCode)
	}
	ts1.Close()
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := New(cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	ts2 := httptest.NewServer(s2.Handler())
	defer ts2.Close()
	url = projectionURL(ts2, "mio", projection.KindWorkSet)
	resp := doRequest(t, http.MethodPut, url, projectionToken, "", body)
	if ack := projectionAck(t, resp); resp.StatusCode != http.StatusOK || ack["status"] != store.ProjectionDuplicate {
		t.Fatalf("duplicate after restart = %d %v", resp.StatusCode, ack)
	}
	next := projectionEnvelope(t, projection.KindWorkSet, "mio", "core-instance-mio", "core-epoch-1", 2, workSetPayload("core-epoch-1", 2, []any{}))
	resp = doRequest(t, http.MethodPut, url, projectionToken, "", next)
	if ack := projectionAck(t, resp); resp.StatusCode != http.StatusOK || ack["status"] != store.ProjectionAccepted {
		t.Fatalf("next after restart = %d %v", resp.StatusCode, ack)
	}
}
