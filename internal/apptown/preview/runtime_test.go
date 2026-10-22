package preview

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type facts Observation

func (f facts) ObservePreview(ctx context.Context) Observation {
	return Observation(f)
}

func TestPreviewStatusPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		boot     bool
		finish   bool
		failure  error
		observed facts
		state    State
	}{
		{name: "cold", state: Cold},
		{name: "no contract", observed: facts{NoContract: true}, state: None},
		{name: "starting beats observation", boot: true, observed: facts{URL: "https://old", NoContract: true}, state: Starting},
		{name: "failed", boot: true, finish: true, failure: errors.New("build failed"), state: Broken},
		{name: "missing contract beats failure", boot: true, finish: true, failure: errors.New("build failed"), observed: facts{NoContract: true}, state: None},
		{name: "live beats failure", boot: true, finish: true, failure: errors.New("old failure"), observed: facts{URL: "https://preview"}, state: Live},
		{name: "successful boot still needs observation", boot: true, finish: true, state: Cold},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var runtime Runtime
			key := ChangeKey(idstest.N(7))
			if tc.boot {
				boot, err := runtime.Begin(context.Background(), key)
				if err != nil {
					t.Fatal(err)
				}
				defer boot.Finish(nil)
				if tc.finish {
					boot.Finish(tc.failure)
				}
			}
			got := runtime.Inspect(context.Background(), key, tc.observed)
			if got.State != tc.state || got.At.IsZero() == tc.boot {
				t.Fatalf("status: %+v", got)
			}
			if (got.Reason != "") != (tc.state == Broken) || (got.URL != "") != (tc.state == Live) {
				t.Fatalf("extraneous status fields: %+v", got)
			}
			body, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatal(err)
			}
			if wire["state"] != string(tc.state) || (wire["at"] != nil) != tc.boot {
				t.Fatalf("wire: %s", body)
			}
		})
	}
}

// Done is consulted only when Begin reaches its wait select. This barrier proves
// cancellation happened during contention, without sleeping or inspecting maps.
type previewWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *previewWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func previewReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("preview operation did not complete")
		var zero T
		return zero
	}
}

func TestPreviewWaitCancellationAndOwnership(t *testing.T) {
	t.Parallel()
	var runtime, other Runtime
	key := ChangeKey(idstest.N(42))
	owner, err := runtime.Begin(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Finish(nil)
	starting := runtime.Inspect(context.Background(), key, facts{})
	// Same IDs on another instance, another change, and project-scoped release
	// numbers must not contend with this change or with one another.
	for _, selection := range []struct {
		runtime *Runtime
		key     Key
	}{
		{&other, key}, {&runtime, ChangeKey(idstest.N(43))}, {&runtime, ReleaseKey(idstest.N(1), 42)}, {&runtime, ReleaseKey(idstest.N(2), 42)},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		boot, err := selection.runtime.Begin(ctx, selection.key)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		defer boot.Finish(nil)
	}
	if got := other.Inspect(context.Background(), key, facts{}); got.State != Starting {
		t.Fatalf("other instance: %+v", got)
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &previewWaitContext{Context: base, waiting: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		boot, err := runtime.Begin(ctx, key)
		if boot != nil {
			boot.Finish(nil)
		}
		result <- err
	}()
	previewReceive(t, ctx.waiting)
	cancel()
	if err := previewReceive(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: %v", err)
	}
	if got := runtime.Inspect(base, key, facts{}); got != starting {
		t.Fatalf("waiter changed owner: %+v", got)
	}

	live, cancelLive := context.WithCancel(context.Background())
	defer cancelLive()
	waiting := &previewWaitContext{Context: live, waiting: make(chan struct{})}
	admitted := make(chan *Booting, 1)
	go func() { boot, _ := runtime.Begin(waiting, key); admitted <- boot }()
	previewReceive(t, waiting.waiting)
	owner.Finish(errors.New("first failed"))
	next := previewReceive(t, admitted)
	if next == nil {
		t.Fatal("live waiter was not admitted")
	}
	defer next.Finish(nil)
	owner.Finish(errors.New("stale completion"))
	if got := runtime.Inspect(base, key, facts{}); got.State != Starting || got.Reason != "" {
		t.Fatalf("old owner finished new boot: %+v", got)
	}
	next.Finish(nil)
	if got := runtime.Inspect(base, key, facts{}); got.State != Cold {
		t.Fatalf("finished boot: %+v", got)
	}
}

type blockingPreviewObserver struct{ entered, release chan struct{} }

func (o blockingPreviewObserver) ObservePreview(ctx context.Context) Observation {
	close(o.entered)
	<-o.release
	return Observation{URL: "https://old"}
}

func TestPreviewObservationDoesNotHoldBootLock(t *testing.T) {
	t.Parallel()
	var runtime Runtime
	key := ChangeKey(idstest.N(1))
	observation := blockingPreviewObserver{make(chan struct{}), make(chan struct{})}
	defer close(observation.release)
	viewed := make(chan Status, 1)
	go func() { viewed <- runtime.Inspect(context.Background(), key, observation) }()
	previewReceive(t, observation.entered)
	started := make(chan *Booting, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { boot, _ := runtime.Begin(ctx, key); started <- boot }()
	boot := previewReceive(t, started)
	if boot == nil {
		t.Fatal("observation blocked admission")
	}
	defer boot.Finish(nil)
	// Send a token rather than close so the deferred close remains valid.
	observation.release <- struct{}{}
	if got := previewReceive(t, viewed); got.State != Starting {
		t.Fatalf("stale observation won: %+v", got)
	}
}

func TestPreviewAdmissionRejectsInvalidOrCancelledRequest(t *testing.T) {
	t.Parallel()
	var runtime Runtime
	for _, key := range []Key{{}, ChangeKey(ids.Nil), ReleaseKey(ids.Nil, 1), ReleaseKey(idstest.N(1), -1)} {
		if boot, err := runtime.Begin(context.Background(), key); err == nil || boot != nil {
			t.Fatalf("invalid identity admitted: %+v %v", key, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if boot, err := runtime.Begin(ctx, ChangeKey(idstest.N(1))); !errors.Is(err, context.Canceled) || boot != nil {
		t.Fatalf("cancelled admission: %+v %v", boot, err)
	}
	if got := runtime.Inspect(ctx, ChangeKey(idstest.N(1)), facts{}); !got.At.IsZero() || got.State != Cold {
		t.Fatalf("cancelled admission changed state: %+v", got)
	}
}
