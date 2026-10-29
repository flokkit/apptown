package apptown

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/gitexec"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

func mergeSourceRun(t *testing.T, branch string, steps ...schema.Step) (*App, *workflow.Run) {
	t.Helper()
	a := testApp(t)
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "merge-source", Name: "Merge Source", Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if branch != "" {
		if err := a.Projects.SetDefaultBranch(context.Background(), p.ID, branch); err != nil {
			t.Fatal(err)
		}
	}
	installFakeGit(t, a, nil)
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish", Steps: steps}}}})
	ctx := asPerson("alice")
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Title: "Merge source"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Stop()
	run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: change.ID, Workflow: "land"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	// These tests build the repository after the run, to reach SCM's Git
	// failure paths; a landing admitted now would have nothing to pin and be
	// refused before them.
	// sql-guarantee: model a landing admitted before step 1b, which recorded no candidate and no pinning marker.
	completionExec(t, a.DB, `DELETE FROM run_events WHERE run_id=$1 AND kind IN ('landing.candidate','landing.pin.required')`, run.ID)
	return a, run
}

// mergeSourceReceiptCount is every run event Work holds.
func mergeSourceReceiptCount(t *testing.T, a *App) int {
	t.Helper()
	return workEventCount(t, a, nil)
}

func readWorkflowMergeSource(ctx context.Context, db database.Queryer, run *workflow.Run) (workflow.MergeSource, error) {
	return workflow.ReadMergeSourceIn(ctx, db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
}

func mergeSourceForTest(t *testing.T, a *App, run *workflow.Run) workflow.MergeSource {
	t.Helper()
	source, err := readWorkflowMergeSource(context.Background(), a.DB, run)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestMergeSourceReadFailureCannotGrantMergeOrAssertion(t *testing.T) {
	t.Parallel()
	a, run := mergeSourceRun(t, "", schema.Step{Name: "merged", Uses: "scm/merged"})
	store := a.DB
	before := mergeSourceReceiptCount(t, a)
	dbtest.FailNextStatement(t, store, "name: WorkflowSource")
	result, err := mergesFor(a).advanceContext(context.Background(), run)
	if err == nil || result.Allowed || result.Blocking != nil {
		t.Fatalf("failed metadata became no-repo permission: %+v %v", result, err)
	}
	snapshot, err := a.Work.Snapshot(asPerson("alice"), run.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 {
		t.Fatalf("declared merge assertion: %+v %v", snapshot, err)
	}
	id := snapshot.Steps[0].ID
	dbtest.FailNextStatement(t, store, "name: WorkflowSource")
	assertions := workflow.NewAssertions(store, mergesFor(a).owner())
	observed, err := assertions.Evaluate(context.Background(), workflow.Assertion{StepID: id})
	if err == nil || observed != nil {
		t.Fatalf("failed metadata became merged assertion: %+v %v", observed, err)
	}
	if got := mergeSourceReceiptCount(t, a); got != before {
		t.Fatalf("failed reads wrote receipts: before=%d after=%d", before, got)
	}
	requireSteps(t, a, 1, func(s *workflow.Step) bool { return s.Status == workflow.StatusPending })

	result, err = mergesFor(a).advanceContext(context.Background(), run)
	if err != nil || !result.Allowed {
		t.Fatalf("actual non-repository run was refused: %+v %v", result, err)
	}
}

func mergeRunForTest(t *testing.T, a *App, run *workflow.Run) (bool, []string, []string) {
	t.Helper()
	observation, err := mergeEffectForTest(t, a, mergeSourceForTest(t, a, run))
	if err != nil {
		t.Fatal(err)
	}
	return observation.Merged, observation.Conflicts, observation.Drift
}
func branchMergedForTest(t *testing.T, a *App, run *workflow.Run) bool {
	t.Helper()
	source := mergeSourceForTest(t, a, run)
	merged, err := gitAncestorForTest(t, requireLocation(t)(source.Repository(a.cfg.dataDir)), "refs/heads/"+source.Branch, "refs/heads/"+source.DefaultBranch)
	if err != nil {
		t.Fatal(err)
	}
	return merged
}

func mergeSourceRepository(t *testing.T, source workflow.MergeSource, dataDir string) string {
	t.Helper()
	repo := requireLocation(t)(source.Repository(dataDir))
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.invalid"}, {"commit", "--allow-empty", "-m", "initial"}} {
		if out, err := gitexec.Run(t.Context(), repo, args...); err != nil {
			t.Fatalf("%v: %s %v", args, out, err)
		}
	}
	return repo
}

func TestMergeSourceGitFailuresCannotBecomeEmptyWork(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"missing repository", "missing default branch", "failed workspace publication"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			a, run := mergeSourceRun(t, "main")
			before := mergeSourceReceiptCount(t, a)
			source := mergeSourceForTest(t, a, run)
			if failure != "missing repository" {
				mergeSourceRepository(t, source, a.cfg.dataDir)
			}
			if failure == "missing default branch" {
				if err := a.Projects.SetDefaultBranch(context.Background(), run.ProjectID, "missing"); err != nil {
					t.Fatal(err)
				}
				source = mergeSourceForTest(t, a, run)
			}
			if failure == "failed workspace publication" {
				if err := os.MkdirAll(filepath.Join(requireLocation(t)(source.Workspace(a.cfg.dataDir)), ".git"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			observation, err := mergeEffectForTest(t, a, source)
			if err == nil || observation.Merged {
				t.Fatalf("%s became a successful merge: %+v %v", failure, observation, err)
			}
			result, err := mergesFor(a).advanceContext(context.Background(), run)
			if err == nil || result.Allowed || result.Blocking != nil {
				t.Fatalf("%s advanced: %+v %v", failure, result, err)
			}
			if got := mergeSourceReceiptCount(t, a); got != before {
				t.Fatalf("failed Git source wrote receipts: before=%d after=%d", before, got)
			}
			requireSteps(t, a, 0, nil)
			requireMergeRecord(t, a, run.ID, scm.Idle, "")
		})
	}
}

func TestMergeProofUsesDefaultBranchAndMergeRefusesWrongCheckout(t *testing.T) {
	t.Parallel()
	a, run := mergeSourceRun(t, "main")
	before := mergeSourceReceiptCount(t, a)
	source := mergeSourceForTest(t, a, run)
	repo := mergeSourceRepository(t, source, a.cfg.dataDir)
	// A landing with no pinned candidate (admitted before pinning) merges
	// nothing from its workspace: it is refused, to be restarted.
	observation, err := mergeEffectForTest(t, a, source)
	if err == nil || observation.Merged || !strings.Contains(err.Error(), "restart it to land") {
		t.Fatalf("an unpinned landing: %+v %v", observation, err)
	}
	for _, args := range [][]string{{"checkout", "-b", source.Branch}, {"commit", "--allow-empty", "-m", "unmerged work"}} {
		if out, err := gitexec.Run(t.Context(), repo, args...); err != nil {
			t.Fatalf("%v: %s %v", args, out, err)
		}
	}
	merged, err := gitAncestorForTest(t, repo, "refs/heads/"+source.Branch, "refs/heads/"+source.DefaultBranch)
	if err != nil || merged {
		t.Fatalf("being on the work branch is not a merge into main: %v %v", merged, err)
	}
	observation, err = mergeEffectForTest(t, a, source)
	if err == nil || observation.Merged {
		t.Fatalf("merged into the wrong checkout: %+v %v", observation, err)
	}
	if got := mergeSourceReceiptCount(t, a); got != before {
		t.Fatalf("wrong checkout observation wrote receipts: before=%d after=%d", before, got)
	}
}
