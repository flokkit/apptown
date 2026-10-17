package model

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/ids/idstest"
)

type capacityFunc func(string) int

func (f capacityFunc) Capacity(ctx context.Context, binding string) int { return f(binding) }

// Capacity belongs to the backend: a binding's declared concurrency, else
// one for a self-hosted endpoint and eight for a hosted API, and the
// daemon's flag only for a binding the forge does not know.
func TestModelCapacityComesFromTheBinding(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	bindings := integration.New(db)
	s := NewScheduler(BindingCapacity{Bindings: bindings}, 2)
	for name, cfg := range map[string]string{"gpu1": `{"concurrency":3,"providerDef":{}}`, "openrouter": `{"models":["x"]}`, "box": `{"baseUrl":"https://box.example/v1"}`, "bad": `{"concurrency":"invalid"}`} {
		if _, err := bindings.Upsert(t.Context(), name, "model", "pi", cfg, false); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Capacity(t.Context(), "gpu1"); got != 3 {
		t.Fatalf("declared concurrency wins: %d", got)
	}
	if got := s.Capacity(t.Context(), "openrouter"); got != hostedModelCapacity {
		t.Fatalf("a hosted API defaults to %d, got %d", hostedModelCapacity, got)
	}
	if got := s.Capacity(t.Context(), "box"); got != selfHostedCapacity {
		t.Fatalf("a custom endpoint defaults to %d, got %d", selfHostedCapacity, got)
	}
	for _, binding := range []string{"", "nobody", "bad"} {
		if got := s.Capacity(t.Context(), binding); got != 2 {
			t.Fatalf("unknown/malformed binding %q: %d", binding, got)
		}
	}
	if _, err := bindings.Upsert(t.Context(), "other", "registry", "docker", `{}`, false); err != nil {
		t.Fatal(err)
	}
	if got := s.Capacity(t.Context(), "other"); got != 2 {
		t.Fatalf("wrong capability: %d", got)
	}
	restore := db.FailNextStatement("SELECT")
	if got := s.Capacity(t.Context(), "gpu1"); got != 2 {
		t.Fatalf("read failure lost fallback: %d", got)
	}
	restore()
	if _, err := bindings.Upsert(t.Context(), "gpu1", "model", "pi", `{"concurrency":4}`, false); err != nil {
		t.Fatal(err)
	}
	if got := s.Capacity(t.Context(), "gpu1"); got != 4 {
		t.Fatalf("updated capacity: %d", got)
	}
	if BindingOfPattern("openrouter/deepseek/deepseek-v4-pro") != "openrouter" || BindingOfPattern("gpu1/Qwen3.6") != "gpu1" {
		t.Fatal("the binding is the pattern's first segment")
	}
}

// A step waits only when ITS backend is full: two steps on a one-stream
// box queue, a step on a hosted API beside them does not, and the run
// page can say who is waiting where.
func TestModelSlotsAreProvisionedPerBinding(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s := NewScheduler(capacityFunc(func(binding string) int {
			if binding == "gpu1" {
				return 1
			}
			return 8
		}), 2)
		rel1, waited := s.Acquire(t.Context(), "gpu1", idstest.N(101))
		if waited {
			t.Fatal("the first step on an idle box does not wait")
		}
		got := make(chan bool, 1)
		go func() {
			rel, w := s.Acquire(t.Context(), "gpu1", idstest.N(102))
			got <- w
			rel()
		}()
		// Every goroutine in the bubble is blocked: 102 is queued, if it waits.
		synctest.Wait()
		if b, ahead, waiting := s.Waiting(idstest.N(102)); !waiting || b != "gpu1" || ahead != 0 {
			t.Fatalf("102 waits on gpu1, next in line: %s %d %v", b, ahead, waiting)
		}
		// A hosted-API step beside them is not held up.
		done := make(chan struct{})
		go func() {
			rel, w := s.Acquire(t.Context(), "openrouter", idstest.N(103))
			if w {
				t.Error("openrouter is not full; 103 must not wait")
			}
			rel()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("a step on another backend waited on the box's queue")
		}
		rel1()
		select {
		case w := <-got:
			if !w {
				t.Fatal("102 waited its turn")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("102 did not get the slot after 101 released it")
		}
		if _, _, waiting := s.Waiting(idstest.N(102)); waiting {
			t.Fatal("102 is no longer waiting")
		}
	})
}

func TestSchedulerResizeKeepsOldHoldersAndWaiters(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var capacity atomic.Int64
		capacity.Store(1)
		s := NewScheduler(capacityFunc(func(string) int { return int(capacity.Load()) }), 1)
		oldRelease, _ := s.Acquire(t.Context(), "gpu", idstest.N(1))
		finished := make(chan bool, 1)
		go func() { release, waited := s.Acquire(t.Context(), "gpu", idstest.N(2)); release(); finished <- waited }()
		synctest.Wait() // 2 is queued behind 1
		capacity.Store(2)
		newRelease, waited := s.Acquire(t.Context(), "gpu", idstest.N(3))
		if waited {
			t.Fatal("resized pool should have room")
		}
		if binding, _, waiting := s.Waiting(idstest.N(2)); !waiting || binding != "gpu" {
			t.Fatal("resize lost old waiter")
		}
		// The old holder still owns the old channel. Releasing it must unblock
		// its waiter even while the replacement pool has a live holder.
		oldRelease()
		select {
		case waited := <-finished:
			if !waited {
				t.Fatal("old waiter did not wait")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("old waiter stranded by resize")
		}
		if _, _, waiting := s.Waiting(idstest.N(2)); waiting {
			t.Fatal("finished waiter remains visible")
		}
		newRelease()
		release, waited := s.Acquire(t.Context(), "gpu", idstest.N(4))
		if waited {
			t.Fatal("replacement pool leaked capacity")
		}
		release()
	})
}

func TestSchedulerReleasesAfterFailedExecution(t *testing.T) {
	t.Parallel()
	s := NewScheduler(nil, 1)
	failed := make(chan struct{})
	go func() {
		defer close(failed)
		defer func() { _ = recover() }()
		release, _ := s.Acquire(t.Context(), "unknown", idstest.N(1))
		defer release()
		panic("execution failed")
	}()
	<-failed
	acquired := make(chan bool, 1)
	go func() {
		release, waited := s.Acquire(t.Context(), "unknown", idstest.N(2))
		release()
		acquired <- waited
	}()
	select {
	case waited := <-acquired:
		if waited {
			t.Fatal("failed execution leaked its permit")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failed execution stranded the next step")
	}
	if s.Capacity(t.Context(), "unknown") != 1 || NewScheduler(nil, 0).Capacity(t.Context(), "unknown") != 1 {
		t.Fatal("missing source lost fallback")
	}
}
