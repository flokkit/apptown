package forge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
)

func mergeSettlementFixture(t *testing.T) (*App, workflow.MergeSource) {
	t.Helper()
	a := testApp(t)
	projectID := plainTestProject(t, a.DB, "merge-settlement", "Merge Settlement")
	p := a.projectByID(context.Background(), projectID)
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{
		{Name: "ready", Steps: []schema.Step{
			{Name: "prepared", Uses: "script", With: map[string]any{"script": "true"}},
			{Name: "hold", Uses: "approval", With: map[string]any{"means": "the source is ready"}},
		}, Gate: &schema.Gate{Require: []string{"prepared"}}},
		{Name: "landed", Steps: []schema.Step{{Name: "record", Uses: "approval", With: map[string]any{"means": "the merge was recorded"}}}},
	}}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: spec})
	a.lifecycle.SelectLane(t.Context(), projectID, "land", true, "fixture")
	ctx := a.asForge(context.Background(), projectID, "fixture")
	ch, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: projectID, Type: "feature", Title: "work to land"})
	if err == nil {
		ch, err = a.Work.Begin(ctx, ch.ID)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	if err := os.WriteFile(filepath.Join(workspace, "work.txt"), []byte("work to land\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The work is committed, as the change's worker does before landing;
	// the landing publishes committed work only.
	gittest.Run(t, workspace, "add", "work.txt")
	gittest.Run(t, workspace, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "work to land")
	a.Work.Stop()
	run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "land"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var prepared ids.ID
	for _, step := range snapshot.Steps {
		if step.Name == "prepared" {
			prepared = step.ID
		}
	}
	if prepared.IsZero() {
		t.Fatalf("preparation step was not materialized: %+v", snapshot.Steps)
	}
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), prepared); err != nil {
		t.Fatal(err)
	}
	return a, mergeSourceForTest(t, a, run)
}

func TestMergeReceiptFailureRequiresExplicitRetry(t *testing.T) {
	t.Parallel()
	a, source := mergeSettlementFixture(t)
	repo := source.Repository(a.cfg.dataDir)
	before := gittest.Run(t, repo, "rev-parse", "HEAD")
	completionExec(t, a.DB, `ALTER TABLE run_events ADD CONSTRAINT refuse_merge_receipt CHECK(kind<>'merge.completed')`)
	result, err := mergeEffectForTest(t, a, source)
	if err == nil || result.Merged {
		t.Fatalf("unrecorded physical merge reported success: %+v %v", result, err)
	}
	after := gittest.Run(t, repo, "rev-parse", "HEAD")
	if after == before {
		t.Fatal("fixture did not perform a physical merge before the failed receipt")
	}
	requireMergeRecord(t, a, source.RunID, scm.Pending, "")
	if got := changeRecord(t, a, source.ChangeID).LandedSHA; got != "" {
		t.Fatalf("unrecorded merge selected source %q", got)
	}
	if m, known, err := scm.ReadMain(context.Background(), a.DB, source.ProjectID); err != nil || known && m.SHA == after {
		t.Fatalf("an unrecorded merge recorded main: %+v %v", m, err)
	}
	requireEvents(t, a, 0, eventKind("merge.completed"))
	completionExec(t, a.DB, `ALTER TABLE run_events DROP CONSTRAINT refuse_merge_receipt`)
	for range 2 {
		result, err = mergeEffectForTest(t, a, source)
		if err != nil || !result.Merged {
			t.Fatalf("explicit retry: %+v %v", result, err)
		}
		if got := gittest.Run(t, repo, "rev-parse", "HEAD"); got != after {
			t.Fatalf("retry merged again: %s -> %s", after, got)
		}
	}
	requireMergeRecord(t, a, source.RunID, scm.Recorded, after)
	if got := changeRecord(t, a, source.ChangeID).LandedSHA; got != after {
		t.Fatalf("recorded merge source: %q, want %q", got, after)
	}
	wantLandingRecordedMain(t, a, source.ProjectID, repo, source.DefaultBranch, source.RunID)
	requireEvents(t, a, 1, func(e workflow.Event) bool {
		return e.Kind == "merge.completed" && payloadField(e, "sha") == after
	})
}

// A landing admitted before candidates were pinned is refused, to be
// restarted, and merges nothing from its workspace.
func TestALandingAdmittedBeforePinningMergesNothing(t *testing.T) {
	t.Parallel()
	a, source := mergeSettlementFixture(t)
	// sql-guarantee: model a landing admitted before step 1b, which recorded no candidate and no pinning marker.
	completionExec(t, a.DB, `DELETE FROM run_events WHERE run_id=? AND kind IN ('landing.candidate','landing.pin.required')`, source.RunID)
	run, err := workflow.ReadRunIn(context.Background(), a.DB, source.RunID)
	if err != nil {
		t.Fatal(err)
	}
	source = mergeSourceForTest(t, a, run)
	main := gittest.Run(t, source.Repository(a.cfg.dataDir), "rev-parse", "refs/heads/"+source.DefaultBranch)
	if result, err := mergeEffectForTest(t, a, source); err == nil || result.Merged || !strings.Contains(err.Error(), "restart it to land") {
		t.Fatalf("an old landing's merge: %+v %v", result, err)
	}
	if after := gittest.Run(t, source.Repository(a.cfg.dataDir), "rev-parse", "refs/heads/"+source.DefaultBranch); after != main {
		t.Fatal("an old landing merged")
	}
}

// A merge whose inspection lacks a required fact or receipt is never
// settled: it stays pending, holding its landing, with nothing recorded,
// and startup goes on (one record never stops the forge from starting).
func TestStartupMergeInspectionLeavesAMergeWithoutItsReadsOrReceiptPending(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"missing repo", "receipt"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			a, source := mergeSettlementFixture(t)
			pendingMergeForTest(t, a, source.RunID, "old-head")
			beforeEvents, beforeAudits := workEventCount(t, a, nil), auditTotal(t, a.DB)
			switch failure {
			case "missing repo":
				if err := os.RemoveAll(source.Repository(a.cfg.dataDir)); err != nil {
					t.Fatal(err)
				}
			case "receipt":
				dbtest.FailNextStatement(t, a.DB, "INSERT INTO audit_log")
			}
			if err := a.RecoverLandings(context.Background()); err != nil {
				t.Fatalf("one merge that could not be inspected stopped startup: %v", err)
			}
			requireMergeRecord(t, a, source.RunID, scm.Pending, "")
			requireEvents(t, a, beforeEvents, nil)
			if got := auditTotal(t, a.DB); got != beforeAudits {
				t.Fatalf("receipts: got %d, want %d", got, beforeAudits)
			}
		})
	}
}

func TestLocalMergeObservationDistinguishesNewWorkFromUnrelatedHistory(t *testing.T) {
	t.Parallel()
	for _, newWork := range []bool{false, true} {
		t.Run(fmt.Sprint(newWork), func(t *testing.T) {
			t.Parallel()
			a, source := mergeSettlementFixture(t)
			if result, err := mergeEffectForTest(t, a, source); err != nil || !result.Merged {
				t.Fatalf("initial merge: %+v %v", result, err)
			}
			repo := source.Repository(a.cfg.dataDir)
			selected := gittest.Run(t, repo, "rev-parse", "HEAD")
			later := ""
			if newWork {
				// New work in the change workspace is not part of this landing:
				// the merge stays the pinned candidate's, and repeating the
				// merge assertion neither publishes nor merges it.
				workspace := source.Workspace(a.cfg.dataDir)
				gittest.Run(t, workspace, "commit", "--allow-empty", "-qm", "additional work")
				later = gittest.Run(t, workspace, "rev-parse", "HEAD")
			} else {
				gittest.Run(t, repo, "commit", "--allow-empty", "-qm", "unrelated default branch work")
			}
			for range 2 {
				if result, err := mergeEffectForTest(t, a, source); err != nil || !result.Merged {
					t.Fatalf("observed merge: %+v %v", result, err)
				}
			}
			if got := changeRecord(t, a, source.ChangeID).LandedSHA; got != selected {
				t.Fatalf("landed source: %q, want the selected %q", got, selected)
			}
			if later != "" {
				if _, notMerged := gitexec.Run(t.Context(), repo, "merge-base", "--is-ancestor", later, "refs/heads/"+source.DefaultBranch); notMerged == nil {
					t.Fatal("work made after the landing's merge reached the default branch")
				}
			}
			requireEvents(t, a, 1, eventKind("merge.completed"))
		})
	}
}

// A script step's commit reaches the hub's branch under the authority its
// attempt was admitted with.
func TestAScriptStepPublishesItsCommit(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	projectID := plainTestProject(t, a.DB, "script-publish", "Script Publish")
	p := a.projectByID(context.Background(), projectID)
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
		{Name: "build", Steps: []schema.Step{{Name: "make", Uses: "script", With: map[string]any{"run": "echo made > made.txt"}}}},
	}}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "work", Version: 1}, Spec: spec})
	a.lifecycle.SelectLane(t.Context(), projectID, "work", true, "fixture")
	ctx := a.asForge(context.Background(), projectID, "fixture")
	ch, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: projectID, Type: "feature", Title: "scripted"})
	if err == nil {
		ch, err = a.Work.Begin(ctx, ch.ID)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
		t.Fatal(err)
	}
	a.Work.Stop()
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "work"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 {
		t.Fatalf("script step: %+v %v", snapshot, err)
	}
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), snapshot.Steps[0].ID); err != nil {
		t.Fatal(err)
	}
	source := mergeSourceForTest(t, a, run)
	workspace := source.Workspace(a.cfg.dataDir)
	head := gittest.Run(t, workspace, "rev-parse", "HEAD")
	if msg := gittest.Run(t, workspace, "log", "-1", "--format=%B"); !strings.Contains(msg, "AppTown-Step: make") {
		t.Fatalf("the script step did not commit its work: %s", msg)
	}
	if got := scm.Ref(ctx, source.Repository(a.cfg.dataDir), "refs/heads/"+source.Branch); got != head {
		t.Fatalf("hub branch = %s after the script step, want its commit %s", got, head)
	}
}

func TestALandingStartedBeforeGuardedPublicationCanStillMerge(t *testing.T) {
	t.Parallel()
	a, source := mergeSettlementFixture(t)
	// sql-guarantee: migration 90 creates an empty authority table for pre-existing runs.
	completionExec(t, a.DB, "DELETE FROM branch_authority WHERE project_id=?", source.ProjectID)
	if err := a.RecoverLandings(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := mergeEffectForTest(t, a, source)
	if err != nil || !result.Merged {
		t.Fatalf("pre-existing landing cannot merge after upgrade: %+v %v", result, err)
	}
}

// The gates checked the pinned candidate. A commit made in the workspace
// before the merge (a chat still editing) is not merged with it.
func TestACommitAfterTheGatesIsNotMerged(t *testing.T) {
	t.Parallel()
	a, source := mergeSettlementFixture(t)
	if source.Landing.Candidate == "" || !source.PinRequired {
		t.Fatalf("a fresh landing has no pinned candidate: %+v", source.Landing)
	}
	workspace, repo := source.Workspace(a.cfg.dataDir), source.Repository(a.cfg.dataDir)
	if err := os.WriteFile(filepath.Join(workspace, "late.txt"), []byte("typed after the gates\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, workspace, "add", "late.txt")
	gittest.Run(t, workspace, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "late edit")
	late := gittest.Run(t, workspace, "rev-parse", "HEAD")
	result, err := mergeEffectForTest(t, a, source)
	if err != nil || !result.Merged {
		t.Fatalf("merging the checked candidate: %+v %v", result, err)
	}
	if _, err := gitexec.Run(t.Context(), repo, "merge-base", "--is-ancestor", source.Landing.Candidate, source.DefaultBranch); err != nil {
		t.Fatal("the checked candidate is not on the default branch")
	}
	if _, err := gitexec.Run(t.Context(), repo, "merge-base", "--is-ancestor", late, source.DefaultBranch); err == nil {
		t.Fatal("a commit made after the gates was merged unchecked")
	}
}

// Another change landed after this landing's checks. Merging now would
// combine the two trees without checking the result, so it is refused.
func TestALandingIsRefusedWhenMainMovedAfterItsChecks(t *testing.T) {
	t.Parallel()
	a, source := mergeSettlementFixture(t)
	repo := source.Repository(a.cfg.dataDir)
	if err := os.WriteFile(filepath.Join(repo, "other.txt"), []byte("another change landed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repo, "add", "other.txt")
	gittest.Run(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "another landing")
	moved := gittest.Run(t, repo, "rev-parse", "HEAD")
	result, err := mergeEffectForTest(t, a, source)
	var landing *scm.LandingMoved
	if !errors.As(err, &landing) || landing.What != "main" || result.Merged {
		t.Fatalf("merging after main moved: %+v %v", result, err)
	}
	if got := gittest.Run(t, repo, "rev-parse", "HEAD"); got != moved {
		t.Fatalf("the refused landing changed the default branch: %s, want %s", got, moved)
	}
}

// wantLandingRecordedMain: the landing run recorded the hub's main, read from
// the SCM owner.
func wantLandingRecordedMain(t *testing.T, a *App, projectID ids.ID, repo, branch string, runID ids.ID) {
	t.Helper()
	m, known, err := scm.ReadMain(context.Background(), a.DB, projectID)
	if err != nil || !known {
		t.Fatalf("no recorded main: %v", err)
	}
	if want := scm.Ref(t.Context(), repo, "refs/heads/"+branch); m.SHA != want || m.Via != string(scm.ViaLanding) || m.RunID != runID {
		t.Fatalf("recorded main %+v, want the hub's %s by landing run %d", m, want, runID)
	}
}
