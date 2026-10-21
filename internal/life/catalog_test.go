package life

import (
	"sort"
	"testing"
)

func TestDefaultCatalogRegistersCOREActivityVocabulary(t *testing.T) {
	want := []string{
		"change_clothes", "exercise", "idle", "read",
		"rest", "sit", "sleep", "walk",
	}
	got := RegisteredNonCognitiveActivityIDs()
	if len(got) != len(want) {
		t.Fatalf("activity IDs = %v, want %v", got, want)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("activity IDs are not sorted: %v", got)
	}
	catalog := DefaultCatalog()
	for _, id := range want {
		if got, want := catalog.IsRegisteredNonCognitive(id), true; got != want {
			t.Fatalf("IsRegisteredNonCognitive(%q) = %v, want %v", id, got, want)
		}
	}
	if catalog.IsRegisteredNonCognitive("stand") {
		t.Fatal("unregistered activity was granted")
	}
}

func TestAssessAcceptsRegisteredLifePolicyVocabulary(t *testing.T) {
	allowed := RegisteredNonCognitiveActivityIDs()
	got := AssessForTest(t, nil, policy(future(), allowed, []string{"rest"}), DefaultCatalog())
	if got.PolicyState != PolicyFresh || !got.NewActivityAllowed {
		t.Fatalf("policy = %+v", got)
	}
	if len(got.PermittedActivityIDs) != len(allowed) {
		t.Fatalf("permitted = %v, want %v", got.PermittedActivityIDs, allowed)
	}
}
