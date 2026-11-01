package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// ChangeRuns is history in descending run-ID order. Zero Limit is uncapped.
type ChangeRuns struct {
	RootsOnly bool
	Limit     int
}

func readChangeRunsIn(ctx context.Context, q database.Queryer, ref ChangeRef, in ChangeRuns) ([]*Run, error) {
	if ref.ProjectID.IsZero() || ref.ID.IsZero() || (in.Limit < 0 || int64(in.Limit) > 2147483647) {
		return nil, fmt.Errorf("Change runs require scope and valid bounds")
	}
	rows, err := workflowsql.New(q.SQL()).ChangeRuns(ctx, workflowsql.ChangeRunsParams{ProjectID: ref.ProjectID, ChangeID: ref.ID, RootsOnly: in.RootsOnly, RunLimit: int32(in.Limit)})
	if err != nil {
		return nil, database.QueryError(err, "reading Change runs")
	}
	out := make([]*Run, 0, len(rows))
	for _, row := range rows {
		run, err := runRecord(row, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

func (s *Service) ChangeRuns(ctx context.Context, id ids.ID, in ChangeRuns) ([]*Run, error) {
	project, _, err := s.changeScope(ctx, id, actionReadRun)
	if err != nil {
		return nil, err
	}
	return readChangeRunsIn(ctx, s.db, ChangeRef{ID: id, ProjectID: project}, in)
}

// LatestChangeEvent is the newest event of kind across every run of the
// change; nil when none was recorded.
func (s *Service) LatestChangeEvent(ctx context.Context, id ids.ID, kind string) (*Event, error) {
	project, _, err := s.changeScope(ctx, id, actionReadEvidence)
	if err != nil {
		return nil, err
	}
	row, err := workflowsql.New(s.db.SQL()).LatestChangeEvent(ctx, workflowsql.LatestChangeEventParams{ProjectID: project, ChangeID: id, Kind: kind})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, database.QueryError(err, "reading the change's latest "+kind)
	}
	return &Event{row.ID, row.RunID, row.At.Format(time.RFC3339Nano), row.Kind, rawJSON(string(row.PayloadJson))}, nil
}

// ReadScopedStepsIn returns observations only; no attempt claim is issued.
func ReadScopedStepsIn(ctx context.Context, q database.Queryer, ref RunRef) ([]*Step, error) {
	if ref.ProjectID.IsZero() || ref.ID.IsZero() {
		return nil, fmt.Errorf("Step observations require Run scope")
	}
	rows, err := stepRecords(workflowsql.New(q.SQL()).RunSteps(ctx, workflowsql.RunStepsParams{RunID: ref.ID, ProjectID: ref.ProjectID}))
	if err == nil && rows == nil {
		rows = []*Step{}
	}
	return rows, err
}

func (s *Service) RunSteps(ctx context.Context, id ids.ID) ([]*Step, error) {
	project, _, err := s.runScope(ctx, id, actionReadEvidence)
	if err != nil {
		return nil, err
	}
	return ReadScopedStepsIn(ctx, s.db, RunRef{ID: id, ProjectID: project})
}

type StepContext struct {
	Run  *Run
	Step *Step
}

func (s *Service) stepScope(ctx context.Context, id ids.ID) (RunRef, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return RunRef{}, err
	}
	identity, err := workflowsql.New(s.db.SQL()).StepRunIdentityMeta(ctx, id)
	if err != nil {
		return RunRef{}, database.Missing(err)
	}
	ref := RunRef{ID: identity.RunID, ProjectID: identity.ProjectID}
	_, err = s.require(ctx, operationPermission{Action: actionReadEvidence, ProjectID: ref.ProjectID, RunID: ref.ID, StepID: id})
	return ref, err
}

func readStepContext(ctx context.Context, q database.Queryer, ref RunRef, id ids.ID) (StepContext, error) {
	run, err := readScopedRun(ctx, q, ref.ID, ref.ProjectID)
	if err != nil {
		return StepContext{}, err
	}
	step, err := readStep(ctx, q, id)
	if err != nil {
		return StepContext{}, err
	}
	if step.RunID != run.ID {
		return StepContext{}, database.Missing(sql.ErrNoRows)
	}
	return StepContext{Run: run, Step: step}, nil
}

func (s *Service) Step(ctx context.Context, id ids.ID) (StepContext, error) {
	ref, err := s.stepScope(ctx, id)
	if err != nil {
		return StepContext{}, err
	}
	tx, err := s.db.ReadSnapshot(ctx)
	if err != nil {
		return StepContext{}, err
	}
	defer func() { _ = tx.Rollback() }()
	out, err := readStepContext(ctx, tx, ref, id)
	if err != nil {
		return StepContext{}, err
	}
	if err := tx.Commit(); err != nil {
		return StepContext{}, err
	}
	return out, nil
}

func (s *Service) Snapshot(ctx context.Context, id ids.ID, view ReadView) (*RunSnapshot, error) {
	project, _, err := s.runScope(ctx, id, actionReadRun)
	if err != nil {
		return nil, err
	}
	out, err := (queries{s.db, s.wait, s.stepAttempts.automatic}).scopedSnapshot(ctx, id, view, project)
	if err != nil {
		return nil, err
	}
	if out.Run.ProjectID != project {
		return nil, &access.Denied{Reason: "Work scope changed during read"}
	}
	if view == DetailView {
		for _, step := range out.Steps {
			if step.Status == StatusPending && isAcceptance(out.Run, step) {
				out.LandingMeans = s.landingMeansFor(ctx, out.Run.ID, step.ID)
				break
			}
		}
	}
	return out, nil
}

func (s *Service) Artifact(ctx context.Context, id ids.ID) (*artifactBodyPart, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	artifactScope, err := workflowsql.New(s.db.SQL()).ArtifactRunScopeMeta(ctx, id)
	if err != nil {
		return nil, database.Missing(err)
	}
	run, project := artifactScope.RunID, artifactScope.ProjectID
	if _, err := s.require(ctx, operationPermission{Action: actionReadEvidence, ProjectID: project, RunID: run}); err != nil {
		return nil, err
	}
	// Read the body and scope together so a changed association cannot leak it.
	tx, err := s.db.ReadSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	currentProject, err := workflowsql.New(tx.SQL()).RunProjectMeta(ctx, run)
	if err != nil {
		return nil, database.Missing(err)
	}
	if currentProject != project {
		return nil, &access.Denied{Reason: "Work scope changed during read"}
	}
	rows, err := readWorkflowArtifacts(ctx, tx, workflowArtifactQuery{ID: id, RunID: run, View: artifactBody})
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, database.Missing(sql.ErrNoRows)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &rows[0], nil
}

func (s *Service) Runs(ctx context.Context, in RunQuery) ([]RunSummary, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	if in.Limit < 0 {
		return nil, fmt.Errorf("invalid Work list bounds")
	}
	if in.ProjectSlug != "" {
		projectID, err := project.IDBySlugIn(ctx, s.db, in.ProjectSlug)
		if errors.Is(err, sql.ErrNoRows) {
			return []RunSummary{}, nil
		}
		if err != nil {
			return nil, err
		}
		if !in.ProjectID.IsZero() && in.ProjectID != projectID {
			return []RunSummary{}, nil
		}
		in.ProjectID = projectID
		in.ProjectSlug = ""
	}
	var err error
	in.projects, err = s.visibleProjects(ctx, actionReadRun, in.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(in.projects) == 0 {
		return []RunSummary{}, nil
	}
	return (queries{db: s.db, wait: s.wait}).list(ctx, in)
}

func (s *Service) Activities(ctx context.Context, idList []ids.ID) (map[ids.ID]Activity, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	scopes, err := workflowsql.New(s.db.SQL()).RunScopesMeta(ctx, idList)
	if err != nil {
		return nil, database.Missing(err)
	}
	owned := make(map[ids.ID]ids.ID, len(scopes))
	for _, v := range scopes {
		if _, err := s.require(ctx, operationPermission{Action: actionReadRun, ProjectID: v.ProjectID, RunID: v.ID}); err != nil {
			return nil, err
		}
		owned[v.ID] = v.ProjectID
	}
	for _, id := range idList {
		if _, ok := owned[id]; !ok {
			return nil, sql.ErrNoRows
		}
	}
	graph, err := readWorkflowActivity(ctx, s.db, idList)
	if err != nil {
		return nil, err
	}
	out := make(map[ids.ID]Activity, len(idList))
	for _, id := range idList {
		if graph[id] == nil {
			return nil, sql.ErrNoRows
		}
		if graph[id].projectID != owned[id] {
			return nil, &access.Denied{Reason: "Work scope changed during read"}
		}
		activity, err := graph.classify(id, s.wait, make(map[ids.ID]bool))
		if err != nil {
			return nil, err
		}
		out[id] = activity
	}
	return out, nil
}

func (s *Service) Run(ctx context.Context, id ids.ID) (*Run, error) {
	project, _, err := s.runScope(ctx, id, actionReadRun)
	if err != nil {
		return nil, err
	}
	return readScopedRun(ctx, s.db, id, project)
}
