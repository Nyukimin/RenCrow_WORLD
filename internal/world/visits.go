package world

import (
	"errors"
	"fmt"
	"time"
)

func (s *WorldState) Advance(requestWorldID WorldID, now time.Time) (*WorldState, error) {
	if err := s.requireWorld(requestWorldID); err != nil {
		return nil, err
	}
	clock, err := s.clock.Advance(now)
	if err != nil {
		return nil, err
	}
	return s.afterObservation(clock), nil
}

func (s *WorldState) AdmitVisit(requestWorldID WorldID, visit Visit, now time.Time) (*WorldState, error) {
	if err := s.requireWorld(requestWorldID); err != nil {
		return nil, err
	}
	visit, err := s.validateVisit(visit)
	if err != nil {
		return nil, err
	}
	clock, err := s.clock.Advance(now)
	if err != nil {
		return nil, err
	}
	observedUTC := clock.LastObservedUTC()
	if observedUTC.Before(visit.ValidFromUTC) || !observedUTC.Before(visit.ValidUntilUTC) {
		return nil, errors.New("visit is outside its admission interval")
	}
	next := s.afterObservation(clock)
	if next.activeVisit != nil {
		return nil, errors.New("Lumina already has an active visit")
	}
	next.actors[ActorLumina] = struct{}{}
	next.activeVisit = &visit
	return next, nil
}

func (s *WorldState) DepartVisit(requestWorldID WorldID, visit Visit, now time.Time) (*WorldState, error) {
	if err := s.requireWorld(requestWorldID); err != nil {
		return nil, err
	}
	visit, err := s.validateVisit(visit)
	if err != nil {
		return nil, err
	}
	clock, err := s.clock.Advance(now)
	if err != nil {
		return nil, err
	}
	if clock.LastObservedUTC().Before(visit.ValidFromUTC) {
		return nil, errors.New("departure precedes visit admission")
	}
	next := s.afterObservation(clock)
	if next.activeVisit == nil || !sameVisit(*next.activeVisit, visit) {
		return nil, errors.New("matching active visit is not present")
	}
	if _, exists := next.actors[ActorLumina]; !exists {
		return nil, errors.New("Lumina is not present")
	}
	delete(next.actors, ActorLumina)
	next.activeVisit = nil
	return next, nil
}

func (s *WorldState) validateVisit(visit Visit) (Visit, error) {
	if visit.WorldID != s.worldID {
		return Visit{}, fmt.Errorf("visit WorldID %q does not match world state", visit.WorldID)
	}
	if err := validateActorID(visit.ActorID); err != nil || visit.ActorID != ActorLumina {
		return Visit{}, errors.New("only Lumina can be admitted as a visitor")
	}
	visit.ValidFromUTC = visit.ValidFromUTC.Round(0).UTC()
	visit.ValidUntilUTC = visit.ValidUntilUTC.Round(0).UTC()
	if !visit.ValidUntilUTC.After(visit.ValidFromUTC) {
		return Visit{}, errors.New("visit end must be after visit start")
	}
	return visit, nil
}

func (s *WorldState) afterObservation(clock ClockState) *WorldState {
	next := &WorldState{
		worldID: s.worldID,
		bundle:  cloneBundle(s.bundle),
		actors:  cloneActors(s.actors),
		clock:   clock,
	}
	if s.activeVisit != nil {
		visit := *s.activeVisit
		next.activeVisit = &visit
		if !clock.LastObservedUTC().Before(visit.ValidUntilUTC) {
			delete(next.actors, ActorLumina)
			next.activeVisit = nil
		}
	}
	return next
}

func sameVisit(a, b Visit) bool {
	return a.WorldID == b.WorldID && a.ActorID == b.ActorID &&
		a.ValidFromUTC.Equal(b.ValidFromUTC) && a.ValidUntilUTC.Equal(b.ValidUntilUTC)
}
