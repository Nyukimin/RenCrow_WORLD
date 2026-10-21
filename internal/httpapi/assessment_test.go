package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/idmap"
	"github.com/Nyukimin/RenCrow_WORLD/internal/projection"
)

func TestAssessmentRequiresReadScopeAndBoundActor(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	url := assessmentURL(t, ts, "mio")

	if resp := doRequest(t, http.MethodGet, url, "", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token = %d", resp.StatusCode)
	}
	if resp := doRequest(t, http.MethodGet, url, streamerToken, "", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stream-only token = %d", resp.StatusCode)
	}
	if resp := doRequest(t, http.MethodGet, url, wrongScopeToken, "", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong-scope token = %d", resp.StatusCode)
	}
	if resp := doRequest(t, http.MethodGet, url, readerToken, "", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("read token = %d", resp.StatusCode)
	}
	if resp := doRequest(t, http.MethodGet, assessmentRawURL(ts, "~"), readerToken, "", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid actor transport = %d", resp.StatusCode)
	}
	if resp := doRequest(t, http.MethodGet, assessmentURL(t, ts, "kuro"), readerToken, "", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unbound actor = %d", resp.StatusCode)
	}
}

func TestAssessmentReturnsMissingHeadsWithoutProjection(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	resp := doRequest(t, http.MethodGet, assessmentURL(t, ts, "mio"), readerToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("assessment = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	body := decodeMap(t, resp)
	if body["schema_version"].(float64) != 1 || body["world_id"] != "rencrow-home" ||
		body["timeline_id"] != "timeline-1" || body["timeline_epoch"] != "epoch-1" ||
		body["actor_id"] != "mio" {
		t.Fatalf("scope = %v", body)
	}
	if body["work_set_state"] != "missing" || body["policy_state"] != "missing" {
		t.Fatalf("states = %v", body)
	}
	if body["new_activity_allowed"] != false || body["work_success"] != false {
		t.Fatalf("permission/success = %v", body)
	}
	if body["blocked_reason"] != "policy_missing" {
		t.Fatalf("blocked_reason = %v", body["blocked_reason"])
	}
	presentation := body["work_presentation"].(map[string]any)
	if presentation["kind"] != "none" || len(presentation["presence_task_ids"].([]any)) != 0 ||
		len(presentation["running_task_ids"].([]any)) != 0 {
		t.Fatalf("presentation = %v", presentation)
	}
	if ids, ok := body["permitted_activity_ids"].([]any); !ok || len(ids) != 0 {
		t.Fatalf("permitted_activity_ids = %v", body["permitted_activity_ids"])
	}
	if _, err := time.Parse(time.RFC3339Nano, body["assessed_at"].(string)); err != nil {
		t.Fatalf("assessed_at = %v: %v", body["assessed_at"], err)
	}
}

func TestAssessmentConsumesDefaultCatalogAndProjectionHeads(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	applyAssessmentProjection(t, ts, projection.KindWorkSet, 1, workSetPayloadAt(future, []any{}))
	applyAssessmentProjection(t, ts, projection.KindLifePolicy, 1, lifePolicyPayloadAt(future, []string{"walk", "rest", "unknown"}, []string{"rest"}))

	body := getAssessment(t, ts, "mio")
	if body["work_set_state"] != "empty" || body["policy_state"] != "fresh" {
		t.Fatalf("states = %v", body)
	}
	assertStringSet(t, body["permitted_activity_ids"], []string{"rest", "walk"})
	if body["new_activity_allowed"] != true || body["work_success"] != false {
		t.Fatalf("permission/success = %v", body)
	}
	if _, present := body["blocked_reason"]; present {
		t.Fatalf("fresh policy blocked: %v", body["blocked_reason"])
	}
	presentation := body["work_presentation"].(map[string]any)
	if presentation["kind"] != "none" {
		t.Fatalf("presentation = %v", presentation)
	}

	item := []any{map[string]any{
		"task_id": "task-1", "run_id": "run-1", "execution_state": "running",
		"work_class": "assigned_work", "requires_house_presence": true,
		"classification_reason_code": "assigned",
	}}
	applyAssessmentProjection(t, ts, projection.KindWorkSet, 2, workSetPayloadAt(future, item))
	body = getAssessment(t, ts, "mio")
	if body["work_set_state"] != "assigned" {
		t.Fatalf("work_set_state = %v", body["work_set_state"])
	}
	presentation = body["work_presentation"].(map[string]any)
	if presentation["kind"] != "presence" {
		t.Fatalf("presentation = %v", presentation)
	}
	assertStringSet(t, presentation["presence_task_ids"], []string{"task-1"})
	assertStringSet(t, presentation["running_task_ids"], []string{"task-1"})
}

func TestAssessmentExpiredPolicyUsesRegisteredFallback(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	applyAssessmentProjection(t, ts, projection.KindLifePolicy, 1, lifePolicyPayloadAt(past, []string{"walk", "rest"}, []string{"rest"}))

	body := getAssessment(t, ts, "mio")
	if body["policy_state"] != "expired" {
		t.Fatalf("policy_state = %v", body["policy_state"])
	}
	assertStringSet(t, body["permitted_activity_ids"], []string{"rest"})
	if body["new_activity_allowed"] != true || body["work_success"] != false {
		t.Fatalf("permission/success = %v", body)
	}
}

func TestAssessmentDoesNotChangeWorldState(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	before := decodeMap(t, doRequest(t, http.MethodGet, snapshotURL(ts), readerToken, "", nil))
	_ = getAssessment(t, ts, "mio")
	after := decodeMap(t, doRequest(t, http.MethodGet, snapshotURL(ts), readerToken, "", nil))
	if before["state_seq"] != after["state_seq"] || before["state_hash"] != after["state_hash"] {
		t.Fatalf("assessment changed World state: before=%v after=%v", before, after)
	}
}

func getAssessment(t *testing.T, ts *httptest.Server, actorID string) map[string]any {
	t.Helper()
	resp := doRequest(t, http.MethodGet, assessmentURL(t, ts, actorID), readerToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("assessment = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	return decodeMap(t, resp)
}

func applyAssessmentProjection(t *testing.T, ts *httptest.Server, kind string, revision int, payload map[string]any) {
	t.Helper()
	payload["source_revision"] = revision
	raw := projectionEnvelope(t, kind, "mio", "core-instance-mio", "core-epoch-1", revision, payload)
	resp := doRequest(t, http.MethodPut, projectionURL(ts, "mio", kind), projectionToken, "", raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply %s revision %d = %d: %s", kind, revision, resp.StatusCode, readBody(t, resp))
	}
}

func assessmentURL(t *testing.T, ts *httptest.Server, actorID string) string {
	t.Helper()
	transport, err := idmap.Encode(actorID)
	if err != nil {
		t.Fatalf("encode actor %q: %v", actorID, err)
	}
	return assessmentRawURL(ts, transport)
}

func assessmentRawURL(ts *httptest.Server, transport string) string {
	return ts.URL + "/v1/worlds/rencrow-home/timelines/timeline-1/actors/" + transport + "/assessment"
}

func workSetPayloadAt(validUntil string, active []any) map[string]any {
	return map[string]any{
		"contract_version": "rencrow.world.work-set/v2",
		"world_id":         "rencrow-home",
		"actor_id":         "mio",
		"source_owner":     "CORE",
		"source_epoch":     "core-epoch-1",
		"source_revision":  1,
		"valid_until":      validUntil,
		"active_work":      active,
	}
}

func lifePolicyPayloadAt(validUntil string, allowed, fallback []string) map[string]any {
	return map[string]any{
		"contract_version":                   "world.life-policy/v1",
		"world_id":                           "rencrow-home",
		"actor_id":                           "mio",
		"source_owner":                       "CORE",
		"source_epoch":                       "core-epoch-1",
		"source_revision":                    1,
		"valid_until":                        validUntil,
		"persona_revision":                   "persona-1",
		"allowed_non_cognitive_activity_ids": allowed,
		"expired_fallback_activity_ids":      fallback,
		"condition_profile_ref":              "condition-1",
	}
}

func assertStringSet(t *testing.T, raw any, want []string) {
	t.Helper()
	values, ok := raw.([]any)
	if !ok || len(values) != len(want) {
		t.Fatalf("values = %v; want %v", raw, want)
	}
	for index, value := range values {
		if value != want[index] {
			t.Fatalf("values = %v; want %v", values, want)
		}
	}
}

func TestAssessmentRouteIsReadOnly(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	resp := doRequest(t, http.MethodPost, assessmentURL(t, ts, "mio"), readerToken, "", []byte(`{}`))
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST assessment = %d", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Fatalf("Allow = %q", allow)
	}
}
