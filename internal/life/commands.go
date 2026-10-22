package life

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

// This file is R10 required item 4 on the logical side (D-11, D-12): the
// Event/Receipt rules that bind a planning decision to the canonical WORLD
// Command -> Handler -> Receipt route. A rule is one registered action with one
// payload schema, one ordered Event group, and one State projection effect.
//
// The rules deliberately keep every undecided boundary closed:
//
//   - A start commits only when the activity's own logical occupancy already
//     equals the Actor's canonical room, so no rule moves anybody (D-17). A
//     start that would relocate the Actor is rejected and changes no State.
//   - A stop records the interruption as a WORLD event and keeps holding the
//     logical occupancy. Cancelling an activity and releasing what the body may
//     still be using stay separate operations with separate actions (D-11), so
//     release is its own action and never runs as part of a stop.
//   - Nothing here reserves or consumes a resource: the binding between an
//     activity and a real resource, quantity, and reservation identity needs
//     the measured evidence in R10 item 2. The logical occupancy name stays a
//     label from the owner mapping in catalog.go.
//   - No Event describes a CORE work result. A WORLD stop is a life event and
//     never cancels, fails, or completes CORE work (D-11).
//
// No production route registers these actions yet, so nothing in a running
// WORLD applies them; the wiring is a separate decision (R10 items 3 and 5).

// Registered action names for the life Activity lifecycle.
const (
	ActionActivityStart    = "life.activity.start"
	ActionActivityStop     = "life.activity.stop"
	ActionOccupancyRelease = "life.occupancy.release"
)

// Committed Event kinds. One action emits exactly one Event: a life transition
// is a meaningful change, not an animation frame.
const (
	EventActivityStarted   = "life.activity.started"
	EventActivityStopped   = "life.activity.stopped"
	EventOccupancyReleased = "life.occupancy.released"
)

// Activity lifecycle statuses in the canonical State projection. A stopped
// activity is still holding its logical occupancy; only a released one is not.
const (
	ActivityStatusRunning  = "running"
	ActivityStatusStopped  = "stopped"
	ActivityStatusReleased = "released"
)

// Rule refusals. The EventStore turns any handler error into a `rejected`
// Receipt with no transaction, no Events, and no State change, so a refusal is
// observable both as the rule error at the handler boundary and as a rejected
// Receipt at the Command boundary.
var (
	ErrCommandPayload       = errors.New("life command payload is invalid")
	ErrUnknownActor         = errors.New("life command names an Actor that is not in the timeline")
	ErrUnregisteredActivity = errors.New("life command names an activity outside WORLD's owner mapping")
	ErrActivityRunning      = errors.New("life Actor already runs an activity")
	ErrActivityNotRunning   = errors.New("life command targets an activity that is not running")
	ErrUnknownActivity      = errors.New("life command targets an unknown activity")
	ErrMovementRequired     = errors.New("life activity would move the Actor, and WORLD performs no movement")
	ErrNotInterruptible     = errors.New("life activity defines no safe interruption")
	ErrStopRequired         = errors.New("life occupancy release requires the stop action first")
	ErrNothingHeld          = errors.New("life activity holds no occupancy to release")
	ErrAlreadyReleased      = errors.New("life occupancy was already released")
)

// ActivityCommand is the payload schema of the three actions. It carries only
// identifiers: the posture, occupancy, and cleanup actions of a transition come
// from WORLD's owner mapping, never from the caller.
type ActivityCommand struct {
	ActorID    string `json:"actor_id"`
	ActivityID string `json:"activity_id"`
}

type startedEvent struct {
	ActorID    string `json:"actor_id"`
	ActivityID string `json:"activity_id"`
	Occupancy  string `json:"occupancy"`
}

type stoppedEvent struct {
	ActorID    string   `json:"actor_id"`
	ActivityID string   `json:"activity_id"`
	StopState  string   `json:"stop_state"`
	Actions    []string `json:"actions"`
	Occupancy  string   `json:"occupancy"`
}

type releasedEvent struct {
	ActorID    string `json:"actor_id"`
	ActivityID string `json:"activity_id"`
	Occupancy  string `json:"occupancy"`
}

// HandleActivityStart commits one admitted candidate that needs no movement.
// It rejects an unregistered activity, an unknown Actor, an Actor that already
// runs one activity (one primary life Activity per Actor), and a start that
// would relocate the Actor, because WORLD has no movement route (D-17).
func HandleActivityStart(state store.State, payload json.RawMessage) (store.Outcome, error) {
	cmd, err := decodeActivityCommand(payload)
	if err != nil {
		return store.Outcome{}, err
	}
	actor, ok := state.Actors[cmd.ActorID]
	if !ok {
		return store.Outcome{}, fmt.Errorf("%w: actor=%q", ErrUnknownActor, cmd.ActorID)
	}
	if actor.ActivityID != "" {
		return store.Outcome{}, fmt.Errorf("%w: running=%q", ErrActivityRunning, actor.ActivityID)
	}
	definition, ok := ownerActivityDefinition(cmd.ActivityID)
	if !ok {
		return store.Outcome{}, fmt.Errorf("%w: activity=%q", ErrUnregisteredActivity, cmd.ActivityID)
	}
	if definition.Occupancy != "" && actor.RoomID != definition.Occupancy {
		return store.Outcome{}, fmt.Errorf("%w: activity=%q occupancy=%q room=%q",
			ErrMovementRequired, cmd.ActivityID, definition.Occupancy, actor.RoomID)
	}

	after := copyState(state)
	after.Actors[cmd.ActorID] = store.ActorState{RoomID: actor.RoomID, ActivityID: cmd.ActivityID}
	after.Activities[cmd.ActivityID] = store.ActivityState{
		ID: cmd.ActivityID, ActorID: cmd.ActorID, Status: ActivityStatusRunning,
	}
	return store.Outcome{State: after, Events: []store.EventDraft{{
		Kind: EventActivityStarted,
		Payload: mustEventPayload(startedEvent{
			ActorID: cmd.ActorID, ActivityID: cmd.ActivityID, Occupancy: definition.Occupancy,
		}),
	}}}, nil
}

// HandleActivityStop ends one running activity through its own interruption
// rule. It runs only the owner-authorized actions, applies the rule's stop
// state, and keeps holding the logical occupancy: cancelling the activity is
// not releasing what the body may still be on (D-11). A rule-less or
// non-interruptible activity is refused, which holds the Actor in place with no
// Event and no State change.
func HandleActivityStop(state store.State, payload json.RawMessage) (store.Outcome, error) {
	cmd, err := decodeActivityCommand(payload)
	if err != nil {
		return store.Outcome{}, err
	}
	activity, held, err := runningActivity(state, cmd)
	if err != nil {
		return store.Outcome{}, err
	}
	if activity.Status != ActivityStatusRunning {
		return store.Outcome{}, fmt.Errorf("%w: activity=%q status=%q",
			ErrActivityNotRunning, cmd.ActivityID, activity.Status)
	}
	plan := PlanStop(held, "world_command")
	if !plan.Allowed {
		if plan.BlockedReason == "not_interruptible" {
			return store.Outcome{}, fmt.Errorf("%w: activity=%q", ErrNotInterruptible, cmd.ActivityID)
		}
		return store.Outcome{}, fmt.Errorf("%w: activity=%q reason=%s", ErrNotInterruptible, cmd.ActivityID, plan.BlockedReason)
	}

	after := copyState(state)
	after.Activities[cmd.ActivityID] = store.ActivityState{
		ID: activity.ID, ActorID: activity.ActorID, Status: ActivityStatusStopped,
	}
	if actor, ok := after.Actors[cmd.ActorID]; ok {
		actor.ActivityID = ""
		after.Actors[cmd.ActorID] = actor
	}
	return store.Outcome{State: after, Events: []store.EventDraft{{
		Kind: EventActivityStopped,
		Payload: mustEventPayload(stoppedEvent{
			ActorID: cmd.ActorID, ActivityID: cmd.ActivityID,
			StopState: plan.StopState, Actions: plan.Actions, Occupancy: held.Occupancy,
		}),
	}}}, nil
}

// HandleOccupancyRelease is the separate capability that stops holding the
// logical occupancy of an activity whose stop action already committed. It
// refuses while the activity runs, when nothing was held, and when the release
// already happened. It releases no reservation, because the reservation
// identity of an activity stays unbound until R10 item 2.
func HandleOccupancyRelease(state store.State, payload json.RawMessage) (store.Outcome, error) {
	cmd, err := decodeActivityCommand(payload)
	if err != nil {
		return store.Outcome{}, err
	}
	activity, held, err := runningActivity(state, cmd)
	if err != nil {
		return store.Outcome{}, err
	}
	switch activity.Status {
	case ActivityStatusRunning:
		return store.Outcome{}, fmt.Errorf("%w: activity=%q", ErrStopRequired, cmd.ActivityID)
	case ActivityStatusReleased:
		return store.Outcome{}, fmt.Errorf("%w: activity=%q", ErrAlreadyReleased, cmd.ActivityID)
	case ActivityStatusStopped:
		if held.Occupancy == "" {
			return store.Outcome{}, fmt.Errorf("%w: activity=%q", ErrNothingHeld, cmd.ActivityID)
		}
	default:
		return store.Outcome{}, fmt.Errorf("%w: activity=%q status=%q",
			ErrActivityNotRunning, cmd.ActivityID, activity.Status)
	}

	after := copyState(state)
	after.Activities[cmd.ActivityID] = store.ActivityState{
		ID: activity.ID, ActorID: activity.ActorID, Status: ActivityStatusReleased,
	}
	return store.Outcome{State: after, Events: []store.EventDraft{{
		Kind: EventOccupancyReleased,
		Payload: mustEventPayload(releasedEvent{
			ActorID: cmd.ActorID, ActivityID: cmd.ActivityID, Occupancy: held.Occupancy,
		}),
	}}}, nil
}

func decodeActivityCommand(payload json.RawMessage) (ActivityCommand, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var cmd ActivityCommand
	if err := decoder.Decode(&cmd); err != nil {
		return ActivityCommand{}, fmt.Errorf("%w: %v", ErrCommandPayload, err)
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return ActivityCommand{}, fmt.Errorf("%w: trailing document", ErrCommandPayload)
	}
	if cmd.ActorID == "" || cmd.ActivityID == "" {
		return ActivityCommand{}, fmt.Errorf("%w: actor_id and activity_id are required", ErrCommandPayload)
	}
	return cmd, nil
}

// runningActivity resolves a command against the canonical projection and
// WORLD's owner mapping, so a rule never trusts a caller-supplied posture,
// occupancy, or cleanup step.
func runningActivity(state store.State, cmd ActivityCommand) (store.ActivityState, RunningActivity, error) {
	activity, ok := state.Activities[cmd.ActivityID]
	if !ok || activity.ActorID != cmd.ActorID {
		return store.ActivityState{}, RunningActivity{}, fmt.Errorf("%w: activity=%q actor=%q",
			ErrUnknownActivity, cmd.ActivityID, cmd.ActorID)
	}
	definition, ok := ownerActivityDefinition(cmd.ActivityID)
	if !ok {
		return store.ActivityState{}, RunningActivity{}, fmt.Errorf("%w: activity=%q",
			ErrUnregisteredActivity, cmd.ActivityID)
	}
	return activity, definition, nil
}

func ownerActivityDefinition(id string) (RunningActivity, bool) {
	for _, definition := range DefaultActivityDefinitions() {
		if definition.ID == id {
			return definition, true
		}
	}
	return RunningActivity{}, false
}

func copyState(state store.State) store.State {
	after := store.State{
		Actors:       make(map[string]store.ActorState, len(state.Actors)+1),
		Activities:   make(map[string]store.ActivityState, len(state.Activities)+1),
		Resources:    make(map[string]store.ResourceState, len(state.Resources)),
		Reservations: append([]store.Reservation(nil), state.Reservations...),
	}
	for id, actor := range state.Actors {
		after.Actors[id] = actor
	}
	for id, activity := range state.Activities {
		after.Activities[id] = activity
	}
	for id, resource := range state.Resources {
		after.Resources[id] = resource
	}
	return after
}

func mustEventPayload(event any) json.RawMessage {
	raw, err := json.Marshal(event)
	if err != nil {
		panic("life command: unserializable event: " + err.Error())
	}
	return raw
}
