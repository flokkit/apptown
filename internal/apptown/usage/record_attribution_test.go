package usage

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Direct storage observations catch swapped/dropped attribution columns even
// when a writer and a rollup could otherwise agree on the same mistake.
func TestRecordKeepsExactAttributionAndFailure(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ledger := New(db)
	totals := Totals{Input: 10, Output: 5, Total: 15, CostMicros: 1000}
	for _, entry := range []Entry{
		{Kind: "approval-brief", ProjectID: idstest.N(42), RunID: idstest.N(7), Actor: "apptown-system", Provider: "openrouter", Model: "vendor/model"},
		{Kind: "narrative", ProjectID: idstest.N(42), Actor: "apptown-system", Provider: "openrouter", Model: "vendor/model"},
	} {
		if err := ledger.Record(context.Background(), entry, totals, ""); err != nil {
			t.Fatal(err)
		}
	}
	var count, brief, narrative int
	// sql-guarantee: Exact attribution columns are the guarantee (see the comment above); a rollup could agree with a writer that swapped them.
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*), COUNT(*) FILTER (WHERE run_id='00000000-0007-7000-8000-000000000000' AND kind='approval-brief'), COUNT(*) FILTER (WHERE run_id IS NULL AND kind='narrative') FROM token_usage WHERE project_id='00000000-002a-7000-8000-000000000000' AND provider='openrouter'
  AND model='vendor/model' AND actor='apptown-system' AND input=10 AND output=5 AND total=15
  AND cost_micros=1000 AND cache_read=0 AND cache_write=0 AND root_run_id IS NULL AND step_id IS NULL
  AND workflow_name='' AND workflow_version=0 AND session_id=''`).Scan(&count, &brief, &narrative); err != nil || count != 2 || brief != 1 || narrative != 1 {
		t.Fatalf("attributed rows: all=%d brief=%d narrative=%d %v", count, brief, narrative, err)
	}
	t.Cleanup(dbtest.NextStatementFailure(db, "INSERT INTO token_usage"))
	if err := ledger.Record(context.Background(), Entry{Kind: "approval-brief", ProjectID: idstest.N(42), RunID: idstest.N(7)}, totals, ""); !errors.Is(err, dbtest.ErrInjectedFailure) {
		t.Fatalf("write failure: %v", err)
	}
	// sql-guarantee: The failed write must leave no row of any attribution, which no rollup can prove.
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM token_usage`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("failed write changed ledger: %d %v", count, err)
	}
}
