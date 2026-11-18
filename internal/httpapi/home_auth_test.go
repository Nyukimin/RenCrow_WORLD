package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_WORLD/internal/life"
	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

const lifeToken = "life-core-token-value"
const visitToken = "world-home-visit-token-value"

func TestVisitCredentialIsDedicatedAndCanOnlyAdmitOrDepartLumina(t *testing.T) {
	dir := t.TempDir()
	layoutPath := filepath.Join(dir, "home-layout.json")
	layoutJSON := `{"schema_version":"world.home-layout/v1","scene_sha256":"` + strings.Repeat("a", 64) + `","room_anchors":{"dining":[10,0,0]},"paths":[],"private_rooms":{},"slots":[]}`
	if err := os.WriteFile(layoutPath, []byte(layoutJSON), 0o600); err != nil {
		t.Fatalf("write layout: %v", err)
	}
	cfg := baseConfig(t, dir)
	cfg.HomeLayoutPath = layoutPath
	cfg.Tokens = append(cfg.Tokens, TokenConfig{
		ID: "living-visit-owner", SHA256: tokenHash(visitToken), Scopes: []string{"visit"},
	})
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = s.Close()
	})

	admitBody := commandBody(t, life.ActionHomeVisitAdmit, map[string]any{
		"actor_id": life.HomeVisitorID, "visit_id": "living-visit-1", "valid_until": nowUTC().Add(5 * time.Minute).Format(time.RFC3339Nano),
	})
	admitted := doRequest(t, http.MethodPost, commandsURL(ts), visitToken, "visit-admit-1", admitBody)
	if admitted.StatusCode != http.StatusOK {
		t.Fatalf("visit admission status=%d body=%s", admitted.StatusCode, readBody(t, admitted))
	}
	_ = admitted.Body.Close()

	departBody := commandBody(t, life.ActionHomeVisitDepart, map[string]any{
		"actor_id": life.HomeVisitorID, "visit_id": "living-visit-1",
	})
	denied := doRequest(t, http.MethodPost, commandsURL(ts), writerToken, "visit-depart-denied", departBody)
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("write credential used visit action: status=%d body=%s", denied.StatusCode, readBody(t, denied))
	}
	_ = denied.Body.Close()
	standard := doRequest(t, http.MethodPost, commandsURL(ts), visitToken, "visit-standard-denied",
		commandBody(t, "debug.noop", map[string]any{"should": "be denied"}))
	if standard.StatusCode != http.StatusForbidden {
		t.Fatalf("visit credential used standard action: status=%d body=%s", standard.StatusCode, readBody(t, standard))
	}
	_ = standard.Body.Close()
	departed := doRequest(t, http.MethodPost, commandsURL(ts), visitToken, "visit-depart-1", departBody)
	if departed.StatusCode != http.StatusOK {
		t.Fatalf("visit departure status=%d body=%s", departed.StatusCode, readBody(t, departed))
	}
	_ = departed.Body.Close()
	state, _, _, err := s.store.Snapshot(cfg.Scope)
	if err != nil {
		t.Fatalf("snapshot after departure: %v", err)
	}
	if _, present := state.Actors[life.HomeVisitorID]; present {
		t.Fatal("Lumina remained present after visit departure")
	}
}

func TestHomeCommandsUseSeparateLifeCredentialActorScopeAndCanonicalReceipt(t *testing.T) {
	dir := t.TempDir()
	layoutPath := filepath.Join(dir, "home-layout.json")
	layoutJSON := `{"schema_version":"world.home-layout/v1","scene_sha256":"` + strings.Repeat("a", 64) + `","room_anchors":{"living":[0,0,0],"bedroom":[10,0,0]},"paths":[{"id":"living-bedroom","from_room_id":"living","to_room_id":"bedroom","points":[[5,0,0]],"duration_ms":1000}],"private_rooms":{"kuro":"bedroom"},"slots":[{"id":"bed-kuro","kind":"bed","actor_id":"kuro","room_id":"bedroom","anchor":[10,0,0]}]}`
	if err := os.WriteFile(layoutPath, []byte(layoutJSON), 0o600); err != nil {
		t.Fatalf("write layout: %v", err)
	}
	cfg := baseConfig(t, dir)
	cfg.HomeLayoutPath = layoutPath
	cfg.Tokens = append(cfg.Tokens, TokenConfig{
		ID: "life-core", PublisherID: "life-core-publisher", SHA256: tokenHash(lifeToken),
		Scopes: []string{"life"}, LifeActorIDs: []string{"kuro"},
	})
	position := [3]float64{0, 0, 0}
	cfg.InitialState.Actors["kuro"] = store.ActorState{RoomID: "living", Position: &position}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = s.Close()
	})

	body := commandBody(t, life.ActionHomeMove, map[string]any{"actor_id": "kuro", "room_id": "bedroom"})
	first := doRequest(t, http.MethodPost, commandsURL(ts), lifeToken, "home-move-1", body)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("life command status=%d body=%s", first.StatusCode, readBody(t, first))
	}
	var firstReceipt receiptBody
	if err := json.NewDecoder(first.Body).Decode(&firstReceipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if firstReceipt.Receipt.ReceiptID == "" || firstReceipt.Receipt.Status != "committed" || len(firstReceipt.Receipt.EventIDs) != 1 {
		t.Fatalf("receipt missing durable outcome: %+v", firstReceipt.Receipt)
	}
	snapshot := doRequest(t, http.MethodGet, snapshotURL(ts), writerToken, "", nil)
	if snapshot.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status=%d body=%s", snapshot.StatusCode, readBody(t, snapshot))
	}
	snapshotBody := decodeMap(t, snapshot)
	if snapshotBody["home_scene_sha256"] != strings.Repeat("a", 64) {
		t.Fatalf("snapshot home scene digest = %v", snapshotBody["home_scene_sha256"])
	}
	moveMap, ok := snapshotBody["home_move_points"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot lacks move points map: %#v", snapshotBody["home_move_points"])
	}
	movePoints, ok := moveMap["kuro"].([]any)
	if !ok || len(movePoints) != 3 {
		t.Fatalf("snapshot lacks the current measured route: %#v", snapshotBody["home_move_points"])
	}

	duplicate := doRequest(t, http.MethodPost, commandsURL(ts), lifeToken, "home-move-1", body)
	if duplicate.StatusCode != http.StatusOK {
		t.Fatalf("duplicate status=%d body=%s", duplicate.StatusCode, readBody(t, duplicate))
	}
	var duplicateBody receiptBody
	if err := json.NewDecoder(duplicate.Body).Decode(&duplicateBody); err != nil {
		t.Fatalf("decode duplicate: %v", err)
	}
	if !duplicateBody.Duplicate || duplicateBody.Receipt.ReceiptID != firstReceipt.Receipt.ReceiptID {
		t.Fatalf("idempotent response = %+v, want same Receipt", duplicateBody)
	}

	otherActor := doRequest(t, http.MethodPost, commandsURL(ts), lifeToken, "home-mio", commandBody(t, life.ActionHomeMove, map[string]any{"actor_id": "mio", "room_id": "2f-room"}))
	if otherActor.StatusCode != http.StatusForbidden {
		t.Fatalf("life credential for another Actor status=%d body=%s", otherActor.StatusCode, readBody(t, otherActor))
	}
	wrongFamily := doRequest(t, http.MethodPost, commandsURL(ts), lifeToken, "life-debug", commandBody(t, "debug.noop", map[string]any{"hello": "world"}))
	if wrongFamily.StatusCode != http.StatusForbidden {
		t.Fatalf("life credential used standard action status=%d body=%s", wrongFamily.StatusCode, readBody(t, wrongFamily))
	}
	noLifeScope := doRequest(t, http.MethodPost, commandsURL(ts), writerToken, "writer-home", commandBody(t, life.ActionHomeMove, map[string]any{"actor_id": "kuro", "room_id": "bedroom"}))
	if noLifeScope.StatusCode != http.StatusForbidden {
		t.Fatalf("write credential used life action status=%d body=%s", noLifeScope.StatusCode, readBody(t, noLifeScope))
	}

	standard := doRequest(t, http.MethodPost, commandsURL(ts), writerToken, "writer-probe", commandBody(t, "debug.noop", map[string]any{"standard": true}))
	if standard.StatusCode != http.StatusOK {
		t.Fatalf("existing write path status=%d body=%s", standard.StatusCode, readBody(t, standard))
	}
	read := doRequest(t, http.MethodGet, snapshotURL(ts), lifeToken, "", nil)
	if read.StatusCode != http.StatusForbidden {
		t.Fatalf("life token unexpectedly gained read access: %d", read.StatusCode)
	}
}

func TestLifeCredentialConfigurationRequiresExactActorListAndLayout(t *testing.T) {
	cases := map[string]func(*Config){
		"no actors": func(cfg *Config) {
			cfg.Tokens = []TokenConfig{{ID: "life", SHA256: tokenHash(lifeToken), Scopes: []string{"life"}}}
		},
		"duplicate actors": func(cfg *Config) {
			cfg.Tokens = []TokenConfig{{ID: "life", SHA256: tokenHash(lifeToken), Scopes: []string{"life"}, LifeActorIDs: []string{"kuro", "kuro"}}}
		},
		"actor list without scope": func(cfg *Config) {
			cfg.Tokens = []TokenConfig{{ID: "writer", SHA256: tokenHash(writerToken), Scopes: []string{"write"}, LifeActorIDs: []string{"kuro"}}}
		},
		"life scope without layout": func(cfg *Config) {
			cfg.Tokens = []TokenConfig{{ID: "life", SHA256: tokenHash(lifeToken), Scopes: []string{"life"}, LifeActorIDs: []string{"kuro"}}}
		},
		"life and write scopes combined": func(cfg *Config) {
			cfg.HomeLayoutPath = filepath.Join(t.TempDir(), "layout.json")
			cfg.Tokens = []TokenConfig{{ID: "life", SHA256: tokenHash(lifeToken), Scopes: []string{"life", "write"}, LifeActorIDs: []string{"kuro"}}}
		},
		"visit scope without layout": func(cfg *Config) {
			cfg.Tokens = []TokenConfig{{ID: "visit", SHA256: tokenHash(visitToken), Scopes: []string{"visit"}}}
		},
		"visit scope must be dedicated": func(cfg *Config) {
			cfg.HomeLayoutPath = filepath.Join(t.TempDir(), "layout.json")
			cfg.Tokens = []TokenConfig{{ID: "visit", SHA256: tokenHash(visitToken), Scopes: []string{"visit", "write"}}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := baseConfig(t, t.TempDir())
			mutate(&cfg)
			if _, err := NormalizeConfig(cfg); err == nil {
				t.Fatal("invalid life credential configuration accepted")
			}
		})
	}
	validVisit := baseConfig(t, t.TempDir())
	validVisit.HomeLayoutPath = filepath.Join(t.TempDir(), "layout.json")
	validVisit.Tokens = append(validVisit.Tokens, TokenConfig{ID: "visit", SHA256: tokenHash(visitToken), Scopes: []string{"visit"}})
	if _, err := NormalizeConfig(validVisit); err != nil {
		t.Fatalf("dedicated visit credential with layout was rejected: %v", err)
	}
}

func TestHomeLayoutReloadRejectsOldArrivalButAllowsSafeStop(t *testing.T) {
	dir := t.TempDir()
	layoutPath := filepath.Join(dir, "home-layout.json")
	layoutJSON := `{"schema_version":"world.home-layout/v1","scene_sha256":"` + strings.Repeat("a", 64) + `","room_anchors":{"living":[0,0,0],"bedroom":[10,0,0]},"paths":[{"id":"living-bedroom","from_room_id":"living","to_room_id":"bedroom","points":[[5,0,0]],"duration_ms":1000}],"private_rooms":{"kuro":"bedroom"},"slots":[{"id":"bed-kuro","kind":"bed","actor_id":"kuro","room_id":"bedroom","anchor":[10,0,0]}]}`
	writeLayout := func(raw string) {
		t.Helper()
		if err := os.WriteFile(layoutPath, []byte(raw), 0o600); err != nil {
			t.Fatalf("write layout: %v", err)
		}
	}
	writeLayout(layoutJSON)
	cfg := baseConfig(t, dir)
	cfg.HomeLayoutPath = layoutPath
	cfg.Tokens = append(cfg.Tokens, TokenConfig{
		ID: "life-core", PublisherID: "life-core-publisher", SHA256: tokenHash(lifeToken),
		Scopes: []string{"life"}, LifeActorIDs: []string{"kuro"},
	})
	position := [3]float64{0, 0, 0}
	cfg.InitialState.Actors["kuro"] = store.ActorState{RoomID: "living", Position: &position}
	firstServer, err := New(cfg)
	if err != nil {
		t.Fatalf("New initial server: %v", err)
	}
	firstHTTP := httptest.NewServer(firstServer.Handler())
	startBody := commandBody(t, life.ActionHomeMove, map[string]any{"actor_id": "kuro", "room_id": "bedroom"})
	started := doRequest(t, http.MethodPost, commandsURL(firstHTTP), lifeToken, "move-start", startBody)
	if started.StatusCode != http.StatusOK {
		t.Fatalf("move start status=%d body=%s", started.StatusCode, readBody(t, started))
	}
	_ = started.Body.Close()
	state, _, _, err := firstServer.store.Snapshot(cfg.Scope)
	if err != nil {
		t.Fatalf("snapshot after move start: %v", err)
	}
	activityID := state.Actors["kuro"].ActivityID
	if activityID == "" {
		t.Fatal("move start did not persist an activity instance")
	}
	firstHTTP.Close()
	if err := firstServer.Close(); err != nil {
		t.Fatalf("close initial server: %v", err)
	}

	changedLayout := strings.Replace(layoutJSON, strings.Repeat("a", 64), strings.Repeat("b", 64), 1)
	writeLayout(changedLayout)
	reloaded, err := New(cfg)
	if err != nil {
		t.Fatalf("reload with changed operator layout must retain safe-stop availability: %v", err)
	}
	secondHTTP := httptest.NewServer(reloaded.Handler())
	t.Cleanup(func() {
		secondHTTP.Close()
		_ = reloaded.Close()
	})

	finishBody := commandBody(t, life.ActionHomeFinish, map[string]any{"actor_id": "kuro", "activity_id": activityID})
	finish := doRequest(t, http.MethodPost, commandsURL(secondHTTP), lifeToken, "old-route-finish", finishBody)
	if finish.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("old-layout arrival status=%d body=%s", finish.StatusCode, readBody(t, finish))
	}
	var finishResult receiptBody
	if err := json.NewDecoder(finish.Body).Decode(&finishResult); err != nil {
		t.Fatalf("decode rejected old-layout arrival: %v", err)
	}
	_ = finish.Body.Close()
	if finishResult.Receipt.Status != "rejected" {
		t.Fatalf("old-layout arrival did not return a rejected Receipt: %+v", finishResult.Receipt)
	}
	stopBody := commandBody(t, life.ActionHomeStop, map[string]any{"actor_id": "kuro", "activity_id": activityID})
	stopped := doRequest(t, http.MethodPost, commandsURL(secondHTTP), lifeToken, "old-route-stop", stopBody)
	if stopped.StatusCode != http.StatusOK {
		t.Fatalf("safe stop after layout change status=%d body=%s", stopped.StatusCode, readBody(t, stopped))
	}
	_ = stopped.Body.Close()
	state, _, _, err = reloaded.store.Snapshot(cfg.Scope)
	if err != nil {
		t.Fatalf("snapshot after safe stop: %v", err)
	}
	actor := state.Actors["kuro"]
	if actor.RoomID != "living" || actor.ActivityID != "" || actor.Position == nil || *actor.Position != position || len(state.Reservations) != 0 {
		t.Fatalf("safe stop fabricated arrival or leaked reservation: actor=%+v reservations=%v", actor, state.Reservations)
	}
}
