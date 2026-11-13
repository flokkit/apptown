package wow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/database/query"
	"github.com/flokkit/apptown/internal/dbtest"
	"testing"
)

// Count real database round trips at the same interface generated code uses.
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

// Selecting a historical version reads that exact publication in one query;
// a missing version must never select the current publication instead.
func TestWorkflowVersionReadsExactPublicationOnce(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	declaration := func(name, description string) string {
		return fmt.Sprintf("apiVersion: apptown.dev/v1\nkind: Workflow\nmetadata: {name: %s, description: %s}\nspec: {kind: pipeline, stages: [{name: work, steps: [{name: work, uses: script, with: {run: 'true'}}]}]}\n", name, description)
	}
	library := NewWorkflows(db)
	for i := range 12 {
		name := fmt.Sprintf("wf-%02d", i)
		for _, description := range []string{"original", "current"} {
			if _, _, err := library.Put(t.Context(), declaration(name, description), "alice"); err != nil {
				t.Fatal(err)
			}
		}
	}
	counted := &countedQueries{Handle: db.SQL()}
	for _, version := range []int{0, 1, 2} {
		counted.reads = 0
		gotVersion, yaml, err := workflowVersionIn(t.Context(), counted, "wf-00", version)
		wantVersion, wantYAML := 2, declaration("wf-00", "current")
		if version == 1 {
			wantVersion, wantYAML = 1, declaration("wf-00", "original")
		}
		if err != nil || counted.reads != 1 || gotVersion != wantVersion || yaml != wantYAML {
			t.Fatalf("version %d: got %d %q in %d reads: %v", version, gotVersion, yaml, counted.reads, err)
		}
	}
	if _, _, err := workflowVersionIn(t.Context(), db, "wf-00", 99); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing historical version silently fell back: %v", err)
	}
}
