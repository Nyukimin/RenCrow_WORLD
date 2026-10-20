package store

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testScope() Scope {
	return Scope{WorldID: "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-1"}
}

func openTestStore(t *testing.T) (*Store, Scope) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "world.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	registerTestHandlers(s)
	scope := testScope()
	initial := State{
		Actors: map[string]ActorState{
			"mio":   {RoomID: "1f-living"},
			"shiro": {RoomID: "2f-room"},
		},
	}
	if err := s.CreateTimeline(scope, initial, map[string]int64{"chair": 1, "drink": 1}); err != nil {
		t.Fatalf("create timeline: %v", err)
	}
	return s, scope
}

func registerTestHandlers(s *Store) {
	s.Register("move", func(st State, payload json.RawMessage) (Outcome, error) {
		var v struct {
			ActorID string `json:"actor_id"`
			RoomID  string `json:"room_id"`
		}
		if err := json.Unmarshal(payload, &v); err != nil {
			return Outcome{}, err
		}
		if v.ActorID == "" || v.RoomID == "" {
			return Outcome{}, errors.New("missing actor or room")
		}
		st.Actors[v.ActorID] = ActorState{RoomID: v.RoomID}
		return Outcome{State: st, Events: []EventDraft{{Kind: "actor_moved", Payload: payload}}}, nil
	})

	s.Register("activity_start", func(st State, payload json.RawMessage) (Outcome, error) {
		var v struct {
			ActivityID string `json:"activity_id"`
			ActorID    string `json:"actor_id"`
			Resources  []struct {
				ResourceID string `json:"resource_id"`
				Quantity   int64  `json:"quantity"`
			} `json:"resources"`
		}
		if err := json.Unmarshal(payload, &v); err != nil {
			return Outcome{}, err
		}
		if v.ActivityID == "" || v.ActorID == "" || len(v.Resources) == 0 {
			return Outcome{}, errors.New("missing activity, actor, or resources")
		}
		actor, ok := st.Actors[v.ActorID]
		if !ok {
			return Outcome{}, errors.New("unknown actor")
		}
		actor.ActivityID = v.ActivityID
		st.Actors[v.ActorID] = actor
		st.Activities[v.ActivityID] = ActivityState{ID: v.ActivityID, ActorID: v.ActorID, Status: "running"}
		out := Outcome{State: st, Events: []EventDraft{{Kind: "activity_started", Payload: payload}}}
		for _, r := range v.Resources {
			out.Reserve = append(out.Reserve, Reservation{
				ResourceID: r.ResourceID, ActorID: v.ActorID, ActivityID: v.ActivityID, Quantity: r.Quantity,
			})
			out.Events = append(out.Events, EventDraft{
				Kind:    "reservation_placed",
				Payload: mustJSON(map[string]any{"activity_id": v.ActivityID, "resource_id": r.ResourceID, "quantity": r.Quantity}),
			})
		}
		return out, nil
	})

	s.Register("activity_cancel", func(st State, payload json.RawMessage) (Outcome, error) {
		var v struct {
			ActivityID string `json:"activity_id"`
		}
		if err := json.Unmarshal(payload, &v); err != nil {
			return Outcome{}, err
		}
		activity, ok := st.Activities[v.ActivityID]
		if !ok || activity.Status != "running" {
			return Outcome{}, errors.New("activity is not running")
		}
		activity.Status = "cancelled"
		st.Activities[v.ActivityID] = activity
		actor := st.Actors[activity.ActorID]
		actor.ActivityID = ""
		st.Actors[activity.ActorID] = actor
		out := Outcome{State: st, Events: []EventDraft{{Kind: "activity_cancelled", Payload: payload}}}
		for _, r := range st.Reservations {
			if r.ActivityID == v.ActivityID {
				out.Release = append(out.Release, r)
			}
		}
		return out, nil
	})

	s.Register("activity_complete", func(st State, payload json.RawMessage) (Outcome, error) {
		var v struct {
			ActivityID string `json:"activity_id"`
		}
		if err := json.Unmarshal(payload, &v); err != nil {
			return Outcome{}, err
		}
		activity, ok := st.Activities[v.ActivityID]
		if !ok || activity.Status != "running" {
			return Outcome{}, errors.New("activity is not running")
		}
		activity.Status = "completed"
		st.Activities[v.ActivityID] = activity
		actor := st.Actors[activity.ActorID]
		actor.ActivityID = ""
		st.Actors[activity.ActorID] = actor
		out := Outcome{State: st, Events: []EventDraft{{Kind: "activity_completed", Payload: payload}}}
		for _, r := range st.Reservations {
			if r.ActivityID != v.ActivityID {
				continue
			}
			if r.ResourceID == "drink" {
				out.Consume = append(out.Consume, r)
				out.Events = append(out.Events, EventDraft{
					Kind:    "resource_consumed",
					Payload: mustJSON(map[string]any{"activity_id": v.ActivityID, "resource_id": r.ResourceID, "quantity": r.Quantity}),
				})
			} else {
				out.Release = append(out.Release, r)
			}
		}
		return out, nil
	})

	s.Register("emit", func(st State, payload json.RawMessage) (Outcome, error) {
		var v struct {
			Count int `json:"count"`
		}
		if err := json.Unmarshal(payload, &v); err != nil {
			return Outcome{}, err
		}
		out := Outcome{State: st}
		for i := 0; i < v.Count; i++ {
			out.Events = append(out.Events, EventDraft{Kind: "emitted", Payload: mustJSON(map[string]int{"n": i})})
		}
		return out, nil
	})
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func testCommand(scope Scope, key, action string, payload any) Command {
	return Command{
		Scope:          scope,
		ClientID:       "client-test",
		IdempotencyKey: key,
		Action:         action,
		Payload:        mustJSON(payload),
		RecordedAt:     time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
	}
}

func TestAtomicCommitAndReceipt(t *testing.T) {
	s, scope := openTestStore(t)
	start := testCommand(scope, "k1", "activity_start", map[string]any{
		"activity_id": "act-1", "actor_id": "mio",
		"resources": []map[string]any{{"resource_id": "chair", "quantity": 1}, {"resource_id": "drink", "quantity": 1}},
	})
	res, err := s.Append(start)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if res.Receipt.Status != "committed" {
		t.Fatalf("status = %q", res.Receipt.Status)
	}
	if res.Receipt.FirstSeq != 1 || res.Receipt.LastSeq != 3 || res.Receipt.StateSeq != 3 {
		t.Fatalf("seq = %d..%d state=%d", res.Receipt.FirstSeq, res.Receipt.LastSeq, res.Receipt.StateSeq)
	}
	if len(res.Receipt.EventIDs) != 3 {
		t.Fatalf("event ids = %d", len(res.Receipt.EventIDs))
	}
	state, seq, hash, err := s.Snapshot(scope)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if seq != 3 || hash == "" {
		t.Fatalf("snapshot seq=%d hash=%q", seq, hash)
	}
	if len(state.Reservations) != 2 {
		t.Fatalf("reservations = %d", len(state.Reservations))
	}
	if state.Resources["chair"].Used != 0 || state.Resources["drink"].Used != 0 {
		t.Fatalf("inventory changed before consumption: %+v", state.Resources)
	}
	events, err := s.Page(scope, 0, 10, 1<<20)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if len(events.Events) != 3 || !events.Done {
		t.Fatalf("events=%d done=%v", len(events.Events), events.Done)
	}
	for i, ev := range events.Events {
		if ev.Seq != uint64(i+1) || ev.TransactionID != res.Receipt.TransactionID {
			t.Fatalf("event %d = %+v", i, ev)
		}
	}
}

func TestIdempotentRetryReturnsSameReceipt(t *testing.T) {
	s, scope := openTestStore(t)
	cmd := testCommand(scope, "retry", "activity_start", map[string]any{
		"activity_id": "act-1", "actor_id": "mio",
		"resources": []map[string]any{{"resource_id": "chair", "quantity": 1}},
	})
	first, err := s.Append(cmd)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	for i := 0; i < 3; i++ {
		again, err := s.Append(cmd)
		if err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		if !again.Duplicate || again.Receipt.ID != first.Receipt.ID {
			t.Fatalf("retry %d duplicate=%v receipt=%q want %q", i, again.Duplicate, again.Receipt.ID, first.Receipt.ID)
		}
		if again.Receipt.TransactionID != first.Receipt.TransactionID {
			t.Fatalf("retry %d txn changed", i)
		}
	}
	page, err := s.Page(scope, 0, 100, 1<<20)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("events after retries = %d, want 2", len(page.Events))
	}
}

func TestIdempotencyConflictOnDifferentBody(t *testing.T) {
	s, scope := openTestStore(t)
	cmd := testCommand(scope, "conflict", "move", map[string]any{"actor_id": "mio", "room_id": "1f-kitchen"})
	if _, err := s.Append(cmd); err != nil {
		t.Fatalf("first: %v", err)
	}
	cmd.Payload = mustJSON(map[string]any{"actor_id": "mio", "room_id": "2f-room"})
	if _, err := s.Append(cmd); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("err = %v, want ErrIdempotencyConflict", err)
	}
	state, _, _, err := s.Snapshot(scope)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if state.Actors["mio"].RoomID != "1f-kitchen" {
		t.Fatalf("state changed on conflict: %+v", state.Actors["mio"])
	}
	page, _ := s.Page(scope, 0, 100, 1<<20)
	if len(page.Events) != 1 {
		t.Fatalf("events = %d", len(page.Events))
	}
}

func TestStrictCAS(t *testing.T) {
	s, scope := openTestStore(t)
	var seq uint64
	a := testCommand(scope, "cas-a", "move", map[string]any{"actor_id": "mio", "room_id": "1f-kitchen"})
	a.ExpectedStateSeq = &seq
	if _, err := s.Append(a); err != nil {
		t.Fatalf("first: %v", err)
	}
	b := testCommand(scope, "cas-b", "move", map[string]any{"actor_id": "mio", "room_id": "2f-room"})
	b.ExpectedStateSeq = &seq
	res, err := s.Append(b)
	if err != nil {
		t.Fatalf("stale append: %v", err)
	}
	if res.Receipt.Status != "rejected" || res.Receipt.Reason != "state_conflict" {
		t.Fatalf("receipt = %+v", res.Receipt)
	}
	if res.Receipt.TransactionID != "" || len(res.Receipt.EventIDs) != 0 {
		t.Fatalf("rejected receipt has effects: %+v", res.Receipt)
	}
	state, stateSeq, _, _ := s.Snapshot(scope)
	if stateSeq != 1 || state.Actors["mio"].RoomID != "1f-kitchen" {
		t.Fatalf("state changed on conflict: seq=%d room=%q", stateSeq, state.Actors["mio"].RoomID)
	}
	one := uint64(1)
	b.ExpectedStateSeq = &one
	b.IdempotencyKey = "cas-b2"
	res, err = s.Append(b)
	if err != nil || res.Receipt.Status != "committed" {
		t.Fatalf("retry: %v %+v", err, res.Receipt)
	}
}

func TestReservationsAreAllOrNothing(t *testing.T) {
	s, scope := openTestStore(t)
	payload := map[string]any{
		"activity_id": "act-%s", "actor_id": "%s",
		"resources": []map[string]any{{"resource_id": "chair", "quantity": 1}, {"resource_id": "drink", "quantity": 1}},
	}
	var wg sync.WaitGroup
	results := make([]Result, 2)
	errs := make([]error, 2)
	for i, actor := range []string{"mio", "shiro"} {
		wg.Add(1)
		go func(i int, actor string) {
			defer wg.Done()
			cmd := testCommand(scope, "race-"+actor, "activity_start", map[string]any{
				"activity_id": "act-" + actor, "actor_id": actor,
				"resources": payload["resources"],
			})
			results[i], errs[i] = s.Append(cmd)
		}(i, actor)
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("append %d: %v", i, errs[i])
		}
	}
	committed, rejected := 0, 0
	for _, r := range results {
		switch r.Receipt.Status {
		case "committed":
			committed++
		case "rejected":
			rejected++
			if r.Receipt.Reason != "resource_conflict" {
				t.Fatalf("reason = %q", r.Receipt.Reason)
			}
		}
	}
	if committed != 1 || rejected != 1 {
		t.Fatalf("committed=%d rejected=%d", committed, rejected)
	}
	state, _, _, err := s.Snapshot(scope)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(state.Reservations) != 2 {
		t.Fatalf("reservations = %+v", state.Reservations)
	}
	seen := map[string]int{}
	for _, r := range state.Reservations {
		seen[r.ResourceID]++
	}
	if seen["chair"] != 1 || seen["drink"] != 1 {
		t.Fatalf("partial reservation: %+v", state.Reservations)
	}
}

func TestCancelReleasesAndCompleteConsumesOnce(t *testing.T) {
	s, scope := openTestStore(t)
	start := func(key, activity string) Result {
		res, err := s.Append(testCommand(scope, key, "activity_start", map[string]any{
			"activity_id": activity, "actor_id": "mio",
			"resources": []map[string]any{{"resource_id": "chair", "quantity": 1}, {"resource_id": "drink", "quantity": 1}},
		}))
		if err != nil || res.Receipt.Status != "committed" {
			t.Fatalf("start %s: %v %+v", activity, err, res.Receipt)
		}
		return res
	}
	start("s1", "act-1")
	cancel, err := s.Append(testCommand(scope, "c1", "activity_cancel", map[string]any{"activity_id": "act-1"}))
	if err != nil || cancel.Receipt.Status != "committed" {
		t.Fatalf("cancel: %v %+v", err, cancel.Receipt)
	}
	state, _, _, _ := s.Snapshot(scope)
	if len(state.Reservations) != 0 {
		t.Fatalf("reservations after cancel = %+v", state.Reservations)
	}
	start("s2", "act-2")
	done, err := s.Append(testCommand(scope, "d1", "activity_complete", map[string]any{"activity_id": "act-2"}))
	if err != nil || done.Receipt.Status != "committed" {
		t.Fatalf("complete: %v %+v", err, done.Receipt)
	}
	state, _, _, _ = s.Snapshot(scope)
	if state.Resources["drink"].Used != 1 {
		t.Fatalf("drink used = %d", state.Resources["drink"].Used)
	}
	if len(state.Reservations) != 0 {
		t.Fatalf("reservations after complete = %+v", state.Reservations)
	}
	again, err := s.Append(testCommand(scope, "d2", "activity_complete", map[string]any{"activity_id": "act-2"}))
	if err != nil {
		t.Fatalf("second complete: %v", err)
	}
	if again.Receipt.Status != "rejected" {
		t.Fatalf("second complete = %+v", again.Receipt)
	}
	state, _, _, _ = s.Snapshot(scope)
	if state.Resources["drink"].Used != 1 {
		t.Fatalf("double consumption: %d", state.Resources["drink"].Used)
	}
}

func TestPaginationKeepsTransactionsWhole(t *testing.T) {
	s, scope := openTestStore(t)
	if _, err := s.Append(testCommand(scope, "t1", "emit", map[string]any{"count": 3})); err != nil {
		t.Fatalf("t1: %v", err)
	}
	if _, err := s.Append(testCommand(scope, "t2", "emit", map[string]any{"count": 2})); err != nil {
		t.Fatalf("t2: %v", err)
	}
	first, err := s.Page(scope, 0, 4, 1<<20)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(first.Events) != 3 || first.Done {
		t.Fatalf("page1 events=%d done=%v", len(first.Events), first.Done)
	}
	second, err := s.Page(scope, first.NextSeq, 4, 1<<20)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(second.Events) != 2 || !second.Done {
		t.Fatalf("page2 events=%d done=%v", len(second.Events), second.Done)
	}
	if second.Events[0].TransactionID == first.Events[0].TransactionID {
		t.Fatalf("transaction split across pages")
	}
}

func TestCommandLimits(t *testing.T) {
	s, scope := openTestStore(t)
	if _, err := s.Append(testCommand(scope, "many", "emit", map[string]any{"count": 129})); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("event limit err = %v", err)
	}
	big := strings.Repeat("x", 300*1024)
	cmd := testCommand(scope, "big", "move", map[string]any{"actor_id": "mio", "room_id": big})
	if _, err := s.Append(cmd); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("size limit err = %v", err)
	}
}

func TestReplayHashIsDeterministic(t *testing.T) {
	s, scope := openTestStore(t)
	if _, err := s.Append(testCommand(scope, "r1", "activity_start", map[string]any{
		"activity_id": "act-1", "actor_id": "mio",
		"resources": []map[string]any{{"resource_id": "chair", "quantity": 1}},
	})); err != nil {
		t.Fatalf("append1: %v", err)
	}
	if _, err := s.Append(testCommand(scope, "r2", "move", map[string]any{"actor_id": "shiro", "room_id": "1f-living"})); err != nil {
		t.Fatalf("append2: %v", err)
	}
	_, liveSeq, liveHash, err := s.Snapshot(scope)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	st1, hash1, seq1, err := s.Replay(scope, liveSeq)
	if err != nil {
		t.Fatalf("replay1: %v", err)
	}
	st2, hash2, seq2, err := s.Replay(scope, liveSeq)
	if err != nil {
		t.Fatalf("replay2: %v", err)
	}
	if hash1 != liveHash || hash2 != liveHash || seq1 != liveSeq || seq2 != liveSeq {
		liveState, _, _, _ := s.Snapshot(scope)
		lb, _ := json.Marshal(liveState)
		rb, _ := json.Marshal(st1)
		t.Fatalf("replay hash mismatch live=%s r1=%s r2=%s\nlive=%s\nreplay=%s", liveHash, hash1, hash2, lb, rb)
	}
	if !reflectStateEqual(st1, st2) {
		t.Fatalf("replay states differ")
	}
}

func reflectStateEqual(a, b State) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func TestRestoreStartsNewEpoch(t *testing.T) {
	s, scope := openTestStore(t)
	if _, err := s.Append(testCommand(scope, "e1", "move", map[string]any{"actor_id": "mio", "room_id": "1f-kitchen"})); err != nil {
		t.Fatalf("append: %v", err)
	}
	state, seq, _, _ := s.Snapshot(scope)
	next := Scope{WorldID: scope.WorldID, TimelineID: scope.TimelineID, TimelineEpoch: "epoch-2"}
	if err := s.CreateTimeline(next, state, map[string]int64{"chair": 1, "drink": 1}); err != nil {
		t.Fatalf("new epoch: %v", err)
	}
	_, newSeq, _, _ := s.Snapshot(next)
	if newSeq != 0 {
		t.Fatalf("new epoch seq = %d (old %d)", newSeq, seq)
	}
	if _, err := s.Append(testCommand(scope, "e2", "move", map[string]any{"actor_id": "mio", "room_id": "2f-room"})); err != nil {
		t.Fatalf("old epoch still writable: %v", err)
	}
}

func TestCrashRecovery(t *testing.T) {
	points := []string{"events", "state", "receipt", "commit"}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "crash.db")
			s, err := Open(db)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			registerTestHandlers(s)
			if err := s.CreateTimeline(testScope(), State{Actors: map[string]ActorState{"mio": {RoomID: "1f-living"}}}, map[string]int64{"chair": 1, "drink": 1}); err != nil {
				t.Fatalf("create: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}

			cmd := exec.Command(os.Args[0], "-test.run=TestCrashChild")
			cmd.Env = append(os.Environ(), "WORLD_CRASH_POINT="+point, "WORLD_CRASH_DB="+db)
			out, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 {
				t.Fatalf("child err = %v, out=%s", err, out)
			}

			reopened, err := Open(db)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			defer reopened.Close()
			state, seq, hash, err := reopened.Snapshot(testScope())
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			if seq == 0 {
				if len(state.Reservations) != 0 {
					t.Fatalf("partial state at seq 0: %+v", state)
				}
				page, err := reopened.Page(testScope(), 0, 10, 1<<20)
				if err != nil {
					t.Fatalf("page: %v", err)
				}
				if len(page.Events) != 0 {
					t.Fatalf("partial events: %d", len(page.Events))
				}
				return
			}
			if seq != 3 {
				t.Fatalf("seq = %d, want 0 or 3", seq)
			}
			if hash == "" {
				t.Fatalf("missing hash")
			}
			page, err := reopened.Page(testScope(), 0, 10, 1<<20)
			if err != nil || len(page.Events) != 3 {
				t.Fatalf("page err=%v events=%d", err, len(page.Events))
			}
			if len(state.Reservations) != 2 {
				t.Fatalf("reservations = %d", len(state.Reservations))
			}
			if _, replayHash, _, err := reopened.Replay(testScope(), seq); err != nil || replayHash != hash {
				t.Fatalf("replay err=%v hash=%s want %s", err, replayHash, hash)
			}
		})
	}
}

func TestCrashChild(t *testing.T) {
	point := os.Getenv("WORLD_CRASH_POINT")
	if point == "" {
		t.Skip("child only")
	}
	s, err := Open(os.Getenv("WORLD_CRASH_DB"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	registerTestHandlers(s)
	transactionHook = func(p string) {
		if p == point {
			os.Exit(17)
		}
	}
	_, _ = s.Append(testCommand(testScope(), "crash", "activity_start", map[string]any{
		"activity_id": "act-1", "actor_id": "mio",
		"resources": []map[string]any{{"resource_id": "chair", "quantity": 1}, {"resource_id": "drink", "quantity": 1}},
	}))
	t.Fatalf("append returned without hitting crash point %q", point)
}
