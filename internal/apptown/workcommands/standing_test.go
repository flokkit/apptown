package workcommands

import (
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"testing"
)

func TestStandingPurposeRequiresTheActualLifecycleRole(t *testing.T) {
	for _, role := range []string{"working", "record", "landing"} {
		for _, working := range []bool{false, true} {
			w := &adoption.ResolvedWorkflow{Name: "declared", Version: 1, Doc: &schema.Doc{Spec: schema.Spec{Role: role, Stages: []schema.Stage{{Name: "accept", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"standing": map[string]any{"class": "routine-update", "means": "accept this bounded update"}}}}}}}}}
			_, err := standingPurpose(w, working)
			want := role == "landing" && !working || role == "working" && working
			if (err == nil) != want {
				t.Fatalf("role=%s working=%v: %v", role, working, err)
			}
		}
	}
}
