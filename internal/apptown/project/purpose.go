package project

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/project/internal/projectsql"
	"github.com/flokkit/apptown/internal/database"
)

// Purpose is what a project is for. It is written in the INSERT that makes
// the project and never changed, so no project edit turns a copy into an
// ordinary project. Effect owners ask the allowances below, not the
// purpose's name; a project whose purpose cannot be read refuses the effect.
type Purpose string

const (
	// Ordinary is every project a person makes, imports or moves.
	Ordinary Purpose = "ordinary"
	// EvalCopy is a throwaway copy an eval made of a project
	// (projectlifecycle.CreateCopy); which eval and arm is Evals' record.
	EvalCopy Purpose = "eval-copy"
)

// Purposes is every purpose, in the order of the column's CHECK.
var Purposes = []Purpose{Ordinary, EvalCopy}

func purposeOf(s string) (Purpose, error) {
	for _, p := range Purposes {
		if string(p) == s {
			return p, nil
		}
	}
	return "", fmt.Errorf("project purpose %q is not one this AppTown knows", s)
}

// The allowances: one row per purpose. A further purpose starts as a row
// here, and each effect's meaning is checked for it (publication through
// AppTown's own registry stays an eval-copy act: integration.EvalPublication).
var allowances = map[Purpose]struct{ integrations, evalDomainOnly, schedules, moves, keepsData bool }{
	Ordinary: {integrations: true, schedules: true, moves: true, keepsData: true},
	EvalCopy: {evalDomainOnly: true},
}

// Integrations says whether the project may be given integrations and what
// the instance holds for ordinary work: attachments and provisioned
// resources, instance step values, registry and SCM credentials.
func (p Purpose) Integrations() bool { return allowances[p].integrations }

// EvalDomainOnly says whether the project is placed, previewed and served
// on the eval domain and nowhere else.
func (p Purpose) EvalDomainOnly() bool { return allowances[p].evalDomainOnly }

// Schedules says whether the project's adopted schedules fire.
func (p Purpose) Schedules() bool { return allowances[p].schedules }

// Moves says whether the project may be exported to another AppTown.
func (p Purpose) Moves() bool { return allowances[p].moves }

// KeepsData says whether a take-down of the project's deployments keeps a
// final copy of their data; a copy's data is synthetic.
func (p Purpose) KeepsData() bool { return allowances[p].keepsData }

// PurposeIn reads a project's purpose through q. A missing project is an
// error, as is a failed read: never "ordinary".
func PurposeIn(ctx context.Context, q database.Queryer, id ids.ID) (Purpose, error) {
	s, err := projectsql.New(q.SQL()).ProjectPurpose(ctx, id)
	if err != nil {
		return "", fmt.Errorf("reading project %d's purpose: %w", id, database.Missing(database.QueryError(err, "ProjectPurpose")))
	}
	return purposeOf(s)
}

// Purpose is PurposeIn on the record's connection.
func (s *Projects) Purpose(ctx context.Context, id ids.ID) (Purpose, error) {
	return PurposeIn(ctx, s.db, id)
}

// Copies answers adoption whether a project is an eval's copy
// (adoption.CopyPurpose).
type Copies struct{}

// IsEvalCopyIn reads the project's purpose through q.
func (Copies) IsEvalCopyIn(ctx context.Context, q database.Queryer, id ids.ID) (bool, error) {
	p, err := PurposeIn(ctx, q, id)
	return p == EvalCopy, err
}
