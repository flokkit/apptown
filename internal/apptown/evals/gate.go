package evals

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/evals/internal/evalssql"
	"github.com/flokkit/apptown/internal/apptown/usage"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// ErrRefused is work an eval does not let proceed: it is deleted, ended or
// stopping, its budget is spent, or (for a new arm) it is held on spend
// it could not count.
var ErrRefused = fault.New(fault.Conflict, "the eval does not let this proceed")

// Spend is the copies' recorded spending (usage.Ledger.ProjectsTotal),
// read through the querier given: the caller's transaction where it holds
// one, so the eval's rule never waits on a second connection.
type Spend interface {
	ProjectsTotal(ctx context.Context, q database.Queryer, projectIDs ...ids.ID) (usage.Totals, error)
	// LockInUnknownIn locks in the projects' expired pending spend as
	// unknown and counts their unknown lines that can never change.
	LockInUnknownIn(ctx context.Context, q database.Queryer, projectIDs ...ids.ID) (int64, error)
}

// Gate is the one "may this proceed?" rule of an eval, asked at the
// effects for a copy: each model dispatch (AdmitModel), each step claim
// and each merge (Work's EvalGate); and, to save wasted work, before an
// arm's copy is made and before its lane starts. It reads the eval's state
// and its copies' whole spend at that moment.
type Gate struct {
	db    *database.DB
	spend Spend
}

func NewGate(db *database.DB, spend Spend) *Gate { return &Gate{db: db, spend: spend} }

// verdict is why work may not proceed, "" when it may. A new arm also
// waits while the eval is held, or when spend it could not count has
// appeared since its starter last continued (hold true: the eval should be
// held).
func verdict(e evalssql.Eval, spent usage.Totals, newArm bool) (why string, stop, hold bool) {
	switch {
	case e.DeletedAt.Valid:
		return fmt.Sprintf("eval %d is deleted", e.ID), false, false
	case e.State == StateFinished || e.State == StateStopped:
		return fmt.Sprintf("eval %d has ended", e.ID), false, false
	case e.State == StateStopping:
		return fmt.Sprintf("eval %d is stopping: %s", e.ID, e.Reason), false, false
	case spent.CostMicros >= e.BudgetMicros:
		return fmt.Sprintf("eval %d's budget of $%.2f is spent", e.ID, float64(e.BudgetMicros)/1e6), true, false
	case newArm && e.State == StateHeld:
		return fmt.Sprintf("eval %d is held: %s", e.ID, e.Reason), false, false
	case newArm && spent.Incomplete > e.IncompleteSeen:
		n := spent.Incomplete - e.IncompleteSeen
		return fmt.Sprintf("%d model %s spent an unknown amount; the person who started the eval may continue", n, plural(n, "call", "calls")), false, true
	}
	return "", false, false
}

func (g *Gate) spent(ctx context.Context, q database.Queryer, evalID ids.ID) (usage.Totals, error) {
	idList, err := queries(q).CopiesOfEval(ctx, evalID)
	if err != nil {
		return usage.Totals{}, err
	}
	return g.spend.ProjectsTotal(ctx, q, idList...)
}

// Admit is the question for a project, asked on the gate's own
// connection: nil for a project that is no eval copy, or a copy whose eval
// may proceed; ErrRefused, with why, otherwise. A read failure is an
// error, never a yes.
func (g *Gate) Admit(ctx context.Context, projectID ids.ID) error {
	return g.AdmitIn(ctx, g.db, projectID)
}

// AdmitIn is Admit read entirely through q: a caller holding a
// transaction (a merge's admission, its SCM claim) asks within it, the
// eval's row shared-locked so a stop or a deletion waits for the effect's
// decision, and never needs another connection meanwhile.
func (g *Gate) AdmitIn(ctx context.Context, q database.Queryer, projectID ids.ID) error {
	copy, ok, err := copyOfIn(ctx, q, projectID)
	if err != nil || !ok {
		return err
	}
	e, err := queries(q).EvalForShare(ctx, copy.EvalID)
	if err != nil {
		return err
	}
	spent, err := g.spent(ctx, q, e.ID)
	if err != nil {
		return err
	}
	if why, _, _ := verdict(e, spent, false); why != "" {
		return fmt.Errorf("%w: %s", ErrRefused, why)
	}
	return nil
}

// admitArm decides, under the eval's row lock, whether a new arm may
// start now, and records the eval's stop or hold when the spend says so.
func (g *Gate) admitArm(ctx context.Context, evalID ids.ID) error {
	var refused error
	err := g.db.InTx(ctx, func(tx *database.Tx) error {
		e, err := queries(tx).EvalForUpdate(ctx, evalID)
		if err != nil {
			return err
		}
		spent, err := g.spent(ctx, tx, evalID)
		if err != nil {
			return err
		}
		why, stop, hold := verdict(e, spent, true)
		if why == "" {
			return nil
		}
		refused = fmt.Errorf("%w: %s", ErrRefused, why)
		switch {
		case stop:
			_, err = queries(tx).StopEval(ctx, evalssql.StopEvalParams{ID: evalID, Reason: why})
		case hold:
			_, err = queries(tx).HoldEval(ctx, evalssql.HoldEvalParams{ID: evalID, Reason: why})
		}
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		return err
	})
	if err != nil {
		return err
	}
	return refused
}

// recordArmCopyIn is the copy's creation transaction's part: under the
// eval's row lock, refused unless the eval still runs, the copy record and
// the arm's link to its copy are written together, so a deletion either
// comes first and no copy is made, or comes after and finds the copy.
func recordArmCopyIn(ctx context.Context, tx *database.Tx, projectID ids.ID, c Copy) error {
	e, err := queries(tx).EvalForUpdate(ctx, c.EvalID)
	if err != nil {
		return err
	}
	if e.DeletedAt.Valid || e.State != StateRunning {
		return fmt.Errorf("%w: eval %d is %s", ErrRefused, e.ID, map[bool]string{true: "deleted", false: e.State}[e.DeletedAt.Valid])
	}
	if err := recordCopyIn(ctx, tx, projectID, c); err != nil {
		return err
	}
	n, err := queries(tx).ArmCopied(ctx, evalssql.ArmCopiedParams{ID: c.ArmID, ProjectID: projectID})
	if err == nil && n != 1 {
		err = fmt.Errorf("%w: arm %d is no longer waiting to start", ErrRefused, c.ArmID)
	}
	return err
}
