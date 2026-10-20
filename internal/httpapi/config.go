package httpapi

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

const (
	maxConfigBytes = 1 << 20
	SchemaVersion  = 1
)

var validScopes = map[string]bool{
	"read":   true,
	"write":  true,
	"stream": true,
}

// Config is the explicit R07 source configuration. There is no default route,
// port, credential, or store path: an operator must provide all of them.
type Config struct {
	Listen           string              `json:"listen"`
	StorePath        string              `json:"store_path"`
	Scope            store.Scope         `json:"scope"`
	Tokens           []TokenConfig       `json:"tokens"`
	Page             PageConfig          `json:"page"`
	SSE              SSEConfig           `json:"sse"`
	AllowProbeAction bool                `json:"allow_probe_action"`
	AllowRemote      bool                `json:"allow_remote"`
	InitialState     *InitialStateConfig `json:"initial_state,omitempty"`
}

// TokenConfig stores only a SHA-256 digest of a bearer token. The raw token is
// supplied by the operator out of band and is never written to this file.
type TokenConfig struct {
	ID     string       `json:"id"`
	SHA256 string       `json:"sha256"`
	Scopes []string     `json:"scopes"`
	Scope  *store.Scope `json:"scope,omitempty"`
}

type PageConfig struct {
	MaxEvents int `json:"max_events"`
	MaxBytes  int `json:"max_bytes"`
}

type SSEConfig struct {
	HeartbeatSeconds int `json:"heartbeat_seconds"`
	PollMillis       int `json:"poll_millis"`
}

type InitialStateConfig struct {
	Actors    map[string]store.ActorState `json:"actors"`
	Resources map[string]int64            `json:"resources"`
}

// LoadConfig reads one strict JSON object, applies documented defaults, and
// rejects unsafe or incomplete configuration before any Store is opened.
func LoadConfig(path string) (Config, error) {
	if path == "" {
		return Config{}, errors.New("config path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return Config{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxConfigBytes {
		return Config{}, errors.New("config must be a regular file under 1 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, maxConfigBytes))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, err
	}
	if err := ensureEOF(dec); err != nil {
		return Config{}, err
	}
	return NormalizeConfig(cfg)
}

func ensureEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("config contains more than one JSON value")
		}
		return err
	}
	return nil
}

// NormalizeConfig validates and fills only documented defaults. It never
// invents a listen address, store path, scope, token, or initial state.
func NormalizeConfig(cfg Config) (Config, error) {
	if cfg.Listen == "" || strings.TrimSpace(cfg.Listen) != cfg.Listen {
		return Config{}, errors.New("listen is required and must not have surrounding whitespace")
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil || port == "" {
		return Config{}, errors.New("listen must be host:port")
	}
	if !isLoopback(host) && !cfg.AllowRemote {
		return Config{}, errors.New("non-loopback listen requires allow_remote:true")
	}
	if cfg.StorePath == "" || strings.TrimSpace(cfg.StorePath) != cfg.StorePath {
		return Config{}, errors.New("store_path is required")
	}
	if !cfg.Scope.Valid() {
		return Config{}, errors.New("scope requires exact world_id, timeline_id, and timeline_epoch")
	}
	if len(cfg.Tokens) == 0 {
		return Config{}, errors.New("at least one token is required")
	}
	seenIDs := map[string]bool{}
	for i := range cfg.Tokens {
		t := &cfg.Tokens[i]
		if t.ID == "" || len(t.ID) > 64 || strings.TrimSpace(t.ID) != t.ID || hasControl(t.ID) {
			return Config{}, fmt.Errorf("token %d has an invalid id", i)
		}
		if seenIDs[t.ID] {
			return Config{}, fmt.Errorf("duplicate token id %q", t.ID)
		}
		seenIDs[t.ID] = true
		t.SHA256 = strings.ToLower(strings.TrimSpace(t.SHA256))
		if len(t.SHA256) != 64 {
			return Config{}, fmt.Errorf("token %q requires a 64-character sha256", t.ID)
		}
		if _, err := hex.DecodeString(t.SHA256); err != nil {
			return Config{}, fmt.Errorf("token %q sha256 is not hexadecimal", t.ID)
		}
		if len(t.Scopes) == 0 {
			return Config{}, fmt.Errorf("token %q has no scopes", t.ID)
		}
		seenScopes := map[string]bool{}
		for _, s := range t.Scopes {
			if !validScopes[s] {
				return Config{}, fmt.Errorf("token %q has unknown scope %q", t.ID, s)
			}
			if seenScopes[s] {
				return Config{}, fmt.Errorf("token %q repeats scope %q", t.ID, s)
			}
			seenScopes[s] = true
		}
		if t.Scope != nil && !t.Scope.Valid() {
			return Config{}, fmt.Errorf("token %q has an invalid bound scope", t.ID)
		}
	}
	if cfg.Page.MaxEvents == 0 {
		cfg.Page.MaxEvents = store.DefaultPageEvents
	}
	if cfg.Page.MaxEvents < 1 || cfg.Page.MaxEvents > store.MaxPageEvents {
		return Config{}, errors.New("page.max_events must be 1..500")
	}
	if cfg.Page.MaxBytes == 0 {
		cfg.Page.MaxBytes = 2 * 1024 * 1024
	}
	if cfg.Page.MaxBytes < 1024 || cfg.Page.MaxBytes > 2*1024*1024 {
		return Config{}, errors.New("page.max_bytes must be 1024..2097152")
	}
	if cfg.SSE.HeartbeatSeconds == 0 {
		cfg.SSE.HeartbeatSeconds = 15
	}
	if cfg.SSE.HeartbeatSeconds < 1 || cfg.SSE.HeartbeatSeconds > 300 {
		return Config{}, errors.New("sse.heartbeat_seconds must be 1..300")
	}
	if cfg.SSE.PollMillis == 0 {
		cfg.SSE.PollMillis = 200
	}
	if cfg.SSE.PollMillis < 20 || cfg.SSE.PollMillis > 5000 {
		return Config{}, errors.New("sse.poll_millis must be 20..5000")
	}
	if cfg.InitialState != nil {
		for id, capacity := range cfg.InitialState.Resources {
			if id == "" || strings.TrimSpace(id) != id || capacity < 0 {
				return Config{}, errors.New("initial_state.resources require exact ids and non-negative capacity")
			}
		}
		for id := range cfg.InitialState.Actors {
			if id == "" || strings.TrimSpace(id) != id || hasControl(id) {
				return Config{}, errors.New("initial_state.actors require exact ids")
			}
		}
	}
	return cfg, nil
}

func isLoopback(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
