package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

var errStandingRevision = fault.New(fault.Conflict, "the standing approval changed; read its current meaning before deciding")

func readStandingRevision(row workflowsql.StandingRuleRevision) (StandingRevision, error) {
	var r StandingRevision
	if err := json.Unmarshal(row.RecordJson, &r); err != nil {
		return r, fmt.Errorf("read standing approval: %w", err)
	}
	if r.RuleID != row.RuleID || r.Revision != row.Revision || r.Enabled != row.Enabled || r.Issuer.Principal.IsZero() || r.Question.Digest == "" {
		return r, fmt.Errorf("standing approval evidence does not match its revision")
	}
	return r, nil
}
func currentStanding(ctx context.Context, q database.Queryer, id ids.ID) (StandingRevision, error) {
	queries := workflowsql.New(q.SQL())
	n, err := queries.StandingRuleHead(ctx, id)
	if err != nil {
		return StandingRevision{}, database.Missing(err)
	}
	row, err := queries.StandingRevision(ctx, workflowsql.StandingRevisionParams{RuleID: id, Revision: n})
	if err != nil {
		return StandingRevision{}, err
	}
	return readStandingRevision(row)
}
func writeStandingRevision(ctx context.Context, tx *database.Tx, r StandingRevision, create bool) error {
	q := workflowsql.New(tx.SQL())
	if create {
		if err := q.InsertStandingRule(ctx, r.RuleID); err != nil {
			return err
		}
	} else {
		n, err := q.AdvanceStandingRule(ctx, workflowsql.AdvanceStandingRuleParams{ID: r.RuleID, ExpectedRevision: r.Revision - 1, NextRevision: r.Revision})
		if err != nil {
			return err
		}
		if n != 1 {
			return errStandingRevision
		}
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err = q.InsertStandingRevision(ctx, workflowsql.InsertStandingRevisionParams{RuleID: r.RuleID, Revision: r.Revision, Enabled: r.Enabled, RecordJson: raw}); err != nil {
		return err
	}
	// One receipt per exact project keeps the decision available through existing
	// project history. All receipts and the head are in this transaction.
	for _, p := range r.Question.Definition.Projects {
		if err = audit.At(r.ChangedAt).RecordIn(ctx, tx, r.ChangedBy.Name, p.ProjectID, "standing-approval.revised", r.RuleID.String(), r); err != nil {
			return err
		}
	}
	return nil
}

// DisableRestoredStandingRules runs after schema migration and before App
// construction/recovery, under the daemon's exclusive database hold. Historical
// consent is retained; only enabled heads acquire a disabled restore revision.
// Repeating a loaded restore continuation therefore does not duplicate history.
func DisableRestoredStandingRules(ctx context.Context, db *database.DB, at time.Time) error {
	_, err := database.Transact(ctx, db, func(tx *database.Tx) (struct{}, error) {
		q := workflowsql.New(tx.SQL())
		rows, err := q.CurrentStandingRules(ctx)
		if err != nil {
			return struct{}{}, err
		}
		for _, row := range rows {
			if !row.Enabled {
				continue
			}
			r, err := readStandingRevision(row)
			if err != nil {
				return struct{}{}, err
			}
			head, err := q.LockStandingRule(ctx, r.RuleID)
			if err != nil {
				return struct{}{}, err
			}
			if head != r.Revision {
				return struct{}{}, errStandingRevision
			}
			r.Revision++
			r.Enabled = false
			r.ChangedBy = access.Actor{Name: "restore", Via: access.SystemInstrument}
			r.ChangedAt = at
			r.Reason = "restored consent requires a new local human confirmation"
			if err = writeStandingRevision(ctx, tx, r, false); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}
