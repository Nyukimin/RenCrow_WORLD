package life

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// Failure / Problem: docs/R10.md records that the three life Activity lifecycle
// actions are registered `store.Handler`s on the canonical Command -> Handler ->
// Receipt route, while no runtime route registers them, because a running WORLD
// must not apply them to WORLD State or a real Actor before R10 item 3 (a single
// life controller owning all movement and occupancy for one Actor) and R10 item 5
// (real CORE Outbox E2E through the canonical route) close. That was a
// documentation-only guarantee, so nothing stopped a later change from wiring the
// actions and turning an undecided boundary into live behaviour.
//
// Cause: an invariant about what production wiring may not do yet had no
// enforcement, only prose.
//
// Lesson / Invariant: while R10 items 3 and 5 stay open, no production file
// outside the owner rule file `internal/life/commands.go` may name a lifecycle
// action. Wiring them is a decision that needs the movement/controller owner and
// the CORE Outbox evidence, not a call to `Store.Register`.
//
// Enforcement: this test scans the repository's production sources. It is
// syntactic, so the allowlist below is part of the contract: wiring needs an
// entry that names the R10 items it closed, and a stale entry fails.
//
// Tests: TestLifecycleActionsStayOffProductionRoutes (current tree, empty
// allowlist) and TestLifecycleWiringGuardDetectsWiring (the scan is not vacuous:
// a fixture route that registers the actions is reported).

// lifecycleActionMarkers name the three registered lifecycle actions, as both
// the literal action names and the exported constants that carry them.
var lifecycleActionMarkers = []string{
	`"life.activity.start"`,
	`"life.activity.stop"`,
	`"life.occupancy.release"`,
	"ActionActivityStart",
	"ActionActivityStop",
	"ActionOccupancyRelease",
}

// lifecycleOwnerFile declares the actions and their handlers, so it is the only
// production file allowed to name them today.
const lifecycleOwnerFile = "internal/life/commands.go"

// lifecycleWiringAllowlist maps a repo-relative production file to the reason it
// may name a lifecycle action. It is empty while R10 items 3 and 5 stay open,
// and every entry must name the evidence that closed them.
var lifecycleWiringAllowlist = map[string]string{}

func TestLifecycleActionsStayOffProductionRoutes(t *testing.T) {
	root := worldRepoRoot(t)
	violations, err := lifecycleWiringViolations(root)
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}
	if len(violations) > 0 {
		t.Errorf("production code registers life lifecycle actions; R10 items 3 and 5 must close first (docs/R10.md):\n%s",
			strings.Join(violations, "\n"))
	}
	for path := range lifecycleWiringAllowlist {
		t.Errorf("stale lifecycle wiring allowlist entry %q names a file that no longer names a lifecycle action", path)
	}
}

func TestLifecycleWiringGuardDetectsWiring(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module fixture\n")
	writeFixture(t, root, "internal/life/commands.go", "package life\n\nconst ActionActivityStart = \"life.activity.start\"\n")
	writeFixture(t, root, "internal/httpapi/server.go", "package httpapi\n\nfunc wire() { register(ActionActivityStart) }\n")
	writeFixture(t, root, "internal/httpapi/server_test.go", "package httpapi\n\nconst marker = \"life.activity.stop\"\n")

	violations, err := lifecycleWiringViolations(root)
	if err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("violations = %v, want only the wiring in internal/httpapi/server.go", violations)
	}
	if !strings.Contains(violations[0], "internal/httpapi/server.go") || !strings.Contains(violations[0], "ActionActivityStart") {
		t.Fatalf("violation = %q, want the wiring file and the action it names", violations[0])
	}
}

// Failure / Problem: `SelectCandidate` connects WORLD's owner catalog to the R10
// controller logic, and docs/R10.md records that no route or real Actor consumes
// that selection while R10 items 1 (geometry evidence) and 3 (a single life
// controller owning movement and occupancy) stay open. Prose alone would not
// stop a later change from starting activities from a route.
//
// Cause: the boundary between a selectable candidate and a started activity had
// no enforcement outside the controller's own refusals.
//
// Lesson / Invariant: production code outside `internal/life/` may not consume
// candidate selection until the movement owner and its measured evidence close.
//
// Enforcement: this test scans production sources for the selection call and
// keeps an evidence-bearing allowlist, so a new consumer fails and a stale entry
// fails.
//
// Tests: TestCandidateSelectionStaysOffProductionRoutes (current tree, empty
// allowlist) and TestCandidateSelectionGuardDetectsConsumer (the scan is not
// vacuous: a fixture route that starts a selected candidate is reported).

// selectionConsumerMarker names the catalog-side selection entry point.
const selectionConsumerMarker = "SelectCandidate("

// selectionOwnerDir owns the planning and controller logic, so files inside it
// may consume selection.
const selectionOwnerDir = "internal/life/"

// selectionConsumerAllowlist maps a production file outside the owner directory
// to the reason it may consume selection. It is empty while R10 items 1 and 3
// stay open, and every entry must name the evidence that closed them.
var selectionConsumerAllowlist = map[string]string{}

func TestCandidateSelectionStaysOffProductionRoutes(t *testing.T) {
	root := worldRepoRoot(t)
	violations, err := selectionConsumerViolations(root)
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}
	if len(violations) > 0 {
		t.Errorf("production code outside %s consumes candidate selection; R10 items 1 and 3 must close first (docs/R10.md):\n%s",
			selectionOwnerDir, strings.Join(violations, "\n"))
	}
	for path := range selectionConsumerAllowlist {
		t.Errorf("stale selection consumer allowlist entry %q names a file that no longer consumes selection", path)
	}
}

func TestCandidateSelectionGuardDetectsConsumer(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module fixture\n")
	writeFixture(t, root, "internal/life/plan.go", "package life\n\nfunc SelectCandidate(a Assessment) (Candidate, RunningActivity, error) { return Candidate{}, RunningActivity{}, nil }\n")
	writeFixture(t, root, "internal/life/plan_test.go", "package life\n\nvar _ = SelectCandidate(\n")
	writeFixture(t, root, "internal/httpapi/server.go", "package httpapi\n\nfunc wire() { candidate, _, _ := life.SelectCandidate(assessment); start(candidate) }\n")

	violations, err := selectionConsumerViolations(root)
	if err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("violations = %v, want only the consumer in internal/httpapi/server.go", violations)
	}
	if !strings.Contains(violations[0], "internal/httpapi/server.go") {
		t.Fatalf("violation = %q, want the file that starts a selected candidate", violations[0])
	}
}

func TestCandidateSelectionAllowlistRequiresEvidence(t *testing.T) {
	for path, reason := range selectionConsumerAllowlist {
		if !strings.Contains(reason, "R10") {
			t.Errorf("allowlist entry %q must name the R10 items it closes; reason = %q", path, reason)
		}
	}
}

// selectionConsumerViolations lists every production file under root that calls
// candidate selection outside the owner directory and outside the allowlist.
func selectionConsumerViolations(root string) ([]string, error) {
	var violations []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "Tmp", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, selectionOwnerDir) {
			return nil
		}
		if _, allowed := selectionConsumerAllowlist[rel]; allowed {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(content), selectionConsumerMarker) {
			violations = append(violations, fmt.Sprintf("%s calls %s", rel, selectionConsumerMarker))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(violations)
	return violations, nil
}

func TestLifecycleWiringAllowlistRequiresEvidence(t *testing.T) {
	for path, reason := range lifecycleWiringAllowlist {
		if !strings.Contains(reason, "R10") {
			t.Errorf("allowlist entry %q must name the R10 items it closes; reason = %q", path, reason)
		}
	}
}

// lifecycleWiringViolations lists every production file under root that names a
// lifecycle action outside the owner rule file and outside the allowlist. Test
// sources and vendored or cached trees are not production wiring.
func lifecycleWiringViolations(root string) ([]string, error) {
	var violations []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "Tmp", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == lifecycleOwnerFile {
			return nil
		}
		reason, allowed := lifecycleWiringAllowlist[rel]
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(content)
		for _, marker := range lifecycleActionMarkers {
			if !strings.Contains(text, marker) {
				continue
			}
			if allowed {
				_ = reason
				return nil
			}
			violations = append(violations, fmt.Sprintf("%s names %s", rel, marker))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(violations)
	return violations, nil
}

func writeFixture(t *testing.T, root string, rel string, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func worldRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test file")
		}
		dir = parent
	}
}
