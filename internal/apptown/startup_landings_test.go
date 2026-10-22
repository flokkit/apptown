package apptown

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// The landing's recovery protocol: a run
// left in state merging is resolved from git on restart.
func TestRecoverLandingsFromGit(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "wf", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "land"}}}})
	a.Work.Stop() // recovery, rather than dispatch, owns these admitted landing runs
	type recoveryRun struct {
		run  *workflow.Run
		head string
		git  func(...string) string
	}
	mkRun := func(slug string) recoveryRun {
		pid, head, git := fleetRepo(t, a, slug, map[string]string{"README.md": "hi\n"})
		if _, err := a.recordAdoption(a.projectByID(context.Background(), pid), "edit", "recovery fixture", "fixture"); err != nil {
			t.Fatal(err)
		}
		ctx := a.asAppTown(context.Background(), pid, "recovery fixture")
		change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: pid, Title: "Recover landing"})
		if err != nil {
			t.Fatal(err)
		}
		run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: change.ID, Workflow: "wf"})
		if !errors.Is(err, workflow.ErrDispatchStopped) {
			t.Fatal(err)
		}
		return recoveryRun{run: run, head: head, git: git}
	}
	// Died after the merge: main carries a commit with the operation trailer.
	after := mkRun("after")
	pending := pendingMergeForTest(t, a, after.run.ID, after.head)
	after.git("commit", "-q", "--allow-empty", "-m", fmt.Sprintf("apptown: complete wf run\n\nAppTown-Run-Id: %s\nAppTown-Operation: %s", after.run.ID, pending.Operation))
	merged := strings.TrimSpace(after.git("rev-parse", "HEAD"))
	// Died before the merge: main's head is still what the intent recorded.
	before := mkRun("before")
	pendingMergeForTest(t, a, before.run.ID, strings.TrimSpace(before.git("rev-parse", "HEAD")))
	// Main moved without this operation; only a person can decide.
	unknown := mkRun("unknown")
	pendingMergeForTest(t, a, unknown.run.ID, "0000000000000000000000000000000000000000")

	if err := a.RecoverLandings(context.Background()); err != nil {
		t.Fatal(err)
	}

	requireMergeRecord(t, a, before.run.ID, scm.Idle, "")
	requireMergeRecord(t, a, after.run.ID, scm.Recorded, merged)
	if ev := workEventCount(t, a, runEventKind(after.run.ID, "merge.completed")); ev != 1 {
		t.Fatalf("the recovered merge must carry its merge.completed event, got %d", ev)
	}
	requireMergeRecord(t, a, unknown.run.ID, scm.Pending, "")
	if audited := auditCount(t, a.DB, "landing.merge-unknown"); audited != 1 {
		t.Fatalf("the unexplained case must be audited once, got %d", audited)
	}
}

// One landing whose merge cannot be inspected (its repository is gone) does
// not stop AppTown from starting: it stays pending, holding its landing,
// and the merges after it are still inspected.
func TestALandingThatCannotBeInspectedDoesNotStopStartup(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "wf", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "land"}}}})
	a.Work.Stop()
	mkRun := func(slug string) (*workflow.Run, func(...string) string) {
		pid, _, git := fleetRepo(t, a, slug, map[string]string{"README.md": "hi\n"})
		if _, err := a.recordAdoption(a.projectByID(context.Background(), pid), "edit", "recovery fixture", "fixture"); err != nil {
			t.Fatal(err)
		}
		ctx := a.asAppTown(context.Background(), pid, "recovery fixture")
		change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: pid, Title: "Recover landing"})
		if err != nil {
			t.Fatal(err)
		}
		run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: change.ID, Workflow: "wf"})
		if !errors.Is(err, workflow.ErrDispatchStopped) {
			t.Fatal(err)
		}
		return run, git
	}
	gone, goneGit := mkRun("gone")
	pendingMergeForTest(t, a, gone.ID, strings.TrimSpace(goneGit("rev-parse", "HEAD")))
	before, beforeGit := mkRun("before")
	pendingMergeForTest(t, a, before.ID, strings.TrimSpace(beforeGit("rev-parse", "HEAD")))
	if err := os.RemoveAll(a.projectRepoPath("gone")); err != nil {
		t.Fatal(err)
	}
	if err := a.RecoverLandings(context.Background()); err != nil {
		t.Fatalf("one landing that cannot be inspected stopped startup: %v", err)
	}
	requireMergeRecord(t, a, gone.ID, scm.Pending, "")
	requireMergeRecord(t, a, before.ID, scm.Idle, "")
}
