package forge

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/preview"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/worker/workertest"
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
		t.Fatalf("a fresh forge has one target, got %d", len(ts))
	}
}

func TestLandingDeployRecordsDeploymentGenerationAndBundle(t *testing.T) {
	t.Parallel()
	a, _ := bootWorld(t)
	a.SeedTargets(t.Context())
	runID, stepID := deployRun(t, a)
	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
	step, err := workflow.ReadStepIn(context.Background(), a.DB, stepID)
	if err != nil || step.Status != workflow.StatusPass {
		t.Fatalf("deploy step %+v: %v", step, err)
	}
	env, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil {
		t.Fatal(err)
	}
	bi, _ := a.Deployment.Targets.ByName(t.Context(), deployment.BuiltInTargetName)
	if !env.Standing() || env.TargetID != bi.ID || env.Feed != "landing" || env.ObservedGeneration != 1 || env.DesiredGeneration != 1 || env.Readiness != deployment.Reconciled {
		t.Fatalf("main after a landing: %+v", env)
	}
	if env.Handle["container"] != docker.ContainerName("p", "main") || env.HostPort == 0 || env.Protected() {
		t.Fatalf("handle %v port %d protected %v", env.Handle, env.HostPort, env.Protected())
	}
	var attempts int
	var attemptStatus string
	// sql-guarantee: no owner read lists a deployment's attempts; exactly one attempt, finished, is the guarantee
	_ = a.DB.QueryRow(`SELECT COUNT(*), MAX(status) FROM deployment_attempts WHERE deployment_id=?`, env.ID).Scan(&attempts, &attemptStatus)
	if attempts != 1 || attemptStatus != "done" {
		t.Fatalf("one finished attempt, got %d %s", attempts, attemptStatus)
	}
	// This test executes the deployment effect directly. Close its admitted
	// work before beginning another landing on the same project.
	firstRun, err := workflow.ReadRunIn(context.Background(), a.DB, runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.Abandon(asPerson("bls"), workflow.StopWork{ChangeID: firstRun.ChangeID, Reason: "deployment fixture complete"}); err != nil {
		t.Fatal(err)
	}
	// A second landing is generation 2 under a new key; the same key is the same attempt.
	_, stepID2 := deployRun(t, a)
	_ = runID
	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID2)
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
	// The landing built its images now, so a static one serves itself.
	if !b.BuiltFrom.SelfServing {
		t.Fatalf("a landing's bundle is not recorded self-serving: %+v", b.BuiltFrom)
	}
	// It was committed by the deploy step attempt that landed it, inside
	// Work's ownership fence.
	if b.BuiltFrom.StepID != stepID2 || b.BuiltFrom.Attempt != 1 {
		t.Fatalf("the bundle names step %d attempt %d, want the deploy step %d's first attempt", b.BuiltFrom.StepID, b.BuiltFrom.Attempt, stepID2)
	}
}

// A forge with no local Docker seeds no built-in host, and a landing with
// no deployment and no attached Docker host offered (a built-in host kept
// from before the mode is not one), or with a deployment on a target that
// needs local Docker, is refused
// before anything is built, naming the next action, and an existing
// deployment is left exactly as it was.
func TestNoLocalDockerLandingNeedsAnAttachedDockerHost(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"no-deployment", "portainer", "built-in-kept", "several-offered"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			// The mode starts only with a worker configured; the deploy step
			// itself runs on the forge.
			a, f := bootWorld(t, Config{NoLocalDocker: true, Worker: workerClientConfig(t, workertest.NewCA(t), "https://worker.invalid")})
			a.SeedTargets(t.Context())
			if _, err := a.Deployment.Targets.ByName(context.Background(), deployment.BuiltInTargetName); err == nil {
				t.Fatal("an AppTown with no local Docker has no built-in host")
			}
			want := "no attached Docker host is offered to p for it; offer one to the project"
			var before *deployment.Deployment
			if scenario == "portainer" {
				lan, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: "lan", Kind: deployment.KindPortainer, Origin: deployment.OriginAttached, Status: deployment.TargetReady})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "main", lan.ID, deployment.Attributes{Feed: "landing", Audience: "team", DataClass: "synthetic"}); err != nil {
					t.Fatal(err)
				}
				if before, err = a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main"); err != nil {
					t.Fatal(err)
				}
				want = "attach a Docker host and deploy there"
			}
			if scenario == "several-offered" { // no choice the forge can make
				for _, name := range []string{"lab", "spare"} {
					offerAttachedHost(t, a, name)
				}
				want = "several attached Docker hosts are offered to p (lab, spare); create main on the one it should run on"
			}
			if scenario == "built-in-kept" { // from before the mode
				if _, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: deployment.BuiltInTargetName, Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady}); err != nil {
					t.Fatal(err)
				}
			}
			_, stepID := deployRun(t, a)
			a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
			step, err := workflow.ReadStepIn(context.Background(), a.DB, stepID)
			if err != nil || step.Status != workflow.StatusError || !strings.Contains(string(step.Evidence), want) {
				t.Fatalf("deploy step: %+v %v", step, err)
			}
			if calls := f.joined(); len(f.matching("build ")) != 0 || len(f.matching("run ")) != 0 {
				t.Fatalf("a refused landing built or ran: %v", calls)
			}
			if b, err := a.Deployment.Bundles.Latest(context.Background(), idstest.N(1)); err != nil || b != nil {
				t.Fatalf("a refused landing cut a bundle: %+v %v", b, err)
			}
			after, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
			if before == nil && err == nil {
				t.Fatal("a refused landing created its deployment")
			}
			// An existing deployment keeps its placement: no port reserved,
			// no container named, nothing else changed.
			if before != nil && (err != nil || !reflect.DeepEqual(after, before)) {
				t.Fatalf("a refused landing changed its deployment:\n before %+v\n after  %+v (%v)", before, after, err)
			}
		})
	}
}

// A new project's first landing in the mode creates main on the one
// attached Docker host offered to the project, as the built-in path
// creates it on the forge's own host; nobody creates it by hand first.
func TestNoLocalDockerLandingCreatesMainOnTheOneOfferedHost(t *testing.T) {
	t.Parallel()
	a, _ := bootWorld(t, Config{NoLocalDocker: true, Worker: workerClientConfig(t, workertest.NewCA(t), "https://worker.invalid")})
	a.SeedTargets(t.Context())
	lab := offerAttachedHost(t, a, "lab")
	_, stepID := deployRun(t, a)

	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)

	main, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil || main.TargetID != lab.ID || main.Feed != "landing" {
		t.Fatalf("main after the landing: %+v %v", main, err)
	}
}

// offerAttachedHost records an attached Docker host offered to everyone.
func offerAttachedHost(t *testing.T, a *App, name string) *deployment.Target {
	t.Helper()
	host, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: name, Kind: deployment.KindDockerHost, Origin: deployment.OriginAttached, Status: deployment.TargetReady,
		Address: map[string]any{"host": "ssh://deploy@192.0.2.10"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Deployment.Targets.Offer(context.Background(), host, "*", deployment.OfferCaps{}, "boss"); err != nil {
		t.Fatal(err)
	}
	return host
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
	return a.deploymentDependencies(workflow.NewChanges(a.DB, access.Checked{Authority: identity.Scoped(a.Access, "work"), Projects: a.Projects}, a.cfg.clock.Now))
}
