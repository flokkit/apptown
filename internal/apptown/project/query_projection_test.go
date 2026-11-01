package project

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database/query"
	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/apptown/project/internal/projectsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type countedQueries struct {
	query.Handle
	reads  int
	writes int
}

func (d *countedQueries) SQL() query.Handle {
	return dbtest.WithExecutor(d.Handle, d)
}
func (d *countedQueries) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	d.writes++
	return d.Executor.ExecContext(ctx, q, args...)
}
func (d *countedQueries) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	d.reads++
	return d.Executor.QueryContext(ctx, q, args...)
}
func (d *countedQueries) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	d.reads++
	return d.Executor.QueryRowContext(ctx, q, args...)
}

func TestProjectQueriesPreserveTypesTransactionsAndErrors(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	id := idstest.Parse(t, "ffffffff-ffff-7fff-bfff-ffffffffffff")
	// The owner accepts an exact UUID and timestamp; keep both independent
	// expected values below while supplying every required creation invariant.
	at, err := time.Parse(time.RFC3339Nano, "2020-01-02T03:04:05.123456+11:00")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTx(t.Context(), func(tx *database.Tx) error {
		if _, err := CreateAt(t.Context(), tx, at, Spec{ID: id, Slug: "typed", Name: "Typed", Namespace: "team", Icon: "star", Blurb: "intent"}); err != nil {
			return err
		}
		return SetDefaultBranchIn(t.Context(), tx, id, "main")
	}); err != nil {
		t.Fatal(err)
	}
	counted := &countedQueries{Handle: db.SQL()}
	p, err := ByIDIn(context.Background(), counted, id)
	if err != nil || p.ID != id || p.Slug != "typed" || p.ServingLabel != "typed" || p.Name != "Typed" || p.Namespace != "team" || p.Icon != "star" || p.Blurb != "intent" || p.CreatedAt != "2020-01-01T16:04:05.123456Z" || counted.reads != 1 {
		t.Fatalf("project projection: %+v, reads=%d: %v", p, counted.reads, err)
	}
	if p, err := ByIDIn(context.Background(), db, idstest.N(999999)); p != (Project{}) || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing project: %+v %v", p, err)
	}
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := projectsql.New(tx.SQL()).SetProjectDefaultBranch(context.Background(), projectsql.SetProjectDefaultBranchParams{ID: id, DefaultBranch: "uncommitted"}); err != nil {
		t.Fatal(err)
	}
	in, err := ByIDIn(context.Background(), tx, id)
	out, outsideErr := ByIDIn(context.Background(), db, id)
	if err != nil || outsideErr != nil || in.DefaultBranch != "uncommitted" || out.DefaultBranch != "main" {
		t.Fatalf("query escaped transaction: inside=%+v outside=%+v errors=%v/%v", in, out, err, outsideErr)
	}
	t.Cleanup(dbtest.NextStatementFailure(db, "ProjectByID"))
	if p, err := ByIDIn(context.Background(), db, id); err == nil || p != (Project{}) {
		t.Fatal("generated row query bypassed failure injection")
	}
}
