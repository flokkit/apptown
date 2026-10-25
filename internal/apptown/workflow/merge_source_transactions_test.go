package workflow

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func mergeSourceProject(t *testing.T, db *database.DB, slug string) ids.ID {
	t.Helper()
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: slug})
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func setMergeSourceDefaultBranch(t *testing.T, db *database.DB, projectID ids.ID, branch string) {
	t.Helper()
	if err := project.New(db, nil).SetDefaultBranch(context.Background(), projectID, branch); err != nil {
		t.Fatal(err)
	}
}

func TestMergeSourceBatchesAncestryRepositoryAdoptionAndMR(t *testing.T) {
	t.Parallel()
	q, root := storedChangeRunFixture(t, workflowsql.InsertChangeParams{ID: ids.New(time.Now()), CreatedAt: time.Now(), Type: "uplift", State: ChangeInProgress.String(), Branch: "feature/kept", ExpectedRevision: 2})
	store := q.db
	setMergeSourceDefaultBranch(t, store, root.ProjectID, "main")
	for revision := 1; revision <= 3; revision++ {
		if err := store.InTx(context.Background(), func(tx *database.Tx) error {
			return adoption.RecordIn(t.Context(), tx, root.ProjectID, int64(revision), adoption.AdoptionEdit, "source fixture", adoption.EmptyReceipt(), "test", time.Now())
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := AddArtifactIn(context.Background(), store, root.ID, "mr", "!1", "", "root/project", time.Now()); err != nil {
		t.Fatal(err)
	}
	parent := root.ID
	for range 2 {
		parent = insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: root.ProjectID, WorkflowName: "child", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: []byte(`{}`), ParentRunID: parent}).ID
	}
	child, err := ReadRunIn(context.Background(), store, parent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AddArtifactIn(context.Background(), store, child.ID, "mr", "!2", "", "old/project", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := AddArtifactIn(context.Background(), store, child.ID, "mr", "!3", "", "child/project", time.Now()); err != nil {
		t.Fatal(err)
	}
	counted := &countedQueries{Handle: store.SQL()}
	source, err := readMergeSourceForRun(context.Background(), counted, child)
	if err != nil || counted.reads != 1 || source.RootRunID != root.ID || source.ChangeID != root.ChangeID || source.RunID != child.ID || source.Branch != "feature/kept" || source.MR.IID != 3 || source.MR.Project != "child/project" || source.AdoptionRevision != 3 || source.ExpectedRevision != 2 {
		t.Fatalf("source %+v reads=%d %v", source, counted.reads, err)
	}
	if source.UpliftRefusal() == "" {
		t.Fatal("moved adoption did not block uplift")
	}
	if requireLocation(t)(source.Workspace("/forge")) != filepath.Join("/forge", "workspaces", fmt.Sprintf("change-%d", root.ChangeID)) || requireLocation(t)(source.Repository("/forge")) != "/forge/workspaces/project-completion" {
		t.Fatalf("wrong ownership: %+v", source)
	}
	// sql-guarantee: Change pinned expected revision between independent source reads; no owner mutation edits an admitted Change's expected revision.
	completionExec(t, store, `UPDATE changes SET expected_revision=3`)
	source, err = readMergeSourceForRun(context.Background(), store, root)
	if err != nil || source.UpliftRefusal() != "" || source.MR.IID != 1 {
		t.Fatalf("root admission: %+v %v", source, err)
	}
}

func TestMergeSourceAbsenceAndDefaultBranchNames(t *testing.T) {
	t.Parallel()
	store, run, _ := completionFixture(t, "working")
	source, err := readMergeSourceForRun(context.Background(), store, run)
	if err != nil || source.RepoBacked() || source.MR.IID != 0 || source.Branch != fmt.Sprintf("apptown/change-%d", run.ChangeID) {
		t.Fatalf("non-repository source: %+v %v", source, err)
	}
	run = insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, WorkflowName: "standalone", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: []byte(run.DefinitionJSON)})
	setMergeSourceDefaultBranch(t, store, run.ProjectID, "trunk")
	run, err = ReadRunIn(context.Background(), store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, err = readMergeSourceForRun(context.Background(), store, run)
	if err != nil || !source.RepoBacked() || source.Branch != fmt.Sprintf("apptown/run-%d", run.ID) || requireLocation(t)(source.Workspace("/forge")) != filepath.Join("/forge", "workspaces", fmt.Sprintf("run-%d", run.ID)) {
		t.Fatalf("run-owned source: %+v %v", source, err)
	}
}

func TestMergeSourceRefusesInvalidAncestryAndChangeScope(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"cycle", "too deep", "cross-project parent", "cross-project change", "missing run", "wrong project"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			store, run, _ := completionFixture(t, "landing")
			other := mergeSourceProject(t, store, "other")
			switch kind {
			case "cycle":
				// sql-guarantee: Create a self-parent cycle on the same stored run; InsertRun cannot refer to its generated identity and admission forbids cycles.
				completionExec(t, store, `UPDATE runs SET parent_run_id=id`)
			case "too deep":
				for range MaxCallDepth {
					id := insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, WorkflowName: "child", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: []byte(`{}`), ParentRunID: run.ID}).ID
					var err error
					run, err = ReadRunIn(context.Background(), store, id)
					if err != nil {
						t.Fatal(err)
					}
				}
			case "cross-project parent":
				id := insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: other, WorkflowName: "other", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: []byte(`{}`)}).ID
				run = insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, WorkflowName: "foreign-parent", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: []byte(run.DefinitionJSON), ParentRunID: id})
			case "cross-project change":
				foreignChange := admissionTestChange(t, store, other, "Foreign")
				run = insertStoredRun(t, store, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, ChangeID: foreignChange, WorkflowName: "foreign-change", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: []byte(run.DefinitionJSON)})
			case "missing run":
				run.ID = idstest.N(9999)
			case "wrong project":
				run.ProjectID = other
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			source, err := readMergeSourceForRun(ctx, store, run)
			if err == nil || source != (MergeSource{}) {
				t.Fatalf("invalid %s produced scope: %+v %v", kind, source, err)
			}
		})
	}
}

func TestMergeSourceDoesNotDiscardUnreadableOrMalformedMR(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"unreadable", "bad", "!0", "!-1", "!3junk", "!03", "!+3", "empty project", "no repository"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store, run, _ := completionFixture(t, "landing")
			setMergeSourceDefaultBranch(t, store, run.ProjectID, "main")
			mr, project := name, "group/project"
			if name == "empty project" {
				mr, project = "!3", ""
			}
			if name == "no repository" {
				mr = "!3"
				setMergeSourceDefaultBranch(t, store, run.ProjectID, "")
			}
			if _, err := AddArtifactIn(context.Background(), store, run.ID, "mr", mr, "", project, time.Now()); err != nil {
				t.Fatal(err)
			}
			if name == "unreadable" {
				dbtest.FailNextStatement(t, store, "name: WorkflowSource")
			}
			source, err := readMergeSourceForRun(context.Background(), store, run)
			if err == nil || source != (MergeSource{}) {
				t.Fatalf("lost MR became local merge permission: %+v %v", source, err)
			}
		})
	}
}
