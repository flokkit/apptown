package remediation

import (
	"encoding/json"
	"time"

	"github.com/flokkit/apptown/internal/apptown/remediation/internal/remediationsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/stamp"
)

// queries binds the facade's named SQL (internal/remediationsql/queries.sql)
// to a pool or a transaction; SQL() carries the test-only statement fault.
func queries(q database.Queryer) *remediationsql.Queries { return remediationsql.New(q.SQL()) }

// The stored rows in the facade's own words. Timestamps on these records have
// always gone out with nanoseconds, unlike the rest of the product, so they
// are formatted here rather than through stamp's RFC3339.
func item(r remediationsql.RemediationItem) Item {
	return Item{
		ID: r.ID, RemediationID: r.RemediationID, ProjectID: r.ProjectID, ProjectSlug: r.ProjectSlug,
		CheckKey: r.CheckKey, Block: r.Block, Version: int(r.Version), Check: r.CheckName, Rank: int(r.Rank),
		State: r.State, Reason: r.Reason, OriginVerdictID: r.OriginVerdictID, OriginSHA: r.OriginSha,
		OriginEvaluatorVer: r.OriginEvaluatorVer, OriginEvidenceHash: r.OriginEvidenceHash,
		OriginDetail: r.OriginDetail, Attempt: int(r.Attempt), TransientRetries: int(r.TransientRetries),
		ChangeID: r.ChangeID, RunID: r.RunID, LeaseWorker: r.LeaseWorker,
		LeaseUntil: stamp.Time{NullTime: r.LeaseUntil}, NotBefore: stamp.Time{NullTime: r.NotBefore},
		VerifiedReviewID: r.VerifiedReviewID, CostMicros: r.CostMicros, OutcomeRevision: r.OutcomeRevision,
		UpdatedAt: r.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func items(rows []remediationsql.RemediationItem) []Item {
	out := make([]Item, 0, len(rows))
	for _, r := range rows {
		out = append(out, item(r))
	}
	return out
}

// remediationOf turns a job row and its lateral totals into the job a person
// reads: the counts by state, the cost in whole units, and the sentence.
func remediationOf(r remediationsql.Remediation, counts string, costMicros int64) (Remediation, error) {
	m := Remediation{
		ID: r.ID, Number: r.Number, Scope: r.Scope, ScopeRef: r.ScopeRef, Mode: r.Mode, Actor: r.Actor, ActorPrincipal: r.ActorPrincipal, Status: r.Status,
		Concurrency: int(r.Concurrency), Retries: int(r.Retries), ReviewID: r.ReviewID,
		FinishedAt: stamp.Time{NullTime: r.FinishedAt},
		CreatedAt:  r.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:  r.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Cost:       float64(costMicros) / 1e6,
	}
	if err := json.Unmarshal([]byte(counts), &m.Counts); err != nil {
		return Remediation{}, err
	}
	m.Sentence = m.progressSentence()
	return m, nil
}
