package conformance

import (
	"encoding/json"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/conformance/internal/conformancesql"
	"flokkit.io/forge/internal/stamp"
)

// queries binds the subsystem's named SQL (internal/conformancesql/queries.sql)
// to a pool or a transaction; SQL() carries the test-only statement fault.
func queries(q database.Queryer) *conformancesql.Queries { return conformancesql.New(q.SQL()) }

// The stored rows in the subsystem's own words. Timestamps are RFC3339
// strings on the wire and have been since the first review; the column is a
// timestamptz, so every mapping goes through stamp and nothing else formats.

func verdict(r conformancesql.ConformanceVerdict) (Verdict, error) {
	state, err := ParseOutcome(r.State)
	if err != nil {
		return Verdict{}, err
	}
	return Verdict{
		ID: r.ID, ReviewID: r.ReviewID, ProjectID: r.ProjectID, ProjectSlug: r.ProjectSlug,
		SHA: r.Sha, Block: r.Block, Version: int(r.Version), Check: r.CheckName,
		Evaluator: r.Evaluator, EvaluatorVer: r.EvaluatorVer, Mode: r.Mode, State: state,
		Severity: r.Severity, Detail: r.Detail, At: stamp.Of(r.CreatedAt).String(),
		ExceptedBy: r.ExceptedBy, EvidenceHash: r.EvidenceHash,
	}, nil
}

// verdicts maps a page of rows, refusing the whole page if any row carries a
// state this AppTown does not know: a half-read page is not a posture.
func verdicts(rows []conformancesql.ConformanceVerdict) ([]Verdict, error) {
	out := make([]Verdict, 0, len(rows))
	for _, r := range rows {
		v, err := verdict(r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func review(r conformancesql.ConformanceReview) (Review, error) {
	status, err := ParseReviewState(r.Status)
	if err != nil {
		return Review{}, err
	}
	return Review{
		ID: r.ID, Number: r.Number, Trigger: r.Trigger, Actor: r.Actor, ActorPrincipal: r.ActorPrincipal, StartedAt: stamp.Of(r.StartedAt).String(),
		FinishedAt: stamp.Time{NullTime: r.FinishedAt}, Projects: int(r.Projects), Verdicts: int(r.Verdicts),
		Errors: int(r.Errors), Cost: r.Cost, Status: status,
		ProjectsTotal: int(r.ProjectsTotal), ProjectsDone: int(r.ProjectsDone), CurrentProject: r.CurrentProject,
	}, nil
}

func reviews(rows []conformancesql.ConformanceReview) ([]Review, error) {
	out := make([]Review, 0, len(rows))
	for _, r := range rows {
		v, err := review(r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func disposition(r conformancesql.Disposition) (Disposition, error) {
	status, err := ParseDispositionKind(r.Status)
	if err != nil {
		return Disposition{}, err
	}
	return Disposition{
		ID: r.ID, ProjectID: r.ProjectID, Fingerprint: r.Fingerprint, Identifier: r.Identifier,
		Package: r.Package, Status: status, Justification: r.Justification, By: r.By, ByPrincipal: r.ByPrincipal, byVia: r.ByVia,
		CreatedAt: stamp.Of(r.CreatedAt).String(), ExpiresAt: stamp.Time{NullTime: r.ExpiresAt},
	}, nil
}

func adjudication(r conformancesql.ConformanceAdjudication) Adjudication {
	return Adjudication{
		ID: r.ID, VerdictID: r.VerdictID, Kind: r.Kind, By: r.By, ByPrincipal: r.ByPrincipal, Note: r.Note,
		CreatedAt: stamp.Of(r.CreatedAt).String(),
	}
}

// A review's exception snapshot is read as the disposition it copied, keyed
// by the disposition's own id: "as of review N" is a record, not a
// reconstruction (design/subsystems/conformance.md).
func reviewException(r conformancesql.ConformanceReviewException) (Disposition, error) {
	status, err := ParseDispositionKind(r.Status)
	if err != nil {
		return Disposition{}, err
	}
	return Disposition{
		ID: r.DispositionID, ProjectID: r.ProjectID, Fingerprint: r.Fingerprint, Identifier: r.Identifier,
		Package: r.Package, Status: status, Justification: r.Justification, By: r.By, ByPrincipal: r.ByPrincipal, byVia: r.ByVia,
		CreatedAt: stamp.Of(r.CreatedAt).String(), ExpiresAt: stamp.Time{NullTime: r.ExpiresAt},
	}, nil
}

// A fix is only legible beside the verdicts it moved between, so its report
// carries both and this turns the joined row into the subsystem's Fix.
func fix(r conformancesql.FixesForProjectRow) (Fix, error) {
	before, err := ParseOutcome(r.BeforeState)
	if err != nil {
		return Fix{}, err
	}
	f := Fix{
		ID: r.ConformanceFix.ID, ProjectID: r.ConformanceFix.ProjectID, CheckKey: r.ConformanceFix.CheckKey,
		Check: r.AfterCheckName, BeforeVerdictID: r.ConformanceFix.BeforeVerdictID,
		AfterVerdictID: r.ConformanceFix.AfterVerdictID, BeforeSHA: r.BeforeSha, AfterSHA: r.AfterSha,
		BeforeState: before, BeforeDetail: r.BeforeDetail, ReviewID: r.AfterReviewID, ReviewNumber: r.AfterReviewNumber,
		Commits: r.ConformanceFix.Commits, RemediationItem: r.ConformanceFix.RemediationItemID,
		AcceptedBy: r.ConformanceFix.AcceptedBy, AcceptedByPrincipal: r.ConformanceFix.AcceptedByPrincipal, At: stamp.Of(r.ConformanceFix.CreatedAt).String(),
	}
	if err := json.Unmarshal(r.ConformanceFix.ChangeIdsJson, &f.ChangeIDs); err != nil {
		return Fix{}, fmt.Errorf("fix %d change attribution: %w", f.ID, err)
	}
	if f.ChangeIDs == nil {
		f.ChangeIDs = []ids.ID{}
	}
	if len(r.ChangeNumbers) != len(f.ChangeIDs) {
		return Fix{}, fmt.Errorf("fix %d change attribution: %d numbers for %d changes", f.ID, len(r.ChangeNumbers), len(f.ChangeIDs))
	}
	f.ChangeNumbers = r.ChangeNumbers
	if f.ChangeNumbers == nil {
		f.ChangeNumbers = []int64{}
	}
	return f, nil
}
