package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/idmap"
	"github.com/Nyukimin/RenCrow_WORLD/internal/life"
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
	if ids, ok := presentation["queued_task_ids"].([]any); !ok || len(ids) != 0 {
		t.Fatalf("queued_task_ids = %v", presentation["queued_task_ids"])
	}
	if ids, ok := presentation["waiting_task_ids"].([]any); !ok || len(ids) != 0 {
		t.Fatalf("waiting_task_ids = %v", presentation["waiting_task_ids"])
	}
	if ids, ok := body["permitted_activity_ids"].([]any); !ok || len(ids) != 0 {
		t.Fatalf("permitted_activity_ids = %v", body["permitted_activity_ids"])
	}
	if ids, ok := body["executable_activity_ids"].([]any); !ok || len(ids) != 0 {
		t.Fatalf("executable_activity_ids = %v", body["executable_activity_ids"])
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

func TestAssessmentPublishesQueuedAndWaitingTasks(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	work := []any{
		workItem("task-q", "queued", boolPtr(false)),
		workItem("task-w", "waiting", boolPtr(true)),
		workItem("task-r", "running", boolPtr(true)),
	}
	applyAssessmentProjection(t, ts, projection.KindWorkSet, 1, workSetPayloadAt(future, work))
	applyAssessmentProjection(t, ts, projection.KindLifePolicy, 1, lifePolicyPayloadAt(future, []string{"idle"}, []string{}))

	presentation := getAssessment(t, ts, "mio")["work_presentation"].(map[string]any)
	if presentation["kind"] != "presence" {
		t.Fatalf("presentation kind = %v", presentation["kind"])
	}
	assertStringSet(t, presentation["queued_task_ids"], []string{"task-q"})
	assertStringSet(t, presentation["waiting_task_ids"], []string{"task-w"})
	assertStringSet(t, presentation["running_task_ids"], []string{"task-r"})

	// Queued work alone is published as queued and never implies movement.
	applyAssessmentProjection(t, ts, projection.KindWorkSet, 2, workSetPayloadAt(future, []any{workItem("task-q", "queued", boolPtr(false))}))
	presentation = getAssessment(t, ts, "mio")["work_presentation"].(map[string]any)
	if presentation["kind"] != "queued" {
		t.Fatalf("presentation kind = %v", presentation["kind"])
	}
	assertStringSet(t, presentation["queued_task_ids"], []string{"task-q"})
	if len(presentation["waiting_task_ids"].([]any)) != 0 || len(presentation["presence_task_ids"].([]any)) != 0 {
		t.Fatalf("presentation = %v", presentation)
	}
}

func workItem(taskID, state string, presence *bool) map[string]any {
	item := map[string]any{
		"task_id": taskID, "execution_state": state,
		"work_class": "assigned_work", "classification_reason_code": "assigned",
	}
	if state == "running" {
		item["run_id"] = "run-" + taskID
	}
	if presence != nil {
		item["requires_house_presence"] = *presence
	}
	return item
}

func boolPtr(v bool) *bool { return &v }

// TestAssessmentPublishesExecutableActivityCandidates publishes, from WORLD's
// owner activity mapping, the candidates a controller may plan now. An activity
// without a definition is never published, presence-required work keeps the
// Actor in place, and an expired WorkSet is unknown rather than a presence
// requirement. The route stays read-only and selects nothing.
func TestAssessmentPublishesExecutableActivityCandidates(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	applyAssessmentProjection(t, ts, projection.KindWorkSet, 1, workSetPayloadAt(future, []any{}))
	applyAssessmentProjection(t, ts, projection.KindLifePolicy, 1, lifePolicyPayloadAt(future, []string{"walk", "read", "idle"}, []string{}))

	body := getAssessment(t, ts, "mio")
	assertStringSet(t, body["permitted_activity_ids"], []string{"idle", "read", "walk"})
	assertStringSet(t, body["executable_activity_ids"], []string{"idle", "read", "walk"})

	presence := []any{map[string]any{
		"task_id": "task-1", "run_id": "run-1", "execution_state": "running",
		"work_class": "assigned_work", "requires_house_presence": true,
		"classification_reason_code": "assigned",
	}}
	applyAssessmentProjection(t, ts, projection.KindWorkSet, 2, workSetPayloadAt(future, presence))
	body = getAssessment(t, ts, "mio")
	if kind := body["work_presentation"].(map[string]any)["kind"]; kind != "presence" {
		t.Fatalf("presentation kind = %v", kind)
	}
	assertStringSet(t, body["permitted_activity_ids"], []string{"idle", "read", "walk"})
	assertStringSet(t, body["executable_activity_ids"], []string{"idle"})

	// Unknown work must not remove the permitted non-cognitive options.
	applyAssessmentProjection(t, ts, projection.KindWorkSet, 3, workSetPayloadAt(past, presence))
	body = getAssessment(t, ts, "mio")
	if body["work_set_state"] != "expired" {
		t.Fatalf("work_set_state = %v", body["work_set_state"])
	}
	assertStringSet(t, body["executable_activity_ids"], []string{"idle", "read", "walk"})
}

// TestAssessmentPublishedCandidatesAreDefinedAndPermitted keeps the published
// set inside WORLD's owner mapping and inside the current policy, including the
// expired-policy fallback.
func TestAssessmentPublishedCandidatesAreDefinedAndPermitted(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	applyAssessmentProjection(t, ts, projection.KindLifePolicy, 1, lifePolicyPayloadAt(past, []string{"walk", "rest"}, []string{"rest"}))

	body := getAssessment(t, ts, "mio")
	if body["policy_state"] != "expired" {
		t.Fatalf("policy_state = %v", body["policy_state"])
	}
	assertStringSet(t, body["executable_activity_ids"], []string{"rest"})

	defined := map[string]bool{}
	for _, definition := range life.DefaultActivityDefinitions() {
		defined[definition.ID] = true
	}
	permitted := map[string]bool{}
	for _, id := range body["permitted_activity_ids"].([]any) {
		permitted[id.(string)] = true
	}
	for _, value := range body["executable_activity_ids"].([]any) {
		id, _ := value.(string)
		if !defined[id] {
			t.Fatalf("published an activity without a definition: %v", id)
		}
		if !permitted[id] {
			t.Fatalf("published an activity the policy does not permit: %v", id)
		}
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

// TestAssessmentStopPlanningEvaluatesOwnerDefinition publishes the safe-stop
// decision for one running activity from WORLD's owner mapping: a permitted
// activity continues, a revoked one stops in the owner posture, and a
// non-interruptible activity holds its position. The caller sends only the
// activity ID, and nothing here starts, stops, or moves anything.
func TestAssessmentStopPlanningEvaluatesOwnerDefinition(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	applyAssessmentProjection(t, ts, projection.KindWorkSet, 1, workSetPayloadAt(future, []any{}))
	applyAssessmentProjection(t, ts, projection.KindLifePolicy, 1, lifePolicyPayloadAt(future, []string{"idle", "rest"}, []string{}))

	if body := getAssessment(t, ts, "mio"); body["stop_planning"] != nil {
		t.Fatalf("assessment without an activity published a stop plan: %v", body["stop_planning"])
	}

	planned := stopPlanningBody(t, ts, "mio", "rest")
	if planned["activity_id"] != "rest" || planned["action"] != "continue" || planned["reason"] != "permitted" {
		t.Fatalf("permitted activity = %v", planned)
	}
	if planned["stop_state"] != "" || planned["release_occupancy"] != false {
		t.Fatalf("continued activity planned a stop: %v", planned)
	}
	if actions, ok := planned["stop_actions"].([]any); !ok || len(actions) != 0 {
		t.Fatalf("continued activity stop_actions = %v", planned["stop_actions"])
	}
	// A fresh empty WorkSet is "no assignment now", which clears the work
	// presentation without implying any work success (D-08).
	if planned["clear_work_presentation"] != true {
		t.Fatalf("empty WorkSet did not clear the presentation: %v", planned)
	}

	assigned := []any{workItem("task-1", "running", boolPtr(false))}
	applyAssessmentProjection(t, ts, projection.KindWorkSet, 2, workSetPayloadAt(future, assigned))

	planned = stopPlanningBody(t, ts, "mio", "change_clothes")
	if planned["activity_id"] != "change_clothes" || planned["action"] != "stop" ||
		planned["reason"] != "activity_not_permitted" {
		t.Fatalf("revoked activity = %v", planned)
	}
	if planned["stop_state"] != "standing" {
		t.Fatalf("stop posture = %v", planned)
	}
	assertStringSet(t, planned["stop_actions"], []string{"stop:change_clothes"})
	if planned["release_occupancy"] != false {
		t.Fatalf("owner mapping grants no occupancy release yet: %v", planned)
	}
	if _, present := planned["retain_occupancy_reason"]; present {
		t.Fatalf("unexpected occupancy retention: %v", planned)
	}
	if planned["clear_work_presentation"] != false {
		t.Fatalf("assigned work was cleared by a stop: %v", planned)
	}

	planned = stopPlanningBody(t, ts, "mio", "sleep")
	if planned["action"] != "hold" || planned["reason"] != "not_interruptible" {
		t.Fatalf("non-interruptible activity = %v", planned)
	}
	if planned["stop_state"] != "" {
		t.Fatalf("held activity published a posture: %v", planned)
	}
	if actions, ok := planned["stop_actions"].([]any); !ok || len(actions) != 0 {
		t.Fatalf("held activity stop_actions = %v", planned["stop_actions"])
	}
}

// TestAssessmentStopPlanningRejectsUnregisteredActivity pins that an activity
// outside WORLD's owner mapping is rejected instead of evaluated with a guessed
// posture, while an empty parameter stays the no-stop-planning request.
func TestAssessmentStopPlanningRejectsUnregisteredActivity(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	applyAssessmentProjection(t, ts, projection.KindLifePolicy, 1, lifePolicyPayloadAt(future, []string{"idle"}, []string{}))

	resp := doRequest(t, http.MethodGet, stopPlanningURL(t, ts, "mio", "walk_the_dog"), readerToken, "", nil)
	body := decodeMap(t, resp)
	if resp.StatusCode != http.StatusBadRequest ||
		body["error"].(map[string]any)["code"] != "unknown_activity" {
		t.Fatalf("unregistered activity = %d: %v", resp.StatusCode, body)
	}

	resp = doRequest(t, http.MethodGet, stopPlanningURL(t, ts, "mio", ""), readerToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty activity parameter = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if body := decodeMap(t, resp); body["stop_planning"] != nil {
		t.Fatalf("empty activity parameter published a stop plan: %v", body["stop_planning"])
	}
}

// TestAssessmentStopPlanningStaysReadOnlyAndAuthorized pins that stop planning
// keeps the assessment route's guarantees: it changes no World state, and an
// unbound Actor stays forbidden even when it asks for a stop plan.
func TestAssessmentStopPlanningStaysReadOnlyAndAuthorized(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	before := decodeMap(t, doRequest(t, http.MethodGet, snapshotURL(ts), readerToken, "", nil))

	if resp := doRequest(t, http.MethodGet, stopPlanningURL(t, ts, "kuro", "read"), readerToken, "", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unbound actor stop planning = %d", resp.StatusCode)
	}
	if resp := doRequest(t, http.MethodGet, stopPlanningURL(t, ts, "mio", "read"), streamerToken, "", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stream-only token stop planning = %d", resp.StatusCode)
	}

	after := decodeMap(t, doRequest(t, http.MethodGet, snapshotURL(ts), readerToken, "", nil))
	if before["state_seq"] != after["state_seq"] || before["state_hash"] != after["state_hash"] {
		t.Fatalf("stop planning changed World state: before=%v after=%v", before, after)
	}
}

func stopPlanningBody(t *testing.T, ts *httptest.Server, actorID, activityID string) map[string]any {
	t.Helper()
	resp := doRequest(t, http.MethodGet, stopPlanningURL(t, ts, actorID, activityID), readerToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stop planning %q = %d: %s", activityID, resp.StatusCode, readBody(t, resp))
	}
	body, ok := decodeMap(t, resp)["stop_planning"].(map[string]any)
	if !ok {
		t.Fatalf("stop_planning missing from %v", body)
	}
	return body
}

func stopPlanningURL(t *testing.T, ts *httptest.Server, actorID, activityID string) string {
	t.Helper()
	return assessmentURL(t, ts, actorID) + "?stop_planning_activity_id=" + url.QueryEscape(activityID)
}
