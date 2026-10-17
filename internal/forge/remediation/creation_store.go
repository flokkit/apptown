package remediation

import (
	"context"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/remediation/internal/remediationsql"
	"flokkit.io/forge/internal/stamp"
)

// A creation request carries the resolved, ordered findings to freeze. Scope
// resolution and external work finish before this operation takes a transaction.
type CreationRequest struct {
	Scope, Ref, Actor string
	Concurrency       int
	Findings          []conformance.Observation
}

// Creations freezes a whole job: the job row, its items and the receipt.
type Creations struct {
	db    *database.DB
	clock func() time.Time // the forge's clock; nil is the real one
}

func (s Creations) now() time.Time { return clock.Or(s.clock)() }

func NewCreations(db *database.DB, now func() time.Time) Creations {
	return Creations{db: db, clock: now}
}

// The job, every queued item and their opening receipt become visible together.
// Only a committed result may be returned to the caller or offered to workers.
func (s Creations) Create(ctx context.Context, in CreationRequest) (*Remediation, []Item, error) {
	at := s.now()
	if len(in.Findings) == 0 {
		return nil, nil, fmt.Errorf("nothing to fix in that scope — no findings a person has not already excepted or that need confirming first")
	}
	for _, v := range in.Findings {
		if v.Record == nil {
			return nil, nil, fmt.Errorf("remediation finding has no saved verdict")
		}
	}
	var reviewID ids.ID
	if in.Scope == "review" {
		var err error
		if reviewID, err = ids.Parse(in.Ref); err != nil || reviewID.IsZero() {
			return nil, nil, fmt.Errorf("review scope needs a review id")
		}
	}
	made := at.UTC().Format(time.RFC3339)
	m := &Remediation{Scope: in.Scope, ScopeRef: in.Ref, Mode: "attended", Actor: in.Actor, Status: RemRunning,
		Concurrency: in.Concurrency, ReviewID: reviewID, CreatedAt: made, UpdatedAt: made}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	number, err := queries(tx).NextRemediationNumber(ctx)
	if err != nil {
		return nil, nil, err
	}
	starter, err := identity.ActorIn(ctx, tx, m.Actor)
	if err != nil {
		return nil, nil, err
	}
	m.ActorPrincipal = starter.Principal
	created, err := queries(tx).InsertRemediation(ctx, remediationsql.InsertRemediationParams{ID: ids.New(stamp.Parse(m.CreatedAt).Time), Number: number,
		Scope: m.Scope, ScopeRef: m.ScopeRef, Mode: m.Mode, Actor: m.Actor, ActorPrincipal: starter.Principal, ActorVia: starter.Via, Status: m.Status,
		Concurrency: int64(m.Concurrency), Retries: 0, ReviewID: m.ReviewID,
		CreatedAt: stamp.Parse(m.CreatedAt).Time, UpdatedAt: stamp.Parse(m.UpdatedAt).Time,
	})
	if err != nil {
		return nil, nil, err
	}
	res := created.ID
	// The job insert acquires admission before origins are validated. A history
	// clear that committed first must make this request fail, not leave dangling work.
	if err := conformance.ValidateRemediationOriginsIn(ctx, tx, in.Findings); err != nil {
		return nil, nil, err
	}
	m.ID, m.Number = res, created.Number
	var items []Item
	rank := map[string]int{}
	for _, v := range in.Findings {
		rank[v.ProjectSlug]++
		it := Item{RemediationID: m.ID, ProjectID: v.ProjectID, ProjectSlug: v.ProjectSlug,
			CheckKey: conformance.CheckKey(v.Block, v.Version, v.Check), Block: v.Block, Version: v.Version, Check: v.Check,
			Rank: rank[v.ProjectSlug], State: ItemQueued, OriginVerdictID: v.Record.ID, OriginSHA: v.Record.SHA,
			OriginEvaluatorVer: v.Record.EvaluatorVer, OriginEvidenceHash: v.Record.EvidenceHash, OriginDetail: v.Detail(), UpdatedAt: m.CreatedAt}
		row, err := queries(tx).InsertItem(ctx, remediationsql.InsertItemParams{ID: ids.New(stamp.Parse(it.UpdatedAt).Time),
			RemediationID: it.RemediationID, ProjectID: it.ProjectID, ProjectSlug: it.ProjectSlug,
			CheckKey: it.CheckKey, Block: it.Block, Version: int64(it.Version), CheckName: it.Check,
			Rank: int64(it.Rank), State: it.State, OriginVerdictID: it.OriginVerdictID, OriginSha: it.OriginSHA,
			OriginEvaluatorVer: it.OriginEvaluatorVer, OriginEvidenceHash: it.OriginEvidenceHash,
			OriginDetail: it.OriginDetail, UpdatedAt: stamp.Parse(it.UpdatedAt).Time,
		})
		if err != nil {
			return nil, nil, err
		}
		it.ID = row.ID
		items = append(items, it)
	}
	receipt := struct {
		Scope string `json:"scope"`
		Ref   string `json:"ref"`
		Items int    `json:"items"`
	}{in.Scope, in.Ref, len(items)}
	if err := audit.At(at).RecordIn(ctx, tx, in.Actor, ids.Nil, "remediation.started", fmt.Sprint(m.ID), receipt); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return m, items, nil
}
