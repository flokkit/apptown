package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/access"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

var errStandingRevision = fault.New(fault.Conflict, "the Janitor choices changed; read them again before saving")

func readStandingRevision(row workflowsql.StandingRuleRevision) (StandingRevision, error) {
	var r StandingRevision
	if err := json.Unmarshal(row.RecordJson, &r); err != nil {
		return r, fmt.Errorf("read standing approval: %w", err)
	}
	if r.RuleID != row.RuleID || r.Revision != row.Revision || r.Enabled != row.Enabled || r.Issuer.Principal.IsZero() {
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

	return nil
}

// DisableRestoredStandingRules runs after schema migration and before App
// construction/recovery, under the daemon's exclusive database hold. Historical
// consent is retained; only enabled heads acquire a disabled restore revision.
// Repeating a loaded restore continuation therefore does not duplicate history.
func DisableRestoredStandingRules(ctx context.Context, db *database.DB, at time.Time) error {
	_, err := database.Transact(ctx, db, func(tx *database.Tx) (struct{}, error) {
		q := workflowsql.New(tx.SQL())
		r, err := latestStanding(ctx, tx)
		if err != nil {
			return struct{}{}, err
		}
		if r == nil || !r.Enabled {
			return struct{}{}, nil
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
		r.Choices = JanitorChoices{Dependencies: "off"}
		r.Issuer = access.Actor{Name: "restore", Principal: r.Issuer.Principal, Via: access.SystemInstrument}
		r.At = at
		if err = writeStandingRevision(ctx, tx, *r, false); err != nil {
			return struct{}{}, err
		}

		return struct{}{}, nil
	})
	return err
}

func latestStanding(ctx context.Context, q database.Queryer) (*StandingRevision, error) {
	rows, err := workflowsql.New(q.SQL()).CurrentStandingRules(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r, err := readStandingRevision(rows[0])
	if err != nil {
		return nil, err
	}
	return &r, nil
}
