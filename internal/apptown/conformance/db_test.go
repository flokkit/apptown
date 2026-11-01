package conformance

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database/query"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M)            { os.Exit(databaseSuite.Run(m)) }
func testDB(t *testing.T) *database.DB { t.Helper(); return databaseSuite.New(t) }
func completionExec(t *testing.T, db *database.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.SQL().ExecContext(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}

func createTestProject(t *testing.T, db *database.DB, slug, name, branch string) ids.ID {
	t.Helper()
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: slug, Name: name})
	})
	if err != nil {
		t.Fatal(err)
	}
	if branch != "" {
		if err := project.New(db, nil).SetDefaultBranch(context.Background(), id, branch); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// Canonical schema, with the same project/check/exception facts the former App
// fixture supplied; storage tests do not need a repository or evaluator process.
func reviewFixture(t *testing.T) (*database.DB, ids.ID) {
	t.Helper()
	db := testDB(t)
	project := createTestProject(t, db, "reviewed", "Reviewed", "main")
	block := stockCheckFixture(t, db, wow.StockBlock{Name: "hygiene", ChecksJSON: `[{"appliesTo": ["repository"], "name":"clean","evaluator":"sh","sh":"exit 0","severity":"high"}]`})
	pinCheckFixture(t, db, project, wow.ResolvedPin{BlockID: block, Name: "hygiene", Version: 1})
	createDispositionFixture(t, db, ExceptionRequest{ProjectID: project, Fingerprint: "guidance|hygiene@1|clean|", Kind: AcceptedRisk, Justification: "fixture"})
	return db, project
}

// Real round-trip instrumentation retains the pool's fault scope.
type countedQueries struct {
	query.Handle
	reads, writes int
}

func (d *countedQueries) SQL() query.Handle { return dbtest.WithExecutor(d.Handle, d) }
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

func stockCheckFixture(t *testing.T, db *database.DB, block wow.StockBlock) ids.ID {
	t.Helper()
	library := wow.NewLibrary(db)
	if err := library.StockInsert(t.Context(), block); err != nil {
		t.Fatal(err)
	}
	saved, err := library.ByName(t.Context(), block.Name)
	if err != nil {
		t.Fatal(err)
	}
	return saved.ID
}
func pinCheckFixture(t *testing.T, db *database.DB, projectID ids.ID, pin wow.ResolvedPin) {
	t.Helper()
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return adoption.PutImportsIn(t.Context(), tx, projectID, []wow.ResolvedPin{pin}, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
}
func createDispositionFixture(t *testing.T, db *database.DB, request ExceptionRequest) {
	t.Helper()
	if _, err := newServiceWithAuthority(db, testAuthority{}, nil).CreateDisposition(caller("reviewer"), request); err != nil {
		t.Fatal(err)
	}
}
