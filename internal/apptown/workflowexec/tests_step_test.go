package workflowexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

// A tests step's command runs on AppTown's check executor against one
// commit it selects. Here the executor is the test's (taskAppTown.checks);
// which executor AppTown chooses (the worker's, or this machine's) is the
// composition root's (TestATestsStepRunsOnTheWorker). A landing's tests,
// which need the landing's pinned candidate, are root journeys too
// (TestALandingTestsItsCandidateWithoutItsFolder); a failure's base rerun
// is asked on a working lane here as well.

// testsWorld is a change whose working lane has one tests step, and its
// folder with a first commit.
func testsWorld(t *testing.T, with map[string]any) (*taskAppTown, *workflow.Change, string) {
	t.Helper()
	f, _ := newTaskAppTown(t, "check", schema.Spec{Kind: "pipeline", Role: "working",
		Stages: []schema.Stage{{Name: "gates", Steps: []schema.Step{{Name: "tests", Uses: TestsKind, With: with}}}}})
	ch := f.beginChange(t)
	folder := filepath.Join(f.dir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	writeMarker(t, folder, "folder")
	return f, ch, folder
}

// commitFolder commits everything in ws, making it a repository first when
// it is not one.
func commitFolder(t *testing.T, ws string) {
	t.Helper()
	if ws == "" {
		t.Fatal("commitFolder needs a folder")
	}
	if _, err := os.Stat(filepath.Join(ws, ".git")); err != nil {
		gittest.Run(t, ws, "init", "-q")
	}
	gittest.Run(t, ws, "add", "-A", "-f")
	gittest.Run(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-q", "--allow-empty", "-m", "the run's work")
}

func writeMarker(t *testing.T, folder, value string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(folder, "marker.txt"), []byte(value+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitFolder(t, folder)
	return gittest.Run(t, folder, "rev-parse", "HEAD")
}

// mirrorBranchForTest puts a folder's branch into the hub as fixture setup.
// It is plain git, not the product path: AppTown publishes only through
// scm.Branches, which checks who holds the branch.
func mirrorBranchForTest(repo, ws, branch string) error {
	out, err := gitexec.Run(context.Background(), repo, "fetch", "--quiet", ws, "+refs/heads/"+branch+":refs/heads/"+branch)
	if err != nil {
		return fmt.Errorf("mirroring %s: %w: %s", branch, err, out)
	}
	return nil
}

// execute runs the run's step called name and answers it as recorded.
func (f *taskAppTown) execute(t *testing.T, run *workflow.Run, name string) *workflow.Step {
	t.Helper()
	snap, err := f.work.Snapshot(f.ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snap.Steps {
		if s.Name == name {
			if err := f.work.ExecuteStep(f.ctx, s.ID); err != nil {
				t.Fatal(err)
			}
			saved, err := workflow.ReadStepIn(context.Background(), f.db, s.ID)
			if err != nil {
				t.Fatal(err)
			}
			return saved
		}
	}
	t.Fatalf("missing step %s", name)
	return nil
}

// retryTests puts run's tests step back to be run again, as a person
// retrying it does, and runs it.
func retryTests(t *testing.T, f *taskAppTown, run *workflow.Run, step *workflow.Step) *workflow.Step {
	t.Helper()
	if _, err := f.work.RetryStep(f.ctx, workflow.RetryCommand{StepID: step.ID}); err != nil {
		t.Fatal(err)
	}
	return f.execute(t, run, "tests")
}

func testsRun(t *testing.T, f *taskAppTown, ch *workflow.Change) *workflow.Run {
	t.Helper()
	return f.admit(t, ch, "check", f.ctx)
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

// A tests step tests the commit the change's views read: the change's saved
// branch while the folder is behind work saved there (a chat on a worker),
// and the branch when the folder is absent. The evidence names that commit.
func TestATestsStepTestsTheWorkTheChangeSaved(t *testing.T) {
	t.Parallel()
	f, ch, folder := testsWorld(t, map[string]any{"command": "cat marker.txt"})
	before := gittest.Run(t, folder, "rev-parse", "HEAD")
	saved := writeMarker(t, folder, "saved")
	// Fixture: the branch in the project's repository has the saved commit,
	// the folder is put back behind it, and the save is recorded as a save
	// on a worker records it (scm.MarkSaved).
	if err := mirrorBranchForTest(f.hub, folder, ch.Branch); err != nil {
		t.Fatal(err)
	}
	if err := scm.MarkSaved(context.Background(), folder, f.hub, ch.Branch, saved); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, folder, "reset", "-q", "--hard", before)

	seen := &markersSeen{}
	f.checks = seen.run
	run := testsRun(t, f, ch)
	step := f.execute(t, run, "tests")
	if step.Status != workflow.StatusPass || stepEvidence(t, step)["commit"] != saved || strings.Join(seen.all(), ",") != "saved" {
		t.Fatalf("behind folder: %s %s, tested %q; want the saved %s", step.Status, step.Evidence, seen.all(), saved)
	}

	if err := os.RemoveAll(folder); err != nil {
		t.Fatal(err)
	}
	step = retryTests(t, f, run, step)
	if step.Status != workflow.StatusPass || stepEvidence(t, step)["commit"] != saved || strings.Join(seen.all(), ",") != "saved,saved" {
		t.Fatalf("absent folder: %s %s, tested %q; want the saved %s", step.Status, step.Evidence, seen.all(), saved)
	}
}

// Source that is missing or cannot be read is an error, whatever the step
// requires, never a pass.
func TestATestsStepWithoutItsSourceIsAnError(t *testing.T) {
	t.Parallel()
	for _, require := range []bool{false, true} {
		t.Run(fmt.Sprint(require), func(t *testing.T) {
			t.Parallel()
			f, ch, folder := testsWorld(t, map[string]any{"require": require})
			seen := &markersSeen{}
			f.checks = seen.run
			run := testsRun(t, f, ch)
			// A folder that is there but damaged by the time the step runs: no
			// repository in it.
			if err := os.RemoveAll(filepath.Join(folder, ".git")); err != nil {
				t.Fatal(err)
			}
			step := f.execute(t, run, "tests")
			if step.Status != workflow.StatusError || len(seen.all()) != 0 {
				t.Fatalf("unreadable source: %s %s, ran %d", step.Status, step.Evidence, len(seen.all()))
			}
		})
	}
}

// The commit is selected once: a branch that moves while the tests run
// changes neither what they read nor the commit the evidence names.
func TestATestsStepNamesTheCommitItSelected(t *testing.T) {
	t.Parallel()
	f, ch, folder := testsWorld(t, map[string]any{"command": "cat marker.txt"})
	selected := gittest.Run(t, folder, "rev-parse", "HEAD")
	seen := &markersSeen{answer: func(int, execution.Request) execution.Outcome {
		writeMarker(t, folder, "moved") // the folder moves on during the run
		return execution.Outcome{Termination: execution.Exited, ExitCode: 0}
	}}
	f.checks = seen.run
	step := f.execute(t, testsRun(t, f, ch), "tests")
	if ev := stepEvidence(t, step); ev["commit"] != selected || strings.Join(seen.all(), ",") != "folder" {
		t.Fatalf("tested %q, evidence %s; want %s", seen.all(), step.Evidence, selected)
	}
}

// Only a witnessed ordinary exit answers. A run that could not start, timed
// out, was cancelled, exited but left its container behind, or whose answer
// was lost is an error: never a pass, never a failure of the tests, and
// never carried debt from the base.
func TestATestsRunWithoutAnAnswerIsAnError(t *testing.T) {
	t.Parallel()
	for name, out := range map[string]execution.Outcome{
		"launch_failed":  {Termination: execution.LaunchFailed, ExitCode: -1, RunErr: errors.New("the worker refused the check")},
		"timed_out":      {Termination: execution.TimedOut, TimedOut: true, ExitCode: -1, RunErr: errors.New("timed out")},
		"canceled":       {Termination: execution.Canceled, ExitCode: -1, RunErr: errors.New("canceled")},
		"cleanup_failed": {Termination: execution.Exited, ExitCode: 0, CleanupErr: errors.New("container left behind")},
		"malformed":      {Termination: execution.LaunchFailed, ExitCode: -1, RunErr: errors.New("the worker's answer to the check: unexpected EOF")},
		"no_answer":      {ExitCode: -1},
		"out_of_memory":  {Termination: execution.OutOfMemory, ExitCode: -1, RunErr: errors.New("the container ran out of memory at its limit of 64 MiB")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, ch, _ := testsWorld(t, map[string]any{"command": "cat marker.txt"})
			seen := &markersSeen{answer: func(int, execution.Request) execution.Outcome { return out }}
			f.checks = seen.run
			step := f.execute(t, testsRun(t, f, ch), "tests")
			ev := stepEvidence(t, step)
			// The evidence names which answer was missing; a malformed reply
			// is the worker's check failing to launch.
			kind := name
			if name == "malformed" {
				kind = string(execution.LaunchFailed)
			}
			if step.Status != workflow.StatusError || ev["carried"] != nil || ev["outcome"] != kind || len(seen.all()) != 1 {
				t.Fatalf("%s: %s %s, ran %d", name, step.Status, step.Evidence, len(seen.all()))
			}
		})
	}
}

// After the change's own tests fail, the base is asked whether it fails
// too. A base rerun that gives no answer is not known, and the change's own
// witnessed failure stands; only a base witnessed failing carries.
func TestABaseWithoutAnAnswerNeverCarriesAFailure(t *testing.T) {
	t.Parallel()
	for name, base := range map[string]execution.Outcome{
		"cleanup_failed": {Termination: execution.Exited, ExitCode: 1, CleanupErr: errors.New("left behind")},
		"timed_out":      {Termination: execution.TimedOut, TimedOut: true, ExitCode: -1},
		"out_of_memory":  {Termination: execution.OutOfMemory, ExitCode: -1},
		"witnessed":      {Termination: execution.Exited, ExitCode: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, ch, _ := testsWorld(t, map[string]any{"command": "exit 1"})
			seen := &markersSeen{answer: func(n int, _ execution.Request) execution.Outcome {
				if n == 1 {
					return execution.Outcome{Termination: execution.Exited, ExitCode: 1}
				}
				return base
			}}
			f.checks = seen.run
			step := f.execute(t, testsRun(t, f, ch), "tests")
			ev := stepEvidence(t, step)
			if len(seen.all()) != 2 {
				t.Fatalf("%s: the base was not asked: ran %d", name, len(seen.all()))
			}
			if name == "witnessed" {
				if step.Status != workflow.StatusPass || ev["carried"] == nil {
					t.Fatalf("a base witnessed failing: %s %s", step.Status, step.Evidence)
				}
				return
			}
			if step.Status != workflow.StatusFail || ev["carried"] != nil || ev["base"] == nil {
				t.Fatalf("%s base: %s %s", name, step.Status, step.Evidence)
			}
		})
	}
}

// A tests run whose answer was lost is asked again as a new run: a test
// that writes to an external service writes again, and nothing makes it
// happen once. Until a witnessed answer, the step is not a pass. The step's
// own command runs here against a service this test controls, which counts
// the writes it receives; the first answer is lost after the write.
func TestALostTestsAnswerIsAskedAgainAndRepeatsItsEffects(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not installed")
	}
	var writes atomic.Int32
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writes.Add(1)
		}
	}))
	defer service.Close()
	f, ch, _ := testsWorld(t, map[string]any{"command": "curl -sf -X POST " + service.URL + "/record"})
	var calls atomic.Int32
	f.checks = func(req execution.Request) execution.Outcome {
		out := (execution.Runner{}).Run(context.Background(), execution.Local, execution.Request{Argv: req.Argv, Workspace: req.Workspace, Timeout: time.Minute})
		if calls.Add(1) == 1 {
			// The command ran and wrote; its answer never reached AppTown.
			return execution.Outcome{Termination: execution.LaunchFailed, ExitCode: -1, RunErr: errors.New("the worker did not answer the check: connection reset")}
		}
		return out
	}
	run := testsRun(t, f, ch)
	first := f.execute(t, run, "tests")
	if first.Status == workflow.StatusPass {
		t.Fatalf("a lost answer passed: %s", first.Evidence)
	}
	if writes.Load() != 1 {
		t.Fatalf("the first run's write: %d", writes.Load())
	}
	second := retryTests(t, f, run, first)
	if second.Status != workflow.StatusPass || writes.Load() != 2 {
		t.Fatalf("asked again: %s %s, the service saw %d writes; want a witnessed pass and the write repeated", second.Status, second.Evidence, writes.Load())
	}
}

// admitRunWithoutAChange admits a run of the tests step in project pid,
// belonging to no change, with dispatch stopped.
func (f *taskAppTown) admitRunWithoutAChange(t *testing.T, pid ids.ID, with map[string]any) *workflow.Run {
	t.Helper()
	f.work.Stop()
	tx, err := f.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.work.AdmitRunIn(f.ctx, tx, workflow.StartRequest{ProjectID: pid, WorkflowName: "check", InputsJSON: `{}`},
		workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "check", Version: 1}, Spec: schema.Spec{Kind: "pipeline",
			Stages: []schema.Stage{{Name: "gates", Steps: []schema.Step{{Name: "tests", Uses: TestsKind, With: with}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return run
}

// prepareTestsRun records real preparation with automatic execution disabled;
// each test retains control of when its tests step runs.
func (f *taskAppTown) prepareTestsRun(t *testing.T, run *workflow.Run) {
	t.Helper()
	var reported error
	work := workflow.New(context.Background(), f.db, workflow.Dependencies{
		Git: workflow.GitWorkspace{DataDir: f.dir}, Repositories: &scm.RepositoryLocks{},
		Report: func(err error) { reported = err },
	})
	if err := work.DispatchStarted(run.ID); err != nil {
		t.Fatal(err)
	}
	work.Wait()
	work.Stop()
	if reported != nil {
		t.Fatal(reported)
	}
}

// A run without a change tests its own folder's HEAD; without that folder
// it reads its branch as published, and with no branch published either it
// is an error.
func TestARunWithoutAChangeTestsItsFolder(t *testing.T) {
	t.Parallel()
	f, _ := newTaskAppTown(t, "check", schema.Spec{Kind: "pipeline"})
	// A project with a repository on main and no recorded way of working:
	// the run is admitted under none.
	pid, err := database.Transact(context.Background(), f.db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "tests-alone", Name: "Tests Alone"})
	})
	if err != nil {
		t.Fatal(err)
	}
	hub := filepath.Join(f.dir, "workspaces", "project-"+pid.String())
	if err := scm.InitializeRepository(t.Context(), hub, "main"); err != nil {
		t.Fatal(err)
	}
	if err := project.New(f.db, nil).SetDefaultBranch(context.Background(), pid, "main"); err != nil {
		t.Fatal(err)
	}
	seen := &markersSeen{}
	f.checks = seen.run
	run := f.admitRunWithoutAChange(t, pid, map[string]any{"command": "cat marker.txt"})
	if !run.ChangeID.IsZero() {
		t.Fatalf("run %d belongs to change %d", run.ID, run.ChangeID)
	}
	// Actual preparation clones the run's branch; its unpublished commit
	// then belongs to this run, rather than to a clone still on main.
	f.prepareTestsRun(t, run)
	folder := filepath.Join(f.dir, "workspaces", fmt.Sprintf("run-%d", run.ID))
	head := writeMarker(t, folder, "folder")
	step := f.execute(t, run, "tests")
	if ev := stepEvidence(t, step); step.Status != workflow.StatusPass || ev["commit"] != head || strings.Join(seen.all(), ",") != "folder" {
		t.Fatalf("tests read %v: %s %s", seen.all(), step.Status, step.Evidence)
	}
	if err := os.RemoveAll(folder); err != nil {
		t.Fatal(err)
	}
	if step = retryTests(t, f, run, step); step.Status != workflow.StatusError || !strings.Contains(string(step.Evidence), "is not in the project's repository") || len(seen.all()) != 1 {
		t.Fatalf("a missing folder answered %s after %v: %s", step.Status, seen.all(), step.Evidence)
	}
}

// A project without a default branch is not waived: a run whose folder is
// absent is an error, and a folder with a commit runs its tests, whose
// failure fails the step, whatever `require` says.
func TestATestsStepWithoutADefaultBranchStillReadsACommit(t *testing.T) {
	t.Parallel()
	for _, require := range []bool{false, true} {
		for _, committed := range []bool{false, true} {
			t.Run(fmt.Sprintf("require=%t/committed=%t", require, committed), func(t *testing.T) {
				t.Parallel()
				f, _ := newTaskAppTown(t, "check", schema.Spec{Kind: "pipeline"})
				// A project recorded, with no repository yet.
				pid, err := database.Transact(context.Background(), f.db, func(tx *database.Tx) (ids.ID, error) {
					return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "tests-uninitialized", Name: "Tests Uninitialized"})
				})
				if err != nil {
					t.Fatal(err)
				}
				seen := &markersSeen{answer: func(int, execution.Request) execution.Outcome {
					return execution.Outcome{Termination: execution.Exited, ExitCode: 7}
				}}
				f.checks = seen.run
				run := f.admitRunWithoutAChange(t, pid, map[string]any{"require": require, "command": "exit 7"})
				source, err := workflow.ReadSourceIn(context.Background(), f.db, workflow.RunRef{ID: run.ID, ProjectID: pid})
				if err != nil {
					t.Fatal(err)
				}
				if source.RepoBacked() {
					t.Fatal("fixture unexpectedly repository-backed")
				}
				folder := requireLocation(t)(source.Workspace(f.dir))
				if _, err := os.Stat(folder); !os.IsNotExist(err) {
					t.Fatalf("fixture source should initially be absent: %v", err)
				}
				want, executions := workflow.StatusError, 0
				if committed {
					if err := os.MkdirAll(folder, 0755); err != nil {
						t.Fatal(err)
					}
					gittest.Run(t, folder, "init", "-q")
					gittest.Run(t, folder, "config", "user.name", "Reviewer")
					gittest.Run(t, folder, "config", "user.email", "reviewer@example.invalid")
					writeMarker(t, folder, "committed")
					want, executions = workflow.StatusFail, 1
				}
				f.prepareTestsRun(t, run)
				step := f.execute(t, run, "tests")
				if step.Status != want || len(seen.all()) != executions {
					t.Fatalf("status=%s evidence=%s executions=%d; want %s with %d executions", step.Status, step.Evidence, len(seen.all()), want, executions)
				}
			})
		}
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

// A tests step runs beside the app's declared needs, fulfilled as the
// dependency catalog fulfils them for a deployment, with the environment
// the app is given to reach them: an app declaring postgres has its tests
// given a database and DATABASE_URL. A need the catalog does not know is
// named in the evidence and the tests run without it; a declaration that
// cannot be read is an error, never a run without its needs.
func TestATestsStepRunsBesideTheAppsDeclaredNeeds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, app   string
		want        workflow.StepState
		services    []string
		unfulfilled bool
	}{
		{"postgres", postgresApp, workflow.StatusPass, []string{"postgres"}, false},
		{"known and unknown needs", postgresApp + "  no-such-kind: {}\n", workflow.StatusPass, []string{"postgres"}, true},
		{"unknown need", strings.Replace(postgresApp, "postgres", "no-such-kind", 1), workflow.StatusPass, nil, true},
		{"unreadable", "needs: [unterminated\n", workflow.StatusError, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, ch, folder := testsWorld(t, map[string]any{"command": "make test"})
			if _, _, err := dependency.NewCatalog(f.db, nil).SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
				t.Fatal(err)
			}
			commitAppDeclaration(t, folder, tc.app)
			var mu sync.Mutex
			var reqs []execution.Request
			f.checks = func(req execution.Request) execution.Outcome {
				mu.Lock()
				reqs = append(reqs, req)
				mu.Unlock()
				return execution.Outcome{Termination: execution.Exited, ExitCode: 0}
			}
			step := f.execute(t, testsRun(t, f, ch), "tests")
			ev := stepEvidence(t, step)
			if step.Status != tc.want || (ev["unfulfilled"] != nil) != tc.unfulfilled {
				t.Fatalf("%s: %s %s", tc.name, step.Status, step.Evidence)
			}
			mu.Lock()
			defer mu.Unlock()
			if tc.want == workflow.StatusError {
				if len(reqs) != 0 {
					t.Fatalf("the tests ran without their declared needs: %+v", reqs)
				}
				return
			}
			var names []string
			for _, s := range reqs[0].Services {
				names = append(names, s.Name)
			}
			if !slices.Equal(names, tc.services) || slices.Contains(reqs[0].Env, postgresURL) != (len(tc.services) > 0) {
				t.Fatalf("the tests ran beside %v with %v", names, reqs[0].Env)
			}
		})
	}
}

const djangoSecrets = `[project]
name = "polls"
revision = "1.0"

[profiles.default]
DJANGO_SECRET_KEY = { description = "signing key", generate = true }
STRIPE_API_KEY = { description = "payments", required = true }
`

// A tests step is given the app's generated secrets as throwaway values
// minted for the run, so settings that need a signing key load; a real
// credential is not given. The evidence names both kinds and carries no
// value.
func TestATestsStepGetsThrowawayGeneratedSecretsAndNoCredentials(t *testing.T) {
	t.Parallel()
	f, ch, folder := testsWorld(t, map[string]any{"command": "make test"})
	if err := os.WriteFile(filepath.Join(folder, "secretspec.toml"), []byte(djangoSecrets), 0o644); err != nil {
		t.Fatal(err)
	}
	commitFolder(t, folder)
	var mu sync.Mutex
	var reqs []execution.Request
	f.checks = func(req execution.Request) execution.Outcome {
		mu.Lock()
		reqs = append(reqs, req)
		mu.Unlock()
		return execution.Outcome{Termination: execution.Exited, ExitCode: 0}
	}
	step := f.execute(t, testsRun(t, f, ch), "tests")
	ev := stepEvidence(t, step)
	if step.Status != workflow.StatusPass {
		t.Fatalf("tests: %s %s", step.Status, step.Evidence)
	}
	mu.Lock()
	defer mu.Unlock()
	var key string
	for _, kv := range reqs[0].Env {
		if v, ok := strings.CutPrefix(kv, "DJANGO_SECRET_KEY="); ok {
			key = v
		}
		if strings.HasPrefix(kv, "STRIPE_API_KEY=") {
			t.Fatalf("a credential was given to the tests: %v", reqs[0].Env)
		}
	}
	if len(key) < 32 {
		t.Fatalf("no generated signing key for the tests: %v", reqs[0].Env)
	}
	given, _ := ev["secretsProvided"].(string)
	withheld, _ := ev["secretsNotProvided"].(string)
	if !strings.Contains(given, "DJANGO_SECRET_KEY") || !strings.Contains(withheld, "STRIPE_API_KEY") || strings.Contains(string(step.Evidence), key) {
		t.Fatalf("the evidence does not name what the tests were given: %s", step.Evidence)
	}
}

// A tests step whose stored configuration does not decode is an error: its
// zero value (no command, not required) is not its configuration, and would
// read as "no tests declared".
//
// Kills: dropping the decode error, which ran the step on the zero value.
func TestATestsStepWhoseConfigurationDoesNotDecodeIsAnError(t *testing.T) {
	t.Parallel()
	f, ch, folder := testsWorld(t, map[string]any{"require": "yes"})
	commitFolder(t, folder)
	seen := &markersSeen{}
	f.checks = seen.run
	run := testsRun(t, f, ch)
	step := f.execute(t, run, "tests")
	if step.Status != workflow.StatusError || !strings.Contains(string(step.Evidence), "configuration could not be read") || len(seen.all()) != 0 {
		t.Fatalf("an unreadable configuration: %s %s, ran %d", step.Status, step.Evidence, len(seen.all()))
	}
}

// goStream is a go test -json stream: one passing test, one skipped with
// its reason.
const goStream = `{"Action":"run","Package":"ex/store","Test":"TestSave"}
{"Action":"output","Package":"ex/store","Test":"TestSave","Output":"--- PASS: TestSave (0.00s)\n"}
{"Action":"pass","Package":"ex/store","Test":"TestSave"}
{"Action":"run","Package":"ex/store","Test":"TestImport"}
{"Action":"output","Package":"ex/store","Test":"TestImport","Output":"    store_test.go:14: WAYPOINT_POSTGRES_TESTS not set\n"}
{"Action":"output","Package":"ex/store","Test":"TestImport","Output":"--- SKIP: TestImport (0.00s)\n"}
{"Action":"skip","Package":"ex/store","Test":"TestImport"}
{"Action":"output","Package":"ex/store","Output":"ok  \tex/store\t0.238s\n"}
{"Action":"pass","Package":"ex/store"}
`

// A tests step offers its command a report file and records what the
// report says: counts, the skipped tests, and a Go stream's log with the
// skip's reason. A report committed with the tree is not the run's; a
// command that writes none, or one that cannot be read, leaves the counts
// unknown and says so. The exit code alone decides the verdict.
func TestATestsStepRecordsWhatItsReportSays(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		report    string
		reportErr error
		counts    string
		note      string
	}{
		"a go stream":       {report: goStream, counts: `{"failed":0,"passed":1,"skipped":1}`},
		"no report":         {report: "", note: "wrote no report"},
		"an unreadable one": {reportErr: errors.New("the report is over the limit"), note: "over the limit"},
		"not a report":      {report: "ok ex/store\n", note: "neither JUnit XML nor"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, ch, folder := testsWorld(t, map[string]any{"command": "go test ./..."})
			if err := os.WriteFile(filepath.Join(folder, ".forge-test-report"), []byte(`<testsuite><testcase name="stale"/></testsuite>`), 0o644); err != nil {
				t.Fatal(err)
			}
			commitFolder(t, folder)
			var asked execution.Request
			var committedSeen bool
			seen := &markersSeen{answer: func(_ int, req execution.Request) execution.Outcome {
				asked = req
				_, err := os.Stat(filepath.Join(req.Workspace, ".forge-test-report"))
				committedSeen = err == nil
				return execution.Outcome{Termination: execution.Exited, ExitCode: 0, Output: []byte("go: downloading x\n"), Report: []byte(tc.report), ReportErr: tc.reportErr}
			}}
			f.checks = seen.run
			step := f.execute(t, testsRun(t, f, ch), "tests")
			ev := stepEvidence(t, step)
			if step.Status != workflow.StatusPass || committedSeen {
				t.Fatalf("%s: %s %s (the committed report was in the tree: %v)", name, step.Status, step.Evidence, committedSeen)
			}
			if asked.Report != ".forge-test-report" || !slices.Contains(asked.Env, "APPTOWN_TEST_REPORT=/work/.forge-test-report") {
				t.Fatalf("the command was not offered the report: %q %q", asked.Report, asked.Env)
			}
			counts, _ := json.Marshal(ev["testCounts"])
			note, _ := ev["testReport"].(string)
			if tc.counts != "" {
				log, _ := ev["logTail"].(string)
				if string(counts) != tc.counts || note != "" || fmt.Sprint(ev["skippedTests"]) != "[ex/store TestImport]" ||
					!strings.Contains(log, "go: downloading x") || !strings.Contains(log, "WAYPOINT_POSTGRES_TESTS not set") || strings.Contains(log, "--- PASS") {
					t.Fatalf("%s: %s", name, step.Evidence)
				}
				return
			}
			_, unreadable := ev["testReportUnreadable"]
			if ev["testCounts"] != nil || !strings.Contains(note, tc.note) || !strings.Contains(note, "not known") || unreadable != (name != "no report") {
				t.Fatalf("%s: %s", name, step.Evidence)
			}
		})
	}
}

// When the change's tests fail and the base is asked, the step's counts
// stay the change's: the base's report is not this change's evidence.
func TestATestsStepCountsTheChangeNotItsBase(t *testing.T) {
	t.Parallel()
	f, ch, _ := testsWorld(t, map[string]any{"command": "exit 1"})
	seen := &markersSeen{answer: func(n int, _ execution.Request) execution.Outcome {
		if n == 1 {
			return execution.Outcome{Termination: execution.Exited, ExitCode: 1, Report: []byte(goStream)}
		}
		return execution.Outcome{Termination: execution.Exited, ExitCode: 1, Report: []byte(`<testsuite><testcase name="a"/><testcase name="b"/></testsuite>`)}
	}}
	f.checks = seen.run
	step := f.execute(t, testsRun(t, f, ch), "tests")
	counts, _ := json.Marshal(stepEvidence(t, step)["testCounts"])
	if len(seen.all()) != 2 || string(counts) != `{"failed":0,"passed":1,"skipped":1}` {
		t.Fatalf("ran %d; evidence %s", len(seen.all()), step.Evidence)
	}
}

// A tests run that fails with nothing that arrived keeps its verdict from
// the exit code (here the base fails too, so it is carried), and its log
// says the report was lost, never an empty log a reader takes for
// "nothing to say".
func TestAFailedTestsRunWhoseOutputDidNotArriveSaysReportLost(t *testing.T) {
	t.Parallel()
	f, ch, folder := testsWorld(t, map[string]any{"command": "go test ./..."})
	commitFolder(t, folder)
	seen := &markersSeen{answer: func(int, execution.Request) execution.Outcome {
		return execution.Outcome{Termination: execution.Exited, ExitCode: 1}
	}}
	f.checks = seen.run
	step := f.execute(t, testsRun(t, f, ch), "tests")
	if log, _ := stepEvidence(t, step)["logTail"].(string); !strings.Contains(log, "report lost") {
		t.Fatalf("%s: %s", step.Status, step.Evidence)
	}
}

// With no test command, a tests step is a failure where a part the step
// applies to is built with a tool that has tests, and not applicable where
// no part is, never a pass on nothing; a command that exists runs
// whatever the contract says, so a project whose contract names no tools
// yet keeps its test run.
//
// Kills: no command read as a pass; a contract that names no tools
// stopping a detected command; not applicable without the step's
// appliesTo, or a failure with no part it applies to.
func TestATestsStepWithNoCommandIsNotApplicableOrAFailure(t *testing.T) {
	t.Parallel()
	goPart := "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n    tools: [go]\n"
	noTools := "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"
	stockWords := []any{"tool:go", "tool:node"}
	for _, tc := range []struct {
		name     string
		with     map[string]any
		contract string
		makefile bool
		want     workflow.StepState
		says     string
		ran      int
	}{
		{"a go part with no command", map[string]any{"appliesTo": stockWords}, goPart, false, workflow.StatusFail, "web is built with a tool that has tests", 0},
		{"no part built with a tool", map[string]any{"appliesTo": stockWords}, noTools, false, workflow.StatusNotApplicable, "no part is built with go or node", 0},
		{"no contract", map[string]any{"appliesTo": stockWords}, "", false, workflow.StatusNotApplicable, "no .apptown/app.yaml", 0},
		{"a step without appliesTo", nil, goPart, false, workflow.StatusNotApplicable, "no test command was found", 0},
		{"required", map[string]any{"require": true}, noTools, false, workflow.StatusFail, "requires tests", 0},
		{"a command found whatever the contract names", map[string]any{"appliesTo": stockWords}, noTools, true, workflow.StatusPass, "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, ch, folder := testsWorld(t, tc.with)
			if tc.makefile {
				if err := os.WriteFile(filepath.Join(folder, "Makefile"), []byte("test:\n\ttrue\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.contract != "" {
				commitAppDeclaration(t, folder, tc.contract)
			} else {
				commitFolder(t, folder)
			}
			seen := &markersSeen{}
			f.checks = seen.run
			step := f.execute(t, testsRun(t, f, ch), "tests")
			reason, _ := stepEvidence(t, step)["reason"].(string)
			if step.Status != tc.want || !strings.Contains(reason, tc.says) || len(seen.all()) != tc.ran {
				t.Fatalf("%s %q, ran %d; want %s saying %q, %d run", step.Status, reason, len(seen.all()), tc.want, tc.says, tc.ran)
			}
		})
	}
}
