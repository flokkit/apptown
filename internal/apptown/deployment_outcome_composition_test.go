package apptown

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// These composition helpers remain in AppTown for runner, recovery, release, and Docker
// composition tests; moving owner transaction tests does not close them.
func deploymentOutcomeFixture(t *testing.T) (*App, *deployment.Deployment) {
	t.Helper()
	a := testApp(t)
	pid, err := database.Transact(context.Background(), a.DB, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "deployment-outcomes", Name: "Deployment outcomes"})
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: "outcome-local", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	env, err := a.Deployment.Deployments.EnsureStanding(t.Context(), pid, "main", target.ID, (deployment.Attributes{}).WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	return a, env
}

func deploymentStartFor(env *deployment.Deployment, key, label string) deployment.Start {
	return deployment.Start{DeploymentID: env.ID, TargetID: env.TargetID, Key: key, Inputs: deployment.Inputs{Revision: label + "-revision", ImageDigest: label + "-image", ArtifactHash: label + "-artifact"}}
}

func deploymentCompletionFor(at deployment.Attempt, label string) deployment.Completion {
	return deployment.Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", Health: "healthy", URL: "https://" + label + ".example", Handle: map[string]any{"container": label, "hostPort": 40001}}}
}

// deploymentReleasingFixture uses the ordinary release lifecycle, including
// its initial receipt, rather than inventing an already-releasing storage row.
func deploymentReleasingFixture(t *testing.T, a *App, env *deployment.Deployment, name, revision, actor string) ids.ID {
	t.Helper()
	r, err := deployment.NewReleases(a.DB).Start(context.Background(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBundle, ProjectID: env.ProjectID, DeploymentID: env.ID, Deployment: name, Revision: revision, Actor: actor}, "prepared for settlement")
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func TestLandingDeployDoesNotPassWhenOutcomeCannotBeRecorded(t *testing.T) {
	t.Parallel()
	a, _ := bootWorld(t)
	a.SeedTargets(t.Context())
	runID, stepID := deployRun(t, a)
	dbtest.FailNextStatement(t, a.DB, "-- name: FinishAttempt")
	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
	if step := readStep(t, a, stepID); step.Status != workflow.StatusError || !strings.HasPrefix(stepReason(t, step), "recording the deployed deployment:") {
		t.Fatalf("deploy step: %s %s", step.Status, step.Evidence)
	}
	if n := runEventCount(t, a, runID, "release.deployed"); n != 0 {
		t.Fatalf("%d release.deployed events for an unrecorded outcome", n)
	}
	// bootWorld's project is the only one.
	if bundle, err := a.Deployment.Bundles.Latest(context.Background(), idstest.N(1)); err != nil || bundle != nil {
		t.Fatalf("bundle cut for an unrecorded outcome: %+v %v", bundle, err)
	}
	if open := openAttempts(t, a); len(open) != 1 || attemptStatus(t, a, open[0].ID) != deployment.AttemptUncertain {
		t.Fatalf("open attempts: %+v", open)
	}
	if n := len(operationJobs(t, a, deployment.RecoveryJob)); n != 1 {
		t.Fatalf("%d recovery jobs", n)
	}
}
