package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/idmap"
	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

const (
	writerToken     = "writer-token-value"
	readerToken     = "reader-token-value"
	streamerToken   = "streamer-token-value"
	projectionToken = "projection-token-value"
	wrongScopeToken = "wrong-scope-token-value"
)

func tokenHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func testScope() store.Scope {
	return store.Scope{WorldID: "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-1"}
}

func baseConfig(t *testing.T, dir string) Config {
	t.Helper()
	wrong := store.Scope{WorldID: "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-2"}
	return Config{
		Listen:    "127.0.0.1:0",
		StorePath: filepath.Join(dir, "world.db"),
		Scope:     testScope(),
		Tokens: []TokenConfig{
			{ID: "writer", SHA256: tokenHash(writerToken), Scopes: []string{"read", "write", "stream"}},
			{ID: "reader", SHA256: tokenHash(readerToken), Scopes: []string{"read"}},
			{ID: "streamer", SHA256: tokenHash(streamerToken), Scopes: []string{"stream"}},
			{ID: "core", SHA256: tokenHash(projectionToken), Scopes: []string{"projection"}},
			{ID: "wrong", SHA256: tokenHash(wrongScopeToken), Scopes: []string{"read"}, Scope: &wrong},
		},
		CoreProjectionActors: []ProjectionActorConfig{
			{ActorID: "mio", ActorInstanceID: "core-instance-mio"},
			{ActorID: "shiro", ActorInstanceID: "core-instance-shiro"},
		},
		Page:             PageConfig{MaxEvents: 4, MaxBytes: 1 << 20},
		SSE:              SSEConfig{HeartbeatSeconds: 30, PollMillis: 20},
		AllowProbeAction: true,
		InitialState: &InitialStateConfig{
			Actors: map[string]store.ActorState{
				"mio":   {RoomID: "1f-living"},
				"shiro": {RoomID: "2f-room"},
			},
			Resources: map[string]int64{"chair": 1, "drink": 1},
		},
	}
}

func newTestServer(t *testing.T, mutate func(*Config)) (*httptest.Server, *Server) {
	t.Helper()
	cfg := baseConfig(t, t.TempDir())
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.store.Register("move", func(state store.State, payload json.RawMessage) (store.Outcome, error) {
		var v struct {
			ActorID string `json:"actor_id"`
			RoomID  string `json:"room_id"`
		}
		if err := json.Unmarshal(payload, &v); err != nil {
			return store.Outcome{}, err
		}
		if v.ActorID == "" || v.RoomID == "" {
			return store.Outcome{}, errors.New("missing actor or room")
		}
		state.Actors[v.ActorID] = store.ActorState{RoomID: v.RoomID}
		return store.Outcome{State: state, Events: []store.EventDraft{{Kind: "actor_moved", Payload: payload}}}, nil
	})
	s.store.Register("emit", func(state store.State, payload json.RawMessage) (store.Outcome, error) {
		var v struct {
			Count int `json:"count"`
		}
		if err := json.Unmarshal(payload, &v); err != nil {
			return store.Outcome{}, err
		}
		out := store.Outcome{State: state}
		for i := 0; i < v.Count; i++ {
			out.Events = append(out.Events, store.EventDraft{
				Kind:    "emitted",
				Payload: json.RawMessage(`{"n":` + itoa(i) + `}`),
			})
		}
		return out, nil
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = s.Close()
	})
	return ts, s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func doRequest(t *testing.T, method, url, token, key string, body []byte) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return raw
}

func decodeMap(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	raw := readBody(t, resp)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return out
}

func commandBody(t *testing.T, action string, payload any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"action": action, "payload": payload})
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	return raw
}

func eventsURL(ts *httptest.Server) string {
	return ts.URL + "/v1/worlds/rencrow-home/timelines/timeline-1/events"
}

func snapshotURL(ts *httptest.Server) string {
	return ts.URL + "/v1/worlds/rencrow-home/timelines/timeline-1/snapshot"
}

func commandsURL(ts *httptest.Server) string {
	return ts.URL + "/v1/worlds/rencrow-home/timelines/timeline-1/commands"
}

func streamURL(ts *httptest.Server) string {
	return ts.URL + "/v1/worlds/rencrow-home/timelines/timeline-1/stream"
}

func TestLoadConfigDefaultsAndValidation(t *testing.T) {
	dir := t.TempDir()
	cfg := baseConfig(t, dir)
	cfg.Page = PageConfig{}
	cfg.SSE = SSEConfig{}
	path := writeConfig(t, cfg)
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if loaded.Page.MaxEvents != store.DefaultPageEvents || loaded.Page.MaxBytes != 2*1024*1024 {
		t.Fatalf("page defaults = %+v", loaded.Page)
	}
	if loaded.SSE.HeartbeatSeconds != 15 || loaded.SSE.PollMillis != 200 {
		t.Fatalf("sse defaults = %+v", loaded.SSE)
	}

	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"missing listen", func(c *Config) { c.Listen = "" }},
		{"remote listen", func(c *Config) { c.Listen = "0.0.0.0:1234" }},
		{"missing store", func(c *Config) { c.StorePath = "" }},
		{"missing scope", func(c *Config) { c.Scope = store.Scope{} }},
		{"no tokens", func(c *Config) { c.Tokens = nil }},
		{"bad hash", func(c *Config) { c.Tokens[0].SHA256 = "not-hex" }},
		{"duplicate id", func(c *Config) { c.Tokens[1].ID = c.Tokens[0].ID }},
		{"bad token scope", func(c *Config) { c.Tokens[0].Scope = &store.Scope{WorldID: "x"} }},
		{"bad token scope name", func(c *Config) { c.Tokens[0].Scopes = []string{"admin"} }},
		{"projection without binding", func(c *Config) { c.CoreProjectionActors = nil }},
		{"duplicate projection actor", func(c *Config) { c.CoreProjectionActors[1].ActorID = c.CoreProjectionActors[0].ActorID }},
		{"duplicate projection instance", func(c *Config) { c.CoreProjectionActors[1].ActorInstanceID = c.CoreProjectionActors[0].ActorInstanceID }},
		{"bad projection actor", func(c *Config) { c.CoreProjectionActors[0].ActorID = "" }},
		{"page too large", func(c *Config) { c.Page.MaxEvents = 501 }},
		{"poll too small", func(c *Config) { c.SSE.PollMillis = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig(t, dir)
			tc.mutate(&c)
			if _, err := LoadConfig(writeConfig(t, c)); err == nil {
				t.Fatalf("expected error")
			}
		})
	}

	remote := baseConfig(t, dir)
	remote.Listen = "0.0.0.0:1234"
	remote.AllowRemote = true
	if _, err := LoadConfig(writeConfig(t, remote)); err != nil {
		t.Fatalf("remote with allow_remote: %v", err)
	}

	unknown := baseConfig(t, dir)
	raw, _ := json.Marshal(unknown)
	raw = append(raw[:len(raw)-1], []byte(`,"unexpected":true}`)...)
	p := filepath.Join(dir, "unknown.json")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func writeConfig(t *testing.T, cfg Config) string {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestHealthAndReadiness(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	if resp := doRequest(t, http.MethodGet, ts.URL+"/health/live", "", "", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("live = %d", resp.StatusCode)
	}
	if resp := doRequest(t, http.MethodGet, ts.URL+"/health/ready", "", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ready without token = %d", resp.StatusCode)
	}
	resp := doRequest(t, http.MethodGet, ts.URL+"/health/ready", readerToken, "", nil)
	body := decodeMap(t, resp)
	if resp.StatusCode != http.StatusOK || body["status"] != "ready" || body["state_seq"].(float64) != 0 {
		t.Fatalf("ready = %d %v", resp.StatusCode, body)
	}
	resp = doRequest(t, http.MethodGet, ts.URL+"/health/ready", wrongScopeToken, "", nil)
	if body := decodeMap(t, resp); resp.StatusCode != http.StatusForbidden || body["error"].(map[string]any)["code"] != "scope_mismatch" {
		t.Fatalf("wrong scope ready = %d %v", resp.StatusCode, body)
	}
}

func TestReadinessWithoutTimeline(t *testing.T) {
	cfg := baseConfig(t, t.TempDir())
	cfg.InitialState = nil
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	resp := doRequest(t, http.MethodGet, ts.URL+"/health/ready", readerToken, "", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ready = %d", resp.StatusCode)
	}
}

func TestSnapshotAuthScopeAndMethod(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	resp := doRequest(t, http.MethodGet, snapshotURL(ts), readerToken, "", nil)
	body := decodeMap(t, resp)
	if resp.StatusCode != http.StatusOK || body["world_id"] != "rencrow-home" || body["timeline_epoch"] != "epoch-1" {
		t.Fatalf("snapshot = %d %v", resp.StatusCode, body)
	}
	state := body["state"].(map[string]any)
	actors := state["actors"].(map[string]any)
	if actors["mio"].(map[string]any)["room_id"] != "1f-living" {
		t.Fatalf("state = %v", state)
	}
	if resp := doRequest(t, http.MethodGet, snapshotURL(ts), streamerToken, "", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("streamer snapshot = %d", resp.StatusCode)
	}
	if resp := doRequest(t, http.MethodGet, snapshotURL(ts), wrongScopeToken, "", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong scope snapshot = %d", resp.StatusCode)
	}
	wrongPath := ts.URL + "/v1/worlds/other/timelines/timeline-1/snapshot"
	if resp := doRequest(t, http.MethodGet, wrongPath, readerToken, "", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong path = %d", resp.StatusCode)
	}
	if resp := doRequest(t, http.MethodPost, snapshotURL(ts), readerToken, "", []byte(`{}`)); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("method = %d", resp.StatusCode)
	}
	if resp := doRequest(t, http.MethodGet, ts.URL+"/nope", readerToken, "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown route = %d", resp.StatusCode)
	}
}

func TestASCIITransportPathScope(t *testing.T) {
	ts, _ := newTestServer(t, func(c *Config) {
		c.Scope = store.Scope{WorldID: "世界", TimelineID: "時間", TimelineEpoch: "epoch-1"}
	})
	world, err := idmap.Encode("世界")
	if err != nil {
		t.Fatalf("encode world: %v", err)
	}
	timeline, err := idmap.Encode("時間")
	if err != nil {
		t.Fatalf("encode timeline: %v", err)
	}
	path := ts.URL + "/v1/worlds/" + world + "/timelines/" + timeline + "/snapshot"
	resp := doRequest(t, http.MethodGet, path, readerToken, "", nil)
	body := decodeMap(t, resp)
	if resp.StatusCode != http.StatusOK || body["world_id"] != "世界" || body["timeline_id"] != "時間" {
		t.Fatalf("transport path = %d %v", resp.StatusCode, body)
	}
	raw := ts.URL + "/v1/worlds/世界/timelines/時間/snapshot"
	resp = doRequest(t, http.MethodGet, raw, readerToken, "", nil)
	if errBody := decodeMap(t, resp); resp.StatusCode != http.StatusBadRequest || errBody["error"].(map[string]any)["code"] != "invalid_scope_id" {
		t.Fatalf("raw unicode path = %d %v", resp.StatusCode, errBody)
	}
}

func TestCommandIdempotencyConflictAndAuth(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	body := commandBody(t, "move", map[string]any{"actor_id": "mio", "room_id": "1f-kitchen"})
	resp := doRequest(t, http.MethodPost, commandsURL(ts), writerToken, "key-1", body)
	first := decodeMap(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first = %d %v", resp.StatusCode, first)
	}
	receipt := first["receipt"].(map[string]any)
	if receipt["status"] != "committed" || receipt["state_seq"].(float64) != 1 {
		t.Fatalf("receipt = %v", receipt)
	}
	firstID := receipt["receipt_id"].(string)

	resp = doRequest(t, http.MethodPost, commandsURL(ts), writerToken, "key-1", body)
	again := decodeMap(t, resp)
	if resp.StatusCode != http.StatusOK || again["duplicate"] != true {
		t.Fatalf("retry = %d %v", resp.StatusCode, again)
	}
	if again["receipt"].(map[string]any)["receipt_id"] != firstID {
		t.Fatalf("retry receipt changed")
	}

	other := commandBody(t, "move", map[string]any{"actor_id": "mio", "room_id": "2f-room"})
	resp = doRequest(t, http.MethodPost, commandsURL(ts), writerToken, "key-1", other)
	if errBody := decodeMap(t, resp); resp.StatusCode != http.StatusConflict || errBody["error"].(map[string]any)["code"] != "idempotency_conflict" {
		t.Fatalf("conflict = %d %v", resp.StatusCode, errBody)
	}

	cases := []struct {
		name  string
		token string
		key   string
		body  []byte
		want  int
	}{
		{"no auth", "", "k", body, http.StatusUnauthorized},
		{"read only", readerToken, "k", body, http.StatusForbidden},
		{"missing key", writerToken, "", body, http.StatusBadRequest},
		{"invalid json", writerToken, "k2", []byte(`{`), http.StatusBadRequest},
		{"unknown action", writerToken, "k3", commandBody(t, "nope", map[string]any{}), http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := doRequest(t, http.MethodPost, commandsURL(ts), tc.token, tc.key, tc.body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d (%s)", resp.StatusCode, tc.want, readBody(t, resp))
			}
		})
	}
	if resp := doRequest(t, http.MethodPost, commandsURL(ts), writerToken, "k4", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("new key = %d", resp.StatusCode)
	}
}

func TestCommandCASAndLimits(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	zero := uint64(0)
	body, _ := json.Marshal(map[string]any{
		"action": "move", "payload": map[string]any{"actor_id": "mio", "room_id": "1f-kitchen"},
		"expected_state_seq": zero,
	})
	resp := doRequest(t, http.MethodPost, commandsURL(ts), writerToken, "cas-1", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cas first = %d", resp.StatusCode)
	}
	resp = doRequest(t, http.MethodPost, commandsURL(ts), writerToken, "cas-2", body)
	rejected := decodeMap(t, resp)
	if resp.StatusCode != http.StatusConflict || rejected["receipt"].(map[string]any)["status"] != "rejected" {
		t.Fatalf("cas stale = %d %v", resp.StatusCode, rejected)
	}
	if rejected["receipt"].(map[string]any)["reason"] != "state_conflict" {
		t.Fatalf("cas reason = %v", rejected["receipt"])
	}

	large := bytes.Repeat([]byte("x"), 300*1024)
	big, _ := json.Marshal(map[string]any{"action": "move", "payload": string(large)})
	if resp := doRequest(t, http.MethodPost, commandsURL(ts), writerToken, "big", big); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("big = %d", resp.StatusCode)
	}
	if resp := doRequest(t, http.MethodPost, commandsURL(ts), writerToken, "many", commandBody(t, "emit", map[string]any{"count": 129})); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("many = %d", resp.StatusCode)
	}
}

func TestEventsPaginationAndReceiptLookup(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	for i, key := range []string{"emit-1", "emit-2"} {
		count := 3
		if i == 1 {
			count = 2
		}
		resp := doRequest(t, http.MethodPost, commandsURL(ts), writerToken, key, commandBody(t, "emit", map[string]any{"count": count}))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("emit %d = %d", i, resp.StatusCode)
		}
	}
	resp := doRequest(t, http.MethodGet, eventsURL(ts)+"?limit=4", readerToken, "", nil)
	first := decodeMap(t, resp)
	if resp.StatusCode != http.StatusOK || len(first["events"].([]any)) != 3 || first["done"] != false {
		t.Fatalf("page1 = %d %v", resp.StatusCode, first)
	}
	if first["next_seq"].(float64) != 3 {
		t.Fatalf("page1 next = %v", first["next_seq"])
	}
	resp = doRequest(t, http.MethodGet, eventsURL(ts)+"?after_seq=3&limit=4", readerToken, "", nil)
	second := decodeMap(t, resp)
	if resp.StatusCode != http.StatusOK || len(second["events"].([]any)) != 2 || second["done"] != true {
		t.Fatalf("page2 = %d %v", resp.StatusCode, second)
	}
	for _, query := range []string{"?limit=0", "?limit=5", "?after_seq=abc"} {
		if resp := doRequest(t, http.MethodGet, eventsURL(ts)+query, readerToken, "", nil); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("query %s = %d", query, resp.StatusCode)
		}
	}
	// Read one receipt by asking the Store through a fresh command response.
	resp = doRequest(t, http.MethodPost, commandsURL(ts), writerToken, "receipt-source", commandBody(t, "move", map[string]any{"actor_id": "mio", "room_id": "2f-room"}))
	committed := decodeMap(t, resp)["receipt"].(map[string]any)
	id := committed["receipt_id"].(string)
	resp = doRequest(t, http.MethodGet, ts.URL+"/v1/worlds/rencrow-home/timelines/timeline-1/receipts/"+id, readerToken, "", nil)
	got := decodeMap(t, resp)
	if resp.StatusCode != http.StatusOK || got["receipt"].(map[string]any)["receipt_id"] != id {
		t.Fatalf("receipt = %d %v", resp.StatusCode, got)
	}
	if resp := doRequest(t, http.MethodGet, ts.URL+"/v1/worlds/rencrow-home/timelines/timeline-1/receipts/rcp_missing", readerToken, "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing receipt = %d", resp.StatusCode)
	}
}

type sseReader struct {
	lines  chan string
	cancel context.CancelFunc
}

func openSSE(t *testing.T, url, token, lastID string) *sseReader {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("sse request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("sse do: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		t.Fatalf("sse status = %d: %s", resp.StatusCode, raw)
	}
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	t.Cleanup(func() {
		cancel()
		resp.Body.Close()
	})
	return &sseReader{lines: lines, cancel: cancel}
}

func (s *sseReader) waitFor(t *testing.T, substr string) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-s.lines:
			if !ok {
				t.Fatalf("stream closed before %q", substr)
			}
			if strings.Contains(line, substr) {
				return line
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q", substr)
		}
	}
}

func TestStreamSnapshotThenLiveEvent(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	stream := openSSE(t, streamURL(ts), writerToken, "")
	stream.waitFor(t, "event: snapshot")
	resp := doRequest(t, http.MethodPost, commandsURL(ts), writerToken, "live-1", commandBody(t, "emit", map[string]any{"count": 1}))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("emit = %d", resp.StatusCode)
	}
	if line := stream.waitFor(t, "event: world.event"); line == "" {
		t.Fatal("missing event")
	}
	stream.waitFor(t, `"seq":1`)
}

func TestStreamResumesAndResyncs(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	if resp := doRequest(t, http.MethodPost, commandsURL(ts), writerToken, "s-1", commandBody(t, "emit", map[string]any{"count": 3})); resp.StatusCode != http.StatusOK {
		t.Fatalf("emit = %d", resp.StatusCode)
	}

	ahead := openSSE(t, streamURL(ts), writerToken, "99")
	ahead.waitFor(t, "event: resync")
	ahead.waitFor(t, `"reason":"cursor_ahead"`)

	inside := openSSE(t, streamURL(ts), writerToken, "1")
	inside.waitFor(t, "event: resync")
	inside.waitFor(t, `"reason":"cursor_not_boundary"`)

	resume := openSSE(t, streamURL(ts), writerToken, "3")
	linesUntilEvent := make(chan []string, 1)
	go func() {
		var got []string
		for line := range resume.lines {
			got = append(got, line)
			if strings.HasPrefix(line, "event: world.event") {
				break
			}
		}
		linesUntilEvent <- got
	}()
	if resp := doRequest(t, http.MethodPost, commandsURL(ts), writerToken, "s-2", commandBody(t, "emit", map[string]any{"count": 1})); resp.StatusCode != http.StatusOK {
		t.Fatalf("emit2 = %d", resp.StatusCode)
	}
	select {
	case got := <-linesUntilEvent:
		for _, line := range got {
			if line == "event: snapshot" {
				t.Fatal("resume unexpectedly sent a snapshot")
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for resumed event")
	}
}

func TestStreamAuthAndCursorErrors(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	if resp := doRequest(t, http.MethodGet, streamURL(ts), readerToken, "", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read token stream = %d", resp.StatusCode)
	}
	if resp := doRequest(t, http.MethodGet, streamURL(ts), wrongScopeToken, "", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong scope stream = %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, streamURL(ts), nil)
	req.Header.Set("Authorization", "Bearer "+writerToken)
	req.Header.Set("Last-Event-ID", "not-a-number")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cursor do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid cursor = %d", resp.StatusCode)
	}
}
