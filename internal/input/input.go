// Package input validates the explicit JSON files consumed by the R05 one-shot
// inspection command.
package input

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_WORLD/internal/world"
)

const maxJSONBytes = 1 << 20

// Config is the validated set of worlds available to the inspection command.
type Config struct {
	Worlds []WorldConfig
}

// WorldConfig contains the validated inputs for one WorldID.
type WorldConfig struct {
	WorldID           world.WorldID
	SpatialBundlePath string
	TimeZone          string
	Location          *time.Location
	TimeScale         float64
}

type configDocument struct {
	SchemaVersion *int            `json:"schema_version"`
	Worlds        *[]*configWorld `json:"worlds"`
}

type configWorld struct {
	WorldID           *string  `json:"world_id"`
	SpatialBundlePath *string  `json:"spatial_bundle_path"`
	TimeZone          *string  `json:"time_zone"`
	TimeScale         *float64 `json:"time_scale"`
}

type bundleDocument struct {
	Version *int             `json:"version"`
	WorldID *string          `json:"world_id"`
	Rooms   *[]*bundleRoom   `json:"rooms"`
	Edges   *[]*bundleEdge   `json:"edges"`
	Anchors *[]*bundleAnchor `json:"anchors"`
}

type bundleRoom struct {
	ID *string `json:"id"`
}

type bundleEdge struct {
	ID         *string `json:"id"`
	FromRoomID *string `json:"from_room_id"`
	ToRoomID   *string `json:"to_room_id"`
}

type bundleAnchor struct {
	ID                *string         `json:"id"`
	RoomID            *string         `json:"room_id"`
	MeasurementStatus *string         `json:"measurement_status"`
	Position          json.RawMessage `json:"position"`
}

type jsonShape struct {
	objectFields map[string]jsonShape
	arrayItem    *jsonShape
}

// LoadConfig reads and validates a version 1 configuration. Relative bundle
// paths are made absolute relative to the directory containing path.
func LoadConfig(path string) (Config, error) {
	data, err := readJSONFile(path)
	if err != nil {
		return Config{}, err
	}
	var document configDocument
	if err := decodeStrict(data, &document); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	if document.SchemaVersion == nil {
		return Config{}, errors.New("configuration schema_version is required")
	}
	if *document.SchemaVersion != 1 {
		return Config{}, errors.New("unsupported configuration schema_version")
	}
	if document.Worlds == nil {
		return Config{}, errors.New("configuration worlds must be an array")
	}
	if len(*document.Worlds) < 1 || len(*document.Worlds) > 32 {
		return Config{}, errors.New("configuration must contain 1 to 32 worlds")
	}
	configPath, err := filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("resolve configuration path: %w", err)
	}

	result := Config{Worlds: make([]WorldConfig, 0, len(*document.Worlds))}
	seen := make(map[world.WorldID]struct{}, len(*document.Worlds))
	for i, entry := range *document.Worlds {
		if entry == nil {
			return Config{}, fmt.Errorf("worlds[%d] must be an object", i)
		}
		if entry.WorldID == nil || entry.SpatialBundlePath == nil || entry.TimeZone == nil || entry.TimeScale == nil {
			return Config{}, fmt.Errorf("worlds[%d] is missing a required field or has a null field", i)
		}
		worldID := world.WorldID(*entry.WorldID)
		if err := world.ValidateWorldID(worldID); err != nil {
			return Config{}, fmt.Errorf("worlds[%d] has an invalid world_id", i)
		}
		if _, exists := seen[worldID]; exists {
			return Config{}, fmt.Errorf("worlds[%d] duplicates a world_id", i)
		}
		seen[worldID] = struct{}{}

		if *entry.SpatialBundlePath == "" {
			return Config{}, fmt.Errorf("worlds[%d] spatial_bundle_path must not be empty", i)
		}
		bundlePath := *entry.SpatialBundlePath
		if !filepath.IsAbs(bundlePath) {
			bundlePath = filepath.Join(filepath.Dir(configPath), bundlePath)
		}
		bundlePath = filepath.Clean(bundlePath)

		if !validTimeZoneName(*entry.TimeZone) {
			return Config{}, fmt.Errorf("worlds[%d] time_zone must be an IANA zone or UTC", i)
		}
		location, err := time.LoadLocation(*entry.TimeZone)
		if err != nil {
			return Config{}, fmt.Errorf("worlds[%d] time_zone must be an IANA zone or UTC", i)
		}
		if math.IsNaN(*entry.TimeScale) || math.IsInf(*entry.TimeScale, 0) || *entry.TimeScale <= 0 {
			return Config{}, fmt.Errorf("worlds[%d] time_scale must be positive and finite", i)
		}

		result.Worlds = append(result.Worlds, WorldConfig{
			WorldID:           worldID,
			SpatialBundlePath: bundlePath,
			TimeZone:          *entry.TimeZone,
			Location:          location,
			TimeScale:         *entry.TimeScale,
		})
	}
	return result, nil
}

// LoadBundle reads a strict version 1 SpatialBundle for selectedWorldID.
// R05 input accepts only unverified anchors with an explicit null position.
func LoadBundle(path string, selectedWorldID world.WorldID) (world.SpatialBundle, error) {
	if err := world.ValidateWorldID(selectedWorldID); err != nil {
		return world.SpatialBundle{}, fmt.Errorf("invalid selected WorldID: %w", err)
	}
	data, err := readJSONFile(path)
	if err != nil {
		return world.SpatialBundle{}, err
	}
	var document bundleDocument
	if err := decodeStrict(data, &document); err != nil {
		return world.SpatialBundle{}, fmt.Errorf("invalid spatial bundle: %w", err)
	}
	if document.Version == nil || document.WorldID == nil || document.Rooms == nil || document.Edges == nil || document.Anchors == nil {
		return world.SpatialBundle{}, errors.New("spatial bundle is missing a required field or has a null field")
	}
	if *document.Version != world.SpatialBundleV1 {
		return world.SpatialBundle{}, errors.New("unsupported spatial bundle version")
	}

	bundle := world.SpatialBundle{
		Version: world.SpatialBundleV1,
		WorldID: world.WorldID(*document.WorldID),
		Rooms:   make([]world.Room, 0, len(*document.Rooms)),
		Edges:   make([]world.Edge, 0, len(*document.Edges)),
		Anchors: make([]world.Anchor, 0, len(*document.Anchors)),
	}
	for i, room := range *document.Rooms {
		if room == nil || room.ID == nil {
			return world.SpatialBundle{}, fmt.Errorf("rooms[%d] must be an object with a required id", i)
		}
		bundle.Rooms = append(bundle.Rooms, world.Room{ID: *room.ID})
	}
	for i, edge := range *document.Edges {
		if edge == nil || edge.ID == nil || edge.FromRoomID == nil || edge.ToRoomID == nil {
			return world.SpatialBundle{}, fmt.Errorf("edges[%d] must have all required fields", i)
		}
		bundle.Edges = append(bundle.Edges, world.Edge{
			ID:         *edge.ID,
			FromRoomID: *edge.FromRoomID,
			ToRoomID:   *edge.ToRoomID,
		})
	}
	for i, anchor := range *document.Anchors {
		if anchor == nil || anchor.ID == nil || anchor.RoomID == nil || anchor.MeasurementStatus == nil {
			return world.SpatialBundle{}, fmt.Errorf("anchors[%d] must have all required fields", i)
		}
		if *anchor.MeasurementStatus != string(world.MeasurementUnverified) {
			return world.SpatialBundle{}, fmt.Errorf("anchors[%d] measurement_status must be unverified", i)
		}
		if len(anchor.Position) == 0 {
			return world.SpatialBundle{}, fmt.Errorf("anchors[%d] position is required and must be null", i)
		}
		if !bytes.Equal(bytes.TrimSpace(anchor.Position), []byte("null")) {
			return world.SpatialBundle{}, fmt.Errorf("anchors[%d] position must be null", i)
		}
		bundle.Anchors = append(bundle.Anchors, world.Anchor{
			ID:                *anchor.ID,
			RoomID:            *anchor.RoomID,
			MeasurementStatus: world.MeasurementUnverified,
		})
	}
	if err := world.ValidateSpatialBundle(selectedWorldID, bundle); err != nil {
		return world.SpatialBundle{}, fmt.Errorf("invalid spatial bundle: %w", err)
	}
	return bundle, nil
}

func readJSONFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open input file: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxJSONBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read input file: %w", err)
	}
	if len(data) > maxJSONBytes {
		return nil, errors.New("input file exceeds 1 MiB")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("input file is not valid UTF-8")
	}
	if err := validateUnicodeEscapes(data); err != nil {
		return nil, err
	}
	return data, nil
}

func validTimeZoneName(name string) bool {
	if name == "UTC" {
		return true
	}
	if name == "" || name == "Local" || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) < 2 || !isSupportedTimeZoneRegion(parts[0]) {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, char := range part {
			if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
				(char >= '0' && char <= '9') || char == '_' || char == '+' || char == '-') {
				return false
			}
		}
	}
	return true
}

func isSupportedTimeZoneRegion(region string) bool {
	switch region {
	case "Africa", "America", "Antarctica", "Arctic", "Asia", "Atlantic", "Australia", "Europe", "Indian", "Pacific", "Etc":
		return true
	default:
		return false
	}
}

func decodeStrict(data []byte, destination any) error {
	shape, err := jsonShapeForDestination(destination)
	if err != nil {
		return err
	}
	if err := validateJSONShape(data, shape); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("input contains a trailing JSON document")
		}
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return nil
}

func jsonShapeForDestination(destination any) (jsonShape, error) {
	typeOfDestination := reflect.TypeOf(destination)
	if typeOfDestination == nil || typeOfDestination.Kind() != reflect.Pointer {
		return jsonShape{}, errors.New("strict JSON destination must be a pointer")
	}
	return jsonShapeForType(typeOfDestination.Elem())
}

func jsonShapeForType(typeOfValue reflect.Type) (jsonShape, error) {
	if typeOfValue == reflect.TypeOf(json.RawMessage{}) {
		return jsonShape{}, nil
	}
	switch typeOfValue.Kind() {
	case reflect.Pointer:
		return jsonShapeForType(typeOfValue.Elem())
	case reflect.Slice, reflect.Array:
		itemShape, err := jsonShapeForType(typeOfValue.Elem())
		if err != nil {
			return jsonShape{}, err
		}
		return arrayJSONShape(itemShape), nil
	case reflect.Struct:
		fields := make(map[string]jsonShape, typeOfValue.NumField())
		for i := 0; i < typeOfValue.NumField(); i++ {
			field := typeOfValue.Field(i)
			if field.PkgPath != "" {
				continue
			}
			tag, ok := field.Tag.Lookup("json")
			if !ok {
				return jsonShape{}, fmt.Errorf("strict JSON field %s.%s has no explicit json tag", typeOfValue, field.Name)
			}
			name, _, _ := strings.Cut(tag, ",")
			if name == "-" {
				continue
			}
			if name == "" {
				return jsonShape{}, fmt.Errorf("strict JSON field %s.%s has an empty json name", typeOfValue, field.Name)
			}
			if _, exists := fields[name]; exists {
				return jsonShape{}, fmt.Errorf("strict JSON type %s has duplicate json field %q", typeOfValue, name)
			}
			fieldShape, err := jsonShapeForType(field.Type)
			if err != nil {
				return jsonShape{}, err
			}
			fields[name] = fieldShape
		}
		return objectJSONShape(fields), nil
	case reflect.Map, reflect.Interface:
		return jsonShape{}, fmt.Errorf("strict JSON field type %s cannot define exact object keys", typeOfValue)
	default:
		return jsonShape{}, nil
	}
}

func validateJSONShape(data []byte, shape jsonShape) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder, shape); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("input contains a trailing JSON document")
		}
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder, shape jsonShape) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("invalid JSON object key: %w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate JSON object key")
			}
			seen[key] = struct{}{}
			childShape := jsonShape{}
			if shape.objectFields != nil {
				var ok bool
				childShape, ok = shape.objectFields[key]
				if !ok {
					return errors.New("input contains an unknown or noncanonical JSON field")
				}
			}
			if err := consumeJSONValue(decoder, childShape); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("invalid JSON object: %w", err)
		}
	case '[':
		for decoder.More() {
			itemShape := jsonShape{}
			if shape.arrayItem != nil {
				itemShape = *shape.arrayItem
			}
			if err := consumeJSONValue(decoder, itemShape); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("invalid JSON array: %w", err)
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func objectJSONShape(fields map[string]jsonShape) jsonShape {
	return jsonShape{objectFields: fields}
}

func arrayJSONShape(item jsonShape) jsonShape {
	return jsonShape{arrayItem: &item}
}

func validateUnicodeEscapes(data []byte) error {
	for i := 0; i < len(data); i++ {
		if data[i] != '"' {
			continue
		}
		i++
		for i < len(data) {
			switch data[i] {
			case '"':
				goto nextString
			case '\\':
				i++
				if i >= len(data) {
					return nil // The JSON decoder reports the incomplete escape.
				}
				if data[i] != 'u' {
					break
				}
				codeUnit, next, ok := unicodeCodeUnit(data, i-1)
				if !ok {
					return nil // The JSON decoder reports malformed hexadecimal escapes.
				}
				i = next - 1
				if codeUnit >= 0xD800 && codeUnit <= 0xDBFF {
					if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
						return errors.New("input contains an unpaired Unicode surrogate")
					}
					low, lowNext, ok := unicodeCodeUnit(data, i+1)
					if !ok || low < 0xDC00 || low > 0xDFFF {
						return errors.New("input contains an unpaired Unicode surrogate")
					}
					i = lowNext - 1
				} else if codeUnit >= 0xDC00 && codeUnit <= 0xDFFF {
					return errors.New("input contains an unpaired Unicode surrogate")
				}
			default:
			}
			i++
		}
		return nil // The JSON decoder reports an unterminated string.

	nextString:
	}
	return nil
}

func unicodeCodeUnit(data []byte, start int) (uint16, int, bool) {
	if start+6 > len(data) || data[start] != '\\' || data[start+1] != 'u' {
		return 0, start, false
	}
	var value uint16
	for _, b := range data[start+2 : start+6] {
		value <<= 4
		switch {
		case b >= '0' && b <= '9':
			value += uint16(b - '0')
		case b >= 'a' && b <= 'f':
			value += uint16(b-'a') + 10
		case b >= 'A' && b <= 'F':
			value += uint16(b-'A') + 10
		default:
			return 0, start, false
		}
	}
	return value, start + 6, true
}
