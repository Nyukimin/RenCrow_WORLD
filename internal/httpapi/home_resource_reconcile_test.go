package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_WORLD/internal/life"
	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

const homeResourceLifeToken = "home-resource-life-token"

func resourceReconcileLayout(paths string, sceneByte byte, restSlotIDs ...string) string {
	slots := []string{
		`{"id":"bed-aka","kind":"bed","actor_id":"aka","room_id":"aka-room","anchor":[10,0,0]}`,
		`{"id":"bed-ao","kind":"bed","actor_id":"ao","room_id":"ao-room","anchor":[20,0,0]}`,
		`{"id":"bed-kin","kind":"bed","actor_id":"kin","room_id":"kin-room","anchor":[30,0,0]}`,
	}
	for index, id := range restSlotIDs {
		slots = append(slots, fmt.Sprintf(`{"id":%q,"kind":"rest","room_id":"lobby","anchor":[0,0,%d],"approach_points":[[0,0,0]],"approach_duration_ms":1000}`, id, index+1))
	}
	return `{"schema_version":"world.home-layout/v1","scene_sha256":"` + strings.Repeat(string(sceneByte), 64) + `","room_anchors":{"lobby":[0,0,0],"hall":[5,0,0],"aka-room":[10,0,0],"ao-room":[20,0,0],"kin-room":[30,0,0]},"paths":[` + paths + `],"private_rooms":{"aka":"aka-room","ao":"ao-room","kin":"kin-room"},"slots":[` + strings.Join(slots, ",") + `]}`
}

func resourceReconcileConfig(t *testing.T, dir, layoutPath string) Config {
	t.Helper()
	cfg := baseConfig(t, dir)
	cfg.HomeLayoutPath = layoutPath
	cfg.Tokens = append(cfg.Tokens, TokenConfig{
		ID: "home-life", PublisherID: "home-life-publisher", SHA256: tokenHash(homeResourceLifeToken),
		Scopes: []string{"life"}, LifeActorIDs: []string{"aka", "ao", "kin", "hold"},
	})
	position := [3]float64{0, 0, 0}
	for _, actorID := range []string{"aka", "ao", "kin", "hold"} {
		p := position
		cfg.InitialState.Actors[actorID] = store.ActorState{RoomID: "lobby", Position: &p}
	}
	return cfg
}

func writeResourceReconcileLayout(t *testing.T, path, raw string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write home layout: %v", err)
	}
}

func TestHomeResourceReconcileAddsNewLayoutResourcesToPersistedTimeline(t *testing.T) {
	dir := t.TempDir()
	layoutPath := filepath.Join(dir, "home-layout.json")
	oldPaths := `{"id":"lobby-hall","from_room_id":"lobby","to_room_id":"hall","points":[[2,0,0]],"duration_ms":1000}`
	newPaths := oldPaths + `,{"id":"lobby-aka","from_room_id":"lobby","to_room_id":"aka-room","points":[[4,0,0]],"duration_ms":1000},{"id":"lobby-ao","from_room_id":"lobby","to_room_id":"ao-room","points":[[8,0,0]],"duration_ms":1000},{"id":"lobby-kin","from_room_id":"lobby","to_room_id":"kin-room","points":[[12,0,0]],"duration_ms":1000}`
	existingSeatIDs := []string{"sofa", "sofa-guest", "dining", "dining-guest"}
	addedSeatIDs := []string{"dining-aisle-north", "dining-aisle-south", "dining-window-north", "dining-window-south"}
	expandedSeatIDs := append(append([]string(nil), existingSeatIDs...), addedSeatIDs...)
	writeResourceReconcileLayout(t, layoutPath, resourceReconcileLayout(oldPaths, 'a', existingSeatIDs...))
	cfg := resourceReconcileConfig(t, dir, layoutPath)

	first, err := New(cfg)
	if err != nil {
		t.Fatalf("New with original layout: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	initial, initialSeq, _, err := first.store.Snapshot(cfg.Scope)
	if err != nil || initialSeq != 0 {
		t.Fatalf("new timeline snapshot seq=%d err=%v", initialSeq, err)
	}
	for id, capacity := range first.home.ResourceCapacities() {
		if got := initial.Resources[id]; got.Capacity != capacity || got.Used != 0 {
			t.Fatalf("initial home resource %q = %+v, want capacity %d and unused", id, got, capacity)
		}
	}

	firstHTTP := httptest.NewServer(first.Handler())
	t.Cleanup(func() { firstHTTP.Close() })
	move := doRequest(t, http.MethodPost, commandsURL(firstHTTP), homeResourceLifeToken, "held-old-move", commandBody(t, life.ActionHomeMove, map[string]any{
		"actor_id": "hold", "room_id": "hall",
	}))
	if move.StatusCode != http.StatusOK {
		t.Fatalf("start old-layout move status=%d body=%s", move.StatusCode, readBody(t, move))
	}
	var moveReceipt receiptBody
	if err := json.NewDecoder(move.Body).Decode(&moveReceipt); err != nil || moveReceipt.Receipt.Status != "committed" {
		t.Fatalf("decode old-layout move receipt=%+v err=%v", moveReceipt, err)
	}
	_ = move.Body.Close()
	before, beforeSeq, _, err := first.store.Snapshot(cfg.Scope)
	if err != nil || len(before.Reservations) != 0 || before.Actors["hold"].ActivityID == "" || before.Actors["hold"].MovePhase != "in_transit" {
		t.Fatalf("new move should remain active without a corridor reservation: state=%+v seq=%d err=%v", before, beforeSeq, err)
	}
	oldReceipt, err := first.store.GetReceipt(cfg.Scope, moveReceipt.Receipt.ReceiptID)
	if err != nil {
		t.Fatalf("get old move receipt: %v", err)
	}
	oldHistory, err := first.store.Page(cfg.Scope, 0, store.DefaultPageEvents, 0)
	if err != nil || len(oldHistory.Events) != 1 {
		t.Fatalf("old event history=%d err=%v", len(oldHistory.Events), err)
	}
	firstHTTP.Close()
	if err := first.Close(); err != nil {
		t.Fatalf("close original server: %v", err)
	}

	writeResourceReconcileLayout(t, layoutPath, resourceReconcileLayout(newPaths, 'b', expandedSeatIDs...))
	cfg.InitialState = nil
	second, err := New(cfg)
	if err != nil {
		t.Fatalf("New with expanded layout and no initial state: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	state, seq, hash, err := second.store.Snapshot(cfg.Scope)
	if err != nil {
		t.Fatalf("snapshot after reconciliation: %v", err)
	}
	if seq != beforeSeq+1 {
		t.Fatalf("reconciliation sequence=%d, want one audited event after %d", seq, beforeSeq)
	}
	added := make(map[string]int64)
	for id, capacity := range second.home.ResourceCapacities() {
		if _, existed := before.Resources[id]; !existed {
			added[id] = capacity
		}
		if got := state.Resources[id]; got.Capacity != capacity {
			t.Fatalf("configured resource %q = %+v, want capacity %d", id, got, capacity)
		}
	}
	if len(added) != 7 {
		t.Fatalf("added configured path and seat resources=%v, want three routes plus four new chairs", added)
	}
	for _, id := range addedSeatIDs {
		resourceID := "home-slot:" + id
		if added[resourceID] != 1 {
			t.Fatalf("new dining chair resource %q = %d, want capacity 1 in the reconciliation event", resourceID, added[resourceID])
		}
	}
	for actorID, actor := range before.Actors {
		if !reflect.DeepEqual(state.Actors[actorID], actor) {
			t.Fatalf("reconciliation changed actor %q: before=%+v after=%+v", actorID, actor, state.Actors[actorID])
		}
	}
	if !reflect.DeepEqual(state.Activities, before.Activities) || !reflect.DeepEqual(state.Reservations, before.Reservations) {
		t.Fatalf("reconciliation changed prior activities or held reservations: activities=%v reservations=%v", state.Activities, state.Reservations)
	}
	for id, resource := range before.Resources {
		if got := state.Resources[id]; got != resource {
			t.Fatalf("reconciliation changed existing resource %q: before=%+v after=%+v", id, resource, got)
		}
	}
	retained, err := second.store.GetReceipt(cfg.Scope, oldReceipt.ID)
	if err != nil || retained.ID != oldReceipt.ID || retained.Status != oldReceipt.Status {
		t.Fatalf("old receipt changed or disappeared: got=%+v err=%v want=%+v", retained, err, oldReceipt)
	}
	newHistory, err := second.store.Page(cfg.Scope, beforeSeq, store.DefaultPageEvents, 0)
	if err != nil || len(newHistory.Events) != 1 || newHistory.Events[0].Kind != "home.resources_reconciled" {
		t.Fatalf("reconciliation history=%+v err=%v", newHistory, err)
	}
	var eventPayload struct {
		Resources map[string]int64 `json:"resources"`
	}
	if err := json.Unmarshal(newHistory.Events[0].Payload, &eventPayload); err != nil || !reflect.DeepEqual(eventPayload.Resources, added) {
		t.Fatalf("reconciliation event payload=%s decoded=%v err=%v want=%v", newHistory.Events[0].Payload, eventPayload.Resources, err, added)
	}

	secondHTTP := httptest.NewServer(second.Handler())
	t.Cleanup(func() { secondHTTP.Close() })
	unauthorized := doRequest(t, http.MethodPost, commandsURL(secondHTTP), "", "no-auth-reconcile", commandBody(t, "internal.world.home_resources_reconcile", map[string]any{}))
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("internal action without auth status=%d body=%s", unauthorized.StatusCode, readBody(t, unauthorized))
	}
	publicAttempt := doRequest(t, http.MethodPost, commandsURL(secondHTTP), writerToken, "public-reconcile-attempt", commandBody(t, "internal.world.home_resources_reconcile", map[string]any{}))
	if publicAttempt.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("internal action public write status=%d body=%s", publicAttempt.StatusCode, readBody(t, publicAttempt))
	}
	var publicReceipt receiptBody
	if err := json.NewDecoder(publicAttempt.Body).Decode(&publicReceipt); err != nil || publicReceipt.Receipt.Status != "rejected" || publicReceipt.Receipt.Reason != "unknown_action" {
		t.Fatalf("public reconciliation attempt receipt=%+v err=%v", publicReceipt, err)
	}
	_ = publicAttempt.Body.Close()

	for _, actorID := range []string{"aka", "ao", "kin"} {
		roomID := actorID + "-room"
		slotID := "bed-" + actorID
		response := doRequest(t, http.MethodPost, commandsURL(secondHTTP), homeResourceLifeToken, "new-layout-"+actorID,
			commandBody(t, life.ActionHomeMove, map[string]any{"actor_id": actorID, "room_id": roomID, "slot_id": slotID}))
		if response.StatusCode != http.StatusOK {
			t.Fatalf("move %s to configured bed status=%d body=%s", actorID, response.StatusCode, readBody(t, response))
		}
		_ = response.Body.Close()
	}

	state, seq, hash, err = second.store.Snapshot(cfg.Scope)
	if err != nil {
		t.Fatalf("snapshot after new routes: %v", err)
	}
	replayed, replayHash, replaySeq, err := second.store.Replay(cfg.Scope, seq)
	if err != nil || replaySeq != seq || replayHash != hash || !reflect.DeepEqual(replayed, state) {
		t.Fatalf("Replay does not match Snapshot: replaySeq=%d seq=%d replayHash=%s hash=%s err=%v", replaySeq, seq, replayHash, hash, err)
	}
	secondHTTP.Close()
	if err := second.Close(); err != nil {
		t.Fatalf("close expanded-layout server: %v", err)
	}

	repeated, err := New(cfg)
	if err != nil {
		t.Fatalf("repeat startup with same layout: %v", err)
	}
	defer repeated.Close()
	_, repeatSeq, repeatHash, err := repeated.store.Snapshot(cfg.Scope)
	if err != nil || repeatSeq != seq || repeatHash != hash {
		t.Fatalf("same-layout startup changed timeline seq/hash: seq=%d/%d hash=%s/%s err=%v", repeatSeq, seq, repeatHash, hash, err)
	}
}

func TestHomeResourceReconcileCapacityMismatchIsAtomic(t *testing.T) {
	dir := t.TempDir()
	layoutPath := filepath.Join(dir, "home-layout.json")
	paths := `{"id":"lobby-hall","from_room_id":"lobby","to_room_id":"hall","points":[[2,0,0]],"duration_ms":1000},{"id":"lobby-aka","from_room_id":"lobby","to_room_id":"aka-room","points":[[4,0,0]],"duration_ms":1000}`
	writeResourceReconcileLayout(t, layoutPath, resourceReconcileLayout(paths, 'c'))
	cfg := resourceReconcileConfig(t, dir, layoutPath)
	cfg.InitialState = nil
	cfg.Tokens = cfg.Tokens[:len(cfg.Tokens)-1]

	// Build a persisted timeline with one configured resource at the wrong capacity
	// and leave the other configured resources absent.
	layoutBytes, err := os.ReadFile(layoutPath)
	if err != nil {
		t.Fatalf("read home layout: %v", err)
	}
	layout, err := life.ParseHomeLayout(layoutBytes)
	if err != nil {
		t.Fatalf("parse home layout: %v", err)
	}
	controller, err := life.NewHomeController(layout, nil)
	if err != nil {
		t.Fatalf("initialize home controller: %v", err)
	}
	capacities := controller.ResourceCapacities()
	var mismatchedID string
	for id := range capacities {
		mismatchedID = id
		break
	}
	if mismatchedID == "" {
		t.Fatal("test layout has no resources")
	}
	st, err := store.Open(cfg.StorePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.CreateTimeline(cfg.Scope, store.State{Actors: cfgActors()}, map[string]int64{mismatchedID: 2, "legacy-resource": 1}); err != nil {
		t.Fatalf("create mismatched timeline: %v", err)
	}
	before, beforeSeq, beforeHash, err := st.Snapshot(cfg.Scope)
	if err != nil {
		t.Fatalf("snapshot before New: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close setup store: %v", err)
	}

	if server, err := New(cfg); err == nil {
		_ = server.Close()
		t.Fatal("New accepted a configured resource with a mismatched capacity")
	}
	reopened, err := store.Open(cfg.StorePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	after, afterSeq, afterHash, err := reopened.Snapshot(cfg.Scope)
	if err != nil || afterSeq != beforeSeq || afterHash != beforeHash || !reflect.DeepEqual(after, before) {
		t.Fatalf("capacity mismatch partially mutated timeline: before=%+v seq=%d hash=%s after=%+v seq=%d hash=%s err=%v", before, beforeSeq, beforeHash, after, afterSeq, afterHash, err)
	}
}

func cfgActors() map[string]store.ActorState {
	return map[string]store.ActorState{
		"mio": {RoomID: "lobby"}, "shiro": {RoomID: "lobby"},
	}
}
