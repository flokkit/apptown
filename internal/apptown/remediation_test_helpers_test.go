package apptown

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/remediation"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// Remediation is built once, so a test that needs a different port rebuilds
// the whole service from the App's own dependencies with that port replaced —
// the same seam rebuildWork gives Work. Overrides accumulate, so a test may
// stub the resolver and the review one after the other.
var fixesOverrides sync.Map // *App -> []func(*remediation.Dependencies)

func rebuildFixes(t testing.TB, a *App, apply ...func(*remediation.Dependencies)) *remediation.Service {
	t.Helper()
	var overrides []func(*remediation.Dependencies)
	if previous, ok := fixesOverrides.Load(a); ok {
		overrides = previous.([]func(*remediation.Dependencies))
	}
	overrides = append(overrides, apply...)
	fixesOverrides.Store(a, overrides)
	deps := a.remediationDependencies()
	for _, override := range overrides {
		override(&deps)
	}
	a.Fixes = remediation.New(a.DB, deps)
	return a.Fixes
}

// fakeWorkDispatch decorates the direct Work port with dispatch replaced, so a
// test can see what was started without launching a real agent.
type fakeWorkDispatch struct {
	remediation.Work
	dispatch func(runID ids.ID) error
}

func (f fakeWorkDispatch) DispatchStarted(runID ids.ID) error { return f.dispatch(runID) }

// remediationAttemptsFor is the App's admission store, named as remediation.
func remediationAttemptsFor(a *App) remediation.Attempts {
	return remediation.NewAttempts(a.DB, a.Work, a.asAppTown(context.Background(), ids.Nil, "remediation"), nil)
}

func remediationReconciliationFor(a *App) remediation.Reconciliation {
	return remediation.NewReconciliation(a.DB, a.Work, a.asAppTown(context.Background(), ids.Nil, "remediation"), nil)
}

// awaitRemediationSignal waits for a worker boundary the test expects.
func awaitRemediationSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not reach the expected boundary")
	}
}

// Independent snapshot for the remaining cross-domain transaction tests.
func outcomeSnapshot(t *testing.T, db *database.DB) string {
	t.Helper()
	var snapshot string
	// sql-guarantee: a failed transaction must leave every remediation row, attempt, job and receipt byte-identical; only the whole tables, read independently of their writers, show that.
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT jsonb_build_object(
		'items',(SELECT jsonb_agg(i ORDER BY id) FROM remediation_items i),
		'attempts',(SELECT jsonb_agg(a ORDER BY id) FROM remediation_attempts a),
		'jobs',(SELECT jsonb_agg(j ORDER BY id) FROM remediations j),
		'audit',(SELECT jsonb_agg(a ORDER BY id) FROM audit_log a))::text`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
