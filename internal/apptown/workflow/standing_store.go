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

// StartJanitorChoices runs at daemon start, after schema migration and before
// App construction/recovery, under the daemon's exclusive database hold. An
// instance with no choices record gets AppTown's enabled first revision; any
// existing record is left intact. After a restore, an enabled head then gets a
// disabled restore revision, so a restored copy does not act on its own, with
// historical consent retained. Both run in one transaction serialized with
// Save; repeating a start (or a loaded restore continuation) adds nothing.
func StartJanitorChoices(ctx context.Context, db *database.DB, at time.Time, restored bool) error {
	_, err := database.Transact(ctx, db, func(tx *database.Tx) (struct{}, error) {
		if err := defaultJanitorChoicesIn(ctx, tx, at); err != nil {
			return struct{}{}, err
		}
		if !restored {
			return struct{}{}, nil
		}
		return struct{}{}, disableRestoredIn(ctx, tx, at)
	})
	return err
}

func defaultJanitorChoicesIn(ctx context.Context, tx *database.Tx, at time.Time) error {
	if err := workflowsql.New(tx.SQL()).LockJanitorChoices(ctx); err != nil {
		return err
	}
	old, err := latestStanding(ctx, tx)
	if err != nil || old != nil {
		return err
	}
	r := StandingRevision{RuleID: ids.New(at), Revision: 1, Enabled: true, Choices: defaultJanitorChoices, Issuer: appTownIssuer, At: at}
	return writeStandingRevision(ctx, tx, r, true)
}

func disableRestoredIn(ctx context.Context, tx *database.Tx, at time.Time) error {
	r, err := latestStanding(ctx, tx)
	if err != nil {
		return err
	}
	if r == nil || !r.Enabled {
		return nil
	}
	head, err := workflowsql.New(tx.SQL()).LockStandingRule(ctx, r.RuleID)
	if err != nil {
		return err
	}
	if head != r.Revision {
		return errStandingRevision
	}
	r.Revision++
	r.Enabled = false
	r.Choices = JanitorChoices{Dependencies: "off"}
	r.Issuer = access.Actor{Name: "restore", Principal: r.Issuer.Principal, Via: access.SystemInstrument}
	r.At = at
	return writeStandingRevision(ctx, tx, *r, false)
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
