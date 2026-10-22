package scm

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/scm/internal/scmsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func replaceStoredMerge(t *testing.T, db *database.DB, runID ids.ID, change func(*intent)) {
	t.Helper()
	prior, err := readIntent(context.Background(), db, runID)
	if err != nil {
		t.Fatal(err)
	}
	next := prior
	change(&next)
	rows, err := scmsql.New(db.SQL()).ReplaceMerge(context.Background(), scmsql.ReplaceMergeParams{
		RunID: runID, State: next.State.String(), Operation: next.Operation, Expected: next.Expected, Result: next.Result,
		PreviousState: prior.State.String(), PreviousOperation: prior.Operation, PreviousExpected: prior.Expected, PreviousResult: prior.Result,
	})
	if err != nil || rows != 1 {
		t.Fatalf("replace stored merge: rows=%d err=%v", rows, err)
	}
}

func replaceStoredMergeValues(t *testing.T, db *database.DB, runID ids.ID, state, operation, expected, result string) {
	t.Helper()
	prior, err := readIntent(context.Background(), db, runID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := scmsql.New(db.SQL()).ReplaceMerge(context.Background(), scmsql.ReplaceMergeParams{
		RunID: runID, State: state, Operation: operation, Expected: expected, Result: result,
		PreviousState: prior.State.String(), PreviousOperation: prior.Operation, PreviousExpected: prior.Expected, PreviousResult: prior.Result,
	})
	if err != nil || rows != 1 {
		t.Fatalf("replace stored merge values: rows=%d err=%v", rows, err)
	}
}

func pendingMergeFixture(t *testing.T) (*preparedMerge, mergeStore) {
	t.Helper()
	store, claim := mergeFixture(t)
	prepared, err := store.prepare(context.Background(), claim, NewEffect)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := prepared.begin(context.Background(), "operation", "original-head")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.intent.State != Idle {
		t.Fatal("admission mutated detached input")
	}
	return pending, store
}

func TestMergeSettlementRollsBackEveryRequiredWrite(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"read", "state", "source", "receipt", "main", "suppressed state", "suppressed source", "suppressed receipt", "suppressed main", "commit"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			p, store := pendingMergeFixture(t)
			switch failure {
			case "read":
				dbtest.FailNextStatement(t, store.db, "-- name: MergeByRun :one")
			case "state":
				dbtest.FailNextStatement(t, store.db, "UPDATE run_merges SET state")
			case "source":
				dbtest.FailNextStatement(t, store.db, "UPDATE changes SET landed_sha")
			case "receipt":
				dbtest.FailNextStatement(t, store.db, "INSERT INTO run_events")
			case "main":
				dbtest.FailNextStatement(t, store.db, "INSERT INTO project_main")
			default:
				table, event, body := "run_merges", "UPDATE", "RETURN NULL;"
				if failure == "suppressed source" {
					table = "changes"
				}
				if failure == "suppressed receipt" {
					table, event = "run_events", "INSERT"
				}
				if failure == "suppressed main" {
					table, event = "project_main", "INSERT"
				}
				trigger := "CREATE TRIGGER veto_settlement BEFORE " + event + " ON " + table
				if failure == "commit" {
					body = "RAISE EXCEPTION 'deferred settlement refusal';"
					trigger = "CREATE CONSTRAINT TRIGGER veto_settlement AFTER UPDATE ON run_merges DEFERRABLE INITIALLY DEFERRED"
				}
				completionExec(t, store.db, `CREATE FUNCTION veto_settlement() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
				completionExec(t, store.db, trigger+` FOR EACH ROW EXECUTE FUNCTION veto_settlement()`)
			}
			if err := p.settle(context.Background(), "result-head"); err == nil {
				t.Fatal("failed settlement returned success")
			}
			saved, err := readIntent(context.Background(), store.db, p.source.RunID)
			if err != nil || saved != p.intent {
				t.Fatalf("operation partially changed: %+v %v", saved, err)
			}
			if sha := fixtureChangeSHA(t, store.db, p.work.(*fixtureClaim).changeID); sha != "" {
				t.Fatalf("failed settlement recorded source %q", sha)
			}
			wantFixtureReceipts(t, store.db, "", 0)
			if got := recordedMain(t, store.db, p.source.ProjectID); got != "" {
				t.Fatalf("failed settlement recorded main %q", got)
			}
		})
	}
}

func TestMergeSettlementRefusesChangedPreparedScope(t *testing.T) {
	t.Parallel()
	for _, mutation := range []struct {
		name string
		run  func(*testing.T, *preparedMerge, mergeStore)
	}{
		{"merge operation", func(t *testing.T, p *preparedMerge, store mergeStore) {
			replaceStoredMerge(t, store.db, p.source.RunID, func(i *intent) { i.Operation = "newer" })
		}},
		{"merge expected head", func(t *testing.T, p *preparedMerge, store mergeStore) {
			replaceStoredMerge(t, store.db, p.source.RunID, func(i *intent) { i.Expected = "different" })
		}},
		{"work revision", func(t *testing.T, _ *preparedMerge, store mergeStore) {
			// sql-guarantee: move the test-only external participant's revision after SCM prepared its guarded claim.
			completionExec(t, store.db, `UPDATE fixture_work SET revision=revision+1`)
		}},
		{"work closure", func(t *testing.T, _ *preparedMerge, store mergeStore) {
			// sql-guarantee: close the test-only external participant after SCM prepared its guarded claim.
			completionExec(t, store.db, `UPDATE fixture_work SET closed=true`)
		}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			t.Parallel()
			p, store := pendingMergeFixture(t)
			mutation.run(t, p, store)
			if err := p.settle(context.Background(), "late-head"); !errors.Is(err, ErrChanged) {
				t.Fatalf("late settlement: %v", err)
			}
			wantFixtureReceipts(t, store.db, "", 0)
			if sha := fixtureChangeSHA(t, store.db, p.work.(*fixtureClaim).changeID); sha != "" {
				t.Fatalf("late settlement recorded source %q", sha)
			}
		})
	}
}

func TestMergeSettlementIsIdempotentButDoesNotReopenTerminalRun(t *testing.T) {
	t.Parallel()
	p, store := pendingMergeFixture(t)
	results := race(8, func(int) int {
		if err := p.settle(context.Background(), "result"); err != nil {
			t.Errorf("same operation settlement: %v", err)
			return 0
		}
		return 1
	})
	if count(results, 1) != 8 {
		t.Fatal("same completed operation was not reusable")
	}
	wantFixtureReceipts(t, store.db, "merge.completed", 1)
	if err := p.settle(context.Background(), "different-result"); err == nil {
		t.Fatal("operation changed its completed result")
	}
	if p.intent.State != Pending || p.intent.Result != "" {
		t.Fatal("settlement mutated the prepared input")
	}
	replaceStoredMerge(t, store.db, p.source.RunID, func(i *intent) { i.Expected = "replaced-base" })
	if err := p.settle(context.Background(), "result"); !errors.Is(err, ErrChanged) {
		t.Fatalf("same result accepted altered intent: %v", err)
	}
	replaceStoredMerge(t, store.db, p.source.RunID, func(i *intent) { i.Expected = p.intent.Expected })
	// sql-guarantee: close the test-only external participant so a fresh SCM admission must refuse terminal work.
	completionExec(t, store.db, `UPDATE fixture_work SET closed=true`)
	current, err := (mergeStore{db: store.db}).prepare(context.Background(), refreshedClaim(t, p), Observe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.begin(context.Background(), "new-operation", "new-head"); !errors.Is(err, errFixtureClosed) {
		t.Fatalf("terminal run admitted new merge: %v", err)
	}
	if _, closed, err := readFixtureWork(context.Background(), store.db.SQL(), p.source.RunID, false); err != nil || !closed {
		t.Fatalf("terminal work reopened: closed=%v %v", closed, err)
	}
}

func TestProvenPendingMergeCanSettleAfterRunEnds(t *testing.T) {
	t.Parallel()
	p, store := pendingMergeFixture(t)
	// sql-guarantee: close the test-only external participant after admission to prove a previously admitted merge may settle.
	completionExec(t, store.db, `UPDATE fixture_work SET closed=true`)
	current, err := (mergeStore{db: store.db}).prepare(context.Background(), refreshedClaim(t, p), Observe)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.settle(context.Background(), "proven-old-result"); err != nil {
		t.Fatal(err)
	}
	if _, closed, err := readFixtureWork(context.Background(), store.db.SQL(), p.source.RunID, false); err != nil || !closed {
		t.Fatalf("settlement reopened ended work: closed=%v %v", closed, err)
	}
	if m, ok := storedMerge(t, store.db, p.source.RunID); !ok || m.Result != "proven-old-result" {
		t.Fatalf("settled merge: %+v %v", m, ok)
	}
	wantFixtureReceipts(t, store.db, "merge.completed", 1)
	wantFixtureReceipts(t, store.db, "run.done", 0)
}

func TestMergeAdmissionAndCancellationAreGuarded(t *testing.T) {
	t.Parallel()
	p, store := pendingMergeFixture(t)
	if _, err := p.begin(context.Background(), "replacement", "another-head"); err == nil {
		t.Fatal("replaced a pending operation")
	}
	dbtest.FailNextStatement(t, store.db, "INSERT INTO run_events")
	if err := p.cancel(context.Background(), "unchanged target"); err == nil {
		t.Fatal("unrecorded cancellation returned success")
	}
	saved, err := readIntent(context.Background(), store.db, p.source.RunID)
	if err != nil || saved != p.intent {
		t.Fatalf("partial cancellation: %+v %v", saved, err)
	}
	if err := p.cancel(context.Background(), "unchanged target"); err != nil {
		t.Fatal(err)
	}
	saved, err = readIntent(context.Background(), store.db, p.source.RunID)
	if err != nil || saved != (intent{State: Idle}) {
		t.Fatalf("cancelled intent: %+v %v", saved, err)
	}
	wantFixtureReceipts(t, store.db, "merge.cancelled", 1)
}

func TestMergeStoreDoesNotHoldAConnectionAcrossItsEffect(t *testing.T) {
	t.Parallel()
	p, store := pendingMergeFixture(t)
	store.db.Pool().SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := readIntent(ctx, store.db, p.source.RunID); err != nil {
		t.Fatal(err)
	}
	if err := p.settle(ctx, "result"); err != nil {
		t.Fatal(err)
	}
}

func TestMergeSettlementCancelsWhileWaitingForAdmission(t *testing.T) {
	t.Parallel()
	p, store := pendingMergeFixture(t)
	lock, err := store.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	// sql-guarantee: holds the project row lock open so settlement waits at admission; no owner operation holds that lock across a test step
	if err := lock.QueryRow(`SELECT id FROM projects WHERE id=? FOR UPDATE`, p.source.ProjectID).Scan(new(ids.ID)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.settle(ctx, "result") }()
	dbtest.WaitForLock(t, store.db, "SELECT id FROM projects")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled settlement: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled settlement kept waiting")
	}
	wantFixtureReceipts(t, store.db, "", 0)
}

func TestMergeIntentRejectsMalformedStoredMetadata(t *testing.T) {
	t.Parallel()
	for _, mutation := range []struct {
		name                     string
		state, operation, expect string
		result                   string
	}{
		{"unknown state", "surprise", "operation", "original-head", ""},
		{"empty state", "", "operation", "original-head", ""},
		{"empty operation", Pending.String(), "", "original-head", ""},
		{"empty expected", Pending.String(), "operation", "", ""},
		{"premature result", Pending.String(), "operation", "original-head", "premature-result"},
		{"recorded without result", Recorded.String(), "operation", "original-head", ""},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			t.Parallel()
			p, store := pendingMergeFixture(t)
			// The schema rejects corruption first; the reader must also fail closed
			// when inspecting a database whose constraint was removed manually.
			if _, err := scmsql.New(store.db.SQL()).ReplaceMerge(context.Background(), scmsql.ReplaceMergeParams{
				RunID: p.source.RunID, State: mutation.state, Operation: mutation.operation, Expected: mutation.expect, Result: mutation.result,
				PreviousState: Pending.String(), PreviousOperation: "operation", PreviousExpected: "original-head", PreviousResult: "",
			}); err == nil {
				t.Fatal("schema accepted malformed merge metadata")
			}
			completionExec(t, store.db, `ALTER TABLE run_merges DROP CONSTRAINT run_merge_tuple`)
			replaceStoredMergeValues(t, store.db, p.source.RunID, mutation.state, mutation.operation, mutation.expect, mutation.result)
			if current, err := (mergeStore{db: store.db}).prepare(context.Background(), refreshedClaim(t, p), Observe); err == nil || current != nil {
				t.Fatalf("invalid metadata admitted: %+v %v", current, err)
			}
		})
	}
}

func TestMergeStateOwnsItsLifecycle(t *testing.T) {
	t.Parallel()
	for state := Idle; state <= Recorded; state++ {
		raw, err := state.Value()
		if err != nil {
			t.Fatal(err)
		}
		var decoded State
		if err := decoded.Scan(raw); err != nil || decoded != state {
			t.Fatalf("state round trip: %v %v", decoded, err)
		}
	}
	if _, err := (State(0)).Value(); err == nil {
		t.Fatal("zero state encoded")
	}
	var invalid State
	if err := invalid.Scan("unknown"); err == nil {
		t.Fatal("unknown state decoded")
	}
	idle := intent{State: Idle}
	if _, err := idle.settle("unadmitted"); err == nil {
		t.Fatal("idle operation settled")
	}
	pending, err := idle.begin("one", "base")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pending.begin("two", "base"); err == nil {
		t.Fatal("pending operation replaced")
	}
	recorded, err := pending.settle("result")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recorded.settle("second-result"); err == nil {
		t.Fatal("recorded operation settled twice")
	}
	if _, err := recorded.begin("one", "result"); err == nil {
		t.Fatal("completed identity reused for another merge")
	}
	if _, err := recorded.begin("two", "result"); err == nil {
		t.Fatal("a recorded merge must be final: a landing merges once")
	}
}

func TestMergeSlotCreationAndDeletionKeepTheirGuards(t *testing.T) {
	t.Parallel()
	store, claim := mergeFixture(t)
	prepared, err := store.prepare(context.Background(), claim, NewEffect)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan *preparedMerge, 8)
	winners := race(8, func(i int) int {
		next, err := prepared.begin(context.Background(), fmt.Sprintf("competing-%d", i), "base")
		if err == nil {
			results <- next
			return 1
		}
		if !errors.Is(err, ErrChanged) {
			t.Errorf("competing admission: %v", err)
		}
		return 0
	})
	if count(winners, 1) != 1 {
		t.Fatalf("expected one admitted operation: %v", winners)
	}
	pending := <-results
	wantStoredMerge(t, store.db, pending.source.RunID, true)
	completionExec(t, store.db, `CREATE FUNCTION suppress_merge_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	completionExec(t, store.db, `CREATE TRIGGER suppress_merge_delete BEFORE DELETE ON run_merges FOR EACH ROW EXECUTE FUNCTION suppress_merge_delete()`)
	if err := pending.cancel(context.Background(), "unchanged target"); !errors.Is(err, ErrChanged) {
		t.Fatalf("suppressed cancellation: %v", err)
	}
	wantStoredMerge(t, store.db, pending.source.RunID, true)
	wantFixtureReceipts(t, store.db, "", 0)
	completionExec(t, store.db, `DROP TRIGGER suppress_merge_delete ON run_merges`)
	if err := pending.cancel(context.Background(), "unchanged target"); err != nil {
		t.Fatal(err)
	}
	wantStoredMerge(t, store.db, pending.source.RunID, false)
	wantFixtureReceipts(t, store.db, "merge.cancelled", 1)
	if err := pending.cancel(context.Background(), "late duplicate"); !errors.Is(err, ErrChanged) {
		t.Fatalf("absent operation was cancelled again: %v", err)
	}
	wantFixtureReceipts(t, store.db, "", 1)
}
