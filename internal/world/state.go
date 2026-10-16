package world

import (
	"errors"
	"fmt"
)

func Bootstrap(worldID WorldID, bundle SpatialBundle, clock ClockInput) (*WorldState, error) {
	if err := validateOpaqueID(string(worldID)); err != nil {
		return nil, fmt.Errorf("invalid WorldID: %w", err)
	}
	if err := validateBundle(worldID, bundle); err != nil {
		return nil, err
	}
	clockState, err := newClockState(clock)
	if err != nil {
		return nil, err
	}
	actors := make(map[ActorID]struct{}, len(residents))
	for _, actor := range residents {
		actors[actor] = struct{}{}
	}
	return &WorldState{
		worldID: worldID,
		bundle:  cloneBundle(bundle),
		actors:  actors,
		clock:   clockState,
	}, nil
}

func (s *WorldState) Actors(requestWorldID WorldID) (map[ActorID]struct{}, error) {
	if err := s.requireWorld(requestWorldID); err != nil {
		return nil, err
	}
	return cloneActors(s.actors), nil
}

func (s *WorldState) HasActor(requestWorldID WorldID, actorID ActorID) (bool, error) {
	if err := s.requireWorld(requestWorldID); err != nil {
		return false, err
	}
	if err := validateActorID(actorID); err != nil {
		return false, err
	}
	_, exists := s.actors[actorID]
	return exists, nil
}

func (s *WorldState) SpatialBundle(requestWorldID WorldID) (SpatialBundle, error) {
	if err := s.requireWorld(requestWorldID); err != nil {
		return SpatialBundle{}, err
	}
	return cloneBundle(s.bundle), nil
}

func (s *WorldState) Readiness(requestWorldID WorldID) (Readiness, error) {
	if err := s.requireWorld(requestWorldID); err != nil {
		return Readiness{}, err
	}
	return Readiness{SpatialReady: false}, nil
}

func (s *WorldState) Clock(requestWorldID WorldID) (ClockState, error) {
	if err := s.requireWorld(requestWorldID); err != nil {
		return ClockState{}, err
	}
	return s.clock, nil
}

func (s *WorldState) Revision(requestWorldID WorldID) (uint64, error) {
	if err := s.requireWorld(requestWorldID); err != nil {
		return 0, err
	}
	return 0, nil
}

func (s *WorldState) requireWorld(requestWorldID WorldID) error {
	if s == nil {
		return errors.New("world state is nil")
	}
	if err := validateOpaqueID(string(s.worldID)); err != nil {
		return errors.New("world state is uninitialized")
	}
	if requestWorldID != s.worldID {
		return errors.New("request WorldID does not match world state")
	}
	return nil
}

func cloneActors(actors map[ActorID]struct{}) map[ActorID]struct{} {
	copy := make(map[ActorID]struct{}, len(actors))
	for actor := range actors {
		copy[actor] = struct{}{}
	}
	return copy
}

func cloneBundle(bundle SpatialBundle) SpatialBundle {
	copy := bundle
	copy.Rooms = append([]Room(nil), bundle.Rooms...)
	copy.Edges = append([]Edge(nil), bundle.Edges...)
	copy.Anchors = append([]Anchor(nil), bundle.Anchors...)
	for i := range copy.Anchors {
		if bundle.Anchors[i].Position != nil {
			position := *bundle.Anchors[i].Position
			copy.Anchors[i].Position = &position
		}
	}
	return copy
}
