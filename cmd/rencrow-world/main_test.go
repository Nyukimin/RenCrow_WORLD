package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validTimestamp = "2025-03-04T05:06:07.123456789-05:00"

type fixtureWorld struct {
	worldID   string
	bundle    string
	timeZone  string
	timeScale float64
}

func TestInspectSelectsExactWorldAndEmitsStableBoundedJSON(t *testing.T) {
	configPath := writeFixture(t, map[string]string{
		"壊れた bundle.json": `{ this bundle is intentionally invalid`,
		"空間 bundle.json":  `{"version":1,"world_id":"target","rooms":[],"edges":[],"anchors":[]}`,
	}, []fixtureWorld{
		{worldID: "unselected", bundle: "壊れた bundle.json", timeZone: "UTC", timeScale: 1},
		{worldID: "target", bundle: "空間 bundle.json", timeZone: "Asia/Tokyo", timeScale: 2},
	})
	args := []string{"inspect", "--config", configPath, "--world-id", "target", "--at", validTimestamp}

	var first, second bytes.Buffer
	var firstErr, secondErr bytes.Buffer
	if code := run(args, &first, &firstErr); code != 0 {
		t.Fatalf("first run exit code = %d; stderr = %q", code, firstErr.String())
	}
	if code := run(args, &second, &secondErr); code != 0 {
		t.Fatalf("second run exit code = %d; stderr = %q", code, secondErr.String())
	}
	if firstErr.Len() != 0 || secondErr.Len() != 0 {
		t.Fatalf("successful run wrote stderr: first=%q second=%q", firstErr.String(), secondErr.String())
	}
	if first.String() != second.String() {
		t.Fatalf("inspection output changed across identical runs:\nfirst:  %q\nsecond: %q", first.String(), second.String())
	}
	want := "{\"schema_version\":1,\"world_id\":\"target\",\"resident_actor_ids\":[\"aka\",\"ao\",\"gin\",\"kin\",\"kuro\",\"midori\",\"mio\",\"shiro\"],\"visitor_present\":false,\"world_time_utc\":\"2025-03-04T10:06:07.123456789Z\",\"world_time_local\":\"2025-03-04T19:06:07.123456789+09:00\",\"spatial_ready\":false,\"world_revision\":0}\n"
	if first.String() != want {
		t.Fatalf("inspection output = %q, want %q", first.String(), want)
	}
	if strings.Count(first.String(), "\n") != 1 || !strings.HasSuffix(first.String(), "\n") {
		t.Fatalf("output must be one newline-terminated JSON object, got %q", first.String())
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(first.Bytes(), &fields); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if len(fields) != 8 {
		t.Fatalf("output has %d fields, want exactly 8", len(fields))
	}
}

func TestInspectInvalidInputsKeepStdoutEmpty(t *testing.T) {
	goodBundle := `{"version":1,"world_id":"target","rooms":[],"edges":[],"anchors":[]}`
	validConfig := writeFixture(t, map[string]string{"valid.json": goodBundle}, []fixtureWorld{
		{worldID: "target", bundle: "valid.json", timeZone: "UTC", timeScale: 1},
	})
	badBundleConfig := writeFixture(t, map[string]string{"broken.json": `{ malformed`}, []fixtureWorld{
		{worldID: "target", bundle: "broken.json", timeZone: "UTC", timeScale: 1},
	})

	tests := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{
			name:     "missing required argument",
			args:     []string{"inspect", "--config", validConfig, "--at", validTimestamp},
			wantCode: 2,
		},
		{
			name:     "unknown argument",
			args:     []string{"inspect", "--config", validConfig, "--world-id", "target", "--at", validTimestamp, "--extra", "value"},
			wantCode: 2,
		},
		{
			name:     "timestamp without offset",
			args:     []string{"inspect", "--config", validConfig, "--world-id", "target", "--at", "2025-03-04T05:06:07"},
			wantCode: 2,
		},
		{
			name:     "malformed timestamp",
			args:     []string{"inspect", "--config", validConfig, "--world-id", "target", "--at", "not-a-timeZ"},
			wantCode: 2,
		},
		{
			name:     "unconfigured world",
			args:     []string{"inspect", "--config", validConfig, "--world-id", "missing", "--at", validTimestamp},
			wantCode: 1,
		},
		{
			name:     "invalid selected bundle",
			args:     []string{"inspect", "--config", badBundleConfig, "--world-id", "target", "--at", validTimestamp},
			wantCode: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr); code != test.wantCode {
				t.Fatalf("exit code = %d, want %d", code, test.wantCode)
			}
			if stdout.Len() != 0 {
				t.Fatalf("invalid input wrote stdout: %q", stdout.String())
			}
			if stderr.Len() == 0 || stderr.Len() > 512 {
				t.Fatalf("stderr length = %d, want 1 to 512 bytes: %q", stderr.Len(), stderr.String())
			}
			if strings.Count(stderr.String(), "\n") != 1 || !strings.HasSuffix(stderr.String(), "\n") {
				t.Fatalf("stderr must contain one bounded line, got %q", stderr.String())
			}
		})
	}
}

func TestParseExplicitTimestampRejectsNonRFC3339NanoSyntax(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "single digit hour", value: "2025-03-04T5:06:07Z"},
		{name: "comma fractional separator", value: "2025-03-04T05:06:07,1Z"},
		{name: "offset hour out of range", value: "2025-03-04T05:06:07+24:00"},
		{name: "offset minute out of range", value: "2025-03-04T05:06:07+00:60"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseExplicitTimestamp(test.value); err == nil {
				t.Fatalf("parseExplicitTimestamp(%q) succeeded, want strict RFC3339Nano rejection", test.value)
			}
		})
	}
}

func TestInspectRejectsUnrepresentableOutputTimesBeforeWritingStdout(t *testing.T) {
	goodBundle := `{"version":1,"world_id":"target","rooms":[],"edges":[],"anchors":[]}`
	tests := []struct {
		name      string
		timeZone  string
		timestamp string
	}{
		{
			name:      "UTC year exceeds four digit output range",
			timeZone:  "UTC",
			timestamp: "9999-12-31T23:30:00-01:00",
		},
		{
			name:      "local year exceeds four digit output range",
			timeZone:  "Asia/Tokyo",
			timestamp: "9999-12-31T23:30:00Z",
		},
		{
			name:      "local year is below four digit output range",
			timeZone:  "Etc/GMT+1",
			timestamp: "0001-01-01T00:30:00Z",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath := writeFixture(t, map[string]string{"bundle.json": goodBundle}, []fixtureWorld{
				{worldID: "target", bundle: "bundle.json", timeZone: test.timeZone, timeScale: 1},
			})
			var stdout, stderr bytes.Buffer
			code := run([]string{"inspect", "--config", configPath, "--world-id", "target", "--at", test.timestamp}, &stdout, &stderr)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1 for unrepresentable output time", code)
			}
			if stdout.Len() != 0 {
				t.Fatalf("unrepresentable output time wrote stdout: %q", stdout.String())
			}
			if stderr.Len() == 0 || stderr.Len() > 512 {
				t.Fatalf("stderr length = %d, want 1 to 512 bytes: %q", stderr.Len(), stderr.String())
			}
			if stderr.String() != "world time cannot be represented\n" {
				t.Fatalf("stderr = %q, want world time representability error", stderr.String())
			}
		})
	}
}

func TestInspectRejectsHistoricalSubMinuteLocalOffset(t *testing.T) {
	location, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skipf("IANA time-zone data unavailable: %v", err)
	}
	_, offsetSeconds := time.Date(1880, time.January, 1, 0, 0, 0, 0, location).Zone()
	if offsetSeconds%60 == 0 {
		t.Skip("Europe/Paris historical offset has no sub-minute component")
	}

	configPath := writeFixture(t, map[string]string{
		"bundle.json": `{"version":1,"world_id":"target","rooms":[],"edges":[],"anchors":[]}`,
	}, []fixtureWorld{{worldID: "target", bundle: "bundle.json", timeZone: "Europe/Paris", timeScale: 1}})
	var stdout, stderr bytes.Buffer
	code := run([]string{"inspect", "--config", configPath, "--world-id", "target", "--at", "1880-01-01T00:00:00Z"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 for historical sub-minute local offset", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("historical sub-minute local offset wrote stdout: %q", stdout.String())
	}
	if stderr.String() != "world time cannot be represented\n" {
		t.Fatalf("stderr = %q, want world time representability error", stderr.String())
	}
}

func TestInspectOutputFailureExitsNonzero(t *testing.T) {
	configPath := writeFixture(t, map[string]string{
		"bundle.json": `{"version":1,"world_id":"target","rooms":[],"edges":[],"anchors":[]}`,
	}, []fixtureWorld{
		{worldID: "target", bundle: "bundle.json", timeZone: "UTC", timeScale: 1},
	})
	var stderr bytes.Buffer
	code := run([]string{"inspect", "--config", configPath, "--world-id", "target", "--at", validTimestamp}, failingWriter{}, &stderr)
	if code == 0 {
		t.Fatal("output failure returned success")
	}
	if stderr.String() != "could not write inspection result\n" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func writeFixture(t *testing.T, bundleFiles map[string]string, worlds []fixtureWorld) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "入力 directory with spaces")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	for name, contents := range bundleFiles {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
			t.Fatalf("write bundle fixture: %v", err)
		}
	}
	type configWorld struct {
		WorldID           string  `json:"world_id"`
		SpatialBundlePath string  `json:"spatial_bundle_path"`
		TimeZone          string  `json:"time_zone"`
		TimeScale         float64 `json:"time_scale"`
	}
	config := struct {
		SchemaVersion int           `json:"schema_version"`
		Worlds        []configWorld `json:"worlds"`
	}{SchemaVersion: 1}
	for _, entry := range worlds {
		config.Worlds = append(config.Worlds, configWorld{
			WorldID:           entry.worldID,
			SpatialBundlePath: entry.bundle,
			TimeZone:          entry.timeZone,
			TimeScale:         entry.timeScale,
		})
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config fixture: %v", err)
	}
	configPath := filepath.Join(root, "設定 file.json")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	return configPath
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("injected output failure")
}
