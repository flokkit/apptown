package project

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/project/internal/projectsql"
	"flokkit.io/forge/internal/ids/idstest"
)

type countedQueries struct {
	database.Handle
	reads  int
	writes int
}

func (d *countedQueries) SQL() database.Handle {
	return d.Handle.WithExecutor(d)
}
func (d *countedQueries) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	d.writes++
	return d.DBTX.ExecContext(ctx, q, args...)
}
func (d *countedQueries) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	d.reads++
	return d.DBTX.QueryContext(ctx, q, args...)
}
func (d *countedQueries) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	d.reads++
	return d.DBTX.QueryRowContext(ctx, q, args...)
}

func TestProjectQueriesPreserveTypesTransactionsAndErrors(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	id := idstest.Parse(t, "ffffffff-ffff-7fff-bfff-ffffffffffff")
	// sql-guarantee: an id with every bit a v7 id may set, plus a historical timestamp, tests lossless projection; InsertProject stamps its own creation time.
	if _, err := db.Exec(`INSERT INTO projects(id,slug,name,default_branch,namespace,icon,blurb,created_at)
		VALUES (?,'typed','Typed','main','team','star','intent','2020-01-02T03:04:05.123456+11:00')`, id); err != nil {
		t.Fatal(err)
	}
	counted := &countedQueries{Handle: db.SQL()}
	p, err := ByIDIn(context.Background(), counted, id)
	if err != nil || p.ID != id || p.Slug != "typed" || p.Name != "Typed" || p.Namespace != "team" || p.Icon != "star" || p.Blurb != "intent" || p.CreatedAt != "2020-01-01T16:04:05.123456Z" || counted.reads != 1 {
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
	t.Cleanup(db.FailNextStatement("ProjectByID"))
	if p, err := ByIDIn(context.Background(), db, id); err == nil || p != (Project{}) {
		t.Fatal("generated row query bypassed failure injection")
	}
}
