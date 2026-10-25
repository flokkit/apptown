package workflow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func storeContinuationForTest(t *testing.T, db *database.DB, source, run ids.ID) {
	t.Helper()
	changed, err := workflowsql.New(db.SQL()).ContinuationLink(context.Background(), workflowsql.ContinuationLinkParams{SourceRunID: source, RunID: run})
	if err != nil || changed != 1 {
		t.Fatalf("store continuation: changed=%d %v", changed, err)
	}
}

func TestWorkflowSourcePreservesWorkspacePathsAndBatchesFacts(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"repository change", "repository run", "project", "run", "continued", "continued child"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			q, run, _ := activityFixture(t)
			store := q.db
			changeID := run.ChangeID
			origin, root := run.ID, run.ID
			want := filepath.Join("/forge", "workspaces", fmt.Sprintf("run-%d", origin))
			switch mode {
			case "repository change":
				if err := project.New(store, nil).SetDefaultBranch(context.Background(), run.ProjectID, "trunk"); err != nil {
					t.Fatal(err)
				}
				want = filepath.Join("/forge", "workspaces", fmt.Sprintf("change-%d", run.ChangeID))
			case "repository run":
				if err := project.New(store, nil).SetDefaultBranch(context.Background(), run.ProjectID, "trunk"); err != nil {
					t.Fatal(err)
				}
				run = insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, WorkflowName: run.WorkflowName, WorkflowVersion: int64(run.WorkflowVersion), Kind: run.Kind, CurrentStage: run.CurrentStage, DefinitionJson: []byte(run.DefinitionJSON), Status: run.Status.String()})
				origin, root = run.ID, run.ID
				want = filepath.Join("/forge", "workspaces", fmt.Sprintf("run-%d", origin))
			case "project":
				run = insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, ChangeID: run.ChangeID, WorkflowName: run.WorkflowName, WorkflowVersion: int64(run.WorkflowVersion), Kind: run.Kind, CurrentStage: run.CurrentStage, DefinitionJson: []byte(`{"workspace":"project"}`), Status: run.Status.String()})
				root = run.ID
				want = "/forge/workspaces/project-completion"
			case "continued", "continued child":
				for range 6 {
					id := insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, ChangeID: run.ChangeID, WorkflowName: "work", WorkflowVersion: 1, Kind: "pipeline", CurrentStage: "finish", DefinitionJson: []byte(`{}`), Status: RunRunning.String()}).ID
					storeContinuationForTest(t, store, root, id)
					root = id
				}
			}
			rootRun, err := readRun(context.Background(), store, root)
			if err != nil {
				t.Fatal(err)
			}
			changed, err := workflowsql.New(store.SQL()).SetRunWorkspaceBase(context.Background(), workflowsql.SetRunWorkspaceBaseParams{Tip: "root-base", RunID: root, ProjectID: rootRun.ProjectID, OldBaseSha: "", OldStatus: rootRun.Status.String(), OldStage: rootRun.CurrentStage, OldRevision: rootRun.RetryRevision})
			if err != nil || changed != 1 {
				t.Fatalf("store root base: changed=%d %v", changed, err)
			}
			change, err := readChange(context.Background(), store, changeID)
			if err != nil {
				t.Fatal(err)
			}
			changed, err = workflowsql.New(store.SQL()).SetChangeWorkspaceBase(context.Background(), workflowsql.SetChangeWorkspaceBaseParams{Tip: "change-base", Branch: "work/kept", ChangeID: change.ID, ProjectID: change.ProjectID, OldBranch: change.Branch, OldBaseSha: change.BaseSHA, OldLandedSha: change.LandedSHA, OldState: change.State.String(), OldRunID: change.RunID})
			if err != nil || changed != 1 {
				t.Fatalf("store change base: changed=%d %v", changed, err)
			}
			id := root
			if mode == "continued child" || mode == "project" || mode == "repository change" {
				for range 2 {
					child := insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, WorkflowName: "child", WorkflowVersion: 1, Kind: "pipeline", CurrentStage: "finish", DefinitionJson: []byte(`{"workspace":"run"}`), Status: RunRunning.String(), ParentRunID: id})
					changed, err := workflowsql.New(store.SQL()).SetRunWorkspaceBase(context.Background(), workflowsql.SetRunWorkspaceBaseParams{Tip: "child-base", RunID: child.ID, ProjectID: child.ProjectID, OldBaseSha: "", OldStatus: child.Status.String(), OldStage: child.CurrentStage, OldRevision: child.RetryRevision})
					if err != nil || changed != 1 {
						t.Fatalf("store child base: changed=%d %v", changed, err)
					}
					id = child.ID
				}
			}
			run, err = ReadRunIn(context.Background(), store, id)
			if err != nil {
				t.Fatal(err)
			}
			store.Pool().SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			counted := &countedQueries{Handle: store.SQL()}
			source, err := ReadSourceIn(ctx, counted, RunRef{ID: run.ID, ProjectID: run.ProjectID})
			if err != nil || counted.reads != 1 || requireLocation(t)(source.Workspace("/forge")) != want || source.RootRunID != root || source.RootBaseSHA != "root-base" {
				t.Fatalf("ownership: %+v path=%s want=%s reads=%d %v", source, requireLocation(t)(source.Workspace("/forge")), want, counted.reads, err)
			}
			if id != root && (source.RunBaseSHA != "child-base" || source.Depth != MaxCallDepth) {
				t.Fatalf("lost child facts: %+v", source)
			}
			if mode != "repository run" && (source.ChangeBaseSHA != "change-base" || source.Branch != "work/kept") {
				t.Fatalf("lost change facts: %+v", source)
			}
			// The source names the read run and the root's change by number:
			// a child run's own number, not its root's.
			if source.RunNumber != run.Number || run.Number <= 0 {
				t.Fatalf("run number %d, want %d", source.RunNumber, run.Number)
			}
			wantChange := change.Number
			if mode == "repository run" {
				wantChange = 0
			}
			if source.ChangeNumber != wantChange {
				t.Fatalf("change number %d, want %d", source.ChangeNumber, wantChange)
			}
		})
	}
}

func TestWorkflowSourceRejectsBrokenContinuationsAndDeclarations(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"cycle", "forward identity", "different project", "different change", "unknown workspace", "null definition"} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			q, run, _ := activityFixture(t)
			store := q.db
			switch mutation {
			case "unknown workspace":
				run = insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, ChangeID: run.ChangeID, WorkflowName: run.WorkflowName, WorkflowVersion: int64(run.WorkflowVersion), Kind: run.Kind, CurrentStage: run.CurrentStage, DefinitionJson: []byte(`{"workspace":"somewhere"}`), Status: run.Status.String()})
			case "null definition":
				run = insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, ChangeID: run.ChangeID, WorkflowName: run.WorkflowName, WorkflowVersion: int64(run.WorkflowVersion), Kind: run.Kind, CurrentStage: run.CurrentStage, DefinitionJson: []byte(`null`), Status: run.Status.String()})
			case "cycle":
				storeContinuationForTest(t, store, run.ID, run.ID)
			default:
				id := insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, ChangeID: run.ChangeID, WorkflowName: "work", WorkflowVersion: 1, Kind: "pipeline", CurrentStage: "finish", DefinitionJson: []byte(`{}`), Status: RunRunning.String()}).ID
				if mutation == "forward identity" {
					storeContinuationForTest(t, store, id, run.ID)
				} else {
					storeContinuationForTest(t, store, run.ID, id)
					if mutation == "different project" {
						pid := workflowTestProject(t, store, "other", "Other", "")
						// sql-guarantee: the broken-source law needs the same continuation graph with only its root project identity corrupted; no Workflow mutation reassigns project_id.
						completionExec(t, store, `UPDATE runs SET project_id=?,number=number+100000 WHERE id=?`, pid, run.ID)
					} else {
						// sql-guarantee: the broken-source law needs the same continuation graph with only its root Change link cleared; no Workflow mutation reassigns change_id.
						completionExec(t, store, `UPDATE runs SET change_id=NULL WHERE id=?`, run.ID)
					}
					run.ID = id
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if source, err := ReadSourceIn(ctx, store, RunRef{ID: run.ID, ProjectID: run.ProjectID}); err == nil || source != (Source{}) {
				t.Fatalf("invalid ownership: %+v %v", source, err)
			}
		})
	}
}

func TestWorkspaceSourceDoesNotRequireMergeRequestValidity(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	store := q.db
	insertTextArtifactForTest(t, store, run.ID, "mr", "malformed", "", "", time.Time{})
	if _, err := ReadSourceIn(context.Background(), store, RunRef{ID: run.ID, ProjectID: run.ProjectID}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMergeSourceIn(context.Background(), store, RunRef{ID: run.ID, ProjectID: run.ProjectID}); err == nil {
		t.Fatal("malformed MR admitted a merge")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if source, err := ReadSourceIn(ctx, store, RunRef{ID: run.ID, ProjectID: run.ProjectID}); !errors.Is(err, context.Canceled) || source != (Source{}) {
		t.Fatalf("cancelled read: %+v %v", source, err)
	}
}
