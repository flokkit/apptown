package apptown

import (
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
)

// A governance view with a failed read fails; it never answers as if the
// project had nothing adopted there. Empty stays a success.
func TestGovernanceViewsFailRatherThanReadEmpty(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	h := a.Handler()
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/repo", `{}`); w.Code != 200 {
		t.Fatalf("init: %d %s", w.Code, w.Body.String())
	}
	if _, err := a.Ways.PublishPack(t.Context(), "any-pack", "A pack", `{}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Ways.PublishWorkflow(t.Context(), lifecycleYAML, "fixture"); err != nil {
		t.Fatal(err)
	}
	views := []struct{ who, path string }{
		{"owner", "/api/v1/projects/game/manifest"},
		{"owner", "/api/v1/projects/game/lanes"},
		{"boss", "/api/v1/policy-library"},
		{"boss", "/api/v1/packs"},
	}
	for _, v := range views {
		if w := authzCall(h, v.who, "GET", v.path, ""); w.Code != 200 {
			t.Fatalf("%s with nothing adopted: %d %s", v.path, w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct{ who, path, read string }{
		{"owner", "/api/v1/projects/game/manifest", "name: WorkflowCatalog :many"},
		{"owner", "/api/v1/projects/game/manifest", "name: GuidanceImportsMeta :many"},
		{"owner", "/api/v1/projects/game/lanes", "name: ProjectLane :one"},
		{"boss", "/api/v1/policy-library", "name: PolicyAdoptions :many"},
		{"boss", "/api/v1/policy-library", "name: OfferedPacks :many"},
		{"boss", "/api/v1/packs", "name: OfferedPacks :many"},
	} {
		dbtest.FailNextStatement(t, a.DB, tc.read)
		if w := authzCall(h, tc.who, "GET", tc.path, ""); w.Code < 500 {
			t.Fatalf("%s answered %d with its %q read failed: %s", tc.path, w.Code, tc.read, w.Body.String())
		}
	}
}
