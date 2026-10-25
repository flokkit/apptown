package apptown

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
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

func TestReleaseTierObservesCurrentDeploymentInsteadOfReleaseHistory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, observed   string
		running, noLocal bool
	}{
		{name: "running", running: true},
		{name: "stopped"},
		{name: "gone"},
		{name: "unknown", observed: "unknown"},
		{name: "unavailable", observed: "unavailable", noLocal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeDocker()
			inspections := 0
			fake.reply = func(args []string) (string, error, bool) {
				if len(args) == 4 && args[0] == "inspect" && args[2] == docker.RuntimeFormat {
					inspections++
					switch tc.name {
					case "gone":
						return "Error: No such object: " + args[3], errors.New("exit status 1"), true
					case "unknown":
						return "", errors.New("daemon unavailable"), true
					default:
						return fmt.Sprintf(`{"running":%t}`, tc.running), nil, true
					}
				}
				return "", nil, false
			}
			cfg := Config{DockerClient: fake}
			if tc.noLocal {
				cfg = withPreviewWorker(t, &fakePreviewWorker{}, Config{DockerClient: ownDockerTripwire{t}, NoLocalDocker: true, ConsoleCLI: fixtureCLI(t)})
			}
			a := authzActors(t, cfg)
			revokeReadingForAll(t, a)
			target, err := a.Deployment.Targets.Put(t.Context(), &deployment.Target{Name: "tier-fixture", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
			if err != nil {
				t.Fatal(err)
			}
			dep, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "lan", target.ID, (deployment.Attributes{}).WithDefaults())
			if err != nil {
				t.Fatal(err)
			}
			cut, err := a.Deployment.Releases.Start(t.Context(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBranch, ProjectID: dep.ProjectID, DeploymentID: dep.ID, Deployment: dep.Name, Revision: "tier-revision", Actor: "owner"}, "tier fixture")
			if err != nil {
				t.Fatal(err)
			}
			outcomes := deployment.NewOutcomes(a.DB, nil)
			at, _, err := outcomes.Admit(t.Context(), deployment.Start{DeploymentID: dep.ID, TargetID: target.ID, Key: "tier-fixture", Inputs: deployment.Inputs{ReleaseID: cut.ID, Revision: "tier-revision", ImageDigest: "registry.example/app@sha256:receipt"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := outcomes.Complete(t.Context(), deployment.Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", URL: "https://historical.example", Handle: map[string]any{"container": "tier-app"}}}); err != nil {
				t.Fatal(err)
			}
			if err := a.Deployment.Deployments.RecordAddress(t.Context(), dep.ID, "https://current.example"); err != nil {
				t.Fatal(err)
			}
			read := func(who, path string) []httpapi.TierApp {
				t.Helper()
				w := authzCall(a.Handler(), who, "GET", path, "")
				var out httpapi.TierMap
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
					t.Fatalf("tiers: %d %s %v", w.Code, w.Body.String(), err)
				}
				for _, tier := range out.Tiers {
					if tier.Name == "lan" {
						return tier.Apps
					}
				}
				t.Fatal("missing configured release tier")
				return nil
			}
			apps := read("owner", "/api/v1/tiers?project=game")
			if len(apps) != 1 || apps[0].Running != tc.running || apps[0].Observed != tc.observed || apps[0].URL != "https://current.example" || apps[0].Detail != "" {
				t.Fatalf("runtime tier inherited history instead of current observation: %+v", apps)
			}
			before := inspections
			if apps := read("outsider", "/api/v1/tiers"); len(apps) != 0 {
				t.Fatalf("foreign deployment leaked: %+v", apps)
			}
			if w := authzCall(a.Handler(), "outsider", "GET", "/api/v1/tiers?project=game", ""); w.Code != 403 {
				t.Fatalf("foreign project filter: %d %s", w.Code, w.Body.String())
			}
			if inspections != before {
				t.Fatal("unreadable project's target was observed")
			}
			// The historical receipt survives owner removal. Reusing the name
			// must show the replacement, never the old address or release label.
			current, err := a.Deployment.Deployments.ByID(t.Context(), dep.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.DB.InTx(t.Context(), func(tx *database.Tx) error {
				_, err := deployment.RemoveRecordedIn(t.Context(), tx, current, true, deployment.RemovalSettlement{Target: target.Name, By: access.Principal{Name: "owner"}, At: time.Now()})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if apps := read("owner", "/api/v1/tiers?project=game"); len(apps) != 0 {
				t.Fatalf("removed deployment still in runtime tier: %+v", apps)
			}
			replacement, err := a.Deployment.Deployments.EnsureStanding(t.Context(), dep.ProjectID, dep.Name, target.ID, (deployment.Attributes{}).WithDefaults())
			if err != nil || replacement.ID == dep.ID {
				t.Fatalf("replacement: %+v %v", replacement, err)
			}
			if err := a.Deployment.Deployments.RecordAddress(t.Context(), replacement.ID, "https://replacement.example"); err != nil {
				t.Fatal(err)
			}
			apps = read("owner", "/api/v1/tiers?project=game")
			if len(apps) != 1 || apps[0].Running || apps[0].Observed != "unknown" || apps[0].URL != "https://replacement.example" || apps[0].Detail != "" {
				t.Fatalf("replacement inherited historical state: %+v", apps)
			}
		})
	}
}

func TestTierDeploymentInventoryFailureIsNotAnEmptyRuntimeMap(t *testing.T) {
	t.Parallel()
	a := authzActors(t, Config{DockerClient: newFakeDocker()})
	dbtest.FailNextStatement(t, a.DB, "-- name: Placements")
	w := authzCall(a.Handler(), "owner", "GET", "/api/v1/tiers?project=game", "")
	if w.Code != 503 {
		t.Fatalf("unavailable deployment inventory: %d %s", w.Code, w.Body.String())
	}
}
