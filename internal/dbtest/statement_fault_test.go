package dbtest_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

// Contention belongs to our one-shot injector; no connection or schema is needed.
func TestStatementFaultConsumedOnceUnderContention(t *testing.T) {
	t.Parallel()
	var db database.DB
	t.Cleanup(dbtest.NextStatementFailure(&db, "target"))
	if err := checkFault(&db, "unrelated"); err != nil {
		t.Fatal(err)
	}
	var failures atomic.Int32
	var workers sync.WaitGroup
	start := make(chan struct{})
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			if err := checkFault(&db, "target"); errors.Is(err, dbtest.ErrInjectedFailure) {
				failures.Add(1)
			} else if err != nil {
				t.Error(err)
			}
		}()
	}
	close(start)
	workers.Wait()
	if got := failures.Load(); got != 1 {
		t.Fatalf("one installation failed %d callers", got)
	}
}

func TestAFaultCanLetEarlierMatchingStatementsRun(t *testing.T) {
	db := &database.DB{}
	t.Cleanup(dbtest.StatementFailureAfter(db, "target", 2))
	for i := 0; i < 2; i++ {
		if err := checkFault(db, "target"); err != nil {
			t.Fatalf("matching statement %d failed before its turn: %v", i+1, err)
		}
	}
	if err := checkFault(db, "target"); !errors.Is(err, dbtest.ErrInjectedFailure) {
		t.Fatalf("third matching statement = %v, want the injected failure", err)
	}
	if err := checkFault(db, "target"); err != nil {
		t.Fatalf("the fault fired twice: %v", err)
	}
}

type statements struct{}

func (statements) ExecContext(context.Context, string, ...any) (sql.Result, error) { return nil, nil }
func (statements) PrepareContext(context.Context, string) (*sql.Stmt, error)       { return nil, nil }
func (statements) QueryContext(context.Context, string, ...any) (*sql.Rows, error) { return nil, nil }
func (statements) QueryRowContext(context.Context, string, ...any) *sql.Row        { return nil }
func checkFault(db *database.DB, q string) error {
	_, err := dbtest.WithExecutor(db.SQL(), statements{}).ExecContext(context.Background(), q)
	return err
}
