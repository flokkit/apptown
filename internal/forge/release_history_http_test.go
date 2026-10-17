package forge

import (
	"encoding/json"
	"fmt"
	"testing"

	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestReleaseHistoryHTTPScopesNumbersAndDistinguishesLatestFromServing(t *testing.T) {
	t.Parallel()
	a := authzFixture(t, Config{DockerClient: newFakeDocker()})
	first := releasedPreviewFixture(t, a, idstest.N(1), "game-first")
	second := releasedPreviewFixture(t, a, idstest.N(1), "game-second")
	other := releasedPreviewFixture(t, a, idstest.N(2), "open-first")
	h := a.Handler()
	var list []struct {
		deployment.Release
		Changes []json.RawMessage
		Serving bool
	}
	w := authzCall(h, "owner", "GET", "/api/v1/projects/game/releases", "")
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("release list: %d %s %v", w.Code, w.Body.String(), err)
	}
	for _, r := range list {
		if r.ProjectID != idstest.N(1) || r.Changes == nil || len(r.Changes) != 0 || r.Serving {
			t.Fatalf("release projection: %+v", r)
		}
	}
	for _, tc := range []struct {
		project string
		number  int64
		id      ids.ID
		live    bool
	}{{"game", first.Number, first.ID, false}, {"game", second.Number, second.ID, true}, {"open", other.Number, other.ID, true}} {
		w := authzCall(h, "owner", "GET", fmt.Sprintf("/api/v1/projects/%s/releases/%d", tc.project, tc.number), "")
		var out struct {
			Release deployment.Release
			Changes []json.RawMessage
			IsLive  bool
			Events  []deployment.ReleaseEvent
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.Release.ID != tc.id || out.IsLive != tc.live || out.Changes == nil || len(out.Changes) != 0 || len(out.Events) == 0 {
			t.Fatalf("release permalink: %d %s %v", w.Code, w.Body.String(), err)
		}
	}
	for _, tc := range []struct {
		path string
		code int
	}{{"open/releases/2", 404}, {"game/releases/no", 400}, {"missing/releases", 404}} {
		w := authzCall(h, "owner", "GET", "/api/v1/projects/"+tc.path, "")
		if w.Code != tc.code {
			t.Fatalf("bad release: %d %s", w.Code, w.Body.String())
		}
	}
	w = authzCall(h, "owner", "GET", "/api/v1/tiers?project=game", "")
	var tiers struct {
		Tiers []struct {
			Name string
			Apps []struct{ Slug string }
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tiers); err != nil || w.Code != 200 || len(tiers.Tiers) != 4 {
		t.Fatalf("tiers: %d %s %v", w.Code, w.Body.String(), err)
	}
	main := false
	for _, tier := range tiers.Tiers {
		if tier.Apps == nil {
			t.Fatalf("null apps: %+v", tier)
		}
		for _, app := range tier.Apps {
			if app.Slug != "game" {
				t.Fatalf("foreign tier: %+v", app)
			}
		}
		if tier.Name == "main" {
			main = true
			if len(tier.Apps) != 1 {
				t.Fatalf("standing main missing: %+v", tier)
			}
		}
	}
	if !main {
		t.Fatal("main tier missing")
	}
	if w := authzCall(h, "owner", "GET", "/api/v1/tiers?project=missing", ""); w.Code != 404 {
		t.Fatalf("missing tier filter: %d %s", w.Code, w.Body.String())
	}
	w = authzCall(h, "owner", "GET", "/api/v1/tiers/departures", "")
	var departures struct {
		Departures []struct{ At, Slug, Tier, State, Label string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &departures); err != nil || w.Code != 200 || len(departures.Departures) != 3 {
		t.Fatalf("departures: %d %s %v", w.Code, w.Body.String(), err)
	}
	counts := map[string]int{}
	for i, d := range departures.Departures {
		counts[d.Slug]++
		if d.Tier != "main deployment" || d.State != "released" || d.Label == "" || (i > 0 && departures.Departures[i-1].At < d.At) {
			t.Fatalf("departure ordering/receipt: %+v", departures)
		}
	}
	if counts["game"] != 2 || counts["open"] != 1 {
		t.Fatalf("departure scope: %+v", counts)
	}
}
