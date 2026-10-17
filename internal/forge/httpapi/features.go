package httpapi

import (
	"net/http"
	"slices"
)

type Features struct{ enabled []string }

func NewFeatures(enabled []string) Features { return Features{enabled: slices.Clone(enabled)} }

// handleFeatures: GET /api/v1/features — the flags on for this forge, so
// the pages show only what the operator turned on.
func (h Features) List(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, 200, FeatureList{Features: h.enabled})
}

// FeatureList is the feature flags this forge has on.
type FeatureList struct {
	Features []string `json:"features"`
}
