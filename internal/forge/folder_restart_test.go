package forge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
)

// A restart from base resets the run's folder to the base, confined, and
// only then rewinds the branch.

// restartWorld is an agent step whose first attempt published P1 on its
// change's branch, retried with a restart. seen records, for each later
// invocation, the folder's HEAD, whether the stray file planted before the
// retry survived, and the branch's tip then.
type restartWorld struct {
	a                   *App
	run                 *workflow.Run
	source              workflow.Source
	stepID              ids.ID
	hub, ws, base, work string
	seen                *[]string
}

func newRestartWorld(t *testing.T) restartWorld {
	t.Helper()
	a := agentTestApp(t)
	runID, stepID := admittedAgentStep(t, a, `{"prompt":"replace work"}`)
	run, err := workflow.ReadRunIn(context.Background(), a.DB, runID)
	if err != nil {
		t.Fatal(err)
	}
	w := restartWorld{a: a, run: run, source: workflowSourceForTest(t, a, run), stepID: stepID, seen: &[]string{}}
	w.hub, w.ws, w.base = w.source.Repository(a.cfg.dataDir), w.source.Workspace(a.cfg.dataDir), w.source.RunBaseSHA
	installFakePi(t, a, func(n int, req ExecRequest) ExecOutcome {
		if n > 1 {
			_, err := os.Stat(filepath.Join(req.Workspace, "stray.txt"))
			*w.seen = append(*w.seen, fmt.Sprintf("%s stray=%t branch=%s", gittest.Run(t, req.Workspace, "rev-parse", "HEAD"), err == nil, scm.Ref(t.Context(), w.hub, "refs/heads/"+w.source.Branch)))
		}
		if err := os.WriteFile(filepath.Join(req.Workspace, "work.txt"), []byte(fmt.Sprintf("attempt %d\n", n)), 0o644); err != nil {
			t.Fatal(err)
		}
		return ExecOutcome{Output: piSays("done")}
	})
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), stepID); err != nil {
		t.Fatal(err)
	}
	if w.base == "" {
		w.source = workflowSourceForTest(t, a, run)
		w.base = w.source.RunBaseSHA
	}
	w.work = scm.Ref(t.Context(), w.hub, "refs/heads/"+w.source.Branch)
	if w.base == "" || w.work == "" || w.work == w.base {
		t.Fatalf("the first attempt published nothing on its base: base=%s branch=%s", w.base, w.work)
	}
	if err := os.WriteFile(filepath.Join(w.ws, "stray.txt"), []byte("left by the first attempt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w restartWorld) restart(t *testing.T) *workflow.Step {
	t.Helper()
	if _, err := w.a.Work.RetryStep(asPerson("alice"), workflow.RetryCommand{StepID: w.stepID, Mode: workflow.RetryRestart}); err != nil {
		t.Fatal(err)
	}
	if err := w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), w.stepID); err != nil {
		t.Fatal(err)
	}
	step, err := workflow.ReadStepIn(context.Background(), w.a.DB, w.stepID)
	if err != nil {
		t.Fatal(err)
	}
	return step
}

// Reset then rewind: the replacement attempt starts from the base, in its
// folder (cleaned) and on its branch, and what it publishes builds on the
// base, never on the rewound work.
func TestARestartResetsTheFolderThenRewindsTheBranch(t *testing.T) {
	t.Parallel()
	w := newRestartWorld(t)
	if step := w.restart(t); step.Status != workflow.StatusPass {
		t.Fatalf("the restart: %s %s", step.Status, step.Evidence)
	}
	want := fmt.Sprintf("%s stray=false branch=%s", w.base, w.base)
	if len(*w.seen) != 1 || (*w.seen)[0] != want {
		t.Fatalf("the replacement attempt started from %v, want %q", *w.seen, want)
	}
	tip := scm.Ref(t.Context(), w.hub, "refs/heads/"+w.source.Branch)
	if parent := gittest.Run(t, w.hub, "rev-parse", tip+"^"); parent != w.base {
		t.Fatalf("the replacement's work builds on %s, want the base %s", parent, w.base)
	}
	if _, err := gitexec.Run(t.Context(), w.hub, "merge-base", "--is-ancestor", w.work, tip); err == nil {
		t.Fatal("the rewound work was published again")
	}
}

// A folder the forge will not run Git in is not reset, and the branch never
// moves: nothing can later republish the rewound work from the folder.
func TestARestartWhoseFolderIsRefusedNeverMovesTheBranch(t *testing.T) {
	t.Parallel()
	w := newRestartWorld(t)
	gittest.Run(t, w.ws, "config", "review.note", "example")
	head := gittest.Run(t, w.ws, "rev-parse", "HEAD")
	step := w.restart(t)
	if step.Status != workflow.StatusError || !strings.Contains(string(step.Evidence), "resetting the workspace") {
		t.Fatalf("a refused reset: %s %s", step.Status, step.Evidence)
	}
	if tip := scm.Ref(t.Context(), w.hub, "refs/heads/"+w.source.Branch); tip != w.work {
		t.Fatalf("the branch moved from %s to %s", w.work, tip)
	}
	if got := gittest.Run(t, w.ws, "rev-parse", "HEAD"); got != head {
		t.Fatalf("a refused folder moved from %s to %s", head, got)
	}
	if _, err := os.Stat(filepath.Join(w.ws, "stray.txt")); err != nil {
		t.Fatalf("a refused folder was cleaned: %v", err)
	}
	if len(*w.seen) != 0 {
		t.Fatalf("the agent ran after a refused reset: %v", *w.seen)
	}
}

// A rewind that fails after the reset leaves the folder at the base and the
// branch at its work: the folder is behind, and the run's readers read the
// branch, never the base in the folder.
func TestARewindThatFailsAfterTheResetLeavesTheFolderBehind(t *testing.T) {
	t.Parallel()
	w := newRestartWorld(t)
	// The hub's branch is locked, as by an update another process left.
	lock := filepath.Join(w.hub, ".git", "refs", "heads", w.source.Branch+".lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, []byte(w.base+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	step := w.restart(t)
	if step.Status != workflow.StatusError || !strings.Contains(string(step.Evidence), "rewinding branch") {
		t.Fatalf("a failed rewind: %s %s", step.Status, step.Evidence)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if tip := scm.Ref(t.Context(), w.hub, "refs/heads/"+w.source.Branch); tip != w.work {
		t.Fatalf("the branch moved from %s to %s", w.work, tip)
	}
	if got := gittest.Run(t, w.ws, "rev-parse", "HEAD"); got != w.base {
		t.Fatalf("the folder was not reset: %s, want %s", got, w.base)
	}
	if behind, err := scm.Behind(context.Background(), w.ws, w.hub); err != nil || !behind {
		t.Fatalf("a folder reset under an unrewound branch is not behind: %v %v", behind, err)
	}
	sel, err := verification.SelectCommit(context.Background(), w.a.DB, w.a.cfg.dataDir, w.run, w.source)
	if err != nil || sel.Commit != w.work {
		t.Fatalf("the run's gates select %+v, want the branch's %s: %v", sel, w.work, err)
	}
	diff := authzCall(w.a.Handler(), "alice", "GET", fmt.Sprintf("/api/v1/changes/%d/diff", w.run.ChangeID), "")
	if diff.Code != 200 || !strings.Contains(diff.Body.String(), "work.txt") {
		t.Fatalf("the change's diff hides its branch's work: %d %s", diff.Code, diff.Body.String())
	}
	files := authzCall(w.a.Handler(), "alice", "GET", fmt.Sprintf("/api/v1/runs/%d/workspace/", w.run.ID), "")
	if files.Code != 409 {
		t.Fatalf("a folder behind its branch is shown: %d %s", files.Code, files.Body.String())
	}
}
