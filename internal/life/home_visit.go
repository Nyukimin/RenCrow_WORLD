package life

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

const (
	HomeVisitorID         = "lumina"
	ActionHomeVisitAdmit  = "life.home.visit.admit"
	ActionHomeVisitDepart = "life.home.visit.depart"
	ActionHomeVisitExpire = "internal.world.home_visit_expire"
)

var errHomeVisit = errors.New("home visit does not match the current admission")

type homeVisitCommand struct {
	ActorID    string `json:"actor_id"`
	VisitID    string `json:"visit_id"`
	ValidUntil string `json:"valid_until,omitempty"`
}

func (c *HomeController) handleVisitAdmit(state store.State, raw json.RawMessage) (store.Outcome, error) {
	var cmd homeVisitCommand
	if err := decodeHomePayload(raw, &cmd); err != nil || cmd.ActorID != HomeVisitorID || !validHomeID(cmd.VisitID) {
		return store.Outcome{}, ErrHomePayload
	}
	now := c.now().UTC()
	until, err := parseHomeTime(cmd.ValidUntil)
	if err != nil || !until.After(now) || until.After(now.Add(time.Hour)) {
		return store.Outcome{}, ErrHomePayload
	}
	if _, exists := state.Actors[HomeVisitorID]; exists {
		return store.Outcome{}, errHomeVisit
	}
	room := "dining"
	position, ok := c.RoomAnchor(room)
	if !ok {
		return store.Outcome{}, ErrHomeRoomUnknown
	}
	pos := [3]float64(position)
	after := copyState(state)
	var sequence uint64
	for _, activity := range state.Activities {
		if activity.ActorID == HomeVisitorID {
			sequence++
		}
	}
	after.Actors[HomeVisitorID] = store.ActorState{
		RoomID: room, Position: &pos, VisitID: cmd.VisitID, VisitUntil: formatHomeTime(until),
		HomeSceneSHA256: c.SceneSHA256(), ActivitySequence: sequence,
	}
	return store.Outcome{State: after, Events: []store.EventDraft{{Kind: "life.home.visit.admitted", Payload: raw}}}, nil
}

func (c *HomeController) handleVisitExpire(state store.State, raw json.RawMessage) (store.Outcome, error) {
	return c.endVisit(state, raw, true)
}

func (c *HomeController) handleVisitDepart(state store.State, raw json.RawMessage) (store.Outcome, error) {
	return c.endVisit(state, raw, false)
}

func (c *HomeController) endVisit(state store.State, raw json.RawMessage, expiry bool) (store.Outcome, error) {
	var cmd homeVisitCommand
	if err := decodeHomePayload(raw, &cmd); err != nil || cmd.ActorID != HomeVisitorID || !validHomeID(cmd.VisitID) || cmd.ValidUntil != "" {
		return store.Outcome{}, ErrHomePayload
	}
	actor, present := state.Actors[HomeVisitorID]
	if !present || actor.VisitID != cmd.VisitID {
		return store.Outcome{}, errHomeVisit
	}
	now := c.now().UTC()
	if expiry {
		until, err := parseHomeTime(actor.VisitUntil)
		if err != nil || now.Before(until) {
			return store.Outcome{}, errHomeVisit
		}
	}
	after := copyState(state)
	delete(after.Actors, HomeVisitorID)
	if activity, ok := after.Activities[actor.ActivityID]; ok {
		activity.Status = HomeActivityStatusStopped
		activity.EndedAt = formatHomeTime(now)
		after.Activities[actor.ActivityID] = activity
	}
	var release []store.Reservation
	for _, reservation := range state.Reservations {
		if reservation.ActorID == HomeVisitorID {
			release = append(release, reservation)
		}
	}
	return store.Outcome{State: after, Release: release, Events: []store.EventDraft{{Kind: "life.home.visit.departed", Payload: raw}}}, nil
}
