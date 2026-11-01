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

type facts observation

func (f facts) ObservePreview(ctx context.Context) observation {
	return observation(f)
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
		{name: "cold", state: cold},
		{name: "no contract", observed: facts{NoContract: true}, state: none},
		{name: "starting beats observation", boot: true, observed: facts{URL: "https://old", NoContract: true}, state: stateStarting},
		{name: "failed", boot: true, finish: true, failure: errors.New("build failed"), state: Broken},
		{name: "missing contract beats failure", boot: true, finish: true, failure: errors.New("build failed"), observed: facts{NoContract: true}, state: none},
		{name: "live beats failure", boot: true, finish: true, failure: errors.New("old failure"), observed: facts{URL: "https://preview"}, state: live},
		{name: "successful boot still needs observation", boot: true, finish: true, state: cold},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var runtime previewRuntime
			key := changeKey(idstest.N(7))
			if tc.boot {
				boot, err := runtime.begin(context.Background(), key)
				if err != nil {
					t.Fatal(err)
				}
				defer boot.finish(nil)
				if tc.finish {
					boot.finish(tc.failure)
				}
			}
			got := runtime.inspect(context.Background(), key, tc.observed)
			if got.State != tc.state || got.At.IsZero() == tc.boot {
				t.Fatalf("status: %+v", got)
			}
			if (got.Reason != "") != (tc.state == Broken) || (got.URL != "") != (tc.state == live) {
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
	var runtime, other previewRuntime
	key := changeKey(idstest.N(42))
	owner, err := runtime.begin(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.finish(nil)
	starting := runtime.inspect(context.Background(), key, facts{})
	// Same IDs on another instance, another change, and project-scoped release
	// numbers must not contend with this change or with one another.
	for _, selection := range []struct {
		runtime *previewRuntime
		key     previewKey
	}{
		{&other, key}, {&runtime, changeKey(idstest.N(43))}, {&runtime, releaseKey(idstest.N(1), 42)}, {&runtime, releaseKey(idstest.N(2), 42)},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		boot, err := selection.runtime.begin(ctx, selection.key)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		defer boot.finish(nil)
	}
	if got := other.inspect(context.Background(), key, facts{}); got.State != stateStarting {
		t.Fatalf("other instance: %+v", got)
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &previewWaitContext{Context: base, waiting: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		boot, err := runtime.begin(ctx, key)
		if boot != nil {
			boot.finish(nil)
		}
		result <- err
	}()
	previewReceive(t, ctx.waiting)
	cancel()
	if err := previewReceive(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: %v", err)
	}
	if got := runtime.inspect(base, key, facts{}); got != starting {
		t.Fatalf("waiter changed owner: %+v", got)
	}

	live, cancelLive := context.WithCancel(context.Background())
	defer cancelLive()
	waiting := &previewWaitContext{Context: live, waiting: make(chan struct{})}
	admitted := make(chan *booting, 1)
	go func() { boot, _ := runtime.begin(waiting, key); admitted <- boot }()
	previewReceive(t, waiting.waiting)
	owner.finish(errors.New("first failed"))
	next := previewReceive(t, admitted)
	if next == nil {
		t.Fatal("live waiter was not admitted")
	}
	defer next.finish(nil)
	owner.finish(errors.New("stale completion"))
	if got := runtime.inspect(base, key, facts{}); got.State != stateStarting || got.Reason != "" {
		t.Fatalf("old owner finished new boot: %+v", got)
	}
	next.finish(nil)
	if got := runtime.inspect(base, key, facts{}); got.State != cold {
		t.Fatalf("finished boot: %+v", got)
	}
}

type blockingPreviewObserver struct{ entered, release chan struct{} }

func (o blockingPreviewObserver) ObservePreview(ctx context.Context) observation {
	close(o.entered)
	<-o.release
	return observation{URL: "https://old"}
}

func TestPreviewObservationDoesNotHoldBootLock(t *testing.T) {
	t.Parallel()
	var runtime previewRuntime
	key := changeKey(idstest.N(1))
	observation := blockingPreviewObserver{make(chan struct{}), make(chan struct{})}
	defer close(observation.release)
	viewed := make(chan Status, 1)
	go func() { viewed <- runtime.inspect(context.Background(), key, observation) }()
	previewReceive(t, observation.entered)
	started := make(chan *booting, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { boot, _ := runtime.begin(ctx, key); started <- boot }()
	boot := previewReceive(t, started)
	if boot == nil {
		t.Fatal("observation blocked admission")
	}
	defer boot.finish(nil)
	// Send a token rather than close so the deferred close remains valid.
	observation.release <- struct{}{}
	if got := previewReceive(t, viewed); got.State != stateStarting {
		t.Fatalf("stale observation won: %+v", got)
	}
}

func TestPreviewAdmissionRejectsInvalidOrCancelledRequest(t *testing.T) {
	t.Parallel()
	var runtime previewRuntime
	for _, key := range []previewKey{{}, changeKey(ids.Nil), releaseKey(ids.Nil, 1), releaseKey(idstest.N(1), -1)} {
		if boot, err := runtime.begin(context.Background(), key); err == nil || boot != nil {
			t.Fatalf("invalid identity admitted: %+v %v", key, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if boot, err := runtime.begin(ctx, changeKey(idstest.N(1))); !errors.Is(err, context.Canceled) || boot != nil {
		t.Fatalf("cancelled admission: %+v %v", boot, err)
	}
	if got := runtime.inspect(ctx, changeKey(idstest.N(1)), facts{}); !got.At.IsZero() || got.State != cold {
		t.Fatalf("cancelled admission changed state: %+v", got)
	}
}
