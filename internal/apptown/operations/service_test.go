package operations_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/poll"
)

var databaseSuite = dbtest.NewSuite(func(_ context.Context, url string) (*database.DB, error) {
	db, err := dbschema.Open(context.Background(), url)
	if err != nil {
		return nil, err
	}
	if err := jobs.Migrate(context.Background(), db.Pool()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
})

func TestMain(m *testing.M) { os.Exit(databaseSuite.Run(m)) }

// authority is the caller's grants and its credentials' standing: every
// capability is allowed unless denied, and a credential counts until it is
// revoked. It answers both the caller check and the check before effects.
type authority struct {
	mu      sync.Mutex
	deny    map[access.Capability]bool
	revoked map[string]bool
	asked   int
}

func newAuthority() *authority {
	return &authority{deny: map[access.Capability]bool{}, revoked: map[string]bool{}}
}

func (a *authority) Require(_ context.Context, p access.Permission) (access.Caller, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deny[p.Capability] {
		return access.Caller{}, &access.Denied{Code: "test", Reason: "denied " + string(p.Capability)}
	}
	return access.Caller{Principal: alice, Verified: true}, nil
}

func (a *authority) RequireStill(ctx context.Context, captured access.Principal, p access.Permission) error {
	a.mu.Lock()
	a.asked++
	gone := a.revoked[captured.Credential]
	a.mu.Unlock()
	if gone {
		return errors.New("the credential " + captured.Credential + " was revoked")
	}
	_, err := a.Require(ctx, p)
	return err
}

var alice = access.Principal{Kind: "user", Name: "alice", Via: "token", Credential: "token:alice-cli"}

// world is a ledger with one kind whose body the test chooses.
type world struct {
	t      *testing.T
	db     *database.DB
	ops    *operations.Service
	auth   *authority
	ran    atomic.Int32
	body   func(context.Context, operations.Operation, *operations.StepLog) operations.Outcome
	settle func(context.Context, *database.Tx, operations.Operation, operations.Outcome) error
	// interrupted is the kind's words for what a stop of AppTown left.
	interrupted func(operations.Operation) string
	// at is the ledger's clock, in Unix seconds; zero reads the real clock.
	at atomic.Int64
}

const kind = "test.effect"

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, db: databaseSuite.New(t), auth: newAuthority()}
	w.ops = operations.New(w.db, w.auth, w.auth, func() time.Time {
		if at := w.at.Load(); at != 0 {
			return time.Unix(at, 0)
		}
		return time.Now()
	})
	w.body = func(context.Context, operations.Operation, *operations.StepLog) operations.Outcome {
		return operations.Outcome{State: operations.Succeeded, Verification: operations.Verified, Detail: "done"}
	}
	w.ops.Register(operations.Kind{Name: kind, Capability: access.CapDeploymentTeardown, Cancellable: true,
		Execute: func(ctx context.Context, op operations.Operation, log *operations.StepLog) operations.Outcome {
			w.ran.Add(1)
			return w.body(ctx, op, log)
		},
		SettleIn: func(ctx context.Context, tx *database.Tx, op operations.Operation, out operations.Outcome) error {
			if w.settle != nil {
				return w.settle(ctx, tx, op, out)
			}
			return nil
		},
		Interrupted: func(op operations.Operation) string {
			if w.interrupted != nil {
				return w.interrupted(op)
			}
			return ""
		}})
	return w
}

func (w *world) admit(subject string, holds ...string) (*operations.Operation, error) {
	return database.Transact(context.Background(), w.db, func(tx *database.Tx) (*operations.Operation, error) {
		return w.ops.AdmitIn(context.Background(), tx, operations.Spec{Kind: kind, ProjectID: idstest.N(7), Subject: subject, Holds: holds, Authority: alice, Input: map[string]any{"what": "x"}})
	})
}

func (w *world) mustAdmit(subject string, holds ...string) *operations.Operation {
	w.t.Helper()
	op, err := w.admit(subject, holds...)
	if err != nil {
		w.t.Fatal(err)
	}
	return op
}

func (w *world) read(id ids.ID) *operations.Operation {
	w.t.Helper()
	op, err := w.ops.ByID(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	return op
}

func (w *world) waitingJobs() []string {
	w.t.Helper()
	keys, err := database.Transact(context.Background(), w.db, func(tx *database.Tx) ([]string, error) {
		return jobs.UnfinishedKeysIn(context.Background(), tx.Native(), operations.Job)
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return keys
}

func (w *world) restart() operations.RestartRepair {
	w.t.Helper()
	r, err := database.Transact(context.Background(), w.db, func(tx *database.Tx) (operations.RestartRepair, error) {
		return w.ops.RepairInterruptedIn(context.Background(), tx)
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

// An admission is the row, its job and its receipt together, or nothing:
// a failure at any of them leaves no operation and no job.
//
// Kills: the job inserted outside the row's transaction; the receipt
// written after commit.
func TestAnAdmissionThatFailsLeavesNeitherRowNorJob(t *testing.T) {
	t.Parallel()
	// The job is inserted between the row and the job's id on the row, so a
	// failure at either of the later statements must take the job with it.
	for _, statement := range []string{"UPDATE operations SET job_id", "INSERT INTO audit_log"} {
		t.Run(statement, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			t.Cleanup(w.db.FailNextStatement(statement))
			if _, err := w.admit("project:7/deployment:1"); err == nil {
				t.Fatal("an admission whose " + statement + " failed was admitted")
			}
			if keys := w.waitingJobs(); len(keys) != 0 {
				t.Fatalf("jobs left by a failed admission: %v", keys)
			}
			if holder, err := w.holder("project:7/deployment:1"); err != nil || holder != nil {
				t.Fatalf("a failed admission holds its subject: %+v %v", holder, err)
			}
			op := w.mustAdmit("project:7/deployment:1")
			if op.ID.IsZero() || op.State != operations.Queued || len(w.waitingJobs()) != 1 {
				t.Fatalf("the next admission: %+v, jobs %v", op, w.waitingJobs())
			}
		})
	}
}

func (w *world) holder(paths ...string) (*operations.Operation, error) {
	return database.Transact(context.Background(), w.db, func(tx *database.Tx) (*operations.Operation, error) {
		return operations.ConflictsIn(context.Background(), tx, paths...)
	})
}

// A request with no credential an effect could be checked against later
// is not admitted.
//
// Kills: an operation admitted that no check before an effect can resolve.
func TestAnAdmissionWithoutACredentialIsRefused(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	_, err := database.Transact(context.Background(), w.db, func(tx *database.Tx) (*operations.Operation, error) {
		return w.ops.AdmitIn(context.Background(), tx, operations.Spec{Kind: kind, ProjectID: idstest.N(7), Subject: "project:7/deployment:1",
			Authority: access.Principal{Kind: "instrument", Name: "run/4", Instrument: "run"}})
	})
	if fault.KindOf(err) != fault.Forbidden {
		t.Fatalf("an admission without a credential: %v", err)
	}
}

// Holds meet by whole path segments: a hold conflicts with the same path,
// an ancestor or a descendant, never with a sibling or a path that only
// shares a prefix of characters. A conflict is a 409 naming the holder;
// an ended operation holds nothing.
//
// Kills: string-prefix matching (project:7 meeting project:70); a data
// hold blocking a release path; holds outliving their operation.
func TestHoldsMeetByPathSegments(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	data := w.mustAdmit("project:7/deployment:1", "project:7/deployment:1/data")
	for _, c := range []struct {
		path string
		held bool
	}{
		{"project:7/deployment:1/data", true},
		{"project:7/deployment:1", true},
		{"project:7", true},
		{"project:7/deployment:1/data/x", true},
		{"project:7/deployment:1/release", false},
		{"project:70", false},
		{"project:7/deployment:10", false},
	} {
		holder, err := w.holder(c.path)
		if err != nil || (holder != nil) != c.held || (holder != nil && holder.ID != data.ID) {
			t.Errorf("%s: holder %+v %v, want held %v", c.path, holder, err, c.held)
		}
	}
	_, err := w.admit("project:7/deployment:1")
	var held *operations.HeldError
	if !errors.As(err, &held) || held.Holder.ID != data.ID || fault.KindOf(err) != fault.Conflict || !strings.Contains(err.Error(), operations.Path(data.ID)) {
		t.Fatalf("a take-down beside a copy: %v", err)
	}
	if _, err := w.admit("project:70/deployment:1"); err != nil {
		t.Fatalf("another project's deployment: %v", err)
	}
	// A whole-deployment hold meets neither a deployment whose id only
	// starts with its id, nor a project whose id only starts with its.
	whole := w.mustAdmit("project:8/deployment:1")
	for _, path := range []string{"project:8/deployment:10", "project:8/deployment:1x/data", "project:80"} {
		if holder, err := w.holder(path); err != nil || holder != nil {
			t.Errorf("%s is held by %+v (%v), whose hold is %v", path, holder, err, whole.Holds)
		}
	}
	if _, err := w.ops.Run(context.Background(), data.ID); err != nil {
		t.Fatal(err)
	}
	if holder, err := w.holder("project:7/deployment:1"); err != nil || holder != nil {
		t.Fatalf("an ended operation still holds: %+v %v", holder, err)
	}
}

// A job delivered twice, or delivered while the operation already runs,
// executes the operation once; an ended one is never run again.
//
// Kills: executing without claiming queued -> running first.
func TestADuplicateDeliveryExecutesOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	op := w.mustAdmit("project:7/deployment:1")
	release := make(chan struct{})
	w.body = func(context.Context, operations.Operation, *operations.StepLog) operations.Outcome {
		<-release
		return operations.Outcome{State: operations.Succeeded}
	}
	key := op.ID.String()
	first := make(chan map[string]any, 1)
	go func() { r, _ := w.ops.JobHandler(context.Background(), key, nil); first <- r }()
	if !poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, func() bool { return w.read(op.ID).State == operations.Running }) {
		t.Fatal("the first delivery did not claim the operation")
	}
	if r, err := w.ops.JobHandler(context.Background(), key, nil); err != nil || r["state"] != "running" {
		t.Fatalf("the second delivery: %v %v", r, err)
	}
	close(release)
	if r := <-first; r["state"] != "succeeded" {
		t.Fatalf("the first delivery: %v", r)
	}
	if r, err := w.ops.JobHandler(context.Background(), key, nil); err != nil || r["state"] != "succeeded" {
		t.Fatalf("a delivery after the end: %v %v", r, err)
	}
	if n := w.ran.Load(); n != 1 {
		t.Fatalf("executed %d times", n)
	}
}

// A restart settles a running operation unknown, with the steps it
// confirmed, the step it was in marked not known, its kind's records
// settled and its holds released, and the kind's words for what the stop
// left added to its detail; a queued one whose job is gone gets a job, one
// whose job waits is left to it; an ended one is untouched. The worker that
// was running it can no longer settle it.
//
// Kills: resuming or rerunning an interrupted operation; leaving it
// running (holds forever); losing a queued operation with its job; a
// repair that skips the kind's records or its words for what was left.
func TestARestartSettlesRunningUnknownAndRequeuesQueued(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	inStep, release := make(chan struct{}), make(chan struct{})
	w.body = func(_ context.Context, _ operations.Operation, log *operations.StepLog) operations.Outcome {
		log.Begin(ctx, "deleting the app")
		log.Begin(ctx, "copying postgres")
		if err := log.Record(ctx, "left", "apptown-env-a"); err != nil {
			t.Error(err)
		}
		close(inStep)
		<-release
		return operations.Outcome{State: operations.Succeeded}
	}
	var settled []operations.Outcome
	w.settle = func(_ context.Context, _ *database.Tx, _ operations.Operation, out operations.Outcome) error {
		settled = append(settled, out)
		return nil
	}
	running := w.mustAdmit("project:7/deployment:1")
	finished := make(chan error, 1)
	go func() { _, err := w.ops.Run(ctx, running.ID); finished <- err }()
	<-inStep
	waiting := w.mustAdmit("project:7/deployment:2")
	jobless := w.mustAdmit("project:7/deployment:3")
	ended := w.mustAdmit("project:7/deployment:4")
	w.body = func(context.Context, operations.Operation, *operations.StepLog) operations.Outcome {
		return operations.Outcome{State: operations.Failed, Detail: "known failure"}
	}
	if _, err := w.ops.Run(ctx, ended.ID); err != nil {
		t.Fatal(err)
	}
	// The old daemon's job for jobless went with it.
	if err := w.db.InTx(ctx, func(tx *database.Tx) error {
		// What a crash leaves behind: no owner operation loses a job.
		// sql-guarantee: the job row is gone as after a crash; no operation removes one.
		_, err := tx.Exec(`DELETE FROM river_job WHERE args->>'key' = ?`, jobless.ID.String())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	settled = nil
	// The kind says, from what the operation recorded, what the stop left.
	w.interrupted = func(op operations.Operation) string {
		left, _ := op.Result["left"].(string)
		return "It left " + left + " stopped."
	}

	r := w.restart()
	if r.Settled != 1 || r.Requeued != 1 {
		t.Fatalf("repair: %+v", r)
	}
	got := w.read(running.ID)
	if got.State != operations.Unknown || !strings.Contains(got.Detail, "Confirmed: deleting the app.") || !strings.Contains(got.Detail, "It was in: copying postgres.") || !strings.HasSuffix(got.Detail, "Look before asking again. It left apptown-env-a stopped.") {
		t.Fatalf("the interrupted operation: %s %q", got.State, got.Detail)
	}
	if last := got.Steps[len(got.Steps)-1]; !last.Unknown || last.Ended == nil {
		t.Fatalf("the step it was in: %+v", last)
	}
	if len(settled) != 1 || settled[0].State != operations.Unknown {
		t.Fatalf("the kind's records were settled with %+v", settled)
	}
	if holder, err := w.holder("project:7/deployment:1"); err != nil || holder != nil {
		t.Fatalf("an interrupted operation still holds: %+v %v", holder, err)
	}
	// (This test runs operations without River, so the jobs of the ones it
	// ran still wait; River's own restart repair settles those.)
	keys := map[string]int{}
	for _, k := range w.waitingJobs() {
		keys[k]++
	}
	if keys[waiting.ID.String()] != 1 || keys[jobless.ID.String()] != 1 {
		t.Fatalf("jobs waiting after the restart: %v", keys)
	}
	if e := w.read(ended.ID); e.State != operations.Failed || e.Detail != "known failure" {
		t.Fatalf("an ended operation was touched: %+v", e)
	}
	close(release)
	if err := <-finished; err == nil {
		t.Fatal("the old worker settled an operation the restart had settled")
	}
	if again := w.read(running.ID); again.State != operations.Unknown {
		t.Fatalf("the old worker's end overwrote the restart's: %s", again.State)
	}
	if n, _ := w.ops.Run(ctx, running.ID); n.State != operations.Unknown || w.ran.Load() != 2 {
		t.Fatalf("an interrupted operation was run again: %s, ran %d", n.State, w.ran.Load())
	}
}

// A panic, an end the kind did not classify and a stop of AppTown are
// unknown, never success or a plain failure.
//
// Kills: a panic leaving the operation running; a zero outcome read as
// success; a cancelled context's "nothing was changed" read as failed.
func TestOutcomesThatCannotBeEstablishedAreUnknown(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		body func(context.Context, operations.Operation, *operations.StepLog) operations.Outcome
		stop bool
		says string
	}{
		{"panic", func(context.Context, operations.Operation, *operations.StepLog) operations.Outcome {
			panic("provider client")
		}, false, "internal error"},
		{"unclassified", func(context.Context, operations.Operation, *operations.StepLog) operations.Outcome {
			return operations.Outcome{}
		}, false, "without saying how"},
		{"stopped", func(ctx context.Context, _ operations.Operation, log *operations.StepLog) operations.Outcome {
			log.Begin(ctx, "deleting the cluster")
			<-ctx.Done()
			return operations.Outcome{State: operations.Failed, Detail: "the provider call was cut off"}
		}, true, "was stopped"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			w.body = c.body
			op := w.mustAdmit("project:7/deployment:1")
			ctx, cancel := context.WithCancel(context.Background())
			if c.stop {
				// Stopped once the body is under way, not after a guessed
				// delay that a loaded machine can outrun before admission.
				began := make(chan struct{})
				w.body = func(ctx context.Context, op operations.Operation, log *operations.StepLog) operations.Outcome {
					close(began)
					return c.body(ctx, op, log)
				}
				go func() { <-began; cancel() }()
			}
			_, err := w.ops.Run(ctx, op.ID)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			if got := w.read(op.ID); got.State != operations.Unknown || !strings.Contains(got.Detail, c.says) {
				t.Fatalf("%s: %s %q", c.name, got.State, got.Detail)
			}
		})
	}
}

// Before each effect the operation asks whether the credential that
// admitted it still stands and its grants still allow the kind: revoking
// that credential, or the grant, refuses the next effect although the
// operation was admitted; an unchanged one is allowed.
//
// Kills: checking authority only at admission; a check that resolves the
// asker's name instead of the credential that asked.
func TestAnEffectAsksTheAdmittingCredentialAgain(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		withdraw func(*authority)
		allowed  bool
	}{
		{"unchanged", func(*authority) {}, true},
		{"credential revoked", func(a *authority) { a.revoked[alice.Credential] = true }, false},
		{"another credential revoked", func(a *authority) { a.revoked["token:alice-other"] = true }, true},
		{"grant withdrawn", func(a *authority) { a.deny[access.CapDeploymentTeardown] = true }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			var effects int
			w.body = func(ctx context.Context, _ operations.Operation, log *operations.StepLog) operations.Outcome {
				if err := log.StillAllowed(ctx); err != nil {
					return operations.Outcome{State: operations.Failed, Detail: err.Error()}
				}
				effects++
				return operations.Outcome{State: operations.Succeeded}
			}
			op := w.mustAdmit("project:7/deployment:1")
			c.withdraw(w.auth)
			if _, err := w.ops.Run(context.Background(), op.ID); err != nil {
				t.Fatal(err)
			}
			got := w.read(op.ID)
			if c.allowed != (effects == 1) || c.allowed != (got.State == operations.Succeeded) {
				t.Fatalf("%s: %d effects, %s %q", c.name, effects, got.State, got.Detail)
			}
			if !c.allowed && !strings.Contains(got.Detail, "authority withdrawn") {
				t.Fatalf("the refusal does not say why: %q", got.Detail)
			}
		})
	}
}

// A settlement whose receipt cannot be written fails the transition: the
// operation stays running with its holds until a restart settles it.
//
// Kills: holds released (or the state ended) without the ended receipt.
func TestASettlementWithoutItsReceiptKeepsTheHolds(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	op := w.mustAdmit("project:7/deployment:1")
	w.body = func(context.Context, operations.Operation, *operations.StepLog) operations.Outcome {
		w.t.Cleanup(w.db.FailNextStatement("INSERT INTO audit_log"))
		return operations.Outcome{State: operations.Succeeded}
	}
	if _, err := w.ops.Run(context.Background(), op.ID); err == nil {
		t.Fatal("a settlement without its receipt succeeded")
	}
	if got := w.read(op.ID); got.State != operations.Running {
		t.Fatalf("the operation without its receipt reads %s", got.State)
	}
	if holder, err := w.holder("project:7/deployment:1"); err != nil || holder == nil || holder.ID != op.ID {
		t.Fatalf("its holds went without the receipt: %+v %v", holder, err)
	}
	if r := w.restart(); r.Settled != 1 || w.read(op.ID).State != operations.Unknown {
		t.Fatalf("the restart did not settle it: %+v", r)
	}
}

// Cancel needs the kind's capability. A queued operation has done nothing,
// so it is cancelled at once, settled like any other end (the kind's
// records and the operation.ended receipt in the same transaction, whose
// failure leaves it queued and holding), and its job then runs nothing.
// A running one records the request for its kind to honour between safe
// steps; a running operation of a kind that is not stopped part way
// refuses with its own reason, though a queued one of that kind cancels.
//
// Kills: a cancel without authority; a queued cancel that skips the
// kind's records or the ended receipt; a cancelled queued operation run by
// its job; a cancel request a running kind cannot see.
func TestCancelStopsQueuedAtOnceAndAsksRunningToStop(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	w.auth.deny[access.CapDeploymentTeardown] = true
	queued := w.mustAdmit("project:7/deployment:1")
	if _, err := w.ops.Cancel(ctx, queued.ID); fault.KindOf(err) != fault.Forbidden && !errors.As(err, new(*access.Denied)) {
		t.Fatalf("a cancel without the kind's capability: %v", err)
	}
	w.auth.deny = map[access.Capability]bool{}
	var settled []operations.Outcome
	w.settle = func(_ context.Context, _ *database.Tx, _ operations.Operation, out operations.Outcome) error {
		settled = append(settled, out)
		return nil
	}
	restore := w.db.FailNextStatement("INSERT INTO audit_log")
	if _, err := w.ops.Cancel(ctx, queued.ID); err == nil {
		t.Fatal("a queued cancel without its ended receipt succeeded")
	}
	restore()
	if got := w.read(queued.ID); got.State != operations.Queued {
		t.Fatalf("a queued cancel without its receipt left it %s", got.State)
	}
	if holder, err := w.holder("project:7/deployment:1"); err != nil || holder == nil || holder.ID != queued.ID {
		t.Fatalf("its hold went without the receipt: %+v %v", holder, err)
	}
	settled = nil
	got, err := w.ops.Cancel(ctx, queued.ID)
	if err != nil || got.State != operations.Cancelled {
		t.Fatalf("cancelling a queued operation: %+v %v", got, err)
	}
	if len(settled) != 1 || settled[0].State != operations.Cancelled {
		t.Fatalf("the kind's records were settled with %+v", settled)
	}
	ended, err := audit.Recent(ctx, w.db, audit.Filter{KindPrefix: "operation.ended", ProjectID: idstest.N(7), Limit: audit.MaxLimit})
	if err != nil || len(ended) != 1 || !strings.Contains(string(ended[0].Payload), `"state":"cancelled"`) {
		t.Fatalf("the ended receipt: %+v %v", ended, err)
	}
	if _, err := w.ops.Run(ctx, queued.ID); err != nil || w.ran.Load() != 0 {
		t.Fatalf("a cancelled operation ran: %v, %d", err, w.ran.Load())
	}
	asked := make(chan bool, 1)
	inStep, cancelled := make(chan struct{}), make(chan struct{})
	w.body = func(_ context.Context, _ operations.Operation, log *operations.StepLog) operations.Outcome {
		close(inStep)
		<-cancelled
		asked <- log.CancelRequested(ctx)
		return operations.Outcome{State: operations.Cancelled, Detail: "stopped between items"}
	}
	running := w.mustAdmit("project:7/deployment:2")
	done := make(chan struct{})
	go func() { _, _ = w.ops.Run(ctx, running.ID); close(done) }()
	<-inStep
	if got, err := w.ops.Cancel(ctx, running.ID); err != nil || !got.CancelRequested || got.State != operations.Running {
		t.Fatalf("cancelling a running operation: %+v %v", got, err)
	}
	close(cancelled)
	<-done
	if !<-asked || w.read(running.ID).State != operations.Cancelled {
		t.Fatal("the running operation did not see the request")
	}
	refusal := errors.New("not stopped part way")
	w.ops.Register(operations.Kind{Name: "test.whole", Capability: access.CapDeploymentRestore, CancelRefusal: refusal,
		Execute: func(context.Context, operations.Operation, *operations.StepLog) operations.Outcome {
			return operations.Outcome{State: operations.Succeeded}
		}})
	whole, err := database.Transact(ctx, w.db, func(tx *database.Tx) (*operations.Operation, error) {
		return w.ops.AdmitIn(ctx, tx, operations.Spec{Kind: "test.whole", ProjectID: idstest.N(7), Subject: "project:7/deployment:3", Authority: alice})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := w.ops.Cancel(ctx, whole.ID); err != nil || got.State != operations.Cancelled {
		t.Fatalf("cancelling a queued operation of a kind not stopped part way: %+v %v", got, err)
	}
	whole, err = database.Transact(ctx, w.db, func(tx *database.Tx) (*operations.Operation, error) {
		return w.ops.AdmitIn(ctx, tx, operations.Spec{Kind: "test.whole", ProjectID: idstest.N(7), Subject: "project:7/deployment:3", Authority: alice})
	})
	if err != nil {
		t.Fatal(err)
	}
	// Claimed by a worker that is still running it.
	release := make(chan struct{})
	w.ops.Register(operations.Kind{Name: "test.whole-running", Capability: access.CapDeploymentRestore, CancelRefusal: refusal,
		Execute: func(context.Context, operations.Operation, *operations.StepLog) operations.Outcome {
			<-release
			return operations.Outcome{State: operations.Succeeded}
		}})
	runningWhole, err := database.Transact(ctx, w.db, func(tx *database.Tx) (*operations.Operation, error) {
		return w.ops.AdmitIn(ctx, tx, operations.Spec{Kind: "test.whole-running", ProjectID: idstest.N(7), Subject: "project:7/deployment:4", Authority: alice})
	})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	go func() { _, _ = w.ops.Run(ctx, runningWhole.ID); close(finished) }()
	if !poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, func() bool { return w.read(runningWhole.ID).State == operations.Running }) {
		t.Fatal("not claimed")
	}
	if _, err := w.ops.Cancel(ctx, runningWhole.ID); !errors.Is(err, refusal) {
		t.Fatalf("cancelling a running operation of a kind not stopped part way: %v", err)
	}
	close(release)
	<-finished
	_ = whole
}

// An operation's read needs its kind's read capability in its project; one
// the caller may not read is missing, not forbidden.
//
// Kills: reads that skip the owner's check; a 403 that confirms the id.
func TestAnOperationIsReadOnlyByThoseWhoMayReadIt(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	op := w.mustAdmit("project:7/deployment:1")
	if got, err := w.ops.Get(context.Background(), op.ID); err != nil || got.ID != op.ID {
		t.Fatalf("a reader: %+v %v", got, err)
	}
	w.auth.deny[access.CapProjectRead] = true
	if _, err := w.ops.Get(context.Background(), op.ID); !errors.Is(err, operations.ErrMissing) {
		t.Fatalf("a caller who may not read it: %v", err)
	}
}

// An operation runs on its own queue with a two-hour budget, so one longer
// than the ordinary twenty-minute ceiling is not cut off at twenty
// minutes; a stop of AppTown still ends in seconds, and the operation
// cut off reads unknown. The ceilings are checked when the queue is built.
//
// Kills: operations on the default queue's twenty-minute budget; a stop
// that waits for a long operation; a cut-off operation read as failed.
func TestAnOperationOutlivesTheOrdinaryBudgetAndShutdownStaysBounded(t *testing.T) {
	t.Parallel()
	run := func(context.Context, string, map[string]any) (map[string]any, error) { return nil, nil }
	w := newWorld(t)
	for _, bad := range []jobs.Kind{
		{Run: run, MaxAttempts: 1, Timeout: 21 * time.Minute},
		{Run: run, MaxAttempts: 1, Timeout: jobs.OperationsTimeout + time.Minute, Queue: jobs.OperationsQueue},
		{Run: run, MaxAttempts: 1, Queue: "elsewhere"},
	} {
		if _, err := jobs.New(jobs.Options{}, w.db.Pool(), map[string]jobs.Kind{"k": bad}, nil); err == nil {
			t.Errorf("a queue with %+v was built", bad)
		}
	}
	if _, err := jobs.New(jobs.Options{}, w.db.Pool(), map[string]jobs.Kind{"k": {Run: run, MaxAttempts: 1, Timeout: 20 * time.Minute}}, nil); err != nil {
		t.Fatalf("an ordinary kind at the ceiling: %v", err)
	}
	started := make(chan time.Duration, 1)
	w.body = func(ctx context.Context, _ operations.Operation, log *operations.StepLog) operations.Outcome {
		deadline, _ := ctx.Deadline()
		log.Begin(ctx, "copying a large database")
		started <- time.Until(deadline)
		<-ctx.Done()
		return operations.Outcome{State: operations.Failed, Detail: "cut off"}
	}
	q, err := jobs.New(jobs.Options{}, w.db.Pool(), map[string]jobs.Kind{operations.Job: {Run: w.ops.JobHandler, MaxAttempts: 1, Timeout: jobs.OperationsTimeout, Queue: jobs.OperationsQueue}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	op := w.mustAdmit("project:7/deployment:1")
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	finished := make(chan error, 1)
	go func() { finished <- q.Run(ctx) }()
	select {
	case budget := <-started:
		if budget < 2*time.Hour-time.Minute {
			t.Fatalf("an operation's budget is %s", budget)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the operations queue did not run the operation")
	}
	stopped := time.Now()
	stop()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the queue did not stop")
	}
	if took := time.Since(stopped); took > 10*time.Second {
		t.Fatalf("stopping took %s", took)
	}
	if got := w.read(op.ID); got.State != operations.Unknown {
		t.Fatalf("an operation cut off by a stop reads %s: %s", got.State, got.Detail)
	}
}

// Every stamp an operation carries — its row's, its steps' and its two
// receipts' — is the ledger's clock, however far it is from the machine's.
func TestAnOperationIsStampedByTheLedgersClock(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	admitted := time.Now().Add(-72 * time.Hour).Truncate(time.Second).UTC()
	ran := admitted.Add(time.Hour)
	w.at.Store(admitted.Unix())
	w.body = func(_ context.Context, _ operations.Operation, log *operations.StepLog) operations.Outcome {
		log.Begin(context.Background(), "the one step")
		log.End(context.Background(), "done", 0)
		return operations.Outcome{State: operations.Succeeded, Verification: operations.Verified, Detail: "done"}
	}
	op := w.mustAdmit("project:7/thing")
	w.at.Store(ran.Unix())
	if _, err := w.ops.Run(context.Background(), op.ID); err != nil {
		t.Fatal(err)
	}
	got := w.read(op.ID)
	for name, c := range map[string]struct{ got, want time.Time }{
		"created": {got.CreatedAt.Time, admitted}, "started": {got.StartedAt.Time, ran},
		"touched": {got.TouchedAt.Time, ran}, "finished": {got.FinishedAt.Time, ran},
	} {
		if !c.got.Equal(c.want) {
			t.Errorf("%s at %v, want the ledger's %v", name, c.got, c.want)
		}
	}
	if len(got.Steps) != 1 || !got.Steps[0].Started.Equal(ran) || got.Steps[0].Ended == nil || !got.Steps[0].Ended.Equal(ran) {
		t.Errorf("steps %+v, want started and ended at %v", got.Steps, ran)
	}
	lines, err := audit.Recent(context.Background(), w.db, audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]time.Time{"operation.started": admitted, "operation.ended": ran}
	for _, l := range lines {
		if at, ok := want[l.Kind]; ok {
			if stamped, err := time.Parse(time.RFC3339Nano, l.At); err != nil || !stamped.Equal(at) {
				t.Errorf("%s stamped %s, want %v", l.Kind, l.At, at)
			}
			delete(want, l.Kind)
		}
	}
	if len(want) != 0 {
		t.Errorf("receipts missing: %v", want)
	}
	if err := w.ops.Dismiss(context.Background(), op.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if d := w.read(op.ID).DismissedAt; !d.Time.Equal(ran) {
		t.Errorf("dismissed at %v, want %v", d, ran)
	}
}

// What waits on the asker is an operation that failed or whose outcome is
// not known. A success, verified or not, and a cancellation need nobody, so
// the Home page's "Needs you" never lists them.
func TestOnlyFailedAndUnknownOperationsWaitOnTheAsker(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	outcomes := map[string]operations.Outcome{
		"verified":     {State: operations.Succeeded, Verification: operations.Verified},
		"not-verified": {State: operations.Succeeded, Verification: operations.NotVerified},
		"cancelled":    {State: operations.Cancelled},
		"failed":       {State: operations.Failed, Detail: "it broke"},
		"unknown":      {State: operations.Unknown},
	}
	w.body = func(_ context.Context, op operations.Operation, _ *operations.StepLog) operations.Outcome {
		return outcomes[strings.TrimPrefix(op.Subject, "project:7/")]
	}
	for name := range outcomes {
		op := w.mustAdmit("project:7/" + name)
		if _, err := w.ops.Run(context.Background(), op.ID); err != nil {
			t.Fatal(err)
		}
	}
	ended, err := w.ops.EndedFor(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, op := range ended {
		got = append(got, op.State.String())
	}
	sort.Strings(got)
	if want := []string{"failed", "unknown"}; !slices.Equal(got, want) {
		t.Fatalf("waiting on the asker: %v, want %v", got, want)
	}
}
