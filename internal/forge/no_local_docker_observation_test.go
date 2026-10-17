package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/httpapi"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/worker"
)

// The worker's listing is an inventory, including stopped containers and
// retained folders (worker.previewNames), not an observation of running: a
// listed preview whose state says stopped is not live.
func TestAListedWorkerPreviewIsLiveOnlyWhenItsStateSaysRunning(t *testing.T) {
	t.Parallel()
	a, _, fake, ch := previewWorkerWorld(t, false)
	name := fmt.Sprintf("forge-preview-change-%d", ch.ID)
	fake.mu.Lock()
	fake.running = map[string]worker.PreviewState{name: {Running: false}}
	fake.mu.Unlock()
	state, err := a.Preview.PreviewOnWorker(context.Background(), name)
	if err != nil || state.Running {
		t.Fatalf("worker observation: %+v %v", state, err)
	}
	testAdministrator(t, a, "boss")
	for _, path := range []string{"/api/v1/tiers", "/api/v1/tiers/departures"} {
		resp := authzCall(a.Handler(), "boss", "GET", path, "")
		if resp.Code != 200 {
			t.Fatalf("%s: %d %s", path, resp.Code, resp.Body.String())
		}
		if path == "/api/v1/tiers" {
			var out httpapi.TierMap
			if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			for _, tier := range out.Tiers {
				if tier.Name == "preview" {
					for _, app := range tier.Apps {
						if app.Running {
							t.Errorf("stopped worker preview reported running: %+v", app)
						}
					}
				}
			}
		} else {
			var out httpapi.Departures
			if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			for _, d := range out.Departures {
				if d.Tier == "preview" && d.State == "live" {
					t.Errorf("stopped worker preview reported live: %+v", d)
				}
			}
		}
	}
}

// A worker that lists its previews but cannot give one's state leaves the
// preview collection unobserved, never a guess either way.
func TestAWorkerPreviewWhoseStateCannotBeReadLeavesThePreviewsUnobserved(t *testing.T) {
	t.Parallel()
	a, _, fake, ch := previewWorkerWorld(t, false)
	name := fmt.Sprintf("forge-preview-change-%d", ch.ID)
	fake.mu.Lock()
	fake.running = map[string]worker.PreviewState{name: {Running: true}}
	fake.stateDown = true
	fake.mu.Unlock()
	testAdministrator(t, a, "boss")
	resp := authzCall(a.Handler(), "boss", "GET", "/api/v1/tiers", "")
	var tiers httpapi.TierMap
	if err := json.Unmarshal(resp.Body.Bytes(), &tiers); resp.Code != 200 || err != nil {
		t.Fatalf("tiers: %d %v %s", resp.Code, err, resp.Body.String())
	}
	for _, tier := range tiers.Tiers {
		if tier.Name == "preview" && (tier.Observed == "" || len(tier.Apps) != 0) {
			t.Fatalf("a preview whose state cannot be read: %+v", tier)
		}
	}
	resp = authzCall(a.Handler(), "boss", "GET", "/api/v1/tiers/departures", "")
	var board httpapi.Departures
	if err := json.Unmarshal(resp.Body.Bytes(), &board); resp.Code != 200 || err != nil {
		t.Fatalf("departures: %d %v %s", resp.Code, err, resp.Body.String())
	}
	if board.PreviewsObserved == "" {
		t.Fatalf("departures with a preview whose state cannot be read: %+v", board)
	}
}

// No observation of the preview tier must not look like a known empty tier.
func TestAnUnobservablePreviewTierIsNotAnEmptyOne(t *testing.T) {
	t.Parallel()
	fake := &fakePreviewWorker{down: true}
	a := authzFixture(t, withPreviewWorker(t, fake, Config{NoLocalDocker: true, ConsoleCLI: fixtureCLI(t), DockerClient: ownDockerTripwire{t}, TierDomains: []string{"preview=preview.example.test"}}))
	if _, err := a.Preview.PreviewOnWorker(context.Background(), "forge-preview-change-00000000-0007-7000-8000-000000000000"); err == nil {
		t.Fatal("the unavailable worker unexpectedly answered")
	}
	w := authzCall(a.Handler(), "boss", "GET", "/api/v1/tiers", "")
	var out struct {
		Tiers []struct {
			Name, Observed string
			Apps           []json.RawMessage
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("tiers: %d %s", w.Code, w.Body.String())
	}
	for _, tier := range out.Tiers {
		if tier.Name == "preview" && len(tier.Apps) == 0 && tier.Observed == "" {
			t.Fatalf("unobservable previews reported as empty success: %s", w.Body.String())
		}
	}
}

// A selected attached host with an unreadable connection is unknown, not
// an observed absence of the release's running container.
func TestAnUnreadableAttachedHostLeavesTheReleaseUnknown(t *testing.T) {
	t.Parallel()
	a := infrastructureModeApp(t)
	ch, err := workflow.NewCreator(a.DB, func(context.Context, workflow.CreatedChange) error { return nil }).Create(context.Background(), workflow.ChangeDraft{ProjectID: idstest.N(1), Type: "feature", Title: "Shipped"}, "boss")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DB.InTx(context.Background(), func(tx *database.Tx) error {
		_, err := workflow.TransitionChangeIn(context.Background(), tx, idstest.N(1), ch.ID, workflow.ChangeMove{Action: workflow.ShipChange}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// The target's recorded secret is unavailable, a supported operational failure.
	target, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: "review-attached", Kind: deployment.KindDockerHost, Origin: deployment.OriginAttached, Status: deployment.TargetReady, Address: map[string]any{"host": "ssh://deploy@host.example", "keyFrom": "MISSING_REVIEW_KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	env, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "main", target.ID, (deployment.Attributes{}).WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	rel, err := a.Deployment.Releases.Start(context.Background(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBranch, ProjectID: idstest.N(1), DeploymentID: env.ID, Deployment: "main", Revision: "game-first", Actor: "boss"}, "review fixture")
	if err != nil {
		t.Fatal(err)
	}
	outcomes := deployment.NewOutcomes(a.DB, nil)
	at, _, err := outcomes.Admit(context.Background(), deployment.Start{DeploymentID: env.ID, TargetID: target.ID, Key: "review-release", Inputs: deployment.Inputs{ReleaseID: rel.ID, Revision: "game-first"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outcomes.Complete(context.Background(), deployment.Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Deployments.ReserveHostPort(t.Context(), idstest.N(1), "main", "forge-env-game-main", 40001); err != nil {
		t.Fatal(err)
	}
	if _, err := a.hosts.DockerScopeFor(context.Background(), target.ID); err == nil {
		t.Fatal("expected unreadable target")
	}
	w := authzCall(a.Handler(), "boss", "GET", fmt.Sprintf("/api/v1/changes/%d", ch.ID), "")
	var out struct {
		Evidence struct {
			Releases []struct {
				Serving  bool
				Observed string
			}
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || len(out.Evidence.Releases) != 1 {
		t.Fatalf("change: %d %s", w.Code, w.Body.String())
	}
	if r := out.Evidence.Releases[0]; !r.Serving && r.Observed == "" {
		t.Fatalf("unreadable attached release has false serving without unknown: %+v", r)
	}
}
