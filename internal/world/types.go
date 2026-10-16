package world

import "time"

type WorldID string

type ActorID string

const (
	ActorMio    ActorID = "mio"
	ActorShiro  ActorID = "shiro"
	ActorKuro   ActorID = "kuro"
	ActorMidori ActorID = "midori"
	ActorAka    ActorID = "aka"
	ActorAo     ActorID = "ao"
	ActorKin    ActorID = "kin"
	ActorGin    ActorID = "gin"
	ActorLumina ActorID = "lumina"
)

const SpatialBundleV1 = 1

type MeasurementStatus string

const (
	MeasurementUnverified MeasurementStatus = "unverified"
	MeasurementMeasured   MeasurementStatus = "measured"
)

type Position struct {
	X float64
	Y float64
	Z float64
}

type Room struct {
	ID string
}

type Edge struct {
	ID         string
	FromRoomID string
	ToRoomID   string
}

type Anchor struct {
	ID                string
	RoomID            string
	Position          *Position
	MeasurementStatus MeasurementStatus
}

type SpatialBundle struct {
	Version int
	WorldID WorldID
	Rooms   []Room
	Edges   []Edge
	Anchors []Anchor
}

type ClockInput struct {
	OriginUTC time.Time
	Location  *time.Location
	Scale     float64
}

type Readiness struct {
	SpatialReady bool
}

type Visit struct {
	WorldID       WorldID
	ActorID       ActorID
	ValidFromUTC  time.Time
	ValidUntilUTC time.Time
}

type WorldState struct {
	worldID     WorldID
	bundle      SpatialBundle
	actors      map[ActorID]struct{}
	activeVisit *Visit
	clock       ClockState
}
