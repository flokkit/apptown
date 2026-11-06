package apptown

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/workflowexec"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

const localReleaseWorldContract = `app:
  web:
    public: true
    static: site
    ports: ["8080"]
  worker:
    build: worker
init:
  - name: migrate
    build: migrations
`

func TestLandingBuildsBundleWithoutBootingOrInitializingAnyDeployment(t *testing.T) {
	for _, worker := range []bool{true, false} {
		name := "worker"
		if !worker {
			name = "Docker host"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var configure func(*Config)
			if !worker {
				configure = func(c *Config) { c.Worker = nil; c.PrivateDocker = false }
			}
			w := newReleaseWorld(t, configure)
			if !worker {
				w.appYAML = localReleaseWorldContract
			}
			w.mainOn(t, w.lab)
			run, stepID := w.landingRun(t)
			w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), stepID)
			step := w.step(t, stepID)
			if step.Status != workflow.StatusPass {
				t.Fatalf("build: %s %s", step.Status, step.Evidence)
			}
			bundle, err := w.a.Deployment.Bundles.OfRun(t.Context(), run.ProjectID, run.ID)
			if err != nil || bundle == nil || len(bundle.Parts) == 0 || len(bundle.Init) == 0 {
				t.Fatalf("bundle: %+v %v", bundle, err)
			}
			deps, err := w.a.Deployment.Deployments.ForProject(t.Context(), run.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			for _, dep := range deps {
				if dep.DesiredGeneration != 0 {
					t.Fatalf("build admitted a runtime generation: %+v", dep)
				}
			}
			for _, host := range []string{"", "lab", "lab2"} {
				for _, command := range w.hosts.daemon(host).joined() {
					if strings.HasPrefix(command, "run ") || strings.HasPrefix(command, "create ") {
						t.Fatalf("build started a container on %s: %s", host, command)
					}
				}
			}
			requireReleases(t, w.a, run.ProjectID, 0, nil)
		})
	}
}

func TestNoRuntimeBuildLandsWithoutReleaseRequests(t *testing.T) {
	t.Parallel()
	w := localReleaseWorld(t)
	run, stepID := w.landingRun(t)
	ws := requireLocation(t)(workflowSourceForTest(t, w.a, run).Workspace(w.a.cfg.dataDir))
	if err := os.Remove(filepath.Join(ws, ".apptown", "app.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "go.mod"), []byte("module example/library\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitexec.Run(t.Context(), ws, "add", "."); err != nil {
		t.Fatal(err)
	}
	if out, err := gitexec.Run(t.Context(), ws, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-qm", "library project"); err != nil {
		t.Fatalf("commit: %s %v", out, err)
	}
	checked := false
	shipping := workflowexec.NewShipping(w.a.DB, workflowexec.ShippingDependencies{Deployment: w.a.Deployment, Progress: &w.a.progress, Checks: workflowexec.CompileCheck{Image: "test-check", Run: func(ctx context.Context, in execution.Request) execution.Outcome {
		checked = true
		if in.Workspace == ws || !strings.Contains(strings.Join(in.Argv, " "), "go build ./...") {
			t.Fatalf("clean-tree build request: %+v", in)
		}
		if _, err := os.Stat(filepath.Join(in.Workspace, "go.mod")); err != nil {
			t.Fatal(err)
		}
		return execution.Outcome{Termination: execution.Exited, ExitCode: 0}
	}}}, workflowexec.ShippingConfig{DataDir: w.a.cfg.dataDir})
	rebuildWork(t, w.a, func(deps *workflow.Dependencies) { deps.Runners[workflowexec.BuildKind] = shipping.RunBuild })
	w.a.Work.Stop()
	w.a.Work.ExecuteStep(w.a.asEngine(t.Context()), stepID)
	step := w.step(t, stepID)
	if !checked || step.Status != workflow.StatusPass || !strings.Contains(string(step.Evidence), `"cleanTreeBuild":"pass"`) {
		t.Fatalf("clean build: %s %s", step.Status, step.Evidence)
	}
	if out, err := w.a.Work.AdvanceFrom(w.a.asEngine(t.Context()), run); err != nil || out.RunStatus != workflow.RunDone {
		t.Fatalf("landing: %+v %v", out, err)
	}
	runQueuedJobForTest(t, w.a, workflowCompletionJob, fmt.Sprintf("%s/release", run.ID))
	requireReleases(t, w.a, run.ProjectID, 0, nil)
	bundle, err := w.a.Deployment.Bundles.OfRun(t.Context(), run.ProjectID, run.ID)
	if err != nil || bundle != nil {
		t.Fatalf("no-runtime bundle: %+v %v", bundle, err)
	}
}

func TestLandingWithoutVotesRequestsUnderFeedSetter(t *testing.T) {
	for _, scenario := range []string{"confirmed", "revoked setter", "reconfirmed setting"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			w := newReleaseWorld(t, nil)
			w.mainOn(t, w.lab)
			deployGrant, err := w.a.Access.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "setter", Capability: string(access.CapDeploymentDeploy), Scope: access.ScopeProject, ScopeID: idstest.N(1).String(), By: "fixture"}, "")
			if err != nil {
				t.Fatal(err)
			}
			dep, err := w.a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
			if err != nil {
				t.Fatal(err)
			}
			person := access.Principal{ID: testPrincipalOf(t, w.a.DB, "setter"), Kind: "user", Name: "setter", Via: "header", Credential: identity.HeaderCredential("setter")}
			if _, err = w.a.Deployment.ConfirmFeed(verifiedContext(t.Context(), person), dep.ProjectID, dep.Name, dep.FeedRevision); err != nil {
				t.Fatal(err)
			}
			run, stepID := w.landingRun(t) // bls holds change:land; the lane has no approval step.
			w.a.Work.ExecuteStep(w.a.asEngine(t.Context()), stepID)
			if step := w.step(t, stepID); step.Status != workflow.StatusPass {
				t.Fatalf("build: %s %s", step.Status, step.Evidence)
			}
			if out, err := w.a.Work.AdvanceFrom(w.a.asEngine(t.Context()), run); err != nil || out.RunStatus != workflow.RunDone {
				t.Fatalf("landing: %+v %v", out, err)
			}
			runQueuedJobForTest(t, w.a, workflowCompletionJob, fmt.Sprintf("%s/release", run.ID))
			requests, err := w.a.Deployment.Releases.LandingRequests(t.Context(), run.ProjectID, run.ChangeID)
			if err != nil || len(requests) != 1 || requests[0].ApprovedBy != "follows main, set by setter" {
				t.Fatalf("authority: %+v %v", requests, err)
			}
			metadata, err := w.a.Deployment.Releases.RequestMetadata(t.Context(), requests[0].ID)
			if err != nil || metadata == nil || metadata.Approvers != "" {
				t.Fatalf("provenance: %+v %v", metadata, err)
			}

			if scenario == "revoked setter" {
				if ok, err := w.a.Access.Revoke(t.Context(), deployGrant, "fixture"); err != nil || !ok {
					t.Fatalf("revoke: %t %v", ok, err)
				}
			}
			if scenario == "reconfirmed setting" {
				dep, err = w.a.Deployment.Deployments.ByID(t.Context(), dep.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = w.a.Deployment.ConfirmFeed(verifiedContext(t.Context(), person), dep.ProjectID, dep.Name, dep.FeedRevision); err != nil {
					t.Fatal(err)
				}
			}
			err = w.a.Deployment.ExecuteRelease(w.a.asEngine(t.Context()), requests[0].ID)
			if scenario == "confirmed" && err != nil {
				t.Fatal(err)
			}
			if scenario == "revoked setter" && (err == nil || !strings.Contains(err.Error(), "confirm follows main again")) {
				t.Fatalf("revoked authority: %v", err)
			}
			if scenario == "reconfirmed setting" && (err == nil || !strings.Contains(err.Error(), "deployment feed changed")) {
				t.Fatalf("changed revision: %v", err)
			}
			stored, e := w.a.Work.Change(asPerson("bls"), run.ChangeID)
			if e != nil || stored.State != workflow.ChangeShipped {
				t.Fatalf("release changed landing: %+v %v", stored, e)
			}
		})
	}
}

func TestFailedNoRuntimeBuildFailsTheBuildStep(t *testing.T) {
	t.Parallel()
	a, _ := bootWorld(t)
	run, stepID := bootRun(t, a, "landing", "build")
	ws := requireLocation(t)(workflowSourceForTest(t, a, run).Workspace(a.cfg.dataDir))
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "go.mod"), []byte("module library\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-qm", "library"}} {
		if out, err := gitexec.Run(t.Context(), ws, args...); err != nil {
			t.Fatalf("source: %s %v", out, err)
		}
	}
	checked := false
	shipping := workflowexec.NewShipping(a.DB, workflowexec.ShippingDependencies{Deployment: a.Deployment, Progress: &a.progress, Checks: workflowexec.CompileCheck{Image: "test-check", Run: func(ctx context.Context, in execution.Request) execution.Outcome {
		checked = true
		if in.Workspace == ws {
			t.Fatal("build reused mutable workspace")
		}
		return execution.Outcome{ExitCode: 1, Output: []byte("committed source fails to build")}
	}}}, workflowexec.ShippingConfig{DataDir: a.cfg.dataDir})
	rebuildWork(t, a, func(deps *workflow.Dependencies) { deps.Runners[workflowexec.BuildKind] = shipping.RunBuild })
	a.Work.Stop()
	if err := a.Work.ExecuteStep(a.asEngine(t.Context()), stepID); err != nil {
		t.Fatal(err)
	}
	step, err := workflow.ReadStepIn(t.Context(), a.DB, stepID)
	if err != nil || !checked || step.Status != workflow.StatusFail {
		t.Fatalf("failed clean build: checked=%t step=%+v %v", checked, step, err)
	}
	requireReleases(t, a, run.ProjectID, 0, nil)
}
