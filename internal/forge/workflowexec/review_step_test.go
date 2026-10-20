package workflowexec

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
)

// admittedReviewStep admits a run whose one stage is the review step
// "review", for a change created by the fixture's administrator; dispatch
// is stopped.
func admittedReviewStep(t *testing.T) (*taskForge, ids.ID, ids.ID) {
	t.Helper()
	f, _ := newTaskForge(t, "review", schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
		{Name: "work", Steps: []schema.Step{{Name: "review", Uses: ReviewKind}}}}})
	f.work.Stop()
	change, err := f.work.Create(f.ctx, workflow.ChangeDraft{ProjectID: f.projectID, Title: "review the change"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.work.StartRun(f.ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "review"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := f.work.Snapshot(f.ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snapshot.Steps {
		if step.Name == "review" && step.Type == ReviewKind && step.Status == workflow.StatusPending {
			return f, run.ID, step.ID
		}
	}
	t.Fatalf("pending review step not materialized: %+v", snapshot.Steps)
	return nil, ids.Nil, ids.Nil
}

func writeVerdict(t *testing.T, ws, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ws, ".apptown", "review.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---- the review -------------------------------------------------------------

// A reviewer's verdict whose recording fails is recorded again; the reviewer
// is not consulted again for it.
func TestReviewVerdictSettlementRetriesWithoutReviewingAgain(t *testing.T) {
	t.Parallel()
	a, _, stepID := admittedReviewStep(t)
	f := installFakePi(a, func(_ int, req execution.Request) execution.Outcome {
		writeVerdict(t, req.Workspace, `{"verdict":"pass","summary":"all claims hold","findings":[]}`)
		dbtest.FailNextStatement(t, a.db, "-- name: FinishAttempt ")
		return execution.Outcome{Output: piSays("reviewed")}
	})
	a.agents.Settle = workflow.Settlement{Wait: time.Millisecond}
	if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
		t.Fatal(err)
	}
	status, ev := stepStatus(t, a, stepID)
	if status != workflow.StatusPass || ev["verdict"] != "pass" || len(f.requests()) != 1 {
		t.Fatalf("want the verdict recorded after one review, got %s %v (invocations %d)", status, ev, len(f.requests()))
	}
}

// A review commits nothing and leaves nothing: what the reviewer changed in
// the change's folder while judging it (a cache from running the tests, an
// edit to a tracked file) is discarded, so neither the review's step nor
// the next producing step commits it as the change's. The verdict, under
// .apptown, is kept and counts.
func TestAReviewCommitsNothingAndLeavesNothing(t *testing.T) {
	t.Parallel()
	a, _, stepID := admittedReviewStep(t)
	var ws, reviewed string
	installFakePi(a, func(_ int, req execution.Request) execution.Outcome {
		ws = req.Workspace
		reviewed = gittest.Run(t, ws, "rev-parse", "HEAD")
		tracked := strings.Fields(gittest.Run(t, ws, "ls-files"))
		if len(tracked) == 0 {
			t.Fatal("the folder tracks no file to edit")
		}
		if err := os.WriteFile(filepath.Join(ws, tracked[0]), []byte("the reviewer was here\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(ws, "__pycache__"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, "__pycache__", "app.cpython-312.pyc"), []byte("cache"), 0o644); err != nil {
			t.Fatal(err)
		}
		writeVerdict(t, ws, `{"verdict":"pass","summary":"all claims hold","findings":[]}`)
		return execution.Outcome{Output: piSays("reviewed")}
	})
	if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
		t.Fatal(err)
	}
	if status, ev := stepStatus(t, a, stepID); status != workflow.StatusPass || ev["verdict"] != "pass" {
		t.Fatalf("the review is %s: %v", status, ev)
	}
	if head := gittest.Run(t, ws, "rev-parse", "HEAD"); head != reviewed {
		t.Fatalf("the review committed: the folder is at %s, it was given %s\n%s", head, reviewed, gittest.Run(t, ws, "log", "--stat", "-1"))
	}
	if left := gittest.Run(t, ws, "status", "--porcelain", "--", ".", ":(exclude).apptown"); left != "" {
		t.Fatalf("the reviewer's changes are still in the folder:\n%s", left)
	}
}
