package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// A release is "serving" when a running environment records it as the
// release it runs — by the landing run, which every environment records.
func TestReleaseServingFollowsTheRunningDeployment(t *testing.T) {
	t.Parallel()
	a, f := bootWorld(t)
	runID, stepID := deployRun(t, a)
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), stepID); err != nil {
		t.Fatal(err)
	}
	w := authzCall(a.Handler(), "bls", "POST", fmt.Sprintf("/api/v1/runs/%d/advance", runID), "")
	var result workflow.AdvanceResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || !result.Advanced || result.RunStatus != workflow.RunDone {
		t.Fatalf("complete landing response: %d %s %v", w.Code, w.Body.String(), err)
	}
	w = authzCall(a.Handler(), "bls", "POST", fmt.Sprintf("/api/v1/runs/%d/advance", runID), "")
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.Advanced || result.RunStatus != workflow.RunDone {
		t.Fatalf("completed run response: %d %s %v", w.Code, w.Body.String(), err)
	}
	requireReleases(t, a, runRecord(t, a, runID).ProjectID, 1, func(r *deployment.Release) bool {
		return r.RunID == runID && r.State == deployment.Released
	})
	// Work execution starts the fake container while creating the real records;
	// stop that provider-side fixture so the view can observe the transition.
	f.mu.Lock()
	delete(f.started, mainNames(t, a).App())
	f.mu.Unlock()
	h := a.Handler()
	serving := func() bool {
		// Reads show the background observer's last look, never ask the host.
		if err := a.Deployment.ObserveRuntime(t.Context()); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("GET", "/api/v1/projects/p/releases", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return strings.Contains(w.Body.String(), `"serving":true`)
	}
	if serving() {
		t.Fatal("nothing runs yet: not serving")
	}
	f.markStarted(mainNames(t, a).App())
	if !serving() {
		t.Fatal("the environment runs the release's landing run: serving")
	}
	// The same landing run released to another deployment, which is not
	// running, is not serving because main runs it: a release serves only where it
	// was released.
	main, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil {
		t.Fatal(err)
	}
	landed, err := a.Deployment.Releases.Last(context.Background(), idstest.N(1), "main")
	if err != nil || landed.RunID.IsZero() {
		t.Fatalf("main's release: %+v %v", landed, err)
	}
	cloud, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "cloud", main.TargetID, (deployment.Attributes{Feed: "promotion"}).WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	cut, err := a.Deployment.Releases.Start(context.Background(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBranch, ProjectID: idstest.N(1), DeploymentID: cloud.ID, RunID: landed.RunID, Deployment: "cloud", Revision: "landed", Actor: "bls"}, "the same landing elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	store := deployment.NewOutcomes(a.DB, nil)
	at, _, err := store.Admit(context.Background(), deployment.Start{DeploymentID: cloud.ID, TargetID: cloud.TargetID, Key: "elsewhere", Inputs: deployment.Inputs{ReleaseID: cut.ID, Revision: "landed"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(context.Background(), deployment.Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", URL: "http://elsewhere"}}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/projects/p/releases", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var listed []struct {
		Number    int64
		ID        ids.ID
		CommitSHA string
		Serving   bool
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	seen := map[ids.ID]bool{}
	for _, r := range listed {
		seen[r.ID] = r.Serving
	}
	if !seen[landed.ID] || seen[cut.ID] {
		t.Fatalf("serving by deployment: main's %v, elsewhere's %v\n%s", seen[landed.ID], seen[cut.ID], w.Body.String())
	}
}

// Reading a change never asks a Docker host: whether its release's deployment
// runs is the background observer's last look. A host that does not answer
// (the restored copy cut off from the demo's hosts) cannot hold up the read.
func TestChangeReadDoesNotAskTheHost(t *testing.T) {
	t.Parallel()
	a, f := bootWorld(t)
	runID, stepID := deployRun(t, a)
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), stepID); err != nil {
		t.Fatal(err)
	}
	if w := authzCall(a.Handler(), "bls", "POST", fmt.Sprintf("/api/v1/runs/%d/advance", runID), ""); w.Code != 200 {
		t.Fatalf("complete landing: %d %s", w.Code, w.Body.String())
	}
	changeID := runRecord(t, a, runID).ChangeID
	// The host stops answering: every Docker call waits until it is given up.
	var asked atomic.Int64
	f.command = func(ctx context.Context, _ string, req docker.Command) ([]byte, error) {
		asked.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, "GET", fmt.Sprintf("/api/v1/changes/%s", changeID), nil)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != 200 || asked.Load() != 0 || ctx.Err() != nil {
		t.Fatalf("change read asked the host %d times (deadline: %v): %d %s", asked.Load(), ctx.Err(), w.Code, w.Body.String())
	}
	var read struct {
		Evidence struct {
			Releases []struct{ Observed, Reason string }
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &read); err != nil || len(read.Evidence.Releases) != 1 || read.Evidence.Releases[0].Observed != "unknown" {
		t.Fatalf("an unobserved release is unknown, not stopped: %v %s", err, w.Body.String())
	}
}
