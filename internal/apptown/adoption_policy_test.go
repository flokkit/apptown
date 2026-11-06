package apptown

import (
	"context"
	"testing"
)

// Policy wording follows the library on unmodified adoptions; a modified
// copy keeps its own words — they describe its own law.
func TestPolicyWordingFollowsLibrary(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	pID := plainTestProject(t, a.DB, "p", "P")
	// An unmodified stock adoption with stale wording, and a modified copy
	// (different parameters) with its own wording.
	if err := a.Adoption.InstallPolicy(t.Context(), pID, "build-requires-scan", "old words", "requireStepBefore", map[string]any{"uses": "build", "requires": "scan"}, "stock", "fixture"); err != nil {
		t.Fatal(err)
	}
	// A second project holds a MODIFIED copy under the same library name —
	// proving the config match is what guards, not the name alone.
	qID := plainTestProject(t, a.DB, "q", "Q")
	if err := a.Adoption.InstallPolicy(t.Context(), qID, "build-requires-scan", "my own words", "requireStepBefore", map[string]any{"uses": "build", "requires": "lint"}, "", "fixture"); err != nil {
		t.Fatal(err)
	}

	a.SyncStock(t.Context()) // what boot does: the policy sync, then the wording refresh

	stock, err := a.Adoption.ProjectPolicies(context.Background(), a.DB, pID)
	if err != nil || len(stock) != 1 {
		t.Fatalf("stock policies: %+v %v", stock, err)
	}
	modified, err := a.Adoption.ProjectPolicies(context.Background(), a.DB, qID)
	if err != nil || len(modified) != 1 {
		t.Fatalf("modified policies: %+v %v", modified, err)
	}
	refreshed, custom := stock[0].Description, modified[0].Description
	if refreshed != "Build the landing after its security check" {
		t.Fatalf("unmodified adoption should track library wording: %q", refreshed)
	}
	if custom != "my own words" {
		t.Fatalf("modified copy must keep its own words: %q", custom)
	}
}
