package life

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

// R10 item 4 (D-11/D-12/D-13 logical side): the Event/Receipt rules for
// presentation start, interruption, and safe stop. Each test runs the real
// SQLite EventStore and the canonical Command -> Handler -> Receipt route, so a
// rule is proven by its committed Events, its Receipt, and the State
// projection - never by a narrative report. No production route registers
// these actions yet, no reservation or movement route exists, and no CORE work
// result is ever claimed here.

var commandScope = store.Scope{WorldID: "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-1"}

func newCommandStore(t *testing.T, actors map[string]store.ActorState) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "world.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateTimeline(commandScope, store.State{Actors: actors}, nil); err != nil {
		t.Fatalf("create timeline: %v", err)
	}
	st.Register(ActionActivityStart, HandleActivityStart)
	st.Register(ActionActivityStop, HandleActivityStop)
	st.Register(ActionOccupancyRelease, HandleOccupancyRelease)
	return st
}

func appendLifeCommand(t *testing.T, st *store.Store, key, action string, payload string) store.Result {
	t.Helper()
	result, err := st.Append(store.Command{
		Scope:          commandScope,
		ClientID:       "world-controller",
		IdempotencyKey: key,
		Action:         action,
		Payload:        json.RawMessage(payload),
		RecordedAt:     time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("append %s (%s): %v", action, key, err)
	}
	return result
}

func committedState(t *testing.T, st *store.Store) (store.State, string) {
	t.Helper()
	state, _, hash, err := st.Snapshot(commandScope)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return state, hash
}

func assertCommitted(t *testing.T, result store.Result) {
	t.Helper()
	if result.Receipt.Status != "committed" {
		t.Fatalf("receipt = %s/%s, want committed", result.Receipt.Status, result.Receipt.Reason)
	}
	if result.Receipt.TransactionID == "" || len(result.Receipt.EventIDs) != 1 {
		t.Fatalf("receipt %s: txn=%q events=%v, want one event in one transaction",
			result.Receipt.ID, result.Receipt.TransactionID, result.Receipt.EventIDs)
	}
}

func decodeEvent(t *testing.T, st *store.Store, seq uint64) (string, map[string]any) {
	t.Helper()
	page, err := st.Page(commandScope, seq-1, store.DefaultPageEvents, 0)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("events at seq %d = %d, want 1", seq, len(page.Events))
	}
	var payload map[string]any
	if err := json.Unmarshal(page.Events[0].Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	return page.Events[0].Kind, payload
}

func TestActivityStartCommandCommitsCanonicalEvent(t *testing.T) {
	st := newCommandStore(t, map[string]store.ActorState{"mio": {RoomID: "1f-living"}})

	result := appendLifeCommand(t, st, "start-idle", ActionActivityStart, `{"actor_id":"mio","activity_id":"idle"}`)
	assertCommitted(t, result)

	kind, payload := decodeEvent(t, st, result.Receipt.LastSeq)
	if kind != EventActivityStarted {
		t.Fatalf("event kind = %q, want %q", kind, EventActivityStarted)
	}
	if payload["actor_id"] != "mio" || payload["activity_id"] != "idle" {
		t.Fatalf("event payload = %v, want actor and activity", payload)
	}
	if _, ok := payload["occupancy"]; !ok {
		t.Fatalf("event payload %v has no explicit occupancy", payload)
	}
	if payload["occupancy"] != "" {
		t.Fatalf("idle must claim no occupancy, got %v", payload["occupancy"])
	}

	state, _ := committedState(t, st)
	if got := state.Actors["mio"].ActivityID; got != "idle" {
		t.Fatalf("actor activity = %q, want idle", got)
	}
	if got := state.Actors["mio"].RoomID; got != "1f-living" {
		t.Fatalf("start changed RoomID to %q", got)
	}
	if got := state.Activities["idle"]; got.Status != ActivityStatusRunning || got.ActorID != "mio" {
		t.Fatalf("activity = %+v, want running for mio", got)
	}
}

func TestActivityStartCommandRefusalsChangeNoState(t *testing.T) {
	st := newCommandStore(t, map[string]store.ActorState{
		"mio":  {RoomID: "1f-living"},
		"kuro": {RoomID: "1f-living"},
	})
	_, before := committedState(t, st)

	cases := []struct {
		name    string
		key     string
		payload string
		rule    error
	}{
		{
			name:    "unregistered activity",
			key:     "start-unknown",
			payload: `{"actor_id":"mio","activity_id":"play_games"}`,
			rule:    ErrUnregisteredActivity,
		},
		{
			name:    "movement required",
			key:     "start-movement",
			payload: `{"actor_id":"mio","activity_id":"rest"}`,
			rule:    ErrMovementRequired,
		},
		{
			name:    "unknown actor",
			key:     "start-actor",
			payload: `{"actor_id":"nobody","activity_id":"idle"}`,
			rule:    ErrUnknownActor,
		},
	}
	for _, tc := range cases {
		result := appendLifeCommand(t, st, tc.key, ActionActivityStart, tc.payload)
		if result.Receipt.Status != "rejected" {
			t.Fatalf("%s: receipt = %s, want rejected", tc.name, result.Receipt.Status)
		}
		if result.Receipt.TransactionID != "" || len(result.Receipt.EventIDs) != 0 {
			t.Fatalf("%s: rejected receipt carries txn %q events %v", tc.name, result.Receipt.TransactionID, result.Receipt.EventIDs)
		}
		_, after := committedState(t, st)
		if after != before {
			t.Fatalf("%s: rejection changed State hash", tc.name)
		}

		_, err := HandleActivityStart(mustTestState(t, map[string]store.ActorState{"mio": {RoomID: "1f-living"}}), json.RawMessage(tc.payload))
		if !errors.Is(err, tc.rule) {
			t.Fatalf("%s: handler err = %v, want %v", tc.name, err, tc.rule)
		}
	}

	if res := appendLifeCommand(t, st, "start-mio", ActionActivityStart, `{"actor_id":"mio","activity_id":"idle"}`); res.Receipt.Status != "committed" {
		t.Fatalf("baseline start rejected: %s", res.Receipt.Reason)
	}
	second := appendLifeCommand(t, st, "start-mio-again", ActionActivityStart, `{"actor_id":"mio","activity_id":"idle"}`)
	if second.Receipt.Status != "rejected" {
		t.Fatalf("second start while running = %s, want rejected", second.Receipt.Status)
	}
	if _, err := HandleActivityStart(mustTestState(t, map[string]store.ActorState{"mio": {RoomID: "1f-living", ActivityID: "rest"}}), json.RawMessage(`{"actor_id":"mio","activity_id":"idle"}`)); !errors.Is(err, ErrActivityRunning) {
		t.Fatalf("running handler err = %v, want %v", err, ErrActivityRunning)
	}
}

func TestActivityStopCommandKeepsHeldOccupancyAndPosition(t *testing.T) {
	st := newCommandStore(t, map[string]store.ActorState{"mio": {RoomID: "sofa"}})
	if res := appendLifeCommand(t, st, "start-rest", ActionActivityStart, `{"actor_id":"mio","activity_id":"rest"}`); res.Receipt.Status != "committed" {
		t.Fatalf("start rest: %s/%s", res.Receipt.Status, res.Receipt.Reason)
	}

	result := appendLifeCommand(t, st, "stop-rest", ActionActivityStop, `{"actor_id":"mio","activity_id":"rest"}`)
	assertCommitted(t, result)
	kind, payload := decodeEvent(t, st, result.Receipt.LastSeq)
	if kind != EventActivityStopped {
		t.Fatalf("event kind = %q, want %q", kind, EventActivityStopped)
	}
	if payload["stop_state"] != "sitting" {
		t.Fatalf("stop_state = %v, want the owner rule's sitting", payload["stop_state"])
	}
	if payload["occupancy"] != "sofa" {
		t.Fatalf("a stop must keep holding the logical occupancy, got %v", payload["occupancy"])
	}
	actions, ok := payload["actions"].([]any)
	if !ok || len(actions) != 1 || actions[0] != "stop:rest" {
		t.Fatalf("actions = %v, want the owner-authorized stop action only", payload["actions"])
	}

	state, _ := committedState(t, st)
	if got := state.Actors["mio"].RoomID; got != "sofa" {
		t.Fatalf("stop moved the Actor to RoomID %q", got)
	}
	if got := state.Actors["mio"].ActivityID; got != "" {
		t.Fatalf("stopped activity still bound to actor: %q", got)
	}
	if got := state.Activities["rest"]; got.Status != ActivityStatusStopped {
		t.Fatalf("activity status = %q, want %q", got.Status, ActivityStatusStopped)
	}

	again := appendLifeCommand(t, st, "stop-rest-again", ActionActivityStop, `{"actor_id":"mio","activity_id":"rest"}`)
	if again.Receipt.Status != "rejected" {
		t.Fatalf("second stop = %s, want rejected", again.Receipt.Status)
	}
	if _, err := HandleActivityStop(mustTestState(t,
		map[string]store.ActorState{"mio": {RoomID: "sofa"}},
		map[string]store.ActivityState{"rest": {ID: "rest", ActorID: "mio", Status: ActivityStatusStopped}},
	), json.RawMessage(`{"actor_id":"mio","activity_id":"rest"}`)); !errors.Is(err, ErrActivityNotRunning) {
		t.Fatalf("repeat stop err = %v, want %v", err, ErrActivityNotRunning)
	}
}

func TestActivityStopCommandRefusesActivityWithoutSafeInterruption(t *testing.T) {
	st := newCommandStore(t, map[string]store.ActorState{"mio": {RoomID: "bed"}})
	if res := appendLifeCommand(t, st, "start-sleep", ActionActivityStart, `{"actor_id":"mio","activity_id":"sleep"}`); res.Receipt.Status != "committed" {
		t.Fatalf("start sleep: %s/%s", res.Receipt.Status, res.Receipt.Reason)
	}
	_, before := committedState(t, st)

	result := appendLifeCommand(t, st, "stop-sleep", ActionActivityStop, `{"actor_id":"mio","activity_id":"sleep"}`)
	if result.Receipt.Status != "rejected" {
		t.Fatalf("stop sleep = %s, want rejected: the owner rule defines no safe interruption", result.Receipt.Status)
	}
	_, after := committedState(t, st)
	if after != before {
		t.Fatal("a refused stop must hold the Actor in place with no State change")
	}

	state, _ := committedState(t, st)
	_, err := HandleActivityStop(state, json.RawMessage(`{"actor_id":"mio","activity_id":"sleep"}`))
	if !errors.Is(err, ErrNotInterruptible) {
		t.Fatalf("handler err = %v, want %v", err, ErrNotInterruptible)
	}
}

func TestOccupancyReleaseIsSeparateFromStop(t *testing.T) {
	st := newCommandStore(t, map[string]store.ActorState{"mio": {RoomID: "study_seat"}})
	if res := appendLifeCommand(t, st, "start-read", ActionActivityStart, `{"actor_id":"mio","activity_id":"read"}`); res.Receipt.Status != "committed" {
		t.Fatalf("start read: %s/%s", res.Receipt.Status, res.Receipt.Reason)
	}

	early := appendLifeCommand(t, st, "release-early", ActionOccupancyRelease, `{"actor_id":"mio","activity_id":"read"}`)
	if early.Receipt.Status != "rejected" {
		t.Fatalf("release before stop = %s, want rejected (D-11)", early.Receipt.Status)
	}
	if _, err := HandleOccupancyRelease(mustTestState(t,
		map[string]store.ActorState{"mio": {RoomID: "study_seat", ActivityID: "read"}},
		map[string]store.ActivityState{"read": {ID: "read", ActorID: "mio", Status: ActivityStatusRunning}},
	), json.RawMessage(`{"actor_id":"mio","activity_id":"read"}`)); !errors.Is(err, ErrStopRequired) {
		t.Fatalf("early release err = %v, want %v", err, ErrStopRequired)
	}

	if res := appendLifeCommand(t, st, "stop-read", ActionActivityStop, `{"actor_id":"mio","activity_id":"read"}`); res.Receipt.Status != "committed" {
		t.Fatalf("stop read: %s/%s", res.Receipt.Status, res.Receipt.Reason)
	}
	result := appendLifeCommand(t, st, "release-read", ActionOccupancyRelease, `{"actor_id":"mio","activity_id":"read"}`)
	assertCommitted(t, result)
	kind, payload := decodeEvent(t, st, result.Receipt.LastSeq)
	if kind != EventOccupancyReleased || payload["occupancy"] != "study_seat" {
		t.Fatalf("release event = %q %v, want the held logical occupancy", kind, payload)
	}
	state, _ := committedState(t, st)
	if got := state.Activities["read"]; got.Status != ActivityStatusReleased {
		t.Fatalf("activity status = %q, want %q", got.Status, ActivityStatusReleased)
	}
	if got := state.Actors["mio"].RoomID; got != "study_seat" {
		t.Fatalf("release changed RoomID to %q", got)
	}

	again := appendLifeCommand(t, st, "release-read-again", ActionOccupancyRelease, `{"actor_id":"mio","activity_id":"read"}`)
	if again.Receipt.Status != "rejected" {
		t.Fatalf("second release = %s, want rejected", again.Receipt.Status)
	}

	if res := appendLifeCommand(t, st, "start-idle", ActionActivityStart, `{"actor_id":"mio","activity_id":"idle"}`); res.Receipt.Status != "committed" {
		t.Fatalf("idle after release must be admitted: %s/%s", res.Receipt.Status, res.Receipt.Reason)
	}
	noHold := appendLifeCommand(t, st, "release-idle", ActionOccupancyRelease, `{"actor_id":"mio","activity_id":"idle"}`)
	if noHold.Receipt.Status == "committed" {
		t.Fatal("releasing an activity that held nothing must not commit")
	}
	if _, err := HandleOccupancyRelease(mustTestState(t,
		map[string]store.ActorState{"mio": {RoomID: "1f-living"}},
		map[string]store.ActivityState{"idle": {ID: "idle", ActorID: "mio", Status: ActivityStatusStopped}},
	), json.RawMessage(`{"actor_id":"mio","activity_id":"idle"}`)); !errors.Is(err, ErrNothingHeld) {
		t.Fatalf("nothing-held err = %v, want %v", err, ErrNothingHeld)
	}
}

func TestActivityCommandsReplayIdempotently(t *testing.T) {
	st := newCommandStore(t, map[string]store.ActorState{"mio": {RoomID: "1f-living"}})
	first := appendLifeCommand(t, st, "start-idle", ActionActivityStart, `{"actor_id":"mio","activity_id":"idle"}`)
	retry := appendLifeCommand(t, st, "start-idle", ActionActivityStart, `{"actor_id":"mio","activity_id":"idle"}`)
	if !retry.Duplicate {
		t.Fatal("same idempotency key and body must replay")
	}
	if retry.Receipt.ID != first.Receipt.ID || len(retry.Receipt.EventIDs) != len(first.Receipt.EventIDs) {
		t.Fatalf("replay = %s %v, want the original %s %v", retry.Receipt.ID, retry.Receipt.EventIDs, first.Receipt.ID, first.Receipt.EventIDs)
	}
	state, _ := committedState(t, st)
	if got := state.Activities["idle"]; got.Status != ActivityStatusRunning {
		t.Fatalf("replay changed activity status: %q", got.Status)
	}

	rejectedKey := appendLifeCommand(t, st, "stop-never-ran", ActionActivityStop, `{"actor_id":"mio","activity_id":"rest"}`)
	if rejectedKey.Receipt.Status != "rejected" {
		t.Fatalf("stop of an unstarted activity = %s, want rejected", rejectedKey.Receipt.Status)
	}
	rejectedRetry := appendLifeCommand(t, st, "stop-never-ran", ActionActivityStop, `{"actor_id":"mio","activity_id":"rest"}`)
	if !rejectedRetry.Duplicate || rejectedRetry.Receipt.ID != rejectedKey.Receipt.ID {
		t.Fatalf("rejected receipt must replay as the same receipt: %s vs %s", rejectedRetry.Receipt.ID, rejectedKey.Receipt.ID)
	}
}

func mustTestState(t *testing.T, actors map[string]store.ActorState, activities ...map[string]store.ActivityState) store.State {
	t.Helper()
	state := store.State{
		Actors:     map[string]store.ActorState{},
		Activities: map[string]store.ActivityState{},
		Resources:  map[string]store.ResourceState{},
	}
	for id, actor := range actors {
		state.Actors[id] = actor
	}
	for _, group := range activities {
		for id, activity := range group {
			state.Activities[id] = activity
		}
	}
	return state
}
