package workflowexec

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// What a worker task's result becomes when a write fails or AppTown stops
// part-way: the worker's workspace, the only copy of work AppTown has not
// kept, is held; a publication the branch owner applied is published, never
// refused; and the step's evidence names the ref that keeps the result.

// corruptResult replies to every collection with a bundle that fails the
// import's checks, so the result is never kept. first runs on the first.
func corruptResult(first func()) func(int, http.ResponseWriter, *httptest.ResponseRecorder) {
	return func(n int, w http.ResponseWriter, reply *httptest.ResponseRecorder) {
		if n == 1 && first != nil {
			first()
		}
		reply.Body.Reset()
		reply.Body.WriteString("# v2 git bundle\nnot a bundle\n")
		pass(w, reply)
	}
}

// requireHeld checks the attempt is held (its result not kept), not
// released, and its workspace still on the worker.
func requireHeld(t *testing.T, a *taskAppTown, fake *executionfake.Tasks, key, when string) {
	t.Helper()
	attempt, err := tasks.New(a.db).Get(context.Background(), key)
	if err != nil || attempt.Observed != tasks.Held || attempt.Released || !fake.Present(key) {
		t.Fatalf("%s: the attempt's workspace is not held: %+v %v; present %v", when, attempt, err, fake.Present(key))
	}
}

// requireRunning checks the step still runs at its attempt, and the attempt
// is neither settled nor released.
func requireRunning(t *testing.T, a *taskAppTown, fake *executionfake.Tasks, step *workflow.Step) tasks.Attempt {
	t.Helper()
	current, err := workflow.ReadStepIn(context.Background(), a.db, step.ID)
	if err != nil || current.Status != workflow.StatusRunning {
		t.Fatalf("the step is not left running: %v %v", current.Status, err)
	}
	attempt, err := tasks.New(a.db).Get(context.Background(), taskKeyOf(step))
	if err != nil || attempt.Observed == tasks.Settled || attempt.Released || !fake.Present(taskKeyOf(step)) {
		t.Fatalf("the attempt is not left recorded: %+v %v", attempt, err)
	}
	return attempt
}

// A hold that cannot be recorded does not end the step: the step stays
// running at its attempt and the attempt collecting, so the restarted AppTown
// collects again and holds the workspace then. It is never released.
func TestAHoldThatCannotBeRecordedIsCollectedAgain(t *testing.T) {
	t.Parallel()
	var db *database.DB
	a, fake, step, _ := runOnResultWorkerWith(t, map[string]string{"only-copy.txt": "keep this work\n"},
		corruptResult(func() { t.Cleanup(db.FailStatements("-- name: HoldAttempt", 3)) }), func(a *taskAppTown) { db = a.db })
	if attempt := requireRunning(t, a, fake, step); attempt.Observed != tasks.Collecting {
		t.Fatalf("the attempt is %s, want collecting", attempt.Observed)
	}
	b := restarted(t, a)
	settled := awaitStep(t, b, step.ID, workflow.StatusError)
	if !strings.Contains(string(settled.Evidence), "result not kept") {
		t.Fatalf("the step's evidence: %s", settled.Evidence)
	}
	if _, err := b.tasks.ReleaseSettled(context.Background()); err != nil {
		t.Fatal(err)
	}
	requireHeld(t, b, fake, taskKeyOf(step), "after the restart")
}

// An AppTown that stops after recording the hold and before the step's error
// leaves the step running: the restarted AppTown finishes it as an error
// from the record, without collecting the result again (only the first
// collection is corrupt here), and the workspace stays held.
func TestACrashBetweenTheHoldAndTheStepKeepsTheHold(t *testing.T) {
	t.Parallel()
	corruptOnce := func(n int, w http.ResponseWriter, reply *httptest.ResponseRecorder) {
		if n == 1 {
			corruptResult(nil)(n, w, reply)
			return
		}
		pass(w, reply)
	}
	a, fake, step, _ := runOnResultWorkerWith(t, map[string]string{"only-copy.txt": "keep this work\n"}, corruptOnce,
		func(a *taskAppTown) { a.tasks.AbandonAt = func(s string) bool { return s == "held" } })
	a.tasks.AbandonAt = nil
	requireRunning(t, a, fake, step)
	requireHeld(t, a, fake, taskKeyOf(step), "before the restart")
	b := restarted(t, a)
	settled := awaitStep(t, b, step.ID, workflow.StatusError)
	if !strings.Contains(string(settled.Evidence), "result not kept") {
		t.Fatalf("the step's evidence: %s", settled.Evidence)
	}
	if _, err := b.tasks.ReleaseSettled(context.Background()); err != nil {
		t.Fatal(err)
	}
	requireHeld(t, b, fake, taskKeyOf(step), "after the restart")
}

// A step that ended while its result was being collected, with no candidate
// or refused ref kept, does not release the workspace at a restart: its
// step having ended is not permission to discard the only copy.
func TestAStepEndedBeforeItsResultWasKeptHoldsTheWorkspace(t *testing.T) {
	t.Parallel()
	var db *database.DB
	a, fake, step, run := runOnResultWorkerWith(t, map[string]string{"only-copy.txt": "keep this work\n"},
		corruptResult(func() { t.Cleanup(db.FailStatements("-- name: HoldAttempt", 3)) }), func(a *taskAppTown) { db = a.db })
	requireRunning(t, a, fake, step)
	if _, err := a.work.Abandon(a.ctx, workflow.StopWork{RunID: run.ID, Reason: "stopped while collecting"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.work.RecoverExecution(a.ctx, step.ID, 1); !errors.Is(err, workflow.ErrStepAttemptLost) {
		t.Fatalf("the step still belongs to its attempt: %v", err)
	}
	b := restarted(t, a)
	if _, err := b.tasks.ReleaseSettled(context.Background()); err != nil {
		t.Fatal(err)
	}
	requireHeld(t, b, fake, taskKeyOf(step), "after the restart")
}

// stepResult is the result a step's evidence names.
type stepResult struct {
	Ref     string `json:"ref"`
	Commit  string `json:"commit"`
	Refused bool   `json:"refused"`
}

func resultOf(t *testing.T, step *workflow.Step) stepResult {
	t.Helper()
	var evidence struct {
		Result stepResult `json:"result"`
	}
	if err := json.Unmarshal(step.Evidence, &evidence); err != nil {
		t.Fatal(err)
	}
	return evidence.Result
}

// requireKept checks the step's evidence names the result as Git keeps it,
// read through the owner (repositoryedit.FindCandidate).
func requireKept(t *testing.T, a *taskAppTown, step *workflow.Step, run *workflow.Run, refused bool) stepResult {
	t.Helper()
	attempt, err := tasks.New(a.db).Get(context.Background(), taskKeyOf(step))
	if err != nil {
		t.Fatal(err)
	}
	hub := a.source(t, run).Repository(a.dir)
	kept, found, err := repositoryedit.FindCandidate(context.Background(), hub, step.ID, 1, attempt.Source)
	if err != nil || !found {
		t.Fatalf("no result kept: %v", err)
	}
	got := resultOf(t, step)
	want := stepResult{Ref: kept.Ref, Commit: kept.Tip, Refused: kept.Refused}
	if got != want || kept.Refused != refused {
		t.Fatalf("the step's evidence names %+v; Git keeps %+v (refused wanted %v)", got, want, refused)
	}
	return got
}

// A publication whose branch moved but whose record could not be settled is
// published, not refused: no refused ref, no commit.unpublished notice. The
// result is asked again, which reads the branch and finishes the notice and
// the step without publishing again; the branch owner settles its record.
func TestAPublicationNotSettledIsPublishedNotRefused(t *testing.T) {
	t.Parallel()
	var db *database.DB
	a, fake, step, run := runOnResultWorkerWith(t, map[string]string{"published.txt": "work\n"}, failSettlementOnce(&db), func(a *taskAppTown) { db = a.db })
	settled := awaitStep(t, a, step.ID, workflow.StatusPass)
	requirePublishedOnce(t, a, step, settled, run)
	if got, err := a.branches.Recover(context.Background()); err != nil || got.Inspected != 0 {
		t.Fatalf("the publication is not settled: %+v %v", got, err)
	}
	awaitReleased(t, a, fake, taskKeyOf(step))
}

// failSettlementOnce passes the worker's result, and fails the next
// settlement of a branch publication, the one publishing it.
func failSettlementOnce(db **database.DB) func(int, http.ResponseWriter, *httptest.ResponseRecorder) {
	return func(n int, w http.ResponseWriter, reply *httptest.ResponseRecorder) {
		if n == 1 {
			(*db).FailNextStatement("-- name: SettlePublication")
		}
		pass(w, reply)
	}
}

// requirePublishedOnce checks a result published once: the branch at the
// candidate, named by the evidence, one commit.created and no refusal.
func requirePublishedOnce(t *testing.T, a *taskAppTown, step, settled *workflow.Step, run *workflow.Run) {
	t.Helper()
	hub := a.source(t, run).Repository(a.dir)
	branch := scm.Ref(t.Context(), hub, "refs/heads/"+a.source(t, run).Branch)
	result := requireKept(t, a, settled, run, false)
	if result.Ref != repositoryedit.CandidateRef(step.ID, 1) || result.Commit != branch {
		t.Fatalf("the evidence names %+v; the branch is at %s", result, branch)
	}
	if kept := scm.Ref(t.Context(), hub, repositoryedit.RefusedStepRef(step.ID, 1)); kept != "" {
		t.Fatalf("published work is kept as refused: %s", kept)
	}
	if n := runEvents(t, a, run, "commit.unpublished"); len(n) != 0 {
		t.Fatalf("published work was noted as unpublished: %+v", n)
	}
	if created := runEvents(t, a, run, "commit.created"); len(created) != 1 || created[0]["sha"] != branch {
		t.Fatalf("published: %+v", created)
	}
}

// An AppTown that stops after the branch moved and before the publication was
// settled (its record left for the branch owner) finishes it after a
// restart: the owner settles the record, and the step passes with its
// notice, publishing nothing again and never running the task again.
func TestACrashAfterAnUnsettledPublicationFinishesIt(t *testing.T) {
	t.Parallel()
	var db *database.DB
	a, fake, step, run := runOnResultWorkerWith(t, map[string]string{"published.txt": "work\n"}, failSettlementOnce(&db),
		func(a *taskAppTown) {
			db = a.db
			a.tasks.AbandonAt = func(s string) bool { return s == "publication pending" }
		})
	a.tasks.AbandonAt = nil
	requireRunning(t, a, fake, step)
	source := a.source(t, run)
	hub := source.Repository(a.dir)
	published := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
	if published != scm.Ref(t.Context(), hub, repositoryedit.CandidateRef(step.ID, 1)) {
		t.Fatalf("the branch %s did not move to the candidate", published)
	}
	for _, kind := range []string{"commit.created", "commit.unpublished"} {
		if n := runEvents(t, a, run, kind); len(n) != 0 {
			t.Fatalf("%s before the restart: %+v", kind, n)
		}
	}
	b := restarted(t, a)
	settled := awaitStep(t, b, step.ID, workflow.StatusPass)
	if tip := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch); tip != published {
		t.Fatalf("the branch moved again, to %s", tip)
	}
	requirePublishedOnce(t, b, step, settled, run)
	if got, err := b.branches.Recover(context.Background()); err != nil || got.Inspected != 0 {
		t.Fatalf("the publication is not settled: %+v %v", got, err)
	}
	if n := fake.Starts(taskKeyOf(step)); n != 1 {
		t.Fatalf("the task started %d times", n)
	}
	awaitReleased(t, b, fake, taskKeyOf(step))
}

// A refused result's evidence names its refused ref, the commit Git keeps
// there and the refusal: when the step settles first, and when a restarted
// AppTown settles it after a stop between the refusal and the step.
func TestARefusalsEvidenceNamesTheRefusedRef(t *testing.T) {
	t.Parallel()
	for _, when := range []string{"first settlement", "after a restart"} {
		t.Run(when, func(t *testing.T) {
			t.Parallel()
			a, fake, step := fakeTaskFixture(t, true)
			state := a.onWorker(t, fake)
			run, err := workflow.ReadRunIn(context.Background(), a.db, step.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if when == "after a restart" {
				a.tasks.AbandonAt = func(s string) bool { return s == "published" }
			}
			executed := make(chan error, 1)
			go func() { executed <- a.work.ExecuteStep(a.ctx, step.ID) }()
			key := taskKeyOf(step)
			awaitStarted(t, fake, key, "the task never started on the worker")
			handOver(t, a, run, a.source(t, run).Branch)
			writeRemote(t, filepath.Join(state, "workspaces", key), map[string]string{"kept.txt": "work\n"})
			fake.Exit(key, 0)
			if err := <-executed; err != nil {
				t.Fatal(err)
			}
			a.tasks.AbandonAt = nil
			settler := a
			if when == "after a restart" {
				requireRunning(t, a, fake, step)
				settler = restarted(t, a)
			}
			settled := awaitStep(t, settler, step.ID, workflow.StatusError)
			result := requireKept(t, settler, settled, run, true)
			if result.Ref != repositoryedit.RefusedStepRef(step.ID, 1) {
				t.Fatalf("the evidence names %s", result.Ref)
			}
			if c := scm.Ref(t.Context(), a.source(t, run).Repository(a.dir), repositoryedit.CandidateRef(step.ID, 1)); c != "" {
				t.Fatalf("the refused result is still a candidate: %s", c)
			}
			awaitReleased(t, settler, fake, key)
		})
	}
}

// Replaying a result reads the branch before its refusal: a refused result
// whose commit the branch holds now (published by the branch's new holder)
// is answered from the branch, not as refused.
func TestAReplayedResultReadsTheBranchBeforeItsRefusal(t *testing.T) {
	t.Parallel()
	a, fake, step, run, remote := startOnWorker(t)
	source := a.source(t, run)
	hub, ws := source.Repository(a.dir), source.Workspace(a.dir)
	handOver(t, a, run, source.Branch)
	writeRemote(t, remote, map[string]string{"kept.txt": "work\n"})
	fake.Exit(taskKeyOf(step), 0)
	awaitStep(t, a, step.ID, workflow.StatusError)
	attempt, err := tasks.New(a.db).Get(context.Background(), taskKeyOf(step))
	if err != nil {
		t.Fatal(err)
	}
	refused, found, err := repositoryedit.FindCandidate(context.Background(), hub, step.ID, 1, attempt.Source)
	if err != nil || !found || !refused.Refused {
		t.Fatalf("the refused result: %+v %v %v", refused, found, err)
	}
	if _, err := a.branches.Publish(context.Background(), scm.Publication{ProjectID: run.ProjectID, Repository: hub, Branch: source.Branch, Commit: refused.Tip,
		Grant: scm.Grant{Holder: scm.TicketHolder(idstest.N(1))}}); err != nil {
		t.Fatal(err)
	}
	folder := gitexec.LockFolder(ws)
	defer folder.Release()
	got, err := repositoryedit.PublishTaskResult(context.Background(), a.db, a.dir, a.branches,
		noPublisher{}, run, step, folder, refused, time.Now())
	if err != nil || got != refused {
		t.Fatalf("a result the branch holds was answered %+v %v", got, err)
	}
}

// The worker's only copy must survive failure to record the hold after a
// collection error. Arm the write fault when the real result is returned,
// after the collecting observation has already been recorded.
func TestTheWorkersOnlyCopySurvivesAHoldThatCannotBeRecorded(t *testing.T) {
	a, fake, step := fakeTaskFixture(t, true)
	state := resultWorker(t, a, fake, func(_ int, w http.ResponseWriter, reply *httptest.ResponseRecorder) {
		dbtest.FailNextStatement(t, a.db, "-- name: HoldAttempt")
		reply.Body.Reset()
		reply.Body.WriteString("# v2 git bundle\nnot a bundle\n")
		pass(w, reply)
	})
	run, err := workflow.ReadRunIn(context.Background(), a.db, step.RunID)
	if err != nil {
		t.Fatal(err)
	}
	source := a.source(t, run)
	key := taskKeyOf(step)
	done := make(chan error, 1)
	go func() { done <- a.work.ExecuteStep(a.ctx, step.ID) }()
	awaitStarted(t, fake, key, "task never started")
	writeRemote(t, filepath.Join(state, "workspaces", key), map[string]string{"only-copy.txt": "keep this work\n"})
	fake.Exit(key, 0)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	awaitStep(t, a, step.ID, workflow.StatusError)
	for _, ref := range []string{repositoryedit.CandidateRef(step.ID, 1), repositoryedit.RefusedStepRef(step.ID, 1)} {
		if tip := scm.Ref(t.Context(), source.Repository(a.dir), ref); tip != "" {
			t.Fatalf("unexpected kept work %s", tip)
		}
	}
	b := restarted(t, a)
	if _, err := b.tasks.ReleaseSettled(context.Background()); err != nil {
		t.Fatal(err)
	}
	attempt, err := tasks.New(a.db).Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Released || !fake.Present(key) {
		t.Fatalf("only worker copy was released after failed hold write: observed=%s released=%v present=%v", attempt.Observed, attempt.Released, fake.Present(key))
	}
}

// Losing the log read after a confirmed exit must not discard uncollected
// repository work. Only Output fails; the task and result remain available.
type failingOutputRead struct{ execution.TaskDriver }

func (d failingOutputRead) Output(context.Context, execution.TaskRef, execution.OutputRead) (execution.OutputPage, error) {
	return execution.OutputPage{}, errors.New("the exited task's output could not be read")
}

func TestALostOutputReadKeepsUncollectedWork(t *testing.T) {
	a, fake, step, run := runOnResultWorkerWith(t, map[string]string{"only-copy.txt": "work survives a log failure\n"},
		func(_ int, w http.ResponseWriter, reply *httptest.ResponseRecorder) { pass(w, reply) },
		func(a *taskAppTown) { a.tasks.Driver = failingOutputRead{a.tasks.Driver} })
	key := taskKeyOf(step)
	attempt, err := tasks.New(a.db).Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	hub := a.source(t, run).Repository(a.dir)
	candidate, beforeKept, err := repositoryedit.FindCandidate(context.Background(), hub, step.ID, 1, attempt.Source)
	if err != nil {
		t.Fatal(err)
	}
	if !beforeKept && !fake.Present(key) {
		t.Fatal("worker task disappeared before restart")
	}
	t.Logf("before restart: observed=%s exit=%v released=%v kept=%v", attempt.Observed, attempt.ExitCode, attempt.Released, beforeKept)
	// The step's error names where its result is kept.
	failed, err := workflow.ReadStepIn(context.Background(), a.db, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != workflow.StatusError || !strings.Contains(string(failed.Evidence), candidate.Ref) || !strings.Contains(string(failed.Evidence), candidate.Tip) {
		t.Fatalf("the step's error does not name its kept result %s %s: %s %s", candidate.Ref, candidate.Tip, failed.Status, failed.Evidence)
	}
	a.tasks.Driver = a.tasks.Driver.(failingOutputRead).TaskDriver // output is readable again
	b := restarted(t, a)
	if _, err := b.tasks.ReleaseSettled(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := tasks.New(b.db).Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	_, kept, err := repositoryedit.FindCandidate(context.Background(), hub, step.ID, 1, attempt.Source)
	if err != nil {
		t.Fatal(err)
	}
	if !kept && (after.Released || !fake.Present(key)) {
		t.Fatalf("uncollected work discarded after output failure: observed=%s released=%v present=%v kept=%v", after.Observed, after.Released, fake.Present(key), kept)
	}
}

func TestRecoverySettlesAPublicationWhoseSettlementKeptFailing(t *testing.T) {
	var db *database.DB
	var restore func()
	a, fake, step, run := runOnResultWorkerWith(t, map[string]string{"published.txt": "work\n"},
		func(n int, w http.ResponseWriter, reply *httptest.ResponseRecorder) {
			if n == 1 {
				restore = db.FailStatements("-- name: SettlePublication", 100)
				t.Cleanup(restore)
			}
			pass(w, reply)
		}, func(a *taskAppTown) { db = a.db })
	settled := awaitStep(t, a, step.ID, workflow.StatusPass)
	requirePublishedOnce(t, a, step, settled, run)
	awaitReleased(t, a, fake, taskKeyOf(step))
	restore()
	report, err := a.branches.Recover(context.Background())
	if err != nil || report.Inspected != 1 || report.Settled != 1 {
		t.Fatalf("leftover owner record: %+v %v", report, err)
	}
}

func TestAPendingPublicationResumesAtRestart(t *testing.T) {
	var db *database.DB
	var restore func()
	a, fake, step, run := runOnResultWorkerWith(t, map[string]string{"published.txt": "work\n"},
		func(n int, w http.ResponseWriter, reply *httptest.ResponseRecorder) {
			if n == 1 {
				restore = db.FailStatements("-- name: BeginPublication", 100)
				t.Cleanup(restore)
			}
			pass(w, reply)
		}, func(a *taskAppTown) { db = a.db })
	attempt := requireRunning(t, a, fake, step)
	if attempt.Observed != tasks.Collecting {
		t.Fatalf("pending attempt: %+v", attempt)
	}
	if got := runEvents(t, a, run, "commit.unpublished"); len(got) != 0 {
		t.Fatalf("pending publication refused: %+v", got)
	}
	restore()
	b := restarted(t, a)
	settled := awaitStep(t, b, step.ID, workflow.StatusPass)
	requirePublishedOnce(t, b, step, settled, run)
	awaitReleased(t, b, fake, taskKeyOf(step))
	if n := fake.Starts(taskKeyOf(step)); n != 1 {
		t.Fatalf("task ran %d times", n)
	}
}
