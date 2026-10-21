package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/forge/workflowexec"
	"flokkit.io/forge/internal/gitexec/gittest"
)

// A tests step's own guarantees (the commit it selects, missing answers,
// needs and throwaway secrets) are workflowexec's (tests_step_test.go). These
// are the landing's tests, against its pinned candidate and base, and tests
// on a worker, whose executor the root chooses.

// testsExecutor runs a's tests steps through run, as AppTown's check
// executor would: the worker's, or this machine's.
func testsExecutor(t *testing.T, a *App, run func(execution.Request) execution.Outcome) {
	t.Helper()
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		commands := workflowexec.NewCommands(a.DB, workflowexec.CommandDependencies{
			Runner: a.processRunner, Progress: &a.progress, Branches: a.branches, Tasks: a.tasks, Panics: jobReporter{}, Docker: &a.docker,
			Checks: workflowexec.CompileCheck{Run: func(_ context.Context, req execution.Request) execution.Outcome { return run(req) }, Image: "apptown-agent:test"}, Catalog: a.Dependencies,
		}, workflowexec.CommandConfig{DataDir: a.cfg.dataDir, Image: a.cfg.agentImage})
		deps.Runners[workflowexec.TestsKind] = commands.RunTests
	})
}

// testsWorld is a change whose working lane has one tests step, and its
// folder with a first commit.
func testsWorld(t *testing.T, with map[string]any) (*App, *project.Project, *workflow.Change, string) {
	t.Helper()
	a := testApp(t)
	pid := plainTestProject(t, a.DB, "tests-commit", "Tests Commit")
	p := a.projectByID(context.Background(), pid)
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "main", ""); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "check", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working",
		Stages: []schema.Stage{{Name: "gates", Steps: []schema.Step{{Name: "tests", Uses: "tests", With: with}}}}}})
	if err := a.lifecycle.SelectLane(t.Context(), p.ID, "check", true, "check"); err != nil {
		t.Fatal(err)
	}
	ch := engineChange(t, a, p)
	if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	writeMarker(t, folder, "folder")
	return a, p, ch, folder
}

func writeMarker(t *testing.T, folder, value string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(folder, "marker.txt"), []byte(value+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitFolder(t, folder)
	return gittest.Run(t, folder, "rev-parse", "HEAD")
}

func testsRun(t *testing.T, a *App, ch *workflow.Change) *workflow.Run {
	t.Helper()
	a.Work.Stop()
	run, err := a.Work.StartRun(a.asForge(context.Background(), ch.ProjectID, "check"), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "check"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	return run
}

// markersSeen records the marker each tests check read from the tree it
// was given, and answers as scripted.
type markersSeen struct {
	mu     sync.Mutex
	seen   []string
	answer func(n int, req execution.Request) execution.Outcome
}

func (m *markersSeen) run(req execution.Request) execution.Outcome {
	b, _ := os.ReadFile(filepath.Join(req.Workspace, "marker.txt"))
	m.mu.Lock()
	m.seen = append(m.seen, strings.TrimSpace(string(b)))
	n := len(m.seen)
	answer := m.answer
	m.mu.Unlock()
	if answer != nil {
		return answer(n, req)
	}
	return execution.Outcome{Termination: execution.Exited, ExitCode: 0}
}

func (m *markersSeen) all() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.seen...)
}

func stepEvidence(t *testing.T, step *workflow.Step) map[string]any {
	t.Helper()
	var ev map[string]any
	if err := json.Unmarshal(step.Evidence, &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

// Guidance checks select their commit the same way.
func TestGuidanceChecksReadTheWorkTheChangeSaved(t *testing.T) {
	t.Parallel()
	a, ch := step1ReviewGuidance(t, true, `[{"name":"marker","evaluator":"sh","sh":"true"}]`)
	p := a.projectByID(context.Background(), ch.ProjectID)
	hub := a.projectRepoPath(p.Slug)
	folder := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	before := writeMarker(t, folder, "folder")
	saved := writeMarker(t, folder, "saved")
	if err := mirrorBranchForTest(hub, folder, ch.Branch); err != nil {
		t.Fatal(err)
	}
	if err := scm.MarkSaved(context.Background(), folder, hub, ch.Branch, saved); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, folder, "reset", "-q", "--hard", before)
	var mu sync.Mutex
	var seen []string
	installCheckEffects(t, a, checkOverrides{run: func(req ExecRequest) ExecOutcome {
		b, _ := os.ReadFile(filepath.Join(req.Workspace, "marker.txt"))
		mu.Lock()
		seen = append(seen, strings.TrimSpace(string(b)))
		mu.Unlock()
		return ExecOutcome{Termination: execution.Exited}
	}})
	step := reviewExecute(t, a, step1ReviewRun(t, a, ch), "guidance")
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || seen[0] != "saved" || stepEvidence(t, step)["commit"] != saved {
		t.Fatalf("guidance checked %q, %s; want the saved %s", seen, step.Evidence, saved)
	}
}

// A landing tests its pinned candidate from the project's repository that
// holds it, even with no run folder of its own.
func TestALandingTestsItsCandidateWithoutItsFolder(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	seen := &markersSeen{}
	testsExecutor(t, a, seen.run)
	run, source := reviewLanding(t, a, []schema.Step{{Name: "tests", Uses: "tests", With: map[string]any{"command": "cat marker.txt"}}},
		map[string]string{"marker.txt": "candidate\n"}, nil)
	if err := os.RemoveAll(filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("run-%d", run.ID))); err != nil {
		t.Fatal(err)
	}
	step := reviewExecute(t, a, run, "tests")
	ev := stepEvidence(t, step)
	if step.Status != workflow.StatusPass || ev["candidate"] != source.Landing.Candidate || ev["commit"] != source.Landing.Candidate || strings.Join(seen.all(), ",") != "candidate" {
		t.Fatalf("landing: %s %s, tested %q; want candidate %s", step.Status, step.Evidence, seen.all(), source.Landing.Candidate)
	}
}

// A change that adds the tests' command (a Makefile with a test target)
// to a base without it has no tests at the base: its failures are its own,
// never carried because the command could not run on the base, which is
// not asked.
func TestABaseWithoutTheDetectedTestCommandCarriesNothing(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	seen := &markersSeen{answer: func(int, execution.Request) execution.Outcome {
		return execution.Outcome{Termination: execution.Exited, ExitCode: 2}
	}}
	testsExecutor(t, a, seen.run)
	run, _ := reviewLanding(t, a, []schema.Step{{Name: "tests", Uses: "tests"}}, map[string]string{"Makefile": "test:\n\tfalse\n"}, nil)
	step := reviewExecute(t, a, run, "tests")
	ev := stepEvidence(t, step)
	base, _ := ev["base"].(string)
	if step.Status != workflow.StatusFail || ev["carried"] != nil || !strings.Contains(base, "no make test") || len(seen.all()) != 1 {
		t.Fatalf("a failure where the base has no tests: %s %s, ran %d", step.Status, step.Evidence, len(seen.all()))
	}
}

// With a worker configured, a tests step runs on the worker and nothing on
// this machine: its command passes and fails by the worker's exit code, a
// failure's base rerun is a second worker run, and a worker that cannot be
// reached is an error, never a pass or a run here.
func TestATestsStepRunsOnTheWorker(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, command string
		want          workflow.StepState
		runs          int
	}{
		{"passes", "true", workflow.StatusPass, 1},
		{"fails, and the base is asked with the same configuration", "exit 7", workflow.StatusPass, 2},
		{"cannot reach the worker", "true", workflow.StatusError, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, _, ch, _ := testsWorld(t, map[string]any{"command": tc.command})
			bindNodeMirror(t, a, "https://mirror.invalid/node/")
			calls := onWorkerWithATripwire(t, a)
			if tc.runs == 0 {
				a.cfg.worker.URL = "https://127.0.0.1:1"
				rebuildWork(t, a, func(*workflow.Dependencies) {})
			}
			step := reviewExecute(t, a, testsRun(t, a, ch), "tests")
			_, carried := stepEvidence(t, step)["carried"]
			if step.Status != tc.want || (tc.runs == 2) != carried {
				t.Fatalf("tests on the worker answered %s: %s", step.Status, step.Evidence)
			}
			recorded, _ := os.ReadFile(calls)
			if n := strings.Count(string(recorded), "--init --name apptown-check-"); n != tc.runs {
				t.Fatalf("the worker ran %d checks, want %d:\n%s", n, tc.runs, recorded)
			}
			if configs := strings.Count(string(recorded), "https://mirror.invalid/node/"); configs != tc.runs {
				t.Fatalf("%d of %d worker runs got the registry configuration:\n%s", configs, tc.runs, recorded)
			}
			onlyTheWorker(t, calls)
		})
	}
}

const postgresApp = "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres: {}\n"

const postgresURL = "DATABASE_URL=postgres://app:app@postgres:5432/app?sslmode=disable"

// commitAppDeclaration commits .apptown/app.yaml into the change's folder.
func commitAppDeclaration(t *testing.T, folder, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(folder, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, ".apptown", "app.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	commitFolder(t, folder)
}

// On a worker, the needs run there, beside the tests, on the check's own
// network, and the tests reach them with the app's environment; nothing
// runs on this machine.
func TestATestsStepOnTheWorkerRunsBesideTheAppsNeeds(t *testing.T) {
	t.Parallel()
	a, _, ch, folder := testsWorld(t, map[string]any{"command": "make test"})
	a.SyncStockDependencies(context.Background())
	commitAppDeclaration(t, folder, postgresApp)
	calls := onWorkerWithATripwire(t, a)
	step := reviewExecute(t, a, testsRun(t, a, ch), "tests")
	if step.Status != workflow.StatusPass {
		t.Fatalf("tests beside postgres on the worker: %s %s", step.Status, step.Evidence)
	}
	recorded, _ := os.ReadFile(calls)
	var service, check string
	for _, line := range strings.Split(string(recorded), "\n") {
		switch {
		case strings.HasPrefix(line, "worker run -d"):
			service = line
		case strings.HasPrefix(line, "worker run --cpus"):
			check = line
		}
	}
	if !strings.Contains(service, "--network-alias postgres") || !strings.Contains(service, "postgres:16-alpine") {
		t.Fatalf("no postgres beside the tests on the worker:\n%s", recorded)
	}
	if !strings.Contains(check, postgresURL) || !strings.Contains(check, "--network apptown-check-") {
		t.Fatalf("the tests did not reach postgres with the app's environment: %s", check)
	}
	onlyTheWorker(t, calls)
}
