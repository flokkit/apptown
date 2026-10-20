package forge

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// Gates accept only the check worker's results, once there is one.

// provisionedWorker is an in-process worker with the given role, fixed an
// hour ago, whose checks run on a fake Docker recording to calls.
func provisionedWorker(t *testing.T, ca *workertest.CA, role, calls string) *httptest.Server {
	t.Helper()
	return workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: t.TempDir(),
		Role: worker.Role{Role: role, Since: time.Now().Add(-time.Hour)}, Checks: execution.Runner{DockerBinary: workerCheckDocker(t, calls)}})
}

// withCheckWorker restarts a's checks with a check worker, as a start with
// -check-worker does: the worker's role is read, and checks and Work are
// composed again.
func withCheckWorker(t *testing.T, a *App) (url, calls string) {
	t.Helper()
	ca := workertest.NewCA(t)
	calls = filepath.Join(t.TempDir(), "check-calls")
	srv := provisionedWorker(t, ca, worker.RoleCheck, calls)
	a.cfg.checkWorker = &execution.WorkerTasks{URL: srv.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	a.cfg.agentContext = "../../deploy/apptown-agent" // names the worker's agent image
	if err := requireCheckWorker(t.Context(), a.cfg); err != nil {
		t.Fatal(err)
	}
	recomposeChecks(t, a)
	a.Work.Stop() // the test runs each step itself
	return srv.URL, calls
}

func placementOf(t *testing.T, s *workflow.Step) *workflow.Placement {
	t.Helper()
	var ev struct{ Placement *workflow.Placement }
	if err := json.Unmarshal(s.Evidence, &ev); err != nil {
		t.Fatal(err)
	}
	return ev.Placement
}

// With one worker, checks run there and a passing tests gate lands, as
// before: its result records that it ran on a worker shared with agents.
func TestWithOneWorkerAPassingGateLandsAsBefore(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	calls := onWorkerWithATripwire(t, a)
	run, source := reviewLanding(t, a, []schema.Step{{Name: "tests", Uses: "tests"}}, map[string]string{"go.mod": "module example\n"}, nil)
	step := reviewExecute(t, a, run, "tests")
	if p := placementOf(t, step); step.Status != workflow.StatusPass || p == nil || *p != (workflow.Placement{Worker: a.cfg.worker.URL}) {
		t.Fatalf("the tests gate on the worker: %s %s", step.Status, step.Evidence)
	}
	onlyTheWorker(t, calls, "go test -json ./...")
	if obs, err := mergeEffectForTest(t, a, source); err != nil || !obs.Merged {
		t.Fatalf("a passing gate with one worker did not land: %+v %v", obs, err)
	}
}

// Once the forge has a check worker, a stored passing result it did not
// produce satisfies no gate: advancing and landing say it must run again,
// the stored result is unchanged, and a fresh run on the check worker
// lands. Here the result is the agent worker's; workflow's
// TestWithACheckWorkerTheGateTakesOnlyItsChecks judges every other
// placement (this machine, unknown) by the same rule.
func TestWithACheckWorkerOnlyItsResultsSatisfyAGate(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	onWorkerWithATripwire(t, a)
	run, source := reviewLanding(t, a, []schema.Step{{Name: "tests", Uses: "tests"}}, map[string]string{"go.mod": "module example\n"}, nil)
	stored := reviewExecute(t, a, run, "tests")
	if stored.Status != workflow.StatusPass || placementOf(t, stored) == nil {
		t.Fatalf("the earlier result: %s %s", stored.Status, stored.Evidence)
	}

	url, calls := withCheckWorker(t, a)
	const rerun = "tests (not independently checked; rerun required)"
	if out, err := a.Work.Advance(a.asEngine(context.Background()), run.ID); err != nil || out.Advanced || !slices.Contains(out.Missing, rerun) {
		t.Fatalf("the gate took the earlier result: %+v %v", out, err)
	}
	if obs, err := mergeEffectForTest(t, a, source); err != nil || obs.Merged || !strings.Contains(obs.Refusal, rerun) {
		t.Fatalf("the landing took the earlier result: %+v %v", obs, err)
	}
	if now := readStep(t, a, stored.ID); now.Status != stored.Status || string(now.Evidence) != string(stored.Evidence) {
		t.Fatalf("the earlier result changed: %s %s, was %s %s", now.Status, now.Evidence, stored.Status, stored.Evidence)
	}

	if _, err := a.Work.RecheckStage(a.asEngine(context.Background()), run.ID); err != nil {
		t.Fatal(err)
	}
	fresh := reviewExecute(t, a, run, "tests")
	if p := placementOf(t, fresh); fresh.Status != workflow.StatusPass || p == nil || *p != (workflow.Placement{Worker: url, Role: worker.RoleCheck}) {
		t.Fatalf("the fresh result: %s %s", fresh.Status, fresh.Evidence)
	}
	if got, _ := os.ReadFile(calls); !strings.Contains(string(got), "go test -json ./...") {
		t.Fatalf("the tests did not run on the check worker:\n%s", got)
	}
	if obs, err := mergeEffectForTest(t, a, source); err != nil || !obs.Merged {
		t.Fatalf("the check worker's result did not land: %+v %v", obs, err)
	}
}

func readStep(t *testing.T, a *App, id ids.ID) *workflow.Step {
	t.Helper()
	s, err := workflow.ReadStepIn(context.Background(), a.DB, id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The forge refuses to start with a check worker that is not one (the
// worker package tests the role's time against the forge's first use).
func TestTheForgeStartsOnlyWithAFreshCheckWorker(t *testing.T) {
	t.Parallel()
	ca := workertest.NewCA(t)
	calls := filepath.Join(t.TempDir(), "calls")
	check := provisionedWorker(t, ca, worker.RoleCheck, calls)
	agent := provisionedWorker(t, ca, worker.RoleAgent, calls)
	dataDir := t.TempDir()
	start := func(url string) error {
		_, err := New(context.Background(), testDB(t), Config{DataDir: dataDir, DockerClient: defaultTestDocker(), CheckWorker: workerClientConfig(t, ca, url)})
		return err
	}
	if err := start(agent.URL); err == nil || !strings.Contains(err.Error(), `reports the role "agent"`) {
		t.Fatalf("an agent worker as the check worker: %v", err)
	}
	if err := start(check.URL); err != nil {
		t.Fatalf("a fresh check worker: %v", err)
	}
}

// Once the forge has a check worker, no script result satisfies a gate:
// scripts run on the agent worker or this machine, so they are not
// independent checks. A script run beside the check worker records where it
// ran, this machine, never the check worker; advancing, landing and the
// run's activity all refuse it, and the stored result is kept. workflow's
// TestWithACheckWorkerTheGateTakesOnlyItsChecks refuses a script whatever
// placement it records.
func TestWithACheckWorkerNoScriptSatisfiesAGate(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	run, source := reviewLanding(t, a, []schema.Step{{Name: "check", Uses: "script", With: map[string]any{"run": "true"}}}, nil, nil)
	withCheckWorker(t, a)
	stored := reviewExecute(t, a, run, "check")
	if p := placementOf(t, stored); stored.Status != workflow.StatusPass || p == nil || *p != (workflow.Placement{Local: true}) {
		t.Fatalf("the script's result: %s %s", stored.Status, stored.Evidence)
	}

	const refused = "check (scripts run beside agents, so they are not independent checks while a check worker is configured"
	if out, err := a.Work.Advance(a.asEngine(context.Background()), run.ID); err != nil || out.Advanced || len(out.Missing) != 1 || !strings.HasPrefix(out.Missing[0], refused) {
		t.Fatalf("the gate took the script: %+v %v", out, err)
	}
	if obs, err := mergeEffectForTest(t, a, source); err != nil || obs.Merged || !strings.Contains(obs.Refusal, refused) {
		t.Fatalf("the landing took the script: %+v %v", obs, err)
	}
	// Running it again cannot help, and activity does not suggest it:
	// it asks for a revised workflow.
	if snap, err := a.Work.Snapshot(a.asForge(context.Background(), run.ProjectID, "review"), run.ID, workflow.DetailView); err != nil || snap.State != workflow.ActivityBlocked ||
		!strings.Contains(snap.StateDetail, "not independent checks") || strings.Contains(snap.StateDetail, "Run its checks again") ||
		!strings.Contains(snap.StateDetail, "revise the workflow and start a new run") || !snap.NeedsRevision {
		t.Fatalf("activity hides the refusal, or suggests a rerun: %+v %v", snap, err)
	}
	if now := readStep(t, a, stored.ID); now.Status != stored.Status || string(now.Evidence) != string(stored.Evidence) {
		t.Fatalf("the stored result changed: %s %s, was %s %s", now.Status, now.Evidence, stored.Status, stored.Evidence)
	}
}

// Activity applies the check worker's rule as admission does: a run whose
// tests passed on the agent worker is blocked, in the detail view and in
// lists, with the rerun it needs, until the check worker reruns them.
func TestWithACheckWorkerActivityShowsTheRequiredRerun(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	onWorkerWithATripwire(t, a)
	run, _ := reviewLanding(t, a, []schema.Step{{Name: "tests", Uses: "tests"}}, map[string]string{"go.mod": "module example\n"}, nil)
	if step := reviewExecute(t, a, run, "tests"); step.Status != workflow.StatusPass {
		t.Fatalf("tests failed: %s %s", step.Status, step.Evidence)
	}
	withCheckWorker(t, a)
	out, err := a.Work.Advance(a.asEngine(context.Background()), run.ID)
	if err != nil || out.Advanced || len(out.Missing) == 0 {
		t.Fatalf("admission did not refuse: %+v %v", out, err)
	}
	ctx := a.asForge(context.Background(), run.ProjectID, "review")
	snap, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil || snap.State != workflow.ActivityBlocked || !strings.Contains(snap.StateDetail, "rerun required") || !strings.Contains(snap.StateDetail, "Run its checks again") || snap.NeedsRevision {
		t.Fatalf("admission requires rerun %v, but detail activity is %+v %v", out.Missing, snap, err)
	}
	listed, err := a.Work.Activities(a.asEngine(context.Background()), []ids.ID{run.ID})
	if err != nil || listed[run.ID].State != workflow.ActivityBlocked || !strings.Contains(listed[run.ID].Detail, "rerun required") {
		t.Fatalf("admission requires rerun %v, but listed activity is %+v %v", out.Missing, listed, err)
	}
	// The rerun on the check worker clears it, in both views.
	if _, err := a.Work.RecheckStage(a.asEngine(context.Background()), run.ID); err != nil {
		t.Fatal(err)
	}
	reviewExecute(t, a, run, "tests")
	snap, err = a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil || snap.State == workflow.ActivityBlocked {
		t.Fatalf("detail activity after the rerun: %+v %v", snap, err)
	}
	if listed, err = a.Work.Activities(a.asEngine(context.Background()), []ids.ID{run.ID}); err != nil || listed[run.ID].State == workflow.ActivityBlocked {
		t.Fatalf("listed activity after the rerun: %+v %v", listed, err)
	}
}
