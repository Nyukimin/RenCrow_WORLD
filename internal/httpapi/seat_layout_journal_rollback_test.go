package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Nyukimin/RenCrow_WORLD/internal/store"
)

func seatLayoutVariants(t *testing.T, dir string) (string, string) {
	t.Helper()
	basePath := filepath.Join("testdata", "seat-capacity-old-layout.json")
	baseRaw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("read canonical house layout: %v", err)
	}
	var base map[string]json.RawMessage
	if err := json.Unmarshal(baseRaw, &base); err != nil {
		t.Fatalf("decode canonical layout: %v", err)
	}
	var slots []json.RawMessage
	if err := json.Unmarshal(base["slots"], &slots); err != nil {
		t.Fatalf("decode canonical slots: %v", err)
	}
	if len(slots) != 22 {
		t.Fatalf("old-layout fixture slot count=%d, want 22", len(slots))
	}
	restIDs := make(map[string]bool)
	for _, rawSlot := range slots {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawSlot, &fields); err != nil {
			t.Fatalf("decode old-layout slot: %v", err)
		}
		var id, kind string
		if err := json.Unmarshal(fields["id"], &id); err != nil {
			t.Fatalf("decode old-layout slot ID: %v", err)
		}
		if err := json.Unmarshal(fields["kind"], &kind); err != nil {
			t.Fatalf("decode old-layout slot kind: %v", err)
		}
		if kind == "rest" {
			restIDs[id] = true
		}
	}
	wantRestIDs := []string{
		"desk-mio", "desk-shiro", "desk-kuro", "desk-midori", "desk-aka", "desk-ao", "desk-kin", "desk-gin",
		"sofa", "sofa-guest", "dining", "dining-guest",
	}
	if len(restIDs) != len(wantRestIDs) {
		t.Fatalf("old-layout rest slots=%v, want the eight desk and four existing furniture slots", restIDs)
	}
	for _, id := range wantRestIDs {
		if !restIDs[id] {
			t.Fatalf("old-layout fixture is missing existing rest slot %q", id)
		}
	}
	var additions []json.RawMessage
	addRaw, err := os.ReadFile(filepath.Join("testdata", "seat-capacity-additions.json"))
	if err != nil {
		t.Fatalf("read measured-seat fixture: %v", err)
	}
	if err := json.Unmarshal(addRaw, &additions); err != nil || len(additions) != 4 {
		t.Fatalf("decode four measured seats: count=%d err=%v", len(additions), err)
	}
	oldPath := filepath.Join(dir, "home-layout-old.json")
	newPath := filepath.Join(dir, "home-layout-expanded.json")
	if err := os.WriteFile(oldPath, baseRaw, 0o600); err != nil {
		t.Fatalf("write old layout: %v", err)
	}
	slots = append(slots, additions...)
	if len(slots) != 26 {
		t.Fatalf("expanded slot count=%d, want 26", len(slots))
	}
	base["slots"], err = json.Marshal(slots)
	if err != nil {
		t.Fatalf("encode expanded slots: %v", err)
	}
	newRaw, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("encode expanded layout: %v", err)
	}
	if err := os.WriteFile(newPath, newRaw, 0o600); err != nil {
		t.Fatalf("write expanded layout: %v", err)
	}
	return oldPath, newPath
}

func TestFourSeatResourceReconcileCanReturnToOldLayoutWithoutJournalRestore(t *testing.T) {
	dir := t.TempDir()
	oldPath, expandedPath := seatLayoutVariants(t, dir)
	cfg := baseConfig(t, dir)
	cfg.HomeLayoutPath = oldPath
	first, err := New(cfg)
	if err != nil {
		t.Fatalf("start old layout: %v", err)
	}
	oldResources := first.home.ResourceCapacities()
	oldState, oldSeq, _, err := first.store.Snapshot(cfg.Scope)
	if err != nil {
		t.Fatalf("snapshot old layout: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close old layout: %v", err)
	}

	cfg.InitialState = nil
	cfg.HomeLayoutPath = expandedPath
	expanded, err := New(cfg)
	if err != nil {
		t.Fatalf("start expanded layout: %v", err)
	}
	newResources := expanded.home.ResourceCapacities()
	added := map[string]int64{}
	for id, cap := range newResources {
		if _, exists := oldResources[id]; !exists {
			added[id] = cap
		}
	}
	want := map[string]int64{
		"home-slot:dining-aisle-north":  1,
		"home-slot:dining-aisle-south":  1,
		"home-slot:dining-window-north": 1,
		"home-slot:dining-window-south": 1,
	}
	if !reflect.DeepEqual(added, want) {
		t.Fatalf("added resource capacities=%v, want %v", added, want)
	}
	newState, newSeq, newHash, err := expanded.store.Snapshot(cfg.Scope)
	if err != nil {
		t.Fatalf("snapshot expanded layout: %v", err)
	}
	if newSeq != oldSeq+1 {
		t.Fatalf("sequence after seat reconcile=%d, want %d", newSeq, oldSeq+1)
	}
	if !reflect.DeepEqual(newState.Actors, oldState.Actors) || !reflect.DeepEqual(newState.Activities, oldState.Activities) || !reflect.DeepEqual(newState.Reservations, oldState.Reservations) {
		t.Fatal("seat reconciliation changed actor/activity/reservation state")
	}
	for id, resource := range oldState.Resources {
		if got := newState.Resources[id]; got != resource {
			t.Fatalf("existing resource %s changed: %+v -> %+v", id, resource, got)
		}
	}
	for id, capacity := range added {
		if got := newState.Resources[id]; got.Capacity != capacity || got.Used != 0 {
			t.Fatalf("new seat resource %s=%+v", id, got)
		}
	}
	page, err := expanded.store.Page(cfg.Scope, oldSeq, store.DefaultPageEvents, 0)
	if err != nil || len(page.Events) != 1 || page.Events[0].Kind != "home.resources_reconciled" {
		t.Fatalf("journal reconcile events=%+v err=%v", page.Events, err)
	}
	if err := expanded.Close(); err != nil {
		t.Fatalf("close expanded layout: %v", err)
	}

	// Restore only the old layout file. Never restore or edit the journal/database.
	cfg.HomeLayoutPath = oldPath
	rolledBack, err := New(cfg)
	if err != nil {
		t.Fatalf("restart with old layout and retained resources: %v", err)
	}
	defer rolledBack.Close()
	state, seq, hash, err := rolledBack.store.Snapshot(cfg.Scope)
	if err != nil {
		t.Fatalf("snapshot after layout rollback: %v", err)
	}
	if seq != newSeq || hash != newHash || !reflect.DeepEqual(state, newState) {
		t.Fatalf("layout rollback changed persisted state: seq=%d/%d hash=%s/%s", seq, newSeq, hash, newHash)
	}
	for id, capacity := range added {
		if got := state.Resources[id]; got.Capacity != capacity || got.Used != 0 {
			t.Fatalf("journaled seat resource was lost or changed: %s=%+v", id, got)
		}
	}
	page, err = rolledBack.store.Page(cfg.Scope, oldSeq, store.DefaultPageEvents, 0)
	if err != nil || len(page.Events) != 1 || page.Events[0].Kind != "home.resources_reconciled" {
		t.Fatalf("old-layout restart changed reconcile journal: events=%+v err=%v", page.Events, err)
	}
	replayed, replayHash, replaySeq, err := rolledBack.store.Replay(cfg.Scope, seq)
	if err != nil || replaySeq != seq || replayHash != hash || !reflect.DeepEqual(replayed, state) {
		t.Fatalf("replay mismatch after layout rollback: seq=%d/%d hash=%s/%s err=%v", replaySeq, seq, replayHash, hash, err)
	}

	ts := httptest.NewServer(rolledBack.Handler())
	defer ts.Close()
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/health/ready", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+readerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("readiness after layout rollback: %v", err)
	}
	defer resp.Body.Close()
	var ready map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&ready); err != nil {
		t.Fatalf("decode readiness: %v", err)
	}
	if resp.StatusCode != http.StatusOK || ready["reconciled"] != true || ready["logical_ready"] != true || ready["state_seq"] != float64(seq) {
		t.Fatalf("readiness after rollback = %d %+v", resp.StatusCode, ready)
	}
}
