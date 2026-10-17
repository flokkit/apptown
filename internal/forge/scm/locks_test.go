package scm

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func waitForRepositoryUsers(t *testing.T, locks *RepositoryLocks, id ids.ID, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		locks.mu.Lock()
		n := 0
		if entry := locks.entries[id]; entry != nil {
			n = entry.users
		}
		locks.mu.Unlock()
		if n == want {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("repository %d did not reach %d participants", id, want)
}
func TestRepositoryLocksSerializeOnlyTheSameProject(t *testing.T) {
	t.Parallel()
	var locks RepositoryLocks
	release, err := locks.Acquire(context.Background(), idstest.N(1))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	acquired := make(chan func(), 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		next, err := locks.Acquire(ctx, idstest.N(1))
		if err == nil {
			acquired <- next
		}
		done <- err
	}()
	waitForRepositoryUsers(t, &locks, idstest.N(1), 2)
	select {
	case next := <-acquired:
		next()
		t.Fatal("same project overlapped")
	default:
	}
	other, err := locks.Acquire(ctx, idstest.N(2))
	if err != nil {
		t.Fatalf("unrelated project blocked: %v", err)
	}
	other()
	release()
	release() // release is safe when called twice.
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	next := <-acquired
	next()
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.entries) != 0 {
		t.Fatal("idle repository keys retained")
	}
}
func TestRepositoryLockWaitCancelsAndReleasesItsEntry(t *testing.T) {
	t.Parallel()
	var locks RepositoryLocks
	release, err := locks.Acquire(context.Background(), idstest.N(1))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		next, err := locks.Acquire(ctx, idstest.N(1))
		if next != nil {
			next()
		}
		done <- err
	}()
	waitForRepositoryUsers(t, &locks, idstest.N(1), 2)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled repository waiter stayed blocked")
	}
	waitForRepositoryUsers(t, &locks, idstest.N(1), 1)
	release()
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.entries) != 0 {
		t.Fatal("cancelled waiter retained repository entry")
	}
}
func TestRepositoryLockConcurrentWaitersHaveOneOwner(t *testing.T) {
	t.Parallel()
	var locks RepositoryLocks
	var active atomic.Int32
	results := race(32, func(int) int {
		release, err := locks.Acquire(context.Background(), idstest.N(1))
		if err != nil {
			t.Error(err)
			return 0
		}
		if active.Add(1) != 1 {
			t.Error("repository writers overlapped")
		}
		runtime.Gosched()
		active.Add(-1)
		release()
		return 1
	})
	if count(results, 1) != 32 {
		t.Fatal(results)
	}
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.entries) != 0 {
		t.Fatal("idle entries remained")
	}
}
