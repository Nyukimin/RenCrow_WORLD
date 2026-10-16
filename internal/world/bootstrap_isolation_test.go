package world

import (
	"testing"
	"time"
)

func TestBootstrapCreatesIndependentActorMaps(t *testing.T) {
	firstID := WorldID("World-A")
	secondID := WorldID("World-B")
	clock := ClockInput{OriginUTC: time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC), Location: time.UTC, Scale: 1}
	first, err := Bootstrap(firstID, SpatialBundle{Version: SpatialBundleV1, WorldID: firstID}, clock)
	if err != nil {
		t.Fatalf("Bootstrap(first) error = %v", err)
	}
	second, err := Bootstrap(secondID, SpatialBundle{Version: SpatialBundleV1, WorldID: secondID}, clock)
	if err != nil {
		t.Fatalf("Bootstrap(second) error = %v", err)
	}

	delete(first.actors, ActorMio)
	first.actors[ActorLumina] = struct{}{}

	if len(second.actors) != len(residents) {
		t.Fatalf("second world actor count = %d, want %d", len(second.actors), len(residents))
	}
	if _, exists := second.actors[ActorLumina]; exists {
		t.Fatal("mutating the first world added Lumina to the second world")
	}
	for _, resident := range residents {
		if _, exists := second.actors[resident]; !exists {
			t.Errorf("resident %q missing from second world", resident)
		}
	}
}

func TestZeroWorldStateRejectsEmptyWorldQuery(t *testing.T) {
	var state WorldState
	if _, err := state.Readiness(""); err == nil {
		t.Fatal("zero WorldState accepted an empty WorldID query")
	}
}

func TestZeroClockStateRemainsInvalid(t *testing.T) {
	if _, err := (ClockState{}).Advance(time.Time{}); err == nil {
		t.Fatal("zero ClockState accepted an advance")
	}
}

func TestBootstrapDetachesClockLocationFromCaller(t *testing.T) {
	id := WorldID("World-Location-Input")
	location := time.FixedZone("Original/Zone", 3600)
	clock := ClockInput{OriginUTC: time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC), Location: location, Scale: 1}
	state, err := Bootstrap(id, SpatialBundle{Version: SpatialBundleV1, WorldID: id}, clock)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	*location = *time.FixedZone("Caller/Mutation", -2*60*60)
	stateClock, err := state.Clock(id)
	if err != nil {
		t.Fatalf("Clock() error = %v", err)
	}
	if got := stateClock.Local().Location().String(); got != "Original/Zone" {
		t.Fatalf("state location changed with caller location: got %q", got)
	}
}

func TestClockLocalLocationMutationDoesNotChangeWorldState(t *testing.T) {
	id := WorldID("World-Location-View")
	location := time.FixedZone("Original/Zone", 3600)
	clock := ClockInput{OriginUTC: time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC), Location: location, Scale: 1}
	state, err := Bootstrap(id, SpatialBundle{Version: SpatialBundleV1, WorldID: id}, clock)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	stateClock, err := state.Clock(id)
	if err != nil {
		t.Fatalf("Clock() error = %v", err)
	}

	*stateClock.Local().Location() = *time.FixedZone("Accessor/Mutation", -2*60*60)
	stateClock, err = state.Clock(id)
	if err != nil {
		t.Fatalf("Clock() after location mutation error = %v", err)
	}
	if got := stateClock.Local().Location().String(); got != "Original/Zone" {
		t.Fatalf("state location changed through Clock().Local().Location(): got %q", got)
	}
}
