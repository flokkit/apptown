package httpapi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy"
)

func TestCockpitNamesCurrentMainDestinations(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	if err := f.Projects.SetDefaultBranch(f.Context, f.ProjectID, "trunk"); err != nil {
		t.Fatal(err)
	}
	s := deployment.New(f.Context, f.DB, deployment.Dependencies{})
	target, err := s.Targets.Put(f.Context, &deployment.Target{Name: "fly", DisplayLabel: "Public host", Kind: deployment.KindDOApp, Origin: deployment.OriginAttached, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deployments.EnsureStanding(f.Context, f.ProjectID, "public", target.ID, deployment.Attributes{Feed: "main", Audience: "public", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	response := workflowChangeRead(t, f, fmt.Sprint(f.ChangeID))
	var got ChangeCockpit
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || got.GoesTo.Branch != "trunk" || len(got.GoesTo.Deployments) != 1 || got.GoesTo.Deployments[0].Name != "public" || got.GoesTo.Deployments[0].Audience != "public" {
		t.Fatalf("cockpit: %s", response.Body.String())
	}
}

func TestLandingReleasePreservesProgressAndDeploymentReach(t *testing.T) {
	t.Parallel()
	w := newDeploymentAuthorityWorld(t, nil)
	target, err := w.service.Targets.Put(t.Context(), &deployment.Target{Name: "fly", Kind: deployment.KindDOApp, Origin: deployment.OriginAttached, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := w.service.Deployments.EnsureStanding(t.Context(), w.project, "public", target.ID, deployment.Attributes{Feed: "main", Audience: "public", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	// A platform endpoint is a deployment fact, independent of this release's progress.
	dep.URL = "https://app.example.test"
	// The endpoint is persisted by provider outcome settlement; use the outcome owner.
	attempt, _, err := w.service.Outcomes.Admit(t.Context(), deployment.Start{DeploymentID: dep.ID, TargetID: target.ID, Key: "endpoint", Inputs: deployment.Inputs{Revision: "abc"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.service.Outcomes.Complete(t.Context(), deployment.Completion{Attempt: attempt, Observed: deploy.Observation{URL: dep.URL, Generation: attempt.Generation, Rollout: "active"}}); err != nil {
		t.Fatal(err)
	}
	elapsed := int64(1200)
	release := &deployment.Release{Number: 7, DeploymentID: dep.ID, Deployment: dep.Name, State: deployment.Releasing, Stage: deploy.Building, LastStage: deploy.Preparing, FailedStage: deploy.Starting, StageElapsedMs: &elapsed, ProgressUncertain: true}
	got, err := (ChangeDetail{deployment: w.service}).landingRelease(t.Context(), "game", release)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["url"] != dep.URL || wire["hostOnly"] != false || wire["stage"] != "building" || wire["lastStage"] != "preparing" || wire["failedStage"] != "starting" || wire["stageElapsedMs"] != float64(elapsed) || wire["progressUncertain"] != true {
		t.Fatalf("landing evidence: %s", raw)
	}
}
