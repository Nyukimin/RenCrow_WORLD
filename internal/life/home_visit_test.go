package life

import (
	"fmt"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

func TestHomeVisitAdmissionExpiryAndReadmission(t *testing.T) {
	now := time.Date(2026, 11, 15, 6, 0, 0, 0, time.UTC)
	layout := sampleHomeLayout()
	layout.RoomAnchors["dining"] = Vec3{100, 0, 0}
	layout.Paths = append(layout.Paths, HomePath{
		ID: "dining-lounge", FromRoomID: "dining", ToRoomID: "lounge",
		Points: []Vec3{{50, 0, 0}}, DurationMS: 1000,
	})
	st, _ := homeTestStore(t, layout, map[string]store.ActorState{
		"kuro": homeActorAt("living", Vec3{0, 0, 0}, ""),
	}, &now)
	commit := func(key, action, payload string) {
		t.Helper()
		result := appendHome(t, st, now, key, action, payload)
		if result.Receipt.Status != "committed" {
			t.Fatalf("%s: %+v", key, result.Receipt)
		}
	}
	admit := func(visit string) {
		t.Helper()
		commit(visit, ActionHomeVisitAdmit, fmt.Sprintf(`{"actor_id":"lumina","visit_id":%q,"valid_until":%q}`,
			visit, formatHomeTime(now.Add(2*time.Minute))))
		actor := homeSnapshot(t, st).Actors[HomeVisitorID]
		if actor.VisitID != visit || actor.RoomID != "dining" {
			t.Fatalf("visit was not admitted at the arrival anchor: %+v", actor)
		}
	}
	admit("visit-one")
	move := `{"actor_id":"lumina","room_id":"lounge","slot_id":"sofa"}`
	commit("visit-first-move", ActionHomeMove, move)
	first := homeSnapshot(t, st).Actors[HomeVisitorID].ActivityID
	now = now.Add(2 * time.Minute)
	commit("visit-first-expiry", ActionHomeVisitExpire, `{"actor_id":"lumina","visit_id":"visit-one"}`)
	state := homeSnapshot(t, st)
	if _, present := state.Actors[HomeVisitorID]; present || len(state.Reservations) != 0 || state.Activities[first].Status != HomeActivityStatusStopped {
		t.Fatal("expiration left the visitor, a reservation, or a running activity")
	}
	admit("visit-two")
	commit("visit-second-move", ActionHomeMove, move)
	if second := homeSnapshot(t, st).Actors[HomeVisitorID].ActivityID; second == first {
		t.Fatal("readmission reused the earlier activity ID")
	}
}
