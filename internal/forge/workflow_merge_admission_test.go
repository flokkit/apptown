package forge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"

	"flokkit.io/forge/internal/forge/schema"
)

func mergeAdmissionFixture(t *testing.T, required ...string) (*App, workflow.MergeSource, ids.ID, ids.ID) {
	t.Helper()
	a := testApp(t)
	projectID := plainTestProject(t, a.DB, "merge-admission", "Merge Admission")
	p := a.projectByID(context.Background(), projectID)
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	a.addGrant("user", "reviewer", access.RoleProjectApprover, "", access.ScopeProject, fmt.Sprint(projectID), "fixture", "setup", "")
	a.addGrant("user", "reviewer", access.RoleProjectContrib, "", access.ScopeProject, fmt.Sprint(projectID), "fixture", "setup", "")
	spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{
		{Name: "approve", Uses: "approval", With: map[string]any{"approvers": []string{"reviewer"}}},
		{Name: "merge", Uses: "scm/merged"},
	}}}}
	if required != nil {
		spec.Stages[0].Gate = &schema.Gate{Require: required}
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: spec})
	a.lifecycle.SelectLane(t.Context(), projectID, "land", true, "fixture")
	ctx := asPerson("reviewer")
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
	gittest.Run(t, workspace, "add", "work.txt")
	gittest.Run(t, workspace, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "the change's work")
	// These tests drive SCM admission and its receipts explicitly.
	a.Work.Stop()
	run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "land"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var approval, merge ids.ID
	for _, step := range snapshot.Steps {
		switch step.Name {
		case "approve":
			approval = step.ID
		case "merge":
			merge = step.ID
		}
	}
	if approval.IsZero() || merge.IsZero() {
		t.Fatalf("landing steps were not materialized: %+v", snapshot.Steps)
	}
	source := mergeSourceForTest(t, a, run)
	return a, source, approval, merge
}

func TestSCMAssertionCannotMergeBeforeRequiredApproval(t *testing.T) {
	t.Parallel()
	a, source, _, merge := mergeAdmissionFixture(t)
	repo := source.Repository(a.cfg.dataDir)
	before := gittest.Run(t, repo, "rev-parse", "HEAD")
	result, err := a.Work.Evaluate(a.asEngine(context.Background()), merge)
	if after := gittest.Run(t, repo, "rev-parse", "HEAD"); after != before {
		t.Fatalf("SCM assertion merged before approval: %s -> %s (result=%+v error=%v)", before, after, result, err)
	}
	if err == nil && result.Status == workflow.StatusPass {
		t.Fatal("unapproved unmerged work received a successful SCM check")
	}
	requireEvents(t, a, 0, eventKind("merge.completed"))
	requireMergeRecord(t, a, source.RunID, scm.Idle, "")
}

// Adapter tests use the real Work admission path; this helper grants no bypass.
func mergeEffectForTest(t *testing.T, a *App, source workflow.MergeSource) (workflow.MergeObservation, error) {
	t.Helper()
	run, err := workflow.ReadRunIn(context.Background(), a.DB, source.RunID)
	if err != nil {
		return workflow.MergeObservation{}, err
	}
	result, err := a.Work.ExecuteMerge(context.Background(), advanceWorkflowMerge(run, source))
	return result, err
}

func approveMergeFixture(t *testing.T, a *App, approval ids.ID) {
	t.Helper()
	if _, err := a.Work.Approve(asPerson("reviewer"), workflow.Approval{StepID: approval, Decision: workflow.VoteApprove, Comment: "ready to land", Adds: shownAdds(t, a, asPerson("reviewer"), approval)}); err != nil {
		t.Fatal(err)
	}
}

func TestSCMAssertionLandsAfterApprovalAndKeepsItsReceipt(t *testing.T) {
	t.Parallel()
	a, source, approval, merge := mergeAdmissionFixture(t)
	if step, err := a.Work.Evaluate(a.asEngine(context.Background()), merge); err != nil || step.Status != workflow.StatusNeedsHuman {
		t.Fatalf("pending approval: %+v %v", step, err)
	}
	approveMergeFixture(t, a, approval)
	a.DB.Pool().SetMaxOpenConns(1)
	for range 2 {
		step, err := a.Work.Evaluate(a.asEngine(context.Background()), merge)
		if err != nil || step.Status != workflow.StatusPass {
			t.Fatalf("admitted assertion: %+v %v", step, err)
		}
	}
	if merged, err := gitAncestorForTest(t, source.Repository(a.cfg.dataDir), "refs/heads/"+source.Branch, "refs/heads/"+source.DefaultBranch); err != nil || !merged {
		t.Fatalf("actual merge: %v %v", merged, err)
	}
	requireEvents(t, a, 1, eventKind("merge.completed"))
	requireEvents(t, a, 1, eventKind("approval"))
	requireRuns(t, a, 1, func(r workflow.RunSummary) bool { return r.Status == workflow.RunRunning })
}

func TestWorkflowAdvanceWaitsForApprovalBeforeSCMEffect(t *testing.T) {
	t.Parallel()
	a, source, _, _ := mergeAdmissionFixture(t)
	repo := source.Repository(a.cfg.dataDir)
	before := gittest.Run(t, repo, "rev-parse", "HEAD")
	result, err := advanceAs(a, source.RunID, "system")
	if err != nil || result.Advanced || len(result.Blocking) != 2 {
		t.Fatalf("blocked advancement: %+v %v", result, err)
	}
	if got := gittest.Run(t, repo, "rev-parse", "HEAD"); got != before {
		t.Fatal("engine merged before approval")
	}
	requireEvents(t, a, 0, eventKind("merge.completed", "run.done"))
}

func TestSCMAdmissionReevaluatesRelaxedLaw(t *testing.T) {
	t.Parallel()
	a, source, approval, merge := mergeAdmissionFixture(t)
	approveMergeFixture(t, a, approval)
	if err := a.Adoption.InstallPolicy(t.Context(), source.ProjectID, "test", "", "requireStepBefore", map[string]any{"uses": "scm/merged", "requires": "scan"}, "", "fixture"); err != nil {
		t.Fatal(err)
	}
	policies, err := a.Adoption.ProjectPolicies(context.Background(), a.DB, source.ProjectID)
	if err != nil || len(policies) != 1 {
		t.Fatalf("installed law: %+v %v", policies, err)
	}
	if step, err := a.Work.Evaluate(a.asEngine(context.Background()), merge); err != nil || step.Status != workflow.StatusNeedsHuman {
		t.Fatalf("law pause: %+v %v", step, err)
	}
	if err := a.Adoption.RemovePolicy(t.Context(), policies[0].ID, "fixture"); err != nil {
		t.Fatal(err)
	}
	if step, err := a.Work.Evaluate(a.asEngine(context.Background()), merge); err != nil || step.Status != workflow.StatusPass {
		t.Fatalf("relaxed law: %+v %v", step, err)
	}
	requireEvents(t, a, 1, eventKind("law.satisfied"))
	requireEvents(t, a, 1, eventKind("merge.completed"))
}

func TestMergeAdmissionHonorsExplicitRequiredChecks(t *testing.T) {
	t.Parallel()
	a, _, _, merge := mergeAdmissionFixture(t, "merge")
	result, err := a.Work.Evaluate(a.asEngine(context.Background()), merge)
	if err != nil || result.Status != workflow.StatusPass {
		t.Fatalf("optional approval became a required gate: %+v %v", result, err)
	}
	requireSteps(t, a, 1, func(s *workflow.Step) bool { return s.Type == "approval" && s.Status == workflow.StatusPending })
	requireEvents(t, a, 1, eventKind("merge.completed"))
}

func TestUpliftRefusalIsNotReportedAsAGitConflict(t *testing.T) {
	t.Parallel()
	a, source, approval, _ := mergeAdmissionFixture(t)
	approveMergeFixture(t, a, approval)
	// sql-guarantee: Make the same admitted Change an uplift with a deliberately stale expected revision; creation would allocate another Change and cannot mutate this pinned source under the merge gate.
	completionExec(t, a.DB, `UPDATE changes SET type='uplift',expected_revision=99 WHERE id=?`, source.ChangeID)
	run, err := workflow.ReadRunIn(context.Background(), a.DB, source.RunID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := mergesFor(a).advanceContext(context.Background(), run)
	if err != nil || result.Allowed || result.Blocking == nil {
		t.Fatalf("uplift refusal: %+v %v", result, err)
	}
	requireEvents(t, a, 1, func(e workflow.Event) bool {
		return e.Kind == "merge.blocked" && strings.Contains(payloadField(e, "reason"), "prepare the adoption")
	})
	requireEvents(t, a, 0, eventKind("merge.conflict", "merge.completed"))
}

// Mutate Work only after real Git/provider preparation. This exercises the
// service's second admission boundary, not an unguarded effect helper.
type afterMergePreparation struct {
	scm.Driver
	after func()
}

func (d afterMergePreparation) Prepare(ctx context.Context, s scm.Source, previous string) (scm.Prepared, error) {
	p, err := d.Driver.Prepare(ctx, s, previous)
	if err == nil {
		d.after()
	}
	return p, err
}
