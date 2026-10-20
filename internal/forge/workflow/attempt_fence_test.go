package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/schema"
)

// A participant's write fenced by a claimed attempt runs only while that
// attempt still owns its step, and is handed that attempt: one interrupted
// (an AppTown restart) or superseded by a later attempt writes nothing, and
// the later attempt writes as itself.
func TestAFencedWriteRunsOnlyWhileItsAttemptOwnsTheStep(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	q, run, stepID := admitSingleStep(t, schema.Step{Name: "deploy", Uses: "deploy"})
	attempts := newStepAttempts(q.db, func(string) bool { return true })
	claim := func() *Execution {
		t.Helper()
		snapshot, err := readStep(ctx, q.db, stepID)
		if err != nil {
			t.Fatal(err)
		}
		step, err := attempts.Start(ctx, snapshot, snapshot.Type)
		if err != nil {
			t.Fatal(err)
		}
		return &Execution{service: &Service{db: q.db}, ctx: ctx, step: step, run: run, claim: step}
	}
	var wrote []int64
	write := func(_ *database.Tx, attempt int64) error { wrote = append(wrote, attempt); return nil }

	first := claim()
	if err := first.InAttempt("deploy", write); err != nil || len(wrote) != 1 || wrote[0] != 1 {
		t.Fatalf("the current attempt: %v, wrote %v", err, wrote)
	}
	if err := first.InAttempt("tests", write); !errors.Is(err, ErrStepAttemptLost) || len(wrote) != 1 {
		t.Fatalf("a claim of another kind: %v, wrote %v", err, wrote)
	}
	if err := q.db.InTx(ctx, func(tx *database.Tx) error { _, err := InterruptStepsIn(ctx, tx, nil, nil, time.Now()); return err }); err != nil {
		t.Fatal(err)
	}
	if err := first.InAttempt("deploy", write); !errors.Is(err, ErrStepAttemptLost) || len(wrote) != 1 {
		t.Fatalf("an interrupted attempt: %v, wrote %v", err, wrote)
	}
	if n, err := invalidateAndRetryForTest(ctx, q.db, run, "deploy"); err != nil || n != 1 {
		t.Fatalf("retrying the step: %d %v", n, err)
	}
	second := claim()
	if err := first.InAttempt("deploy", write); !errors.Is(err, ErrStepAttemptLost) || len(wrote) != 1 {
		t.Fatalf("a superseded attempt: %v, wrote %v", err, wrote)
	}
	if err := second.InAttempt("deploy", write); err != nil || len(wrote) != 2 || wrote[1] != 2 {
		t.Fatalf("the later attempt: %v, wrote %v", err, wrote)
	}
	refused := errors.New("the participant's own refusal")
	if err := second.InAttempt("deploy", func(*database.Tx, int64) error { return refused }); !errors.Is(err, refused) {
		t.Fatalf("a write's own failure: %v", err)
	}
}
