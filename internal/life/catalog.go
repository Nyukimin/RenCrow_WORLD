package life

import "sort"

// registeredNonCognitiveActivityIDs is WORLD's registration of CORE's
// non-cognitive LifePolicy vocabulary. CORE owns the IDs and their meaning;
// WORLD registers the concrete posture, motion, and occupancy mapping for each
// ID. Adding or removing an ID is a cross-repo contract change, not a local
// preference. Unknown IDs are never granted.
var registeredNonCognitiveActivityIDs = []string{
	"change_clothes",
	"exercise",
	"idle",
	"read",
	"rest",
	"sit",
	"sleep",
	"walk",
}

// RegisteredNonCognitiveActivityIDs returns a sorted copy of the registered
// activity IDs.
func RegisteredNonCognitiveActivityIDs() []string {
	out := append([]string(nil), registeredNonCognitiveActivityIDs...)
	sort.Strings(out)
	return out
}

// DefaultCatalog is the production non-cognitive activity catalog registered
// by WORLD. An empty or nil catalog cannot grant activity.
func DefaultCatalog() Catalog {
	catalog := make(StaticCatalog, len(registeredNonCognitiveActivityIDs))
	for _, id := range registeredNonCognitiveActivityIDs {
		catalog[id] = true
	}
	return catalog
}
