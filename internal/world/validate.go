package world

import (
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

var residents = [...]ActorID{
	ActorMio, ActorShiro, ActorKuro, ActorMidori,
	ActorAka, ActorAo, ActorKin, ActorGin,
}

func validateOpaqueID(id string) error {
	if id == "" || !utf8.ValidString(id) || strings.TrimSpace(id) != id {
		return errors.New("ID must be nonempty valid UTF-8 without surrounding whitespace")
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return errors.New("ID must not contain control characters")
		}
	}
	return nil
}

// ValidateWorldID reports whether id is a valid opaque WorldID.
func ValidateWorldID(id WorldID) error {
	return validateOpaqueID(string(id))
}

func validateActorID(id ActorID) error {
	if id == ActorLumina {
		return nil
	}
	for _, resident := range residents {
		if id == resident {
			return nil
		}
	}
	return errors.New("unknown actor ID")
}

func validateBundle(worldID WorldID, bundle SpatialBundle) error {
	if bundle.Version != SpatialBundleV1 {
		return errors.New("unsupported spatial bundle version")
	}
	if bundle.WorldID != worldID {
		return errors.New("spatial bundle WorldID does not match")
	}
	rooms := make(map[string]struct{}, len(bundle.Rooms))
	for _, room := range bundle.Rooms {
		if err := validateOpaqueID(room.ID); err != nil {
			return errors.New("invalid room ID")
		}
		if _, exists := rooms[room.ID]; exists {
			return errors.New("duplicate room ID")
		}
		rooms[room.ID] = struct{}{}
	}
	edgeIDs := make(map[string]struct{}, len(bundle.Edges))
	for _, edge := range bundle.Edges {
		if err := validateOpaqueID(edge.ID); err != nil {
			return errors.New("invalid edge ID")
		}
		if _, exists := edgeIDs[edge.ID]; exists {
			return errors.New("duplicate edge ID")
		}
		edgeIDs[edge.ID] = struct{}{}
		if _, exists := rooms[edge.FromRoomID]; !exists {
			return errors.New("edge references unknown from room")
		}
		if _, exists := rooms[edge.ToRoomID]; !exists {
			return errors.New("edge references unknown to room")
		}
	}
	anchorIDs := make(map[string]struct{}, len(bundle.Anchors))
	for _, anchor := range bundle.Anchors {
		if err := validateOpaqueID(anchor.ID); err != nil {
			return errors.New("invalid anchor ID")
		}
		if _, exists := anchorIDs[anchor.ID]; exists {
			return errors.New("duplicate anchor ID")
		}
		anchorIDs[anchor.ID] = struct{}{}
		if _, exists := rooms[anchor.RoomID]; !exists {
			return errors.New("anchor references unknown room")
		}
		switch anchor.MeasurementStatus {
		case MeasurementUnverified:
			if anchor.Position != nil {
				return errors.New("unverified anchor must not have a position")
			}
		case MeasurementMeasured:
			if anchor.Position == nil {
				return errors.New("measured anchor requires a position")
			}
			if math.IsNaN(anchor.Position.X) || math.IsInf(anchor.Position.X, 0) ||
				math.IsNaN(anchor.Position.Y) || math.IsInf(anchor.Position.Y, 0) ||
				math.IsNaN(anchor.Position.Z) || math.IsInf(anchor.Position.Z, 0) {
				return errors.New("measured anchor position must be finite")
			}
		default:
			return errors.New("unknown anchor measurement status")
		}
	}
	return nil
}

// ValidateSpatialBundle validates a bundle and its WorldID-scoped references.
func ValidateSpatialBundle(worldID WorldID, bundle SpatialBundle) error {
	if err := ValidateWorldID(worldID); err != nil {
		return err
	}
	return validateBundle(worldID, bundle)
}
