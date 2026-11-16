package life

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

var homeScope = store.Scope{WorldID: "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-1"}

func sampleHomeLayout() HomeLayout {
	return HomeLayout{
		SchemaVersion: HomeLayoutSchemaVersion,
		SceneSHA256:   strings.Repeat("a", 64),
		RoomAnchors: map[string]Vec3{
			"living":   {0, 0, 0},
			"bedroom":  {10, 0, 0},
			"exercise": {20, 0, 0},
			"mirror":   {30, 0, 0},
			"lounge":   {40, 0, 0},
		},
		Paths: []HomePath{
			{ID: "living-bedroom", FromRoomID: "living", ToRoomID: "bedroom", Points: []Vec3{{5, 0, 1}}, DurationMS: 1000},
			{ID: "bedroom-living", FromRoomID: "bedroom", ToRoomID: "living", Points: []Vec3{{5, 0, 1}}, DurationMS: 1000},
			{ID: "bedroom-exercise", FromRoomID: "bedroom", ToRoomID: "exercise", Points: []Vec3{{15, 0, 0}}, DurationMS: 1500},
			{ID: "exercise-mirror", FromRoomID: "exercise", ToRoomID: "mirror", Points: []Vec3{{25, 0, 0}}, DurationMS: 750},
			{ID: "living-lounge", FromRoomID: "living", ToRoomID: "lounge", Points: []Vec3{{20, 0, 0}}, DurationMS: 1000},
			{ID: "lounge-living", FromRoomID: "lounge", ToRoomID: "living", Points: []Vec3{{20, 0, 0}}, DurationMS: 1000},
		},
		PrivateRooms: map[string]string{"kuro": "bedroom"},
		Slots: []HomeSlot{
			{ID: "bed-kuro", Kind: HomeSlotBed, ActorID: "kuro", RoomID: "bedroom", Anchor: Vec3{10, 0, 0}},
			{ID: "mat-shiro", Kind: HomeSlotExercise, ActorID: "shiro", RoomID: "exercise", Anchor: Vec3{20, 0, 0}, DurationMS: 5000},
			{ID: "mirror-kuro", Kind: HomeSlotMirror, ActorID: "kuro", RoomID: "mirror", Anchor: Vec3{31, 0, 0}, ApproachPoints: []Vec3{{30.5, 0, 0}}, ApproachDurationMS: 250},
			{ID: "sofa", Kind: HomeSlotRest, RoomID: "lounge", Anchor: Vec3{40, 0, 0}},
		},
		MirrorPoses: map[string][]MirrorPose{
			"kuro": {{ID: "wave", SlotID: "mirror-kuro", DurationMS: 2000}, {ID: "bow", SlotID: "mirror-kuro", DurationMS: 3000}},
		},
	}
}

func homeTestStore(t *testing.T, layout HomeLayout, actors map[string]store.ActorState, now *time.Time) (*store.Store, *HomeController) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "home.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	controller, err := NewHomeController(layout, func() time.Time { return *now })
	if err != nil {
		t.Fatalf("NewHomeController: %v", err)
	}
	if err := st.CreateTimeline(homeScope, store.State{Actors: actors}, controller.ResourceCapacities()); err != nil {
		t.Fatalf("CreateTimeline: %v", err)
	}
	controller.Register(st)
	return st, controller
}

func homeActorAt(room string, pos Vec3, slotID string) store.ActorState {
	position := [3]float64(pos)
	return store.ActorState{RoomID: room, Position: &position, SlotID: slotID}
}

func appendHome(t *testing.T, st *store.Store, now time.Time, key, action, payload string) store.Result {
	t.Helper()
	result, err := st.Append(store.Command{
		Scope: homeScope, ClientID: "core-life", IdempotencyKey: key,
		Action: action, Payload: json.RawMessage(payload), RecordedAt: now,
	})
	if err != nil {
		t.Fatalf("append %s: %v", action, err)
	}
	return result
}

func homeSnapshot(t *testing.T, st *store.Store) store.State {
	t.Helper()
	state, _, _, err := st.Snapshot(homeScope)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return state
}

func readHomeEvent(t *testing.T, st *store.Store, seq uint64) (string, map[string]any) {
	t.Helper()
	page, err := st.Page(homeScope, seq-1, store.DefaultPageEvents, 0)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("page at %d: events=%d err=%v", seq, len(page.Events), err)
	}
	var payload map[string]any
	if err := json.Unmarshal(page.Events[0].Payload, &payload); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	return page.Events[0].Kind, payload
}

func TestHomeLayoutStrictVersionAndMeasuredCoordinates(t *testing.T) {
	valid := `{"schema_version":"world.home-layout/v1","scene_sha256":"` + strings.Repeat("a", 64) + `","room_anchors":{"living":[0,0,0],"bedroom":[1,0,0]},"paths":[{"id":"door","from_room_id":"living","to_room_id":"bedroom","points":[[0.5,0,0]],"duration_ms":1000}],"private_rooms":{"kuro":"bedroom"},"slots":[{"id":"bed-kuro","kind":"bed","actor_id":"kuro","room_id":"bedroom","anchor":[1,0,0]}]}`
	if _, err := ParseHomeLayout([]byte(valid)); err != nil {
		t.Fatalf("valid layout rejected: %v", err)
	}
	for name, raw := range map[string]string{
		"unknown version":        strings.Replace(valid, HomeLayoutSchemaVersion, "world.home-layout/v9", 1),
		"short vector":           strings.Replace(valid, `"living":[0,0,0]`, `"living":[0,0]`, 1),
		"unknown field":          strings.Replace(valid, `"scene_sha256":"`, `"operator_note":"x","scene_sha256":"`, 1),
		"unknown room":           strings.Replace(valid, `"to_room_id":"bedroom"`, `"to_room_id":"attic"`, 1),
		"duplicate private room": strings.Replace(valid, `"kuro":"bedroom"`, `"kuro":"bedroom","mio":"bedroom"`, 1),
		"trailing value":         valid + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseHomeLayout([]byte(raw)); err == nil {
				t.Fatal("invalid layout accepted")
			}
		})
	}
	if _, err := ParseHomeLayout([]byte(strings.Replace(valid, `"living":[0,0,0]`, `"living":[0,1e999,0]`, 1))); err == nil {
		t.Fatal("non-finite coordinate accepted")
	}
}

func TestHomeMoveKeepsConfirmedRoomUntilMeasuredRouteArrival(t *testing.T) {
	now := time.Date(2026, 11, 14, 1, 0, 0, 0, time.UTC)
	layout := sampleHomeLayout()
	st, controller := homeTestStore(t, layout, map[string]store.ActorState{
		"kuro": homeActorAt("living", Vec3{0, 0, 0}, ""),
	}, &now)

	started := appendHome(t, st, now, "move-1", ActionHomeMove, `{"actor_id":"kuro","room_id":"bedroom"}`)
	if started.Receipt.Status != "committed" || len(started.Receipt.EventIDs) != 1 {
		t.Fatalf("move start receipt = %+v", started.Receipt)
	}
	state := homeSnapshot(t, st)
	actor := state.Actors["kuro"]
	if actor.RoomID != "living" || actor.TargetRoomID != "bedroom" || actor.ActivityKind != HomeActivityMove || actor.ActivityID == "" {
		t.Fatalf("move start state = %+v; confirmed room must remain source", actor)
	}
	points, err := controller.MovePoints(state, "kuro")
	if err != nil || len(points) != 3 || points[1] != (Vec3{5, 0, 1}) || points[2] != (Vec3{10, 0, 0}) {
		t.Fatalf("measured move points = %v err=%v", points, err)
	}
	kind, startEvent := readHomeEvent(t, st, started.Receipt.LastSeq)
	if kind != EventHomeMoveStarted || startEvent["activity_id"] != actor.ActivityID || startEvent["started_at"] == nil || startEvent["due_at"] == nil {
		t.Fatalf("move started event = %s %v", kind, startEvent)
	}

	activityID := actor.ActivityID
	early := appendHome(t, st, now, "move-early", ActionHomeFinish, `{"actor_id":"kuro","activity_id":"`+activityID+`"}`)
	if early.Receipt.Status != "rejected" || homeSnapshot(t, st).Actors["kuro"].RoomID != "living" {
		t.Fatalf("early finish must reject without arrival: %+v", early.Receipt)
	}
	now = now.Add(time.Second)
	arrived := appendHome(t, st, now, "move-finish", ActionHomeFinish, `{"actor_id":"kuro","activity_id":"`+activityID+`"}`)
	if arrived.Receipt.Status != "committed" || arrived.Receipt.ID == "" {
		t.Fatalf("arrival receipt = %+v", arrived.Receipt)
	}
	state = homeSnapshot(t, st)
	actor = state.Actors["kuro"]
	if actor.RoomID != "bedroom" || actor.ActivityID != "" || actor.TargetRoomID != "" || actor.SlotID != "" || actor.Position == nil || Vec3(*actor.Position) != (Vec3{10, 0, 0}) {
		t.Fatalf("arrival state = %+v", actor)
	}
	kind, endEvent := readHomeEvent(t, st, arrived.Receipt.LastSeq)
	if kind != EventHomeMoveArrived || endEvent["activity_id"] != activityID || endEvent["ended_at"] == nil || endEvent["elapsed_ms"] != float64(1000) {
		t.Fatalf("arrival event = %s %v", kind, endEvent)
	}
	if len(state.Reservations) != 0 {
		t.Fatalf("arrival must release path reservations: %v", state.Reservations)
	}
	if arrived.Receipt.ID == started.Receipt.ID {
		t.Fatal("different command transactions returned the same receipt ID")
	}
}

func TestHomeMoveRequiresDirectedPathAndOwnerPrivateRoom(t *testing.T) {
	now := time.Date(2026, 11, 14, 1, 0, 0, 0, time.UTC)
	layout := sampleHomeLayout()
	// The fixture normally has a direct living→lounge route. Remove that edge
	// so the second assertion checks an actually unreachable destination.
	paths := layout.Paths[:0]
	for _, path := range layout.Paths {
		if path.ID != "living-lounge" {
			paths = append(paths, path)
		}
	}
	layout.Paths = paths
	st, _ := homeTestStore(t, layout, map[string]store.ActorState{
		"mio": homeActorAt("living", Vec3{0, 0, 0}, ""),
	}, &now)
	private := appendHome(t, st, now, "private", ActionHomeMove, `{"actor_id":"mio","room_id":"bedroom"}`)
	if private.Receipt.Status != "rejected" || homeSnapshot(t, st).Actors["mio"].RoomID != "living" {
		t.Fatalf("non-owner entered private room: %+v", private.Receipt)
	}
	noPath := appendHome(t, st, now, "no-path", ActionHomeMove, `{"actor_id":"mio","room_id":"lounge"}`)
	if noPath.Receipt.Status != "rejected" || homeSnapshot(t, st).Actors["mio"].RoomID != "living" {
		t.Fatalf("unrouted move changed room: %+v", noPath.Receipt)
	}
}

func TestHomeReverseCorridorDoesNotSerializeActiveMoves(t *testing.T) {
	now := time.Date(2026, 11, 14, 1, 15, 0, 0, time.UTC)
	st, controller := homeTestStore(t, sampleHomeLayout(), map[string]store.ActorState{
		"kuro":  homeActorAt("living", Vec3{0, 0, 0}, ""),
		"shiro": homeActorAt("lounge", Vec3{40, 0, 0}, ""),
	}, &now)
	if got := len(controller.ResourceCapacities()); got != 8 {
		t.Fatalf("resources=%d, want path-edge and slot capacities retained for existing moves", got)
	}
	first := appendHome(t, st, now, "corridor-forward", ActionHomeMove, `{"actor_id":"kuro","room_id":"lounge"}`)
	second := appendHome(t, st, now, "corridor-reverse", ActionHomeMove, `{"actor_id":"shiro","room_id":"living"}`)
	state := homeSnapshot(t, st)
	if first.Receipt.Status != "committed" || second.Receipt.Status != "committed" ||
		len(state.Reservations) != 0 ||
		state.Actors["kuro"].MovePhase != "in_transit" || state.Actors["shiro"].MovePhase != "in_transit" {
		t.Fatalf("reverse-direction concurrent moves = first=%+v second=%+v reservations=%+v kuro=%+v shiro=%+v, want both active without corridor reservations",
			first.Receipt, second.Receipt, state.Reservations, state.Actors["kuro"], state.Actors["shiro"])
	}
}

func TestHomeMovesReserveTheirTargetSlot(t *testing.T) {
	now := time.Date(2026, 11, 14, 1, 18, 0, 0, time.UTC)
	layout := sampleHomeLayout()
	layout.Paths = append(layout.Paths, HomePath{
		ID: "exercise-lounge", FromRoomID: "exercise", ToRoomID: "lounge", Points: []Vec3{{30, 0, 0}}, DurationMS: 1000,
	})
	st, _ := homeTestStore(t, layout, map[string]store.ActorState{
		"kuro":  homeActorAt("living", Vec3{0, 0, 0}, ""),
		"shiro": homeActorAt("exercise", Vec3{20, 0, 0}, ""),
	}, &now)
	first := appendHome(t, st, now, "sofa-first", ActionHomeMove, `{"actor_id":"kuro","room_id":"lounge","slot_id":"sofa"}`)
	firstState := homeSnapshot(t, st)
	if first.Receipt.Status != "committed" || len(firstState.Reservations) != 1 ||
		firstState.Reservations[0].ResourceID != homeSlotResource("sofa") {
		t.Fatalf("first route reservations=%+v, want only the exclusive target slot: receipt=%+v", firstState.Reservations, first.Receipt)
	}
	second := appendHome(t, st, now, "sofa-second", ActionHomeMove, `{"actor_id":"shiro","room_id":"lounge","slot_id":"sofa"}`)
	state := homeSnapshot(t, st)
	if second.Receipt.Status != "rejected" || second.Receipt.Reason != "resource_conflict" ||
		state.Actors["shiro"].RoomID != "exercise" || len(state.Reservations) != 1 ||
		state.Reservations[0].ResourceID != homeSlotResource("sofa") {
		t.Fatalf("parallel route bypassed target slot reservation: receipt=%+v shiro=%+v reservations=%v", second.Receipt, state.Actors["shiro"], state.Reservations)
	}
}

func TestHomeOccupiedSeatRemainsExclusiveUntilDepartureCompletes(t *testing.T) {
	now := time.Date(2026, 11, 14, 1, 18, 0, 0, time.UTC)
	layout := sampleHomeLayout()
	layout.Paths = append(layout.Paths, HomePath{
		ID: "exercise-lounge", FromRoomID: "exercise", ToRoomID: "lounge", Points: []Vec3{{30, 0, 0}}, DurationMS: 1000,
	})
	st, _ := homeTestStore(t, layout, map[string]store.ActorState{
		"kuro":  homeActorAt("living", Vec3{0, 0, 0}, ""),
		"shiro": homeActorAt("exercise", Vec3{20, 0, 0}, ""),
	}, &now)

	first := appendHome(t, st, now, "occupied-seat-first", ActionHomeMove, `{"actor_id":"kuro","room_id":"lounge","slot_id":"sofa"}`)
	if first.Receipt.Status != "committed" {
		t.Fatalf("start first move: %+v", first.Receipt)
	}
	second := appendHome(t, st, now, "occupied-seat-second", ActionHomeMove, `{"actor_id":"shiro","room_id":"lounge","slot_id":"sofa"}`)
	if second.Receipt.Status != "rejected" {
		t.Fatalf("seat reservation accepted a parallel move: %+v", second.Receipt)
	}

	finishKuroMove := func(key string) {
		t.Helper()
		now = now.Add(time.Minute)
		activityID := homeSnapshot(t, st).Actors["kuro"].ActivityID
		result := appendHome(t, st, now, key, ActionHomeFinish,
			fmt.Sprintf(`{"actor_id":"kuro","activity_id":%q}`, activityID))
		if result.Receipt.Status != "committed" {
			t.Fatalf("finish Kuro move: %+v", result.Receipt)
		}
	}
	assertSeatUnavailable := func(key string) {
		t.Helper()
		result := appendHome(t, st, now, key, ActionHomeMove, `{"actor_id":"shiro","room_id":"lounge","slot_id":"sofa"}`)
		shiro := homeSnapshot(t, st).Actors["shiro"]
		if result.Receipt.Status != "rejected" || shiro.RoomID != "exercise" || shiro.ActivityID != "" {
			t.Fatalf("occupied seat accepted a second actor: receipt=%+v Shiro=%+v", result.Receipt, shiro)
		}
	}

	finishKuroMove("occupied-seat-arrived")
	kuro := homeSnapshot(t, st).Actors["kuro"]
	if kuro.RoomID != "lounge" || kuro.SlotID != "sofa" {
		t.Fatalf("confirmed seat occupancy = %+v", kuro)
	}
	assertSeatUnavailable("occupied-seat-after-arrival")

	departure := appendHome(t, st, now, "occupied-seat-leave", ActionHomeMove, `{"actor_id":"kuro","room_id":"living"}`)
	if departure.Receipt.Status != "committed" {
		t.Fatalf("start departure: %+v", departure.Receipt)
	}
	kuro = homeSnapshot(t, st).Actors["kuro"]
	if kuro.SlotID != "sofa" || kuro.ActivityID == "" {
		t.Fatalf("seat was released before departure completed: %+v", kuro)
	}
	assertSeatUnavailable("occupied-seat-departure-pending")

	finishKuroMove("occupied-seat-departed")
	kuro = homeSnapshot(t, st).Actors["kuro"]
	if kuro.RoomID != "living" || kuro.SlotID != "" || kuro.ActivityID != "" {
		t.Fatalf("completed departure did not release the seat: %+v", kuro)
	}
	next := appendHome(t, st, now, "occupied-seat-next", ActionHomeMove, `{"actor_id":"shiro","room_id":"lounge","slot_id":"sofa"}`)
	if next.Receipt.Status != "committed" {
		t.Fatalf("vacated seat did not accept the next actor: %+v", next.Receipt)
	}
}

func TestHouse105HomeLayoutLoadsWithDigestAndAllPrivateRoomsReachable(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	layoutPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "examples", "house-105", "home-layout.json")
	layout, err := LoadHomeLayout(layoutPath)
	if err != nil {
		t.Fatalf("load checked house layout: %v", err)
	}
	if layout.SceneSHA256 != "602a80fee5ce300ecfd3bee8cb4b316f4b3c95a6fda207a14cc95e06aaac840e" || len(layout.RoomAnchors) != 16 || len(layout.Paths) != 30 || len(layout.Slots) != 18 || len(layout.MirrorPoses["kuro"]) != 8 {
		t.Fatalf("house layout contract mismatch: digest=%q rooms=%d paths=%d slots=%d Kuro poses=%d", layout.SceneSHA256, len(layout.RoomAnchors), len(layout.Paths), len(layout.Slots), len(layout.MirrorPoses["kuro"]))
	}
	controller, err := NewHomeController(layout, nil)
	if err != nil {
		t.Fatalf("initialize checked house layout: %v", err)
	}
	for actorID, roomID := range layout.PrivateRooms {
		t.Run(actorID, func(t *testing.T) {
			living := layout.RoomAnchors["living"]
			livingPosition := [3]float64(living)
			fromLiving := store.ActorState{RoomID: "living", Position: &livingPosition}
			toPrivateRoom, err := controller.resolveRoute(actorID, fromLiving, roomID, "")
			if err != nil || len(toPrivateRoom.pathIDs) == 0 {
				t.Fatalf("Actor cannot reach its private room: route=%+v err=%v", toPrivateRoom, err)
			}
			var bed HomeSlot
			for _, slot := range layout.Slots {
				if slot.Kind == HomeSlotBed && slot.ActorID == actorID {
					bed = slot
					break
				}
			}
			if bed.ID == "" {
				t.Fatal("Actor has no configured bed slot")
			}
			toBed, err := controller.resolveRoute(actorID, fromLiving, roomID, bed.ID)
			if err != nil || len(toBed.points) < 2 || toBed.points[len(toBed.points)-1] != bed.Anchor {
				t.Fatalf("Actor cannot reach its exact bed anchor: route=%+v err=%v", toBed, err)
			}
			privateAnchor := layout.RoomAnchors[roomID]
			privatePosition := [3]float64(privateAnchor)
			fromPrivate := store.ActorState{RoomID: roomID, Position: &privatePosition}
			toLiving, err := controller.resolveRoute(actorID, fromPrivate, "living", "")
			if err != nil || len(toLiving.pathIDs) == 0 {
				t.Fatalf("Actor cannot leave its private room: route=%+v err=%v", toLiving, err)
			}
		})
	}
}

func TestHomeShortestPathCannotTransitAnotherActorsPrivateRoom(t *testing.T) {
	now := time.Date(2026, 11, 14, 1, 20, 0, 0, time.UTC)
	layout := sampleHomeLayout()
	layout.Paths = []HomePath{
		{ID: "living-bedroom", FromRoomID: "living", ToRoomID: "bedroom", Points: []Vec3{{5, 0, 1}}, DurationMS: 1000},
		{ID: "bedroom-lounge", FromRoomID: "bedroom", ToRoomID: "lounge", Points: []Vec3{{25, 0, 1}}, DurationMS: 1000},
	}
	st, _ := homeTestStore(t, layout, map[string]store.ActorState{
		"mio": homeActorAt("living", Vec3{0, 0, 0}, ""),
	}, &now)
	result := appendHome(t, st, now, "private-shortcut", ActionHomeMove, `{"actor_id":"mio","room_id":"lounge"}`)
	if result.Receipt.Status != "rejected" || homeSnapshot(t, st).Actors["mio"].RoomID != "living" {
		t.Fatalf("route through Kuro's private room was accepted: %+v", result.Receipt)
	}
}

func TestHomeMoveCanReachAnExactSlotThroughMeasuredInRoomApproach(t *testing.T) {
	now := time.Date(2026, 11, 14, 1, 30, 0, 0, time.UTC)
	st, controller := homeTestStore(t, sampleHomeLayout(), map[string]store.ActorState{
		"kuro": homeActorAt("living", Vec3{0, 0, 0}, ""),
	}, &now)
	started := appendHome(t, st, now, "slot-move", ActionHomeMove, `{"actor_id":"kuro","room_id":"mirror","slot_id":"mirror-kuro"}`)
	if started.Receipt.Status != "committed" {
		t.Fatalf("move to configured slot: %+v", started.Receipt)
	}
	state := homeSnapshot(t, st)
	actor := state.Actors["kuro"]
	points, err := controller.MovePoints(state, "kuro")
	if err != nil || points[len(points)-1] != (Vec3{31, 0, 0}) || actor.RoomID != "living" || actor.Position == nil || Vec3(*actor.Position) != (Vec3{0, 0, 0}) {
		t.Fatalf("slot approach route=%v actor=%+v err=%v", points, actor, err)
	}
	activityID := actor.ActivityID
	now = now.Add(time.Duration(1000+1500+750+250) * time.Millisecond)
	finished := appendHome(t, st, now, "slot-arrived", ActionHomeFinish, `{"actor_id":"kuro","activity_id":"`+activityID+`"}`)
	if finished.Receipt.Status != "committed" {
		t.Fatalf("slot arrival: %+v", finished.Receipt)
	}
	actor = homeSnapshot(t, st).Actors["kuro"]
	if actor.RoomID != "mirror" || actor.SlotID != "mirror-kuro" || actor.Position == nil || Vec3(*actor.Position) != (Vec3{31, 0, 0}) {
		t.Fatalf("confirmed slot arrival = %+v", actor)
	}
}

func TestHomeExerciseAndKuroPoseRequireExactSlotAndCountCompletionOnce(t *testing.T) {
	now := time.Date(2026, 11, 14, 2, 0, 0, 0, time.UTC)
	st, _ := homeTestStore(t, sampleHomeLayout(), map[string]store.ActorState{
		"shiro": homeActorAt("exercise", Vec3{20, 0, 0}, "mat-shiro"),
		"kuro":  homeActorAt("mirror", Vec3{31, 0, 0}, "mirror-kuro"),
	}, &now)
	wrongPose := appendHome(t, st, now, "pose-mio-key", ActionHomeMirrorPose, `{"actor_id":"shiro","pose_id":"wave"}`)
	if wrongPose.Receipt.Status != "rejected" {
		t.Fatalf("unconfigured actor performed Kuro pose: %+v", wrongPose.Receipt)
	}
	wrongSlot := appendHome(t, st, now, "exercise-wrong-slot", ActionHomeExercise, `{"actor_id":"kuro"}`)
	if wrongSlot.Receipt.Status != "rejected" {
		t.Fatalf("exercise outside configured exercise slot accepted: %+v", wrongSlot.Receipt)
	}
	exercise := appendHome(t, st, now, "exercise-start", ActionHomeExercise, `{"actor_id":"shiro"}`)
	if exercise.Receipt.Status != "committed" {
		t.Fatalf("exercise start: %+v", exercise.Receipt)
	}
	exerciseID := homeSnapshot(t, st).Actors["shiro"].ActivityID
	earlyExercise := appendHome(t, st, now, "exercise-early-finish", ActionHomeFinish, `{"actor_id":"shiro","activity_id":"`+exerciseID+`"}`)
	if earlyExercise.Receipt.Status != "rejected" || homeSnapshot(t, st).Actors["shiro"].ActivityID != exerciseID {
		t.Fatalf("exercise completed before its configured duration: %+v", earlyExercise.Receipt)
	}
	now = now.Add(5 * time.Second)
	exerciseDone := appendHome(t, st, now, "exercise-finish", ActionHomeFinish, `{"actor_id":"shiro","activity_id":"`+exerciseID+`"}`)
	if exerciseDone.Receipt.Status != "committed" {
		t.Fatalf("exercise completion: %+v", exerciseDone.Receipt)
	}
	kind, exerciseEvent := readHomeEvent(t, st, exerciseDone.Receipt.LastSeq)
	if kind != EventHomeExerciseCompleted || exerciseEvent["activity_id"] != exerciseID || exerciseEvent["elapsed_ms"] != float64(5000) {
		t.Fatalf("exercise completion event = %s %v", kind, exerciseEvent)
	}

	started := appendHome(t, st, now, "pose-start", ActionHomeMirrorPose, `{"actor_id":"kuro","pose_id":"wave"}`)
	if started.Receipt.Status != "committed" {
		t.Fatalf("pose start: %+v", started.Receipt)
	}
	actor := homeSnapshot(t, st).Actors["kuro"]
	activityID := actor.ActivityID
	if actor.ActivityKind != HomeActivityMirrorPose || actor.PoseID != "wave" || activityID == "wave" {
		t.Fatalf("pose activity state = %+v; ID must identify an instance", actor)
	}
	now = now.Add(2 * time.Second)
	completed := appendHome(t, st, now, "pose-finish", ActionHomeFinish, `{"actor_id":"kuro","activity_id":"`+activityID+`"}`)
	if completed.Receipt.Status != "committed" {
		t.Fatalf("pose completion: %+v", completed.Receipt)
	}
	state := homeSnapshot(t, st)
	if state.Actors["kuro"].MirrorPoseCompletions != 1 {
		t.Fatalf("pose completion count = %d, want one", state.Actors["kuro"].MirrorPoseCompletions)
	}
	kind, event := readHomeEvent(t, st, completed.Receipt.LastSeq)
	if kind != EventHomeMirrorPoseCompleted || event["pose_id"] != "wave" || event["activity_id"] != activityID {
		t.Fatalf("pose completion event = %s %v", kind, event)
	}
	retry := appendHome(t, st, now, "pose-finish", ActionHomeFinish, `{"actor_id":"kuro","activity_id":"`+activityID+`"}`)
	if !retry.Duplicate || retry.Receipt.ID != completed.Receipt.ID || homeSnapshot(t, st).Actors["kuro"].MirrorPoseCompletions != 1 {
		t.Fatalf("idempotent finish counted twice: duplicate=%v receipt=%s count=%d", retry.Duplicate, retry.Receipt.ID, homeSnapshot(t, st).Actors["kuro"].MirrorPoseCompletions)
	}
}

func TestHomeSleepWakesOnlyExactSleepAndCannotGenericStop(t *testing.T) {
	now := time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)
	st, _ := homeTestStore(t, sampleHomeLayout(), map[string]store.ActorState{
		"kuro": homeActorAt("bedroom", Vec3{10, 0, 0}, "bed-kuro"),
	}, &now)
	started := appendHome(t, st, now, "sleep-start", ActionHomeSleep, `{"actor_id":"kuro"}`)
	if started.Receipt.Status != "committed" {
		t.Fatalf("sleep start: %+v", started.Receipt)
	}
	activityID := homeSnapshot(t, st).Actors["kuro"].ActivityID
	wrongWake := appendHome(t, st, now, "wake-stale", ActionHomeWake, `{"actor_id":"kuro","activity_id":"stale"}`)
	if wrongWake.Receipt.Status != "rejected" || homeSnapshot(t, st).Actors["kuro"].ActivityID != activityID {
		t.Fatalf("stale wake affected active sleep: %+v", wrongWake.Receipt)
	}
	stop := appendHome(t, st, now, "sleep-stop", ActionHomeStop, `{"actor_id":"kuro","activity_id":"`+activityID+`"}`)
	if stop.Receipt.Status != "rejected" || homeSnapshot(t, st).Actors["kuro"].ActivityKind != HomeActivitySleep {
		t.Fatalf("generic stop interrupted sleep: %+v", stop.Receipt)
	}
	now = now.Add(time.Minute)
	woke := appendHome(t, st, now, "wake-exact", ActionHomeWake, `{"actor_id":"kuro","activity_id":"`+activityID+`"}`)
	if woke.Receipt.Status != "committed" {
		t.Fatalf("wake: %+v", woke.Receipt)
	}
	state := homeSnapshot(t, st)
	if state.Actors["kuro"].ActivityID != "" || state.Actors["kuro"].RoomID != "bedroom" || len(state.Reservations) != 0 {
		t.Fatalf("wake state = actor %+v reservations=%v", state.Actors["kuro"], state.Reservations)
	}
	kind, event := readHomeEvent(t, st, woke.Receipt.LastSeq)
	if kind != EventHomeSleepWoke || event["activity_id"] != activityID || event["ended_at"] == nil {
		t.Fatalf("wake event = %s %v", kind, event)
	}
}

func TestHomeStrictPayloadAndLayoutChangeStopsArrivalButAllowsSafeStop(t *testing.T) {
	now := time.Date(2026, 11, 14, 4, 0, 0, 0, time.UTC)
	layout := sampleHomeLayout()
	st, _ := homeTestStore(t, layout, map[string]store.ActorState{
		"kuro": homeActorAt("living", Vec3{0, 0, 0}, ""),
	}, &now)
	unknown := appendHome(t, st, now, "unknown-field", ActionHomeMove, `{"actor_id":"kuro","room_id":"bedroom","position":[10,0,0]}`)
	if unknown.Receipt.Status != "rejected" {
		t.Fatalf("caller-chosen position accepted: %+v", unknown.Receipt)
	}
	started := appendHome(t, st, now, "move-start", ActionHomeMove, `{"actor_id":"kuro","room_id":"bedroom"}`)
	activityID := homeSnapshot(t, st).Actors["kuro"].ActivityID
	if started.Receipt.Status != "committed" || activityID == "" {
		t.Fatalf("start: %+v activity=%q", started.Receipt, activityID)
	}

	changed := layout
	changed.SceneSHA256 = strings.Repeat("b", 64)
	changed.Paths = append([]HomePath(nil), layout.Paths...)
	changed.Paths[0].Points = []Vec3{{5, 2, 1}}
	controller2, err := NewHomeController(changed, func() time.Time { return now.Add(time.Minute) })
	if err != nil {
		t.Fatalf("changed controller: %v", err)
	}
	controller2.Register(st)
	now = now.Add(time.Minute)
	finish := appendHome(t, st, now, "changed-layout-finish", ActionHomeFinish, `{"actor_id":"kuro","activity_id":"`+activityID+`"}`)
	if finish.Receipt.Status != "rejected" || homeSnapshot(t, st).Actors["kuro"].RoomID != "living" {
		t.Fatalf("changed geometry allowed stale arrival: %+v", finish.Receipt)
	}
	stop := appendHome(t, st, now, "changed-layout-stop", ActionHomeStop, `{"actor_id":"kuro","activity_id":"`+activityID+`"}`)
	if stop.Receipt.Status != "committed" || homeSnapshot(t, st).Actors["kuro"].RoomID != "living" || len(homeSnapshot(t, st).Reservations) != 0 {
		t.Fatalf("safe stop did not preserve confirmed location and release reservation: %+v", stop.Receipt)
	}
	if err := decodeHomePayload(json.RawMessage(`{"actor_id":"kuro","room_id":"bedroom","room_id":"living"}`), new(homeMoveCommand)); !errors.Is(err, ErrHomePayload) {
		t.Fatalf("duplicate payload key error = %v", err)
	}
}

func TestHouse105HasEightDistinctPhysicalRestSlots(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	layoutPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "examples", "house-105", "home-layout.json")
	layout, err := LoadHomeLayout(layoutPath)
	if err != nil {
		t.Fatalf("load checked house layout: %v", err)
	}
	controller, err := NewHomeController(layout, nil)
	if err != nil {
		t.Fatalf("initialize checked house layout: %v", err)
	}
	type expectedSeat struct {
		room       string
		anchor     Vec3
		approach   []Vec3
		durationMS int64
	}
	expected := map[string]expectedSeat{
		"sofa":                {"living", Vec3{3.05, 0.21, 0.09}, []Vec3{{3.05, 0.21, 3.27}, {3.05, 0.21, 0.86}}, 4562},
		"sofa-guest":          {"living", Vec3{4.67, 0.21, 0.09}, []Vec3{{4.67, 0.21, 3.27}, {4.67, 0.21, 0.86}}, 6004},
		"dining":              {"dining", Vec3{10.1, 0.21, 0.85}, []Vec3{{11.17, 0.21, 3.36}, {11.17, 0.21, 0.85}, {10.74, 0.21, 0.85}}, 3737},
		"dining-guest":        {"dining", Vec3{7.98, 0.21, 0.85}, []Vec3{{7.34, 0.21, 3.36}, {7.34, 0.21, 0.85}}, 5822},
		"dining-aisle-north":  {"dining", Vec3{7.98, 0.21, 2.15}, []Vec3{{7.34, 0.21, 3.36}, {7.34, 0.21, 2.15}}, 4652},
		"dining-aisle-south":  {"dining", Vec3{7.98, 0.21, -0.48}, []Vec3{{7.34, 0.21, 3.36}, {7.34, 0.21, -0.48}}, 7020},
		"dining-window-north": {"dining", Vec3{10.1, 0.21, 2.15}, []Vec3{{11.17, 0.21, 3.36}, {11.17, 0.21, 2.15}, {10.74, 0.21, 2.15}}, 2568},
		"dining-window-south": {"dining", Vec3{10.1, 0.21, -0.48}, []Vec3{{11.17, 0.21, 3.36}, {11.17, 0.21, -0.48}, {10.74, 0.21, -0.48}}, 4930},
	}
	got := make(map[string]HomeSlot)
	for _, slot := range layout.Slots {
		if slot.Kind != HomeSlotRest {
			continue
		}
		want, ok := expected[slot.ID]
		if !ok {
			t.Fatalf("unexpected rest slot %q; only sofa and dining-chair slots count", slot.ID)
		}
		if slot.RoomID != want.room || slot.ActorID != "" || slot.Anchor != want.anchor || slot.ApproachDurationMS != want.durationMS {
			t.Fatalf("rest slot %q = %+v, want room=%s actor-unbound anchor=%v duration=%dms", slot.ID, slot, want.room, want.anchor, want.durationMS)
		}
		if len(slot.ApproachPoints) != len(want.approach) {
			t.Fatalf("rest slot %q approach points=%v, want %v", slot.ID, slot.ApproachPoints, want.approach)
		}
		for i, point := range want.approach {
			if slot.ApproachPoints[i] != point {
				t.Fatalf("rest slot %q approach point %d=%v, want %v", slot.ID, i, slot.ApproachPoints[i], point)
			}
		}
		from := [3]float64(layout.RoomAnchors[slot.RoomID])
		route, err := controller.resolveRoute("mio", store.ActorState{RoomID: slot.RoomID, Position: &from}, slot.RoomID, slot.ID)
		if err != nil || len(route.points) < 2 || route.points[len(route.points)-1] != slot.Anchor || route.durationMS != slot.ApproachDurationMS {
			t.Fatalf("rest slot %q route=%+v err=%v, want exact anchor and measured in-room approach", slot.ID, route, err)
		}
		got[slot.ID] = slot
	}
	if len(got) != len(expected) {
		t.Fatalf("physical rest slots=%d, want exactly %d sofa/chair slots", len(got), len(expected))
	}
	ids := make([]string, 0, len(expected))
	for id := range expected {
		if _, ok := got[id]; !ok {
			t.Errorf("missing physical rest slot %q", id)
		}
		ids = append(ids, id)
	}
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			a, b := got[ids[i]].Anchor, got[ids[j]].Anchor
			dx, dz := a[0]-b[0], a[2]-b[2]
			if dx*dx+dz*dz < 1.29*1.29 {
				t.Errorf("rest seats %q and %q are too close: anchors=%v,%v", ids[i], ids[j], a, b)
			}
		}
	}
}
