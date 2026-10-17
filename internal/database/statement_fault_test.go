package database

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// Contention belongs to our one-shot injector; no connection or schema is needed.
func TestStatementFaultConsumedOnceUnderContention(t *testing.T) {
	t.Parallel()
	var db DB
	t.Cleanup(db.FailNextStatement("target"))
	if err := db.faults.check("unrelated"); err != nil {
		t.Fatal(err)
	}
	var failures atomic.Int32
	var workers sync.WaitGroup
	start := make(chan struct{})
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			if err := db.faults.check("target"); errors.Is(err, ErrInjectedFailure) {
				failures.Add(1)
			} else if err != nil {
				t.Error(err)
			}
		}()
	}
	close(start)
	workers.Wait()
	if got := failures.Load(); got != 1 {
		t.Fatalf("one installation failed %d callers", got)
	}
}

func TestAFaultCanLetEarlierMatchingStatementsRun(t *testing.T) {
	db := &DB{}
	t.Cleanup(db.FailStatementAfter("target", 2))
	for i := 0; i < 2; i++ {
		if err := db.faults.check("target"); err != nil {
			t.Fatalf("matching statement %d failed before its turn: %v", i+1, err)
		}
	}
	if err := db.faults.check("target"); !errors.Is(err, ErrInjectedFailure) {
		t.Fatalf("third matching statement = %v, want the injected failure", err)
	}
	if err := db.faults.check("target"); err != nil {
		t.Fatalf("the fault fired twice: %v", err)
	}
}
