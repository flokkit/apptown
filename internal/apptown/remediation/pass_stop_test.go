package remediation

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// awaitRemediationSignal waits for a worker to reach a boundary. The deadline
// is a hang guard only: under the full gate's load a dispatch through Work's
// transactions took longer than the 2 s it once was (a recurring failure on
// main), while an idle run takes well under a second.
func awaitRemediationSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(30 * time.Second):
		t.Fatal("worker did not reach the expected boundary")
	}
}

// A pass stopped (shutdown, or the queue's budget) while it verifies an
// item stops between operations: the review under way records its answer,
// and no queued work starts after the stop; the next pass reconciles from
// the tables. Nothing resumes the stopped pass.
//
// Kills: a pass that goes on to start queued work after its context ended.
func TestAStoppedPassStartsNoQueuedWork(t *testing.T) {
	t.Parallel()
	f, _, item, _ := outcomeFixture(t)
	f.moveChange(t, item, workflow.ShipChange)
	item = f.outcome(t, item, Outcome{Kind: "landed"})
	queued := f.job(t, 0, f.project(t, "queued"))[0]
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	service := New(f.db, Dependencies{Work: f.work, Caller: f.ctx, Checks: conformance.New(f.db, reconcileActor{}, attemptChecks{}, nil),
		Review: func(context.Context, Item, VerificationScope) Review {
			close(entered)
			<-release
			return Review{State: conformance.Conforms}
		},
		Resolve: func(context.Context, Item, VerificationScope) (StartDefinition, error) {
			t.Error("a stopped pass offered queued work")
			return StartDefinition{}, dbtest.ErrInjectedFailure
		}})
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); service.Pass(ctx) }()
	awaitRemediationSignal(t, entered)
	stop()
	releaseOnce.Do(func() { close(release) })
	awaitRemediationSignal(t, done)
	// The review answered, so its judgement is recorded; the stop prevents
	// only what comes after it.
	if current := f.item(t, item.ID); current.State != ItemDone {
		t.Fatalf("the item under review: %+v", current)
	}
	if current := f.item(t, queued.ID); current.State != ItemQueued || current.Attempt != 0 {
		t.Fatalf("queued work started after the stop: %+v", current)
	}
}

// While the pool cools down after a provider refusal, a pass starts no
// queued work. Kills: the pass's scheduler not asking the cooldown.
func TestACoolingPoolStartsNothing(t *testing.T) {
	t.Parallel()
	f, _, _, _ := outcomeFixture(t)
	queued := f.job(t, 0, f.project(t, "queued"))[0]
	service := New(f.db, Dependencies{Work: f.work, Caller: f.ctx, Checks: conformance.New(f.db, reconcileActor{}, attemptChecks{}, nil),
		Resolve: func(context.Context, Item, VerificationScope) (StartDefinition, error) {
			t.Error("a cooling pool offered queued work")
			return StartDefinition{}, dbtest.ErrInjectedFailure
		}})
	service.cool.hold("the provider refused")
	service.Pass(context.Background())
	if current := f.item(t, queued.ID); current.State != ItemQueued || current.Attempt != 0 {
		t.Fatalf("queued work started while cooling: %+v", current)
	}
}
