package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"

	"flokkit.io/forge/internal/database"
)

// ArtifactSelection selects text evidence. Bodies are exact; binary downloads
// remain Service.Artifact's responsibility. Latest selects the greatest ID.
type ArtifactSelection struct {
	Kinds  []string
	Name   string
	Latest bool
}

// ReadProjectArtifactsIn serves already-admitted cross-run agent references.
func ReadProjectArtifactsIn(ctx context.Context, q database.Queryer, project ids.ID, in ArtifactSelection) ([]Artifact, error) {
	if project.IsZero() {
		return nil, fmt.Errorf("artifacts require project scope")
	}
	return readSelectedArtifacts(ctx, q, project, ids.Nil, ids.Nil, in)
}

// MissingArtifactKindsIn checks all prerequisites in one statement, in the
// caller's order. It reads presence, never large report bodies.
func MissingArtifactKindsIn(ctx context.Context, q database.Queryer, project ids.ID, kinds []string) ([]string, error) {
	if project.IsZero() {
		return nil, fmt.Errorf("artifact prerequisites require project scope")
	}
	if len(kinds) == 0 {
		return []string{}, ctx.Err()
	}
	rows, err := workflowsql.New(q.SQL()).MissingArtifactKinds(ctx, workflowsql.MissingArtifactKindsParams{ProjectID: project, Kinds: kinds})
	if err == nil && rows == nil {
		rows = []string{}
	}
	return rows, err
}

func ReadRunArtifactsIn(ctx context.Context, q database.Queryer, ref RunRef, in ArtifactSelection) ([]Artifact, error) {
	if ref.ID.IsZero() || ref.ProjectID.IsZero() {
		return nil, fmt.Errorf("artifacts require Run scope")
	}
	return readSelectedArtifacts(ctx, q, ref.ProjectID, ref.ID, ids.Nil, in)
}
func readChangeArtifactsIn(ctx context.Context, q database.Queryer, ref ChangeRef, in ArtifactSelection) ([]Artifact, error) {
	if ref.ID.IsZero() || ref.ProjectID.IsZero() {
		return nil, fmt.Errorf("artifacts require Change scope")
	}
	return readSelectedArtifacts(ctx, q, ref.ProjectID, ids.Nil, ref.ID, in)
}
func (s *Service) RunArtifacts(ctx context.Context, id ids.ID, in ArtifactSelection) ([]Artifact, error) {
	project, _, err := s.runScope(ctx, id, actionReadEvidence)
	if err != nil {
		return nil, err
	}
	return ReadRunArtifactsIn(ctx, s.db, RunRef{ID: id, ProjectID: project}, in)
}
func (s *Service) ChangeArtifacts(ctx context.Context, id ids.ID, in ArtifactSelection) ([]Artifact, error) {
	project, _, err := s.changeScope(ctx, id, actionReadEvidence)
	if err != nil {
		return nil, err
	}
	return readChangeArtifactsIn(ctx, s.db, ChangeRef{ID: id, ProjectID: project}, in)
}
func readSelectedArtifacts(ctx context.Context, q database.Queryer, project, run, change ids.ID, in ArtifactSelection) ([]Artifact, error) {
	limit := 0
	if in.Latest {
		limit = 1
	}
	rows, err := workflowsql.New(q.SQL()).SelectedTextArtifacts(ctx, workflowsql.SelectedTextArtifactsParams{ProjectID: project, RunID: run, ChangeID: change, Kinds: nonNilStrings(in.Kinds), Name: in.Name, Latest: in.Latest, LimitCount: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]Artifact, 0, len(rows))
	for _, row := range rows {
		out = append(out, selectedArtifact(row))
	}
	return out, nil
}
func selectedArtifact(row workflowsql.SelectedTextArtifactsRow) Artifact {
	return Artifact{ID: row.ID, RunID: row.RunID, Kind: row.Kind, Name: row.Name, Ref: row.Ref, Body: row.Body, CreatedAt: row.CreatedAt.Format(time.RFC3339Nano)}
}

// ReadClaimIn reads the implementer's report on a run's work: the newest
// artifact, across the run's change (the run alone when it has none), of a
// kind one of its review steps declares it audits (agent/review "of"). nil
// when no review declares one or none was written. The declaration decides,
// never a kind's spelling: the newest "*report" was once a scanner's.
func ReadClaimIn(ctx context.Context, q database.Queryer, ref RunRef, changeID ids.ID) (*Artifact, error) {
	if ref.ID.IsZero() || ref.ProjectID.IsZero() {
		return nil, fmt.Errorf("the implementer's report requires Run scope")
	}
	row, err := workflowsql.New(q.SQL()).LatestClaim(ctx, workflowsql.LatestClaimParams{ProjectID: ref.ProjectID, ChangeID: changeID, RunID: ref.ID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, database.QueryError(err, "reading the implementer's report")
	}
	claim := selectedArtifact(workflowsql.SelectedTextArtifactsRow(row))
	return &claim, nil
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// AcceptedScanReports is the latest report per binding from completed Runs.
// It describes accepted project posture, never in-flight branch findings.
func (s *Service) AcceptedScanReports(ctx context.Context, project ids.ID) ([]Artifact, error) {
	if project.IsZero() {
		return nil, fmt.Errorf("scan reports require project scope")
	}
	if _, err := s.require(ctx, operationPermission{Action: actionIdentify}); err != nil {
		return nil, err
	}
	if _, err := s.require(ctx, operationPermission{Action: actionReadEvidence, ProjectID: project}); err != nil {
		return nil, err
	}
	rows, err := workflowsql.New(s.db.SQL()).AcceptedScanReports(ctx, workflowsql.AcceptedScanReportsParams{ProjectID: project, Status: RunDone.String()})
	if err != nil {
		return nil, err
	}
	out := make([]Artifact, 0, len(rows))
	for _, row := range rows {
		out = append(out, selectedArtifact(workflowsql.SelectedTextArtifactsRow(row)))
	}
	return out, nil
}
