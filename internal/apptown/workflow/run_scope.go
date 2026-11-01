package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// runScope is a recorded run binding for trusted readers. It grants no mutation
// authority and resolves its project afresh on each use.
type runScope struct {
	db *database.DB
	id ids.ID
}

func (s *Service) RunScope(id ids.ID) runScope { return runScope{db: s.db, id: id} }
func (r runScope) ProjectID(ctx context.Context) (ids.ID, error) {
	if r.db == nil || r.id.IsZero() {
		return ids.Nil, ErrCallerRequired
	}
	return runProject(ctx, r.db, r.id)
}
func runProject(ctx context.Context, q database.Queryer, id ids.ID) (ids.ID, error) {
	project, err := workflowsql.New(q.SQL()).RunProjectMeta(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ids.Nil, missingRun{id}
	}
	return project, err
}

// missingRun is a run id that names no run: not found, in words a caller can
// act on (`apptown wait 1300` with a change's id answered "sql: no rows in
// result set").
type missingRun struct{ id ids.ID }

func (e missingRun) Error() string {
	return fmt.Sprintf("no run %s: it does not exist, or its project was deleted", e.id)
}
func (missingRun) FaultKind() fault.Kind { return fault.NotFound }
func (missingRun) Unwrap() error         { return sql.ErrNoRows }

// missingChange and missingStep say what is missing in words: a deleted
// project's change links would otherwise answer "sql: no rows in result set".
// A deleted project's changes, runs and steps are removed with it.
type missingChange struct{ id ids.ID }

func (e missingChange) Error() string {
	return fmt.Sprintf("no change %s: it does not exist, or its project was deleted", e.id)
}
func (missingChange) FaultKind() fault.Kind { return fault.NotFound }
func (missingChange) Unwrap() error         { return sql.ErrNoRows }

type missingStep struct{ id ids.ID }

func (e missingStep) Error() string {
	return fmt.Sprintf("no step %s: it does not exist, or its project was deleted", e.id)
}
func (missingStep) FaultKind() fault.Kind { return fault.NotFound }
func (missingStep) Unwrap() error         { return sql.ErrNoRows }
