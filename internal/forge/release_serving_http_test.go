package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
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
	delete(f.started, docker.Current.Container("p", "main"))
	f.mu.Unlock()
	h := a.Handler()
	serving := func() bool {
		req := httptest.NewRequest("GET", "/api/v1/projects/p/releases", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return strings.Contains(w.Body.String(), `"serving":true`)
	}
	if serving() {
		t.Fatal("nothing runs yet: not serving")
	}
	f.markStarted(docker.Current.Container("p", "main"))
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
