package input

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_WORLD/internal/world"
)

func writeInput(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func TestLoadConfigResolvesUnicodeRelativePathFromConfigDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "データ room")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	configPath := filepath.Join(dir, "設定.json")
	contents := `{"schema_version":1,"worlds":[{"world_id":"家","spatial_bundle_path":"間取り file.json","time_zone":"Asia/Tokyo","time_scale":1.25}]}`
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if len(got.Worlds) != 1 {
		t.Fatalf("world count = %d, want 1", len(got.Worlds))
	}
	entry := got.Worlds[0]
	wantBundlePath, err := filepath.Abs(filepath.Join(dir, "間取り file.json"))
	if err != nil {
		t.Fatalf("Abs() error = %v", err)
	}
	if entry.WorldID != world.WorldID("家") {
		t.Errorf("WorldID = %q, want %q", entry.WorldID, "家")
	}
	if entry.SpatialBundlePath != wantBundlePath {
		t.Errorf("SpatialBundlePath = %q, want %q", entry.SpatialBundlePath, wantBundlePath)
	}
	if entry.Location == nil || entry.Location.String() != "Asia/Tokyo" {
		t.Errorf("Location = %v, want Asia/Tokyo", entry.Location)
	}
	if entry.TimeZone != "Asia/Tokyo" {
		t.Errorf("TimeZone = %q, want Asia/Tokyo", entry.TimeZone)
	}
	if entry.TimeScale != 1.25 {
		t.Errorf("TimeScale = %v, want 1.25", entry.TimeScale)
	}
}

func TestLoadConfigRejectsInvalidInputs(t *testing.T) {
	validWorld := `{"world_id":"world","spatial_bundle_path":"bundle.json","time_zone":"UTC","time_scale":1}`
	tests := []struct {
		name string
		json string
	}{
		{name: "unsupported version", json: `{"schema_version":2,"worlds":[` + validWorld + `]}`},
		{name: "missing version", json: `{"worlds":[` + validWorld + `]}`},
		{name: "null required field", json: `{"schema_version":1,"worlds":[{"world_id":null,"spatial_bundle_path":"bundle.json","time_zone":"UTC","time_scale":1}]}`},
		{name: "missing world field", json: `{"schema_version":1,"worlds":[{"world_id":"world","spatial_bundle_path":"bundle.json","time_zone":"UTC"}]}`},
		{name: "unknown field", json: `{"schema_version":1,"worlds":[` + validWorld[:len(validWorld)-1] + `,"server":"localhost"}]}`},
		{name: "case variant beside canonical field", json: `{"schema_version":1,"worlds":[{"world_id":"world","World_ID":"other","spatial_bundle_path":"bundle.json","time_zone":"UTC","time_scale":1}]}`},
		{name: "duplicate nested key", json: `{"schema_version":1,"worlds":[{"world_id":"world","spatial_bundle_path":"bundle.json","time_zone":"UTC","time_scale":1,"time_scale":2}]}`},
		{name: "trailing document", json: `{"schema_version":1,"worlds":[` + validWorld + `]} {}`},
		{name: "empty worlds", json: `{"schema_version":1,"worlds":[]}`},
		{name: "null worlds", json: `{"schema_version":1,"worlds":null}`},
		{name: "too many worlds", json: `{"schema_version":1,"worlds":[` + strings.TrimSuffix(strings.Repeat(validWorld+",", 33), ",") + `]}`},
		{name: "duplicate WorldID", json: `{"schema_version":1,"worlds":[` + validWorld + `,` + validWorld + `]}`},
		{name: "invalid WorldID whitespace", json: `{"schema_version":1,"worlds":[{"world_id":" world ","spatial_bundle_path":"bundle.json","time_zone":"UTC","time_scale":1}]}`},
		{name: "control in WorldID", json: `{"schema_version":1,"worlds":[{"world_id":"bad\nvalue","spatial_bundle_path":"bundle.json","time_zone":"UTC","time_scale":1}]}`},
		{name: "empty spatial path", json: `{"schema_version":1,"worlds":[{"world_id":"world","spatial_bundle_path":"","time_zone":"UTC","time_scale":1}]}`},
		{name: "local time zone", json: `{"schema_version":1,"worlds":[{"world_id":"world","spatial_bundle_path":"bundle.json","time_zone":"Local","time_scale":1}]}`},
		{name: "localtime zone file", json: `{"schema_version":1,"worlds":[{"world_id":"world","spatial_bundle_path":"bundle.json","time_zone":"localtime","time_scale":1}]}`},
		{name: "posix time zone namespace", json: `{"schema_version":1,"worlds":[{"world_id":"world","spatial_bundle_path":"bundle.json","time_zone":"posix/UTC","time_scale":1}]}`},
		{name: "right time zone namespace", json: `{"schema_version":1,"worlds":[{"world_id":"world","spatial_bundle_path":"bundle.json","time_zone":"right/UTC","time_scale":1}]}`},
		{name: "SystemV time zone namespace", json: `{"schema_version":1,"worlds":[{"world_id":"world","spatial_bundle_path":"bundle.json","time_zone":"SystemV/EST5","time_scale":1}]}`},
		{name: "non-region time zone alias", json: `{"schema_version":1,"worlds":[{"world_id":"world","spatial_bundle_path":"bundle.json","time_zone":"EST","time_scale":1}]}`},
		{name: "unknown time zone", json: `{"schema_version":1,"worlds":[{"world_id":"world","spatial_bundle_path":"bundle.json","time_zone":"Not/A_Zone","time_scale":1}]}`},
		{name: "time zone path traversal", json: `{"schema_version":1,"worlds":[{"world_id":"world","spatial_bundle_path":"bundle.json","time_zone":"../UTC","time_scale":1}]}`},
		{name: "zero scale", json: `{"schema_version":1,"worlds":[{"world_id":"world","spatial_bundle_path":"bundle.json","time_zone":"UTC","time_scale":0}]}`},
		{name: "negative scale", json: `{"schema_version":1,"worlds":[{"world_id":"world","spatial_bundle_path":"bundle.json","time_zone":"UTC","time_scale":-1}]}`},
		{name: "nonfinite scale", json: `{"schema_version":1,"worlds":[{"world_id":"world","spatial_bundle_path":"bundle.json","time_zone":"UTC","time_scale":1e10000}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := LoadConfig(writeInput(t, "config.json", test.json)); err == nil {
				t.Fatal("LoadConfig() accepted invalid config")
			}
		})
	}

	t.Run("duplicate escaped key", func(t *testing.T) {
		path := writeInput(t, "config.json", `{"schema_version":1,"worlds":[{"world_id":"world","spatial_bundle_path":"bundle.json","time_zone":"UTC","time_scale":1,"time\u005fscale":2}]}`)
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("LoadConfig() accepted duplicate keys with an escaped spelling")
		}
	})

	t.Run("invalid UTF-8", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte{'{', '"', 0xff, '"', '}', '\n'}, 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("LoadConfig() accepted invalid UTF-8")
		}
	})

	t.Run("unpaired Unicode surrogate", func(t *testing.T) {
		path := writeInput(t, "config.json", `{"schema_version":1,"worlds":[{"world_id":"\uD800","spatial_bundle_path":"bundle.json","time_zone":"UTC","time_scale":1}]}`)
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("LoadConfig() accepted an unpaired Unicode surrogate")
		}
	})

	t.Run("too large", func(t *testing.T) {
		path := writeInput(t, "config.json", `{"schema_version":1,"worlds":[]}`+strings.Repeat(" ", maxJSONBytes))
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("LoadConfig() accepted file larger than 1 MiB")
		}
	})
}

func TestValidTimeZoneNameRequiresSupportedIANARegion(t *testing.T) {
	for _, name := range []string{"UTC", "Asia/Tokyo", "America/Argentina/Buenos_Aires", "Etc/GMT+5"} {
		t.Run("accepts "+name, func(t *testing.T) {
			if !validTimeZoneName(name) {
				t.Fatalf("validTimeZoneName(%q) = false", name)
			}
		})
	}

	for _, name := range []string{
		"",
		"Local",
		"localtime",
		"posix/UTC",
		"right/UTC",
		"SystemV/EST5",
		"EST",
		"Africa",
		"Factory/Zone",
		"../UTC",
		"/etc/localtime",
		`America\\Los_Angeles`,
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			if validTimeZoneName(name) {
				t.Fatalf("validTimeZoneName(%q) = true", name)
			}
		})
	}
}

func TestDecodeStrictDerivesAllowedKeysFromJSONTags(t *testing.T) {
	type item struct {
		ID string `json:"entity_id"`
	}
	type document struct {
		Items []item `json:"entries"`
	}

	var got document
	if err := decodeStrict([]byte(`{"entries":[{"entity_id":"room"}]}`), &got); err != nil {
		t.Fatalf("decodeStrict() error = %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "room" {
		t.Fatalf("decoded document = %#v", got)
	}

	if err := decodeStrict([]byte(`{"entries":[{"ID":"room"}]}`), &document{}); err == nil {
		t.Fatal("decodeStrict() accepted a Go field name in place of the JSON tag")
	}
}

func TestLoadBundleRequiresExplicitNullPositionAndUnverifiedStatus(t *testing.T) {
	valid := `{"version":1,"world_id":"world","rooms":[{"id":"living"}],"edges":[],"anchors":[{"id":"chair","room_id":"living","measurement_status":"unverified","position":null}]}`
	path := writeInput(t, "bundle.json", valid)
	bundle, err := LoadBundle(path, world.WorldID("world"))
	if err != nil {
		t.Fatalf("LoadBundle() error = %v", err)
	}
	if len(bundle.Rooms) != 1 || len(bundle.Anchors) != 1 || bundle.Anchors[0].Position != nil {
		t.Fatalf("LoadBundle() returned unexpected bundle: %#v", bundle)
	}

	tests := []struct {
		name string
		json string
	}{
		{name: "omitted position", json: `{"version":1,"world_id":"world","rooms":[{"id":"living"}],"edges":[],"anchors":[{"id":"chair","room_id":"living","measurement_status":"unverified"}]}`},
		{name: "non-null position", json: `{"version":1,"world_id":"world","rooms":[{"id":"living"}],"edges":[],"anchors":[{"id":"chair","room_id":"living","measurement_status":"unverified","position":{"x":0,"y":0,"z":0}}]}`},
		{name: "measured position", json: `{"version":1,"world_id":"world","rooms":[{"id":"living"}],"edges":[],"anchors":[{"id":"chair","room_id":"living","measurement_status":"measured","position":{"x":1,"y":2,"z":3}}]}`},
		{name: "measured null position", json: `{"version":1,"world_id":"world","rooms":[{"id":"living"}],"edges":[],"anchors":[{"id":"chair","room_id":"living","measurement_status":"measured","position":null}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := LoadBundle(writeInput(t, "bundle.json", test.json), "world"); err == nil {
				t.Fatal("LoadBundle() accepted invalid anchor position")
			}
		})
	}
}

func TestLoadBundleRejectsInvalidShapeAndDomainData(t *testing.T) {
	base := `"version":1,"world_id":"world","rooms":[],"edges":[],"anchors":[]`
	tests := []struct {
		name string
		json string
	}{
		{name: "unsupported version", json: `{"version":2,"world_id":"world","rooms":[],"edges":[],"anchors":[]}`},
		{name: "missing required array", json: `{"version":1,"world_id":"world","rooms":[],"edges":[]}`},
		{name: "null required array", json: `{"version":1,"world_id":"world","rooms":null,"edges":[],"anchors":[]}`},
		{name: "unknown field", json: `{"version":1,"world_id":"world","rooms":[],"edges":[],"anchors":[],"snapshot":{}}`},
		{name: "nested case variant beside canonical field", json: `{"version":1,"world_id":"world","rooms":[{"id":"room","ID":"other"}],"edges":[],"anchors":[]}`},
		{name: "duplicate key", json: `{"version":1,"version":1,"world_id":"world","rooms":[],"edges":[],"anchors":[]}`},
		{name: "duplicate escaped key", json: `{"version":1,"ver\u0073ion":1,"world_id":"world","rooms":[],"edges":[],"anchors":[]}`},
		{name: "missing anchor id", json: `{"version":1,"world_id":"world","rooms":[{"id":"room"}],"edges":[],"anchors":[{"room_id":"room","measurement_status":"unverified","position":null}]}`},
		{name: "trailing document", json: `{` + base + `} {}`},
		{name: "world mismatch", json: `{"version":1,"world_id":"other","rooms":[],"edges":[],"anchors":[]}`},
		{name: "duplicate room ID", json: `{"version":1,"world_id":"world","rooms":[{"id":"room"},{"id":"room"}],"edges":[],"anchors":[]}`},
		{name: "dangling edge", json: `{"version":1,"world_id":"world","rooms":[],"edges":[{"id":"door","from_room_id":"a","to_room_id":"b"}],"anchors":[]}`},
		{name: "invalid anchor status", json: `{"version":1,"world_id":"world","rooms":[{"id":"room"}],"edges":[],"anchors":[{"id":"chair","room_id":"room","measurement_status":"unknown","position":null}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := LoadBundle(writeInput(t, "bundle.json", test.json), "world"); err == nil {
				t.Fatal("LoadBundle() accepted invalid bundle")
			}
		})
	}
}
