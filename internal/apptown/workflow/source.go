package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

const MaxCallDepth = 3

type RunRef struct{ ID, ProjectID ids.ID }

// A source is one checked observation of the run's workspace ownership. Paths
// and consumers share these facts instead of rewalking parents or silently
// substituting a per-run directory after a failed repository lookup.
type Source struct {
	RunID, ProjectID, RootRunID, ChangeID  ids.ID
	RunNumber, ChangeNumber                int64 // within the project; ChangeNumber 0 without a change
	WorkflowName                           string
	WorkflowVersion                        int
	Slug, DefaultBranch, Branch            string
	ChangeType                             string
	ExpectedRevision, AdoptionRevision     int64
	RunBaseSHA, RootBaseSHA, ChangeBaseSHA string
	RootRole                               string
	Depth                                  int
	WorkspaceRunID                         ids.ID
	ProjectWorkspace                       bool
}

func readSourceRow(ctx context.Context, db database.Queryer, run RunRef) (workflowsql.WorkflowSourceRow, error) {
	row, err := workflowsql.New(db.SQL()).WorkflowSource(ctx, workflowsql.WorkflowSourceParams{RunID: run.ID, ProjectID: run.ProjectID, MaxDepth: MaxCallDepth})
	return row, database.QueryError(err, "reading workflow source")
}

func sourceRecord(run RunRef, row workflowsql.WorkflowSourceRow) (Source, error) {
	if !row.ParentRunID.IsZero() {
		return Source{}, fmt.Errorf("run %d has an invalid or over-deep parent chain", run.ID)
	}
	if !row.RootChangeID.IsZero() && row.ChangeProjectID != run.ProjectID {
		return Source{}, fmt.Errorf("run %d has no matching change in its project", run.ID)
	}
	var declaration *struct {
		Workspace string
		Role      string
	}
	if err := json.Unmarshal(row.RootDefinition, &declaration); err != nil || declaration == nil {
		return Source{}, fmt.Errorf("root run %d has an invalid workspace declaration", row.RootRunID)
	}
	if declaration.Workspace != "" && declaration.Workspace != "run" && declaration.Workspace != "project" {
		return Source{}, fmt.Errorf("root run %d declares unknown workspace %q", row.RootRunID, declaration.Workspace)
	}
	if row.DefaultBranch == "" && declaration.Workspace != "project" && !row.ValidLineage {
		return Source{}, fmt.Errorf("run %d has an invalid workspace continuation chain", run.ID)
	}
	source := Source{RunID: run.ID, ProjectID: run.ProjectID, RootRunID: row.RootRunID, ChangeID: row.RootChangeID, RunNumber: row.RunNumber, ChangeNumber: row.ChangeNumber,
		WorkflowName: row.WorkflowName, WorkflowVersion: int(row.WorkflowVersion), Slug: row.Slug, DefaultBranch: row.DefaultBranch,
		Branch: row.Branch, ChangeType: row.ChangeType, ExpectedRevision: row.ExpectedRevision, AdoptionRevision: row.AdoptionRevision,
		RunBaseSHA: row.RunBaseSha, RootBaseSHA: row.RootBaseSha, ChangeBaseSHA: row.ChangeBaseSha,
		RootRole: declaration.Role, Depth: int(row.Depth), WorkspaceRunID: row.WorkspaceRunID, ProjectWorkspace: declaration.Workspace == "project"}
	if source.Branch == "" {
		source.Branch = fmt.Sprintf("apptown/run-%d", source.RootRunID)
		if !source.ChangeID.IsZero() {
			source.Branch = fmt.Sprintf("apptown/change-%d", source.ChangeID)
		}
	}
	return source, nil
}

func ReadSourceIn(ctx context.Context, db database.Queryer, run RunRef) (Source, error) {
	row, err := readSourceRow(ctx, db, run)
	if err != nil {
		return Source{}, err
	}
	return sourceRecord(run, row)
}

func (s Source) RepoBacked() bool { return s.DefaultBranch != "" }
func (s Source) Repository(dataDir string) (string, error) {
	return (project.RepositoryRef{ID: s.ProjectID, Slug: s.Slug}).Path(dataDir)
}

// Workspace is where the run works. A change has one working copy, its
// folder, used by whoever holds the change, so a writing run on a change
// works there. A landing only reads: it works in a copy of its own, made
// from the branch it checks, and publishes nothing.
func (s Source) Workspace(dataDir string) (string, error) {
	id := s.WorkspaceRunID
	if s.RepoBacked() {
		if !s.ChangeID.IsZero() && s.RootRole != "landing" {
			return (Layout{DataDir: dataDir}).ChangeWorkspace(s.ChangeID), nil
		}
		id = s.RootRunID
	} else if s.ProjectWorkspace {
		return s.Repository(dataDir)
	}
	return (Layout{DataDir: dataDir}).RunWorkspace(id), nil
}

// RunTreeIn lists the runs of run's tree, its root and every run it called:
// the runs that share its workspace.
func RunTreeIn(ctx context.Context, q database.Queryer, run *Run) ([]ids.ID, error) {
	sql := workflowsql.New(q.SQL())
	root, err := sql.RunRoot(ctx, workflowsql.RunRootParams{RunID: run.ID, MaxDepth: MaxCallDepth})
	if err != nil {
		return nil, database.QueryError(err, "reading a run's root")
	}
	tree, err := sql.RunTreeActivity(ctx, workflowsql.RunTreeActivityParams{RootID: root, ProjectID: run.ProjectID,
		RunRunning: RunRunning.String(), StepRunning: StatusRunning.String()})
	if err != nil {
		return nil, database.QueryError(err, "reading a run's tree")
	}
	return tree.RunIds, nil
}
