package life

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

func TestHomeConcurrentMovesShareCorridorWithoutPathReservation(t *testing.T) {
	now := time.Date(2026, time.November, 15, 3, 0, 0, 0, time.UTC)
	layout := sampleHomeLayout()
	actors := make(map[string]store.ActorState, 8)
	type move struct {
		actorID string
		roomID  string
	}
	moves := make([]move, 0, 8)
	for i := 0; i < 8; i++ {
		actorID := fmt.Sprintf("guest-%d", i+1)
		roomID := "living"
		targetRoomID := "lounge"
		if i%2 == 1 {
			roomID, targetRoomID = "lounge", "living"
		}
		actors[actorID] = homeActorAt(roomID, layout.RoomAnchors[roomID], "")
		moves = append(moves, move{actorID: actorID, roomID: targetRoomID})
	}
	st, _ := homeTestStore(t, layout, actors, &now)

	var firstResult store.Result
	for i, move := range moves {
		payload, err := json.Marshal(map[string]string{"actor_id": move.actorID, "room_id": move.roomID})
		if err != nil {
			t.Fatalf("encode move %d: %v", i, err)
		}
		result := appendHome(t, st, now, fmt.Sprintf("shared-corridor-%d", i+1), ActionHomeMove, string(payload))
		if result.Receipt.Status != "committed" {
			t.Fatalf("move %d for %s to %s = %+v, want concurrent route admission", i+1, move.actorID, move.roomID, result.Receipt)
		}
		if i == 0 {
			firstResult = result
		}
	}
	duplicate := appendHome(t, st, now, "shared-corridor-1", ActionHomeMove, `{"actor_id":"guest-1","room_id":"lounge"}`)
	if !duplicate.Duplicate || duplicate.Receipt.ID != firstResult.Receipt.ID || duplicate.Receipt.TransactionID != firstResult.Receipt.TransactionID {
		t.Fatalf("same-key move retry = duplicate=%v receipt=%+v; original=%+v, want original durable receipt", duplicate.Duplicate, duplicate.Receipt, firstResult.Receipt)
	}
	state := homeSnapshot(t, st)
	if len(state.Reservations) != 0 {
		t.Fatalf("active corridor moves reserved shared-path resources: %+v, want no path reservations", state.Reservations)
	}
	for _, move := range moves {
		actor := state.Actors[move.actorID]
		if actor.ActivityKind != HomeActivityMove || actor.MovePhase != "in_transit" {
			t.Errorf("actor %s after admitted shared-corridor move = %+v, want active in-transit move", move.actorID, actor)
		}
	}
}

func TestHomeMoveRetainsTargetSlotExclusivityWithoutPathReservation(t *testing.T) {
	now := time.Date(2026, time.November, 15, 3, 10, 0, 0, time.UTC)
	layout := sampleHomeLayout()
	layout.Paths = append(layout.Paths, HomePath{
		ID: "exercise-lounge", FromRoomID: "exercise", ToRoomID: "lounge", Points: []Vec3{{30, 0, 0}}, DurationMS: 1000,
	})
	st, _ := homeTestStore(t, layout, map[string]store.ActorState{
		"kuro":  homeActorAt("living", Vec3{0, 0, 0}, ""),
		"shiro": homeActorAt("exercise", Vec3{20, 0, 0}, ""),
	}, &now)

	moveToSofa := func(key, actorID string) store.Result {
		payload, err := json.Marshal(map[string]string{"actor_id": actorID, "room_id": "lounge", "slot_id": "sofa"})
		if err != nil {
			t.Fatalf("encode move to sofa: %v", err)
		}
		return appendHome(t, st, now, key, ActionHomeMove, string(payload))
	}

	first := moveToSofa("sofa-first", "kuro")
	firstState := homeSnapshot(t, st)
	if first.Receipt.Status != "committed" || len(firstState.Reservations) != 1 ||
		firstState.Reservations[0].ResourceID != homeSlotResource("sofa") ||
		firstState.Reservations[0].Quantity != 1 {
		t.Fatalf("first move to sofa = receipt=%+v reservations=%+v, want only the exclusive target-slot reservation", first.Receipt, firstState.Reservations)
	}

	second := moveToSofa("sofa-second", "shiro")
	secondState := homeSnapshot(t, st)
	if second.Receipt.Status != "rejected" || second.Receipt.Reason != "resource_conflict" ||
		secondState.Actors["shiro"].ActivityID != "" || len(secondState.Reservations) != 1 {
		t.Fatalf("second move to occupied target slot = receipt=%+v actor=%+v reservations=%+v, want resource-conflict rejection without partial admission",
			second.Receipt, secondState.Actors["shiro"], secondState.Reservations)
	}
}

func TestHomeExistingPathAndSlotReservationsReleaseAfterRestart(t *testing.T) {
	for _, action := range []string{ActionHomeFinish, ActionHomeStop} {
		t.Run(action, func(t *testing.T) {
			now := time.Date(2026, time.November, 15, 3, 20, 0, 0, time.UTC)
			layout := sampleHomeLayout()
			storePath := filepath.Join(t.TempDir(), "home.sqlite")
			actors := map[string]store.ActorState{
				"kuro":  homeActorAt("living", Vec3{0, 0, 0}, ""),
				"shiro": homeActorAt("lounge", Vec3{40, 0, 0}, ""),
			}
			st, controller := openHomeStoreForRecovery(t, storePath, layout, actors, &now, true)
			t.Cleanup(func() {
				if st != nil {
					_ = st.Close()
				}
			})

			legacyActivityID, legacyPathResource := appendLegacyHomeMove(t, st, controller, now, "kuro", "lounge", "sofa")
			beforeNewMove := homeSnapshot(t, st)
			if len(beforeNewMove.Reservations) != 2 ||
				!hasHomeReservation(beforeNewMove, legacyPathResource) ||
				!hasHomeReservation(beforeNewMove, homeSlotResource("sofa")) {
				t.Fatalf("legacy move reservations before parallel move = %+v, want exact path and slot reservation", beforeNewMove.Reservations)
			}

			newMovePayload, err := json.Marshal(map[string]string{"actor_id": "shiro", "room_id": "living"})
			if err != nil {
				t.Fatal(err)
			}
			newMove := appendHome(t, st, now, "new-reverse-move", ActionHomeMove, string(newMovePayload))
			if newMove.Receipt.Status != "committed" {
				t.Fatalf("new reverse move while legacy reservation is active = %+v, want committed", newMove.Receipt)
			}
			midState := homeSnapshot(t, st)
			if len(midState.Reservations) != 2 ||
				!hasHomeReservation(midState, legacyPathResource) ||
				!hasHomeReservation(midState, homeSlotResource("sofa")) {
				t.Fatalf("new move changed legacy reservations = %+v, want old path and slot held until settlement", midState.Reservations)
			}
			shiroActivityID := midState.Actors["shiro"].ActivityID
			if shiroActivityID == "" || midState.Actors["shiro"].MoveResourceIDs != "[]" {
				t.Fatalf("new reverse move checkpoint = %+v, want active move with no path reservation IDs", midState.Actors["shiro"])
			}

			if err := st.Close(); err != nil {
				t.Fatalf("close before controller restart: %v", err)
			}
			st = nil
			now = now.Add(time.Second)
			st, controller = openHomeStoreForRecovery(t, storePath, layout, nil, &now, false)
			restartedState := homeSnapshot(t, st)
			for _, actorID := range []string{"kuro", "shiro"} {
				if points, err := controller.MovePoints(restartedState, actorID); err != nil || len(points) < 2 {
					t.Fatalf("recovered measured route for %s = %v err=%v", actorID, points, err)
				}
			}

			kuroPayload, err := json.Marshal(map[string]string{"actor_id": "kuro", "activity_id": legacyActivityID})
			if err != nil {
				t.Fatal(err)
			}
			settleLegacy := appendHome(t, st, now, "settle-legacy-move", action, string(kuroPayload))
			if settleLegacy.Receipt.Status != "committed" {
				t.Fatalf("settle legacy %s after restart = %+v, want committed and old reservations released", action, settleLegacy.Receipt)
			}
			afterLegacy := homeSnapshot(t, st)
			if len(afterLegacy.Reservations) != 0 {
				t.Fatalf("legacy %s left stale reservations after restart: %+v", action, afterLegacy.Reservations)
			}
			if afterLegacy.Actors["shiro"].ActivityID != shiroActivityID || afterLegacy.Actors["shiro"].MovePhase != "in_transit" {
				t.Fatalf("settling legacy move disturbed concurrent new move: %+v", afterLegacy.Actors["shiro"])
			}

			now = now.Add(time.Second)
			shiroPayload, err := json.Marshal(map[string]string{"actor_id": "shiro", "activity_id": shiroActivityID})
			if err != nil {
				t.Fatal(err)
			}
			settleNew := appendHome(t, st, now, "settle-new-move", ActionHomeFinish, string(shiroPayload))
			if settleNew.Receipt.Status != "committed" {
				t.Fatalf("finish new move after restart = %+v, want committed", settleNew.Receipt)
			}
			finalState, _, _, err := st.Snapshot(homeScope)
			if err != nil {
				t.Fatal(err)
			}
			replayed, _, replaySeq, err := st.Replay(homeScope, finalStateSeq(t, st))
			if err != nil || replaySeq == 0 || !reflect.DeepEqual(replayed, finalState) {
				t.Fatalf("replay after old and new move settlement = replay=%+v final=%+v seq=%d err=%v, want exact durable final state", replayed, finalState, replaySeq, err)
			}

			if err := st.Close(); err != nil {
				t.Fatalf("close before replay restart: %v", err)
			}
			st = nil
			st, controller = openHomeStoreForRecovery(t, storePath, layout, nil, &now, false)
			_ = controller
			reopened, _, seq, err := st.Replay(homeScope, finalStateSeq(t, st))
			if err != nil || seq == 0 || !reflect.DeepEqual(reopened, finalState) {
				t.Fatalf("replay after store restart = state=%+v seq=%d err=%v, want exact final state", reopened, seq, err)
			}
		})
	}
}

func openHomeStoreForRecovery(t *testing.T, path string, layout HomeLayout, actors map[string]store.ActorState, now *time.Time, create bool) (*store.Store, *HomeController) {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open home store: %v", err)
	}
	controller, err := NewHomeController(layout, func() time.Time { return *now })
	if err != nil {
		_ = st.Close()
		t.Fatalf("create home controller: %v", err)
	}
	if create {
		if err := st.CreateTimeline(homeScope, store.State{Actors: actors}, controller.ResourceCapacities()); err != nil {
			_ = st.Close()
			t.Fatalf("create home timeline: %v", err)
		}
	}
	controller.Register(st)
	return st, controller
}

func appendLegacyHomeMove(t *testing.T, st *store.Store, controller *HomeController, now time.Time, actorID, targetRoomID, targetSlotID string) (string, string) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"actor_id": actorID, "room_id": targetRoomID, "slot_id": targetSlotID})
	if err != nil {
		t.Fatal(err)
	}
	activityID := ""
	pathResourceID := ""
	result, err := st.AppendWithHandler(store.Command{
		Scope: homeScope, ClientID: "core-life", IdempotencyKey: "legacy-move-" + actorID,
		Action: ActionHomeMove, Payload: payload, RecordedAt: now,
	}, func(state store.State, raw json.RawMessage) (store.Outcome, error) {
		var cmd homeMoveCommand
		if err := decodeHomePayload(raw, &cmd); err != nil {
			return store.Outcome{}, err
		}
		actor, ok := state.Actors[cmd.ActorID]
		if !ok {
			return store.Outcome{}, ErrHomeActorUnknown
		}
		route, err := controller.resolveRoute(cmd.ActorID, actor, cmd.RoomID, cmd.SlotID)
		if err != nil {
			return store.Outcome{}, err
		}
		if len(route.pathIDs) != 1 {
			return store.Outcome{}, fmt.Errorf("legacy fixture expected one path, got %v", route.pathIDs)
		}
		activityID = homeActivityID(cmd.ActorID, actor.ActivitySequence+1)
		startedAt := formatHomeTime(now)
		dueAt := formatHomeTime(now.Add(time.Duration(route.durationMS) * time.Millisecond))
		resourceIDs := make([]string, 0, len(route.pathIDs)+1)
		for _, pathID := range route.pathIDs {
			resourceID := controller.pathResources[pathID]
			if resourceID == "" {
				return store.Outcome{}, fmt.Errorf("missing path resource for %s", pathID)
			}
			pathResourceID = resourceID
			resourceIDs = append(resourceIDs, resourceID)
		}
		if cmd.SlotID != "" {
			resourceIDs = append(resourceIDs, homeSlotResource(cmd.SlotID))
		}

		after := state
		after.Actors = make(map[string]store.ActorState, len(state.Actors))
		for id, current := range state.Actors {
			after.Actors[id] = current
		}
		after.Activities = make(map[string]store.ActivityState, len(state.Activities)+1)
		for id, current := range state.Activities {
			after.Activities[id] = current
		}
		actor.ActivitySequence++
		actor.ActivityID = activityID
		actor.ActivityKind = HomeActivityMove
		actor.ActivityStartedAt = startedAt
		actor.ActivityDueAt = dueAt
		actor.TargetRoomID = cmd.RoomID
		actor.TargetSlotID = cmd.SlotID
		actor.MovePhase = "in_transit"
		actor.MoveRouteID = route.routeID
		actor.MovePathIDs = encodePathIDs(route.pathIDs)
		actor.MoveResourceIDs = encodePathIDs(resourceIDs)
		actor.LayoutSHA256 = controller.layoutSHA256
		actor.HomeSceneSHA256 = controller.layout.SceneSHA256
		after.Actors[cmd.ActorID] = actor
		after.Activities[activityID] = store.ActivityState{
			ID: activityID, ActorID: cmd.ActorID, Status: HomeActivityStatusRunning, Kind: HomeActivityMove,
			StartedAt: startedAt, DueAt: dueAt, LayoutSHA256: controller.layoutSHA256, SceneSHA256: controller.layout.SceneSHA256,
		}
		reservations := make([]store.Reservation, 0, len(resourceIDs))
		for _, resourceID := range resourceIDs {
			reservations = append(reservations, store.Reservation{
				ResourceID: resourceID, ActorID: cmd.ActorID, ActivityID: activityID, Quantity: 1,
			})
		}
		return store.Outcome{
			State: after,
			Events: []store.EventDraft{{Kind: EventHomeMoveStarted, Payload: marshalHomeEvent(homeEvent{
				ActorID: cmd.ActorID, ActivityID: activityID, ActivityKind: HomeActivityMove,
				RoomID: actor.RoomID, FromRoomID: actor.RoomID, TargetRoomID: cmd.RoomID, SlotID: cmd.SlotID,
				RouteID: route.routeID, LayoutSHA256: controller.layoutSHA256, SceneSHA256: controller.layout.SceneSHA256,
				StartedAt: startedAt, DueAt: dueAt,
			})}},
			Reserve: reservations,
		}, nil
	})
	if err != nil || result.Receipt.Status != "committed" {
		t.Fatalf("seed persisted legacy path+slot move: receipt=%+v err=%v", result.Receipt, err)
	}
	return activityID, pathResourceID
}

func hasHomeReservation(state store.State, resourceID string) bool {
	for _, reservation := range state.Reservations {
		if reservation.ResourceID == resourceID {
			return true
		}
	}
	return false
}

func finalStateSeq(t *testing.T, st *store.Store) uint64 {
	t.Helper()
	_, seq, _, err := st.Snapshot(homeScope)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}
