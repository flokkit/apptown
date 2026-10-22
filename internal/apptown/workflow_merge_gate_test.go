package apptown

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/gitexec"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestWorkflowMergeAdapterRequiresRecordedGateOutcomes(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	project, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "merge-adapter", Name: "Merge adapter", Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.lifecycle.InitializeRepository(t.Context(), project, "", ""); err != nil {
		t.Fatal(err)
	}
	spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "work"}}}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: spec})
	a.Work.Stop()
	ctx := asPerson("alice")
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: project.ID, Title: "Merge"})
	if err != nil {
		t.Fatal(err)
	}
	// The change's work exists before its landing is admitted, as it would:
	// the landing pins and merges that work.
	if err := prepareChangeWorkspaceForTest(a, project, change); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", change.ID))
	if err := os.WriteFile(filepath.Join(workspace, "work.txt"), []byte("change work"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := scm.StageAll(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := gitexec.Run(ctx, workspace, "commit", "-m", "merge fixture"); err != nil {
		t.Fatal(err)
	}
	if err := mirrorBranchForTest(a.projectRepoPath(project.Slug), workspace, change.Branch); err != nil {
		t.Fatal(err)
	}
	run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: change.ID, Workflow: "land"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	repo := a.projectRepoPath(project.Slug)
	drift := filepath.Join(repo, "untracked.txt")
	if err := os.WriteFile(drift, []byte("unrecorded work"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, a.DB, "name: CreateWorkflowGate")
	result, err := mergesFor(a).advanceContext(context.Background(), run)
	if err == nil || result.Allowed || result.Blocking != nil {
		t.Fatalf("unrecorded block: %+v %v", result, err)
	}
	requireSteps(t, a, 0, func(s *workflow.Step) bool { return s.Type == "scm/merged" })
	result, err = mergesFor(a).advanceContext(context.Background(), run)
	if err != nil || result.Allowed || result.Blocking == nil || result.Blocking.Status != workflow.StatusNeedsHuman {
		t.Fatalf("recorded drift: %+v %v", result, err)
	}
	if err := os.Remove(drift); err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, a.DB, "name: RecordStepEvaluation")
	result, err = mergesFor(a).advanceContext(context.Background(), run)
	if err == nil || result.Allowed {
		t.Fatalf("unrecorded gate clearance: %+v %v", result, err)
	}
	if !branchMergedForTest(t, a, run) {
		t.Fatal("fixture did not exercise a physical merge before the failed receipt")
	}
	requireSteps(t, a, 1, func(s *workflow.Step) bool { return s.Type == "scm/merged" && s.Status == workflow.StatusNeedsHuman })
	requireEvents(t, a, 0, eventKind("check.evaluated"))
	result, err = mergesFor(a).advanceContext(context.Background(), run)
	if err != nil || !result.Allowed || result.Blocking != nil {
		t.Fatalf("explicit retry: %+v %v", result, err)
	}
	requireSteps(t, a, 1, func(s *workflow.Step) bool {
		return s.Type == "scm/merged" && s.Status == workflow.StatusPass && canonJSON(string(s.Evidence)) == `{"merged":true}`
	})
	requireEvents(t, a, 1, eventKind("check.evaluated"))
}
