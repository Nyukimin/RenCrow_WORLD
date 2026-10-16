package world_test

import (
	"math"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/world"
)

func testTime(hour int) time.Time {
	return time.Date(2026, time.January, 2, hour, 0, 0, 0, time.FixedZone("input", 3*60*60))
}

func testBundle(id world.WorldID) world.SpatialBundle {
	return world.SpatialBundle{Version: world.SpatialBundleV1, WorldID: id}
}

func testClock(origin time.Time, scale float64) world.ClockInput {
	return world.ClockInput{OriginUTC: origin, Location: time.FixedZone("Test/Zone", -5*60*60), Scale: scale}
}

func bootstrap(t *testing.T, id world.WorldID, bundle world.SpatialBundle, origin time.Time, scale float64) *world.WorldState {
	t.Helper()
	state, err := world.Bootstrap(id, bundle, testClock(origin, scale))
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	return state
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBootstrapHasExactlyEightResidentsAndNoVisitor(t *testing.T) {
	id := world.WorldID("House-A")
	state := bootstrap(t, id, testBundle(id), testTime(0), 1)

	actors, err := state.Actors(id)
	requireNoError(t, err)
	want := map[world.ActorID]struct{}{
		world.ActorMio: {}, world.ActorShiro: {}, world.ActorKuro: {}, world.ActorMidori: {},
		world.ActorAka: {}, world.ActorAo: {}, world.ActorKin: {}, world.ActorGin: {},
	}
	if len(actors) != len(want) {
		t.Fatalf("actor count = %d, want %d", len(actors), len(want))
	}
	for actor := range want {
		if _, ok := actors[actor]; !ok {
			t.Errorf("resident %q missing", actor)
		}
	}
	if _, ok := actors[world.ActorLumina]; ok {
		t.Fatal("Lumina present at bootstrap")
	}
}

func TestActorAndWorldIDsAreValidatedExactly(t *testing.T) {
	id := world.WorldID("Room-A")
	state := bootstrap(t, id, testBundle(id), testTime(0), 1)

	if _, err := state.HasActor(id, world.ActorID("unknown")); err == nil {
		t.Fatal("unknown actor was accepted")
	}
	for _, wrong := range []world.WorldID{"room-a", " Room-A", "Room-A "} {
		if _, err := state.HasActor(wrong, world.ActorMio); err == nil {
			t.Errorf("world ID %q was accepted", wrong)
		}
	}
	if _, err := world.Bootstrap("Room-A", testBundle("room-a"), testClock(testTime(0), 1)); err == nil {
		t.Fatal("bundle with differently cased WorldID was accepted")
	}
}

func TestBootstrapRejectsInvalidWorldIDs(t *testing.T) {
	for _, id := range []world.WorldID{"", " leading", "trailing ", "bad\nvalue", world.WorldID(string([]byte{0xff}))} {
		t.Run(string(id), func(t *testing.T) {
			if _, err := world.Bootstrap(id, testBundle(id), testClock(testTime(0), 1)); err == nil {
				t.Fatalf("WorldID %q was accepted", id)
			}
		})
	}
}

func TestBootstrapRejectsInvalidBundles(t *testing.T) {
	id := world.WorldID("World")
	tests := map[string]world.SpatialBundle{
		"wrong version":         {Version: world.SpatialBundleV1 + 1, WorldID: id},
		"empty room ID":         {Version: world.SpatialBundleV1, WorldID: id, Rooms: []world.Room{{ID: ""}}},
		"duplicate room ID":     {Version: world.SpatialBundleV1, WorldID: id, Rooms: []world.Room{{ID: "a"}, {ID: "a"}}},
		"duplicate edge ID":     {Version: world.SpatialBundleV1, WorldID: id, Rooms: []world.Room{{ID: "a"}}, Edges: []world.Edge{{ID: "e", FromRoomID: "a", ToRoomID: "a"}, {ID: "e", FromRoomID: "a", ToRoomID: "a"}}},
		"duplicate anchor ID":   {Version: world.SpatialBundleV1, WorldID: id, Rooms: []world.Room{{ID: "r"}}, Anchors: []world.Anchor{{ID: "a", RoomID: "r", MeasurementStatus: world.MeasurementUnverified}, {ID: "a", RoomID: "r", MeasurementStatus: world.MeasurementUnverified}}},
		"dangling edge":         {Version: world.SpatialBundleV1, WorldID: id, Edges: []world.Edge{{ID: "e", FromRoomID: "missing", ToRoomID: "also-missing"}}},
		"dangling anchor":       {Version: world.SpatialBundleV1, WorldID: id, Anchors: []world.Anchor{{ID: "a", RoomID: "missing", MeasurementStatus: world.MeasurementUnverified}}},
		"unknown measurement":   {Version: world.SpatialBundleV1, WorldID: id, Rooms: []world.Room{{ID: "r"}}, Anchors: []world.Anchor{{ID: "a", RoomID: "r", MeasurementStatus: "guessed"}}},
		"unverified position":   {Version: world.SpatialBundleV1, WorldID: id, Rooms: []world.Room{{ID: "r"}}, Anchors: []world.Anchor{{ID: "a", RoomID: "r", Position: &world.Position{}, MeasurementStatus: world.MeasurementUnverified}}},
		"measured nil position": {Version: world.SpatialBundleV1, WorldID: id, Rooms: []world.Room{{ID: "r"}}, Anchors: []world.Anchor{{ID: "a", RoomID: "r", MeasurementStatus: world.MeasurementMeasured}}},
		"nonfinite position":    {Version: world.SpatialBundleV1, WorldID: id, Rooms: []world.Room{{ID: "r"}}, Anchors: []world.Anchor{{ID: "a", RoomID: "r", Position: &world.Position{X: math.NaN()}, MeasurementStatus: world.MeasurementMeasured}}},
	}
	wantErrors := map[string]string{
		"duplicate edge ID":     "duplicate edge ID",
		"duplicate anchor ID":   "duplicate anchor ID",
		"unknown measurement":   "unknown anchor measurement status",
		"unverified position":   "unverified anchor must not have a position",
		"measured nil position": "measured anchor requires a position",
		"nonfinite position":    "measured anchor position must be finite",
	}
	for name, bundle := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := world.Bootstrap(id, bundle, testClock(testTime(0), 1)); err == nil {
				t.Fatal("invalid bundle was accepted")
			} else if want, ok := wantErrors[name]; ok && err.Error() != want {
				t.Fatalf("Bootstrap() error = %q, want %q", err, want)
			}
		})
	}
}

func TestUnverifiedNilPositionIsPreservedAndNeverSpatialReady(t *testing.T) {
	id := world.WorldID("World")
	bundle := testBundle(id)
	bundle.Rooms = []world.Room{{ID: "room"}}
	bundle.Anchors = []world.Anchor{{ID: "anchor", RoomID: "room", MeasurementStatus: world.MeasurementUnverified}}
	state := bootstrap(t, id, bundle, testTime(0), 1)

	copy, err := state.SpatialBundle(id)
	requireNoError(t, err)
	if copy.Anchors[0].Position != nil {
		t.Fatalf("nil position became %#v", copy.Anchors[0].Position)
	}
	readiness, err := state.Readiness(id)
	requireNoError(t, err)
	if readiness.SpatialReady {
		t.Fatal("logical bundle established spatial readiness")
	}
}

func TestStateAndReturnedCollectionsDoNotAlias(t *testing.T) {
	id := world.WorldID("World")
	bundle := testBundle(id)
	bundle.Rooms = []world.Room{{ID: "original"}}
	state := bootstrap(t, id, bundle, testTime(0), 1)
	bundle.Rooms[0].ID = "caller-change"

	actors, err := state.Actors(id)
	requireNoError(t, err)
	delete(actors, world.ActorMio)
	actors[world.ActorLumina] = struct{}{}
	current, err := state.Actors(id)
	requireNoError(t, err)
	if len(current) != 8 {
		t.Fatalf("state actor count changed through returned map: %d", len(current))
	}
	if _, ok := current[world.ActorMio]; !ok {
		t.Fatal("returned map mutation removed resident")
	}
	if _, ok := current[world.ActorLumina]; ok {
		t.Fatal("returned map mutation added visitor")
	}

	spatial, err := state.SpatialBundle(id)
	requireNoError(t, err)
	if spatial.Rooms[0].ID != "original" {
		t.Fatalf("caller slice mutation changed state: %q", spatial.Rooms[0].ID)
	}
	spatial.Rooms[0].ID = "returned-change"
	spatialAgain, err := state.SpatialBundle(id)
	requireNoError(t, err)
	if spatialAgain.Rooms[0].ID != "original" {
		t.Fatalf("returned bundle mutation changed state: %q", spatialAgain.Rooms[0].ID)
	}

	next, err := state.Advance(id, testTime(1))
	requireNoError(t, err)
	oldRevision, err := state.Revision(id)
	requireNoError(t, err)
	newRevision, err := next.Revision(id)
	requireNoError(t, err)
	if oldRevision != 0 || newRevision != 0 {
		t.Fatalf("revision changed: old=%d new=%d", oldRevision, newRevision)
	}
	oldActors, err := state.Actors(id)
	requireNoError(t, err)
	newActors, err := next.Actors(id)
	requireNoError(t, err)
	if len(oldActors) != len(newActors) || len(oldActors) != 8 {
		t.Fatalf("Advance changed actor set: old=%d new=%d", len(oldActors), len(newActors))
	}
}

func TestClockRejectsInvalidInputsRollbackAndDurationOverflow(t *testing.T) {
	id := world.WorldID("World")
	origin := testTime(0)
	invalidClocks := []world.ClockInput{
		{},
		{OriginUTC: origin, Location: nil, Scale: 1},
		{OriginUTC: origin, Location: time.Local, Scale: 1},
		{OriginUTC: origin, Location: time.FixedZone("Local", 0), Scale: 1},
		{OriginUTC: origin, Location: time.UTC, Scale: 0},
		{OriginUTC: origin, Location: time.UTC, Scale: math.NaN()},
		{OriginUTC: origin, Location: time.UTC, Scale: math.Inf(1)},
	}
	for i, input := range invalidClocks {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			if _, err := world.Bootstrap(id, testBundle(id), input); err == nil {
				t.Fatal("invalid clock was accepted")
			}
		})
	}

	state := bootstrap(t, id, testBundle(id), origin, 1)
	if _, err := state.Advance(id, origin.Add(-time.Second)); err == nil {
		t.Fatal("clock rollback was accepted")
	}
	if _, err := state.Advance(id, time.Time{}); err == nil {
		t.Fatal("clock rollback to year 0001 was accepted")
	}
	tooFar := origin.Add(time.Duration(math.MaxInt64)).Add(time.Nanosecond)
	if _, err := state.Advance(id, tooFar); err == nil {
		t.Fatal("unrepresentable elapsed duration was accepted")
	}
	fastState := bootstrap(t, id, testBundle(id), origin, 2)
	if _, err := fastState.Advance(id, origin.Add(time.Duration(math.MaxInt64))); err == nil {
		t.Fatal("unrepresentable scaled duration was accepted")
	}
}

func TestYearOneIsValidClockOriginAndTransitionsAreImmutable(t *testing.T) {
	id := world.WorldID("Year-One")
	origin := time.Time{}
	state, err := world.Bootstrap(id, testBundle(id), testClock(origin, 1))
	requireNoError(t, err)

	initialClock, err := state.Clock(id)
	requireNoError(t, err)
	if !initialClock.UTC().Equal(origin) || !initialClock.LastObservedUTC().Equal(origin) {
		t.Fatalf("initial clock = (%s, %s), want year 0001 origin", initialClock.UTC(), initialClock.LastObservedUTC())
	}

	sameTime, err := state.Advance(id, origin)
	requireNoError(t, err)
	forwardAt := origin.Add(time.Second)
	forward, err := sameTime.Advance(id, forwardAt)
	requireNoError(t, err)
	if _, err := forward.Advance(id, origin); err == nil {
		t.Fatal("clock rollback was accepted")
	}

	assertClock := func(state *world.WorldState, wantWorld, wantObserved time.Time) {
		t.Helper()
		clock, err := state.Clock(id)
		requireNoError(t, err)
		if !clock.UTC().Equal(wantWorld) || !clock.LastObservedUTC().Equal(wantObserved) {
			t.Fatalf("clock = (%s, %s), want (%s, %s)", clock.UTC(), clock.LastObservedUTC(), wantWorld, wantObserved)
		}
	}
	assertClock(state, origin, origin)
	assertClock(sameTime, origin, origin)
	assertClock(forward, forwardAt, forwardAt)
}

func TestClockScaleUsesFixedOriginAcrossTickPartitions(t *testing.T) {
	id := world.WorldID("World")
	origin := testTime(0)
	first := bootstrap(t, id, testBundle(id), origin, 1.75)
	second := bootstrap(t, id, testBundle(id), origin, 1.75)
	first, err := first.Advance(id, origin.Add(2*time.Second))
	requireNoError(t, err)
	first, err = first.Advance(id, origin.Add(5*time.Second))
	requireNoError(t, err)
	second, err = second.Advance(id, origin.Add(5*time.Second))
	requireNoError(t, err)
	firstClock, err := first.Clock(id)
	requireNoError(t, err)
	secondClock, err := second.Clock(id)
	requireNoError(t, err)
	if !firstClock.UTC().Equal(secondClock.UTC()) {
		t.Fatalf("scaled time depends on tick partition: %s != %s", firstClock.UTC(), secondClock.UTC())
	}
	if !firstClock.LastObservedUTC().Equal(origin.Add(5 * time.Second).UTC()) {
		t.Fatalf("last observed UTC = %s", firstClock.LastObservedUTC())
	}
	if !firstClock.Local().Equal(firstClock.UTC()) || firstClock.Local().Location().String() != "Test/Zone" {
		t.Fatalf("local clock view did not use supplied location: %s", firstClock.Local())
	}
	firstRevision, err := first.Revision(id)
	requireNoError(t, err)
	secondRevision, err := second.Revision(id)
	requireNoError(t, err)
	if firstRevision != 0 || secondRevision != 0 {
		t.Fatal("time advancement committed a revision")
	}
}

func TestVisitorAdmissionDepartureAndIntervalBoundaries(t *testing.T) {
	id := world.WorldID("World")
	from := testTime(1)
	until := from.Add(time.Hour)
	visit := world.Visit{WorldID: id, ActorID: world.ActorLumina, ValidFromUTC: from, ValidUntilUTC: until}
	state := bootstrap(t, id, testBundle(id), testTime(0), 1)

	if _, err := state.AdmitVisit(id, visit, from.Add(-time.Nanosecond)); err == nil {
		t.Fatal("admission before interval was accepted")
	}
	if _, err := state.AdmitVisit(id, visit, until); err == nil {
		t.Fatal("admission at exclusive interval end was accepted")
	}
	if _, err := state.AdmitVisit(id, world.Visit{WorldID: id, ActorID: world.ActorLumina, ValidFromUTC: from, ValidUntilUTC: from}, from); err == nil {
		t.Fatal("empty interval was accepted")
	}

	active, err := state.AdmitVisit(id, visit, from)
	requireNoError(t, err)
	present, err := active.HasActor(id, world.ActorLumina)
	requireNoError(t, err)
	if !present {
		t.Fatal("Lumina absent after admission at interval start")
	}
	if _, err := active.AdmitVisit(id, visit, from.Add(time.Second)); err == nil {
		t.Fatal("duplicate active admission was accepted")
	}
	if _, err := active.DepartVisit(id, world.Visit{WorldID: id, ActorID: world.ActorLumina, ValidFromUTC: from, ValidUntilUTC: until.Add(time.Second)}, from); err == nil {
		t.Fatal("departure with a different visit was accepted")
	}
	if _, err := active.DepartVisit("other-world", visit, from.Add(time.Minute)); err == nil {
		t.Fatal("departure for another world was accepted")
	}

	departed, err := active.DepartVisit(id, visit, from.Add(time.Minute))
	requireNoError(t, err)
	present, err = departed.HasActor(id, world.ActorLumina)
	requireNoError(t, err)
	if present {
		t.Fatal("Lumina remained present after departure")
	}
	oldPresent, err := active.HasActor(id, world.ActorLumina)
	requireNoError(t, err)
	if !oldPresent {
		t.Fatal("departure mutated prior state")
	}
	revision, err := departed.Revision(id)
	requireNoError(t, err)
	if revision != 0 {
		t.Fatal("departure committed a revision")
	}
}

func TestVisitorExpiresAtRealUTCBoundaryAndResidentsRemain(t *testing.T) {
	id := world.WorldID("World")
	from := testTime(1)
	until := from.Add(time.Hour)
	visit := world.Visit{WorldID: id, ActorID: world.ActorLumina, ValidFromUTC: from, ValidUntilUTC: until}
	state := bootstrap(t, id, testBundle(id), from, 0.5)
	active, err := state.AdmitVisit(id, visit, from)
	requireNoError(t, err)
	expired, err := active.Advance(id, until)
	requireNoError(t, err)
	present, err := expired.HasActor(id, world.ActorLumina)
	requireNoError(t, err)
	if present {
		t.Fatal("Lumina remained present at ValidUntilUTC")
	}
	actors, err := expired.Actors(id)
	requireNoError(t, err)
	if len(actors) != 8 {
		t.Fatalf("residents changed after visitor expiry: %d", len(actors))
	}
	clock, err := expired.Clock(id)
	requireNoError(t, err)
	if !clock.LastObservedUTC().Equal(until.UTC()) {
		t.Fatalf("expiry transition did not observe real UTC: %s", clock.LastObservedUTC())
	}
	if !clock.UTC().Before(until.UTC()) {
		t.Fatalf("test expected scaled display clock before expiry, got %s", clock.UTC())
	}
	activeRevision, err := active.Revision(id)
	requireNoError(t, err)
	expiredRevision, err := expired.Revision(id)
	requireNoError(t, err)
	if activeRevision != 0 || expiredRevision != 0 {
		t.Fatal("expiry committed a revision")
	}
}

func TestVisitCanStartAtYearOneWithHalfSpeedClock(t *testing.T) {
	id := world.WorldID("Year-One-Visit")
	from := time.Time{}
	until := from.Add(2 * time.Second)
	visit := world.Visit{WorldID: id, ActorID: world.ActorLumina, ValidFromUTC: from, ValidUntilUTC: until}
	state := bootstrap(t, id, testBundle(id), from, 0.5)

	emptyVisit := visit
	emptyVisit.ValidUntilUTC = from
	if _, err := state.AdmitVisit(id, emptyVisit, from); err == nil {
		t.Fatal("empty visit interval at year 0001 was accepted")
	}

	active, err := state.AdmitVisit(id, visit, from)
	requireNoError(t, err)
	present, err := active.HasActor(id, world.ActorLumina)
	requireNoError(t, err)
	if !present {
		t.Fatal("Lumina absent after admission at year 0001")
	}

	departed, err := active.DepartVisit(id, visit, from.Add(time.Second))
	requireNoError(t, err)
	present, err = departed.HasActor(id, world.ActorLumina)
	requireNoError(t, err)
	if present {
		t.Fatal("Lumina remained present after departure")
	}
	departureClock, err := departed.Clock(id)
	requireNoError(t, err)
	if !departureClock.LastObservedUTC().Equal(from.Add(time.Second)) || !departureClock.UTC().Equal(from.Add(500*time.Millisecond)) {
		t.Fatalf("departure clock = (%s, %s), want real UTC +1s and world UTC +500ms", departureClock.LastObservedUTC(), departureClock.UTC())
	}

	expired, err := active.Advance(id, until)
	requireNoError(t, err)
	present, err = expired.HasActor(id, world.ActorLumina)
	requireNoError(t, err)
	if present {
		t.Fatal("Lumina remained present at exclusive visit end")
	}
	expiryClock, err := expired.Clock(id)
	requireNoError(t, err)
	if !expiryClock.LastObservedUTC().Equal(until) || !expiryClock.UTC().Equal(from.Add(time.Second)) {
		t.Fatalf("expiry clock = (%s, %s), want real UTC +2s and world UTC +1s", expiryClock.LastObservedUTC(), expiryClock.UTC())
	}

	present, err = state.HasActor(id, world.ActorLumina)
	requireNoError(t, err)
	if present {
		t.Fatal("admission mutated the original state")
	}
	present, err = active.HasActor(id, world.ActorLumina)
	requireNoError(t, err)
	if !present {
		t.Fatal("departure or expiry mutated the active state")
	}
}

func TestVisitRequiresLuminaAndMatchingWorldIDs(t *testing.T) {
	id := world.WorldID("World")
	from := testTime(1)
	visit := world.Visit{WorldID: id, ActorID: world.ActorMio, ValidFromUTC: from, ValidUntilUTC: from.Add(time.Hour)}
	state := bootstrap(t, id, testBundle(id), testTime(0), 1)
	if _, err := state.AdmitVisit(id, visit, from); err == nil {
		t.Fatal("resident was admitted as a visitor")
	}
	visit.ActorID = world.ActorLumina
	if _, err := state.AdmitVisit("other-world", visit, from); err == nil {
		t.Fatal("admission request with wrong world was accepted")
	}
	visit.WorldID = "other-world"
	if _, err := state.AdmitVisit(id, visit, from); err == nil {
		t.Fatal("visit with wrong world was accepted")
	}
}
