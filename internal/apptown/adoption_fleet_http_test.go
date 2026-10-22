package apptown

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/flokkit/apptown/internal/ids"
)

func TestAdoptionFleetHTTPFiltersProjectsWithoutHidingPackVersions(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	h := a.Handler()
	if _, err := a.Ways.PublishPack(t.Context(), "shared-pack", "First", `{}`, ""); err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/projects/game/pack", `{"name":"shared-pack"}`); w.Code != 200 {
		t.Fatalf("first adoption: %d %s", w.Code, w.Body.String())
	}
	if _, err := a.Ways.PublishPack(t.Context(), "shared-pack", "Second", `{"description":"changed"}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := a.Ways.RecommendPack(t.Context(), "shared-pack", 2, "boss", ids.Nil); err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/projects/open/pack", `{"name":"shared-pack"}`); w.Code != 200 {
		t.Fatalf("second adoption: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		who   string
		first []string
	}{{"owner", []string{"game"}}, {"outsider", []string{}}} {
		w := authzCall(h, tc.who, "GET", "/api/v1/wow", "")
		var out struct {
			Packs []struct {
				Pack        string
				Recommended int64
				Versions    []struct {
					Version  int64
					Projects []string
				}
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || len(out.Packs) != 1 || out.Packs[0].Pack != "shared-pack" || out.Packs[0].Recommended != 2 || len(out.Packs[0].Versions) != 2 {
			t.Fatalf("fleet history: %d %s %v", w.Code, w.Body.String(), err)
		}
		for _, version := range out.Packs[0].Versions {
			want := []string{"open"}
			if version.Version == 1 {
				want = tc.first
			} else if version.Version != 2 {
				t.Fatalf("unknown version: %+v", version)
			}
			if !slices.Equal(version.Projects, want) {
				t.Fatalf("%s saw wrong projects at version %d: %v want %v", tc.who, version.Version, version.Projects, want)
			}
		}
	}
	// A pack that is no longer offered still has the projects on it: the
	// view lists it, marked, rather than leaving them out.
	if _, err := a.Ways.PublishPack(t.Context(), "other-pack", "Keeps one pack on", `{}`, ""); err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/packs/shared-pack/enabled", `{"enabled":false}`); w.Code != 200 {
		t.Fatalf("stop offering: %d %s", w.Code, w.Body.String())
	}
	w := authzCall(h, "owner", "GET", "/api/v1/wow", "")
	var out struct {
		Packs []struct {
			Pack     string
			Enabled  bool
			Versions []struct{ Projects []string }
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("fleet: %d %v", w.Code, err)
	}
	found := false
	for _, p := range out.Packs {
		if p.Pack == "shared-pack" {
			found = !p.Enabled && len(p.Versions) == 2 && slices.Equal(p.Versions[1].Projects, []string{"game"})
		}
	}
	if !found {
		t.Fatalf("a pack no longer offered left the view, or its projects did: %s", w.Body.String())
	}
}
