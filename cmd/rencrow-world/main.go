package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/input"
	"github.com/Nyukimin/RenCrow_WORLD/internal/world"
)

const usage = "usage: rencrow-world inspect --config PATH --world-id ID --at RFC3339Nano | rencrow-world serve --config PATH"

type inspectOptions struct {
	configPath string
	worldID    world.WorldID
	at         time.Time
}

type inspection struct {
	SchemaVersion    int             `json:"schema_version"`
	WorldID          world.WorldID   `json:"world_id"`
	ResidentActorIDs []world.ActorID `json:"resident_actor_ids"`
	VisitorPresent   bool            `json:"visitor_present"`
	WorldTimeUTC     string          `json:"world_time_utc"`
	WorldTimeLocal   string          `json:"world_time_local"`
	SpatialReady     bool            `json:"spatial_ready"`
	WorldRevision    uint64          `json:"world_revision"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "serve" {
		return runServe(args[1:], stdout, stderr)
	}
	options, ok := parseArgs(args)
	if !ok {
		writeError(stderr, usage)
		return 2
	}

	config, err := input.LoadConfig(options.configPath)
	if err != nil {
		writeError(stderr, "invalid configuration")
		return 1
	}

	var selected *input.WorldConfig
	for i := range config.Worlds {
		if config.Worlds[i].WorldID == options.worldID {
			selected = &config.Worlds[i]
			break
		}
	}
	if selected == nil {
		writeError(stderr, "world_id is not configured")
		return 1
	}

	bundle, err := input.LoadBundle(selected.SpatialBundlePath, selected.WorldID)
	if err != nil {
		writeError(stderr, "invalid spatial bundle")
		return 1
	}

	state, err := world.Bootstrap(selected.WorldID, bundle, world.ClockInput{
		OriginUTC: options.at,
		Location:  selected.Location,
		Scale:     selected.TimeScale,
	})
	if err != nil {
		writeError(stderr, "could not bootstrap world")
		return 1
	}

	actors, err := state.Actors(selected.WorldID)
	if err != nil {
		writeError(stderr, "could not inspect world")
		return 1
	}
	residentIDs := make([]world.ActorID, 0, len(actors))
	for actorID := range actors {
		residentIDs = append(residentIDs, actorID)
	}
	sort.Slice(residentIDs, func(i, j int) bool { return residentIDs[i] < residentIDs[j] })

	visitorPresent, err := state.HasActor(selected.WorldID, world.ActorLumina)
	if err != nil {
		writeError(stderr, "could not inspect world")
		return 1
	}
	clock, err := state.Clock(selected.WorldID)
	if err != nil {
		writeError(stderr, "could not inspect world")
		return 1
	}
	worldTimeUTC, err := formatRFC3339Nano(clock.UTC())
	if err != nil {
		writeError(stderr, "world time cannot be represented")
		return 1
	}
	worldTimeLocal, err := formatRFC3339Nano(clock.Local())
	if err != nil {
		writeError(stderr, "world time cannot be represented")
		return 1
	}
	readiness, err := state.Readiness(selected.WorldID)
	if err != nil {
		writeError(stderr, "could not inspect world")
		return 1
	}
	revision, err := state.Revision(selected.WorldID)
	if err != nil {
		writeError(stderr, "could not inspect world")
		return 1
	}

	result := inspection{
		SchemaVersion:    1,
		WorldID:          selected.WorldID,
		ResidentActorIDs: residentIDs,
		VisitorPresent:   visitorPresent,
		WorldTimeUTC:     worldTimeUTC,
		WorldTimeLocal:   worldTimeLocal,
		SpatialReady:     readiness.SpatialReady,
		WorldRevision:    revision,
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		writeError(stderr, "could not write inspection result")
		return 1
	}
	return 0
}

func parseArgs(args []string) (inspectOptions, bool) {
	if len(args) == 0 || args[0] != "inspect" || (len(args)-1)%2 != 0 {
		return inspectOptions{}, false
	}

	values := make(map[string]string, 3)
	for i := 1; i < len(args); i += 2 {
		key := args[i]
		if key != "--config" && key != "--world-id" && key != "--at" {
			return inspectOptions{}, false
		}
		if _, exists := values[key]; exists {
			return inspectOptions{}, false
		}
		values[key] = args[i+1]
	}
	if len(values) != 3 || values["--config"] == "" || values["--world-id"] == "" || values["--at"] == "" {
		return inspectOptions{}, false
	}

	at, err := parseExplicitTimestamp(values["--at"])
	if err != nil {
		return inspectOptions{}, false
	}
	return inspectOptions{
		configPath: values["--config"],
		worldID:    world.WorldID(values["--world-id"]),
		at:         at,
	}, true
}

func parseExplicitTimestamp(value string) (time.Time, error) {
	if !hasStrictRFC3339NanoSyntax(value) {
		return time.Time{}, errors.New("timestamp must use strict RFC3339Nano syntax")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed, nil
}

func hasStrictRFC3339NanoSyntax(value string) bool {
	if len(value) < 20 ||
		value[4] != '-' || value[7] != '-' || value[10] != 'T' ||
		value[13] != ':' || value[16] != ':' ||
		!isASCIIDigits(value[:4]) || !isASCIIDigits(value[5:7]) || !isASCIIDigits(value[8:10]) ||
		!isASCIIDigits(value[11:13]) || !isASCIIDigits(value[14:16]) || !isASCIIDigits(value[17:19]) {
		return false
	}

	zoneStart := 19
	if value[zoneStart] == '.' {
		fractionStart := zoneStart + 1
		zoneStart = fractionStart
		for zoneStart < len(value) && isASCIIDigit(value[zoneStart]) {
			zoneStart++
		}
		if zoneStart == fractionStart || zoneStart-fractionStart > 9 {
			return false
		}
	}
	if zoneStart >= len(value) {
		return false
	}
	if value[zoneStart] == 'Z' {
		return zoneStart == len(value)-1
	}
	if len(value)-zoneStart != 6 ||
		(value[zoneStart] != '+' && value[zoneStart] != '-') ||
		value[zoneStart+3] != ':' ||
		!isASCIIDigits(value[zoneStart+1:zoneStart+3]) || !isASCIIDigits(value[zoneStart+4:zoneStart+6]) {
		return false
	}
	offsetHour := int(value[zoneStart+1]-'0')*10 + int(value[zoneStart+2]-'0')
	offsetMinute := int(value[zoneStart+4]-'0')*10 + int(value[zoneStart+5]-'0')
	return offsetHour <= 23 && offsetMinute <= 59
}

func isASCIIDigits(value string) bool {
	for i := 0; i < len(value); i++ {
		if !isASCIIDigit(value[i]) {
			return false
		}
	}
	return true
}

func isASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func formatRFC3339Nano(value time.Time) (string, error) {
	if value.Year() < 1 || value.Year() > 9999 {
		return "", errors.New("timestamp year is outside the RFC3339Nano range")
	}
	_, offsetSeconds := value.Zone()
	if offsetSeconds%60 != 0 {
		return "", errors.New("timestamp offset is not a whole minute")
	}

	formatted := value.Format(time.RFC3339Nano)
	parsed, err := parseExplicitTimestamp(formatted)
	if err != nil || !parsed.Equal(value) {
		return "", errors.New("timestamp does not round-trip as RFC3339Nano")
	}
	return formatted, nil
}

func writeError(stderr io.Writer, message string) {
	_, _ = io.WriteString(stderr, message+"\n")
}
