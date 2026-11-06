package apptown

import (
	"context"
	"github.com/flokkit/apptown/internal/ids"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/preview"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// Deployment targets and deployments (design/subsystems/deployment.md): the
// nouns exist and the existing rails record through them.

func TestSeedTargetsNamesTheBuiltInHost(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	a.SyncStockDependencies(context.Background())
	a.SeedTargets(t.Context())
	bi, err := a.Deployment.Targets.ByName(context.Background(), deployment.BuiltInTargetName)
	if err != nil {
		t.Fatal(err)
	}
	if bi.Kind != deployment.KindDockerHost || bi.Origin != deployment.OriginBuiltIn || !bi.Capabilities.PersistentVolumes || !bi.Capabilities.Previews {
		t.Fatalf("built-in host: %+v", bi)
	}
	if !containsString(bi.Capabilities.Needs, "postgres") {
		t.Fatalf("the built-in host runs the catalog's emulators: %v", bi.Capabilities.Needs)
	}
	a.SeedTargets(t.Context()) // idempotent
	ts, _ := a.Deployment.Targets.All(context.Background())
	if len(ts) != 1 {
		t.Fatalf("a fresh AppTown has one target, got %d", len(ts))
	}
}

func TestLandingDeployRecordsDeploymentGenerationAndBundle(t *testing.T) {
	t.Parallel()
	a, _ := bootWorld(t)
	a.SeedTargets(t.Context())
	statelessRun := func() (ids.ID, ids.ID) {
		runID, stepID := deployRun(t, a)
		run := runRecord(t, a, runID)
		ws := requireLocation(t)(workflowSourceForTest(t, a, run).Workspace(a.cfg.dataDir))
		raw := strings.ReplaceAll(bootFixtureYAML, "    dataDir: /data\n", "")
		raw = strings.ReplaceAll(raw, "needs:\n  postgres: {}\n", "")
		if err := os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte(raw), 0644); err != nil {
			t.Fatal(err)
		}
		commitFolder(t, ws)
		return runID, stepID
	}
	runID, stepID := statelessRun()
	executeBootFixture(t, a, a.asEngine(context.Background()), stepID)
	step, err := workflow.ReadStepIn(context.Background(), a.DB, stepID)
	if err != nil || step.Status != workflow.StatusPass {
		t.Fatalf("deploy step %+v: %v", step, err)
	}
	env, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil {
		t.Fatal(err)
	}
	bi, _ := a.Deployment.Targets.ByName(t.Context(), deployment.BuiltInTargetName)
	if !env.Standing() || env.TargetID != bi.ID || env.Feed != "main" || env.ObservedGeneration != 1 || env.DesiredGeneration != 1 || env.Readiness != deployment.Reconciled {
		t.Fatalf("main after a landing: %+v", env)
	}
	if env.Handle["container"] != docker.ByKey(env.ID).App() || env.HostPort == 0 || env.Protected() {
		t.Fatalf("handle %v port %d protected %v", env.Handle, env.HostPort, env.Protected())
	}
	var attempts int
	var attemptStatus string
	// sql-guarantee: no owner read lists a deployment's attempts; exactly one attempt, finished, is the guarantee
	_ = a.DB.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*), MAX(status) FROM deployment_attempts WHERE deployment_id=$1`, env.ID).Scan(&attempts, &attemptStatus)
	if attempts != 1 || attemptStatus != "done" {
		t.Fatalf("one finished attempt, got %d %s", attempts, attemptStatus)
	}
	// A second landing is generation 2 under a new key; the same key is the same attempt.
	_, stepID2 := statelessRun()
	_ = runID
	executeBootFixture(t, a, a.asEngine(context.Background()), stepID2)
	env, _ = a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if env.ObservedGeneration != 2 {
		t.Fatalf("the second landing is generation 2, got %d", env.ObservedGeneration)
	}
	store := a.Deployment.Outcomes
	first, _, err := store.Admit(context.Background(), deploymentStartFor(env, "same-key", "same"))
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := store.Admit(context.Background(), deploymentStartFor(env, "same-key", "same"))
	if err != nil || first != again {
		t.Fatalf("one key is one attempt: %+v vs %+v: %v", first, again, err)
	}
	// The bundle was cut from what booted.
	b, err := a.Deployment.Bundles.Latest(context.Background(), idstest.N(1))
	if err != nil || b == nil {
		t.Fatalf("a landing cuts a bundle: %v", err)
	}
	if b.Parts["web"] == "" || b.Parts["worker"] == "" || b.Init["migrate"] == "" || b.ContractHash == "" || b.ContractRevision == "" {
		t.Fatalf("bundle: %+v", b)
	}
	if b.BuiltFrom.TargetID != bi.ID || b.BuiltFrom.RunID.IsZero() {
		t.Fatalf("the bundle names the target its images were read from: %+v", b.BuiltFrom)
	}
	// It was committed by the deploy step attempt that landed it, inside
	// Work's ownership fence.
	if b.BuiltFrom.StepID != stepID2 || b.BuiltFrom.Attempt != 1 {
		t.Fatalf("the bundle names step %d attempt %d, want the deploy step %d's first attempt", b.BuiltFrom.StepID, b.BuiltFrom.Attempt, stepID2)
	}
}

// An AppTown with no local Docker seeds no built-in host, and a landing with
// no deployment and no attached Docker host offered (a built-in host kept
// from before the mode is not one), or with a deployment on a target that
// needs local Docker, is refused
// before anything is built, naming the next action, and an existing
// deployment is left exactly as it was.
func TestWorkerBuildDoesNotNeedAnAvailableDeployment(t *testing.T) {
	for _, destination := range []string{"none", "portainer", "unknown platform"} {
		t.Run(destination, func(t *testing.T) {
			t.Parallel()
			w := newReleaseWorld(t, func(c *Config) { c.NoLocalDocker = true })
			if destination == "portainer" {
				target, err := w.a.Deployment.Targets.Put(t.Context(), &deployment.Target{Name: "lan", Kind: deployment.KindPortainer, Origin: deployment.OriginAttached, Status: deployment.TargetReady})
				if err != nil {
					t.Fatal(err)
				}
				w.mainOn(t, target)
			}
			if destination == "unknown platform" {
				w.mainOn(t, w.lab)
				w.hosts.platform = "linux "
			}
			run, stepID := w.landingRun(t)
			if err := w.a.Work.ExecuteStep(w.a.asEngine(t.Context()), stepID); err != nil {
				t.Fatal(err)
			}
			if step := w.step(t, stepID); step.Status != workflow.StatusPass {
				t.Fatalf("build: %s %s", step.Status, step.Evidence)
			}
			bundle, err := w.a.Deployment.Bundles.OfRun(t.Context(), run.ProjectID, run.ID)
			if err != nil || bundle == nil {
				t.Fatalf("build: %+v %v", bundle, err)
			}
			for _, host := range []string{"", "lab"} {
				if len(containersRun(w.hosts.daemon(host))) != 0 {
					t.Fatal("build booted a deployment")
				}
			}
			deps, err := w.a.Deployment.Deployments.ForProject(t.Context(), run.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			for _, dep := range deps {
				if dep.DesiredGeneration != 0 {
					t.Fatalf("build admitted deployment: %+v", dep)
				}
			}
		})
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func previewDependenciesForTest(a *App) preview.Dependencies {
	return previewDependencies(a, workflow.NewChanges(a.DB, access.Checked{Authority: identity.Scoped(a.Access, "work"), Projects: a.Projects}, a.cfg.clock.Now))
}

func deploymentDependenciesForTest(a *App) deployment.Dependencies {
	return a.deploymentDependencies()
}
