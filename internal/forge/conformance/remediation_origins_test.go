package conformance

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestRemediationOriginsAreCanonicalAndBatched(t *testing.T) {
	t.Parallel()
	store, previous, next := verdictReceiptFixture(t, "sh")
	observations := []Observation{Recorded(previous)}
	for _, n := range []int{1, 12} {
		for len(observations) < n {
			v := previous
			v.ID = ids.Nil
			v.ReviewID = next.Verdict.ReviewID
			v.ProjectSlug = fmt.Sprintf("origin-%d", len(observations))
			v.ProjectID = createTestProject(t, store.db, v.ProjectSlug, v.ProjectSlug, "")
			saved, err := store.recordVerdict(context.Background(), verdictAdmission{Verdict: v})
			if err != nil {
				t.Fatal(err)
			}
			observations = append(observations, Recorded(saved))
		}
		tx, err := store.db.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		counted := &countedQueries{Handle: tx.SQL()}
		err = validateRemediationOrigins(context.Background(), counted, observations)
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			t.Fatal(rollbackErr)
		}
		if err != nil || counted.reads != 2 {
			t.Fatalf("%d origins: reads=%d err=%v", n, counted.reads, err)
		}
	}
	// A projected name may be current while its saved record keeps the old name.
	renamed := observations[0]
	renamed.ProjectSlug = "renamed"
	tx, err := store.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ValidateRemediationOriginsIn(context.Background(), tx, []Observation{renamed}); err != nil {
		t.Fatalf("rename lost saved origin: %v", err)
	}
}

func TestRemediationOriginsRejectMissingOrAlteredEvidence(t *testing.T) {
	t.Parallel()
	store, previous, _ := verdictReceiptFixture(t, "sh")
	original := Recorded(previous)
	changedRecord := previous
	changedRecord.Detail = "invented evidence"
	changedSubject := original
	changedSubject.ProjectID = idstest.N(999999)
	changedCheck := original
	changedCheck.Check = "invented check"
	stale := original
	stale.State = Stale
	for _, observation := range []Observation{Unassessed(previous.Reference()), Recorded(changedRecord), changedSubject, changedCheck, stale} {
		tx, err := store.db.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		err = ValidateRemediationOriginsIn(context.Background(), tx, []Observation{observation})
		_ = tx.Rollback()
		if !errors.Is(err, ErrOriginsChanged) {
			t.Fatalf("altered origin admitted: %+v %v", observation, err)
		}
	}
	if n, err := queries(store.db).DeleteAllVerdicts(context.Background()); err != nil || n != 1 {
		t.Fatalf("delete sole origin: rows=%d err=%v", n, err)
	}
	tx, err := store.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ValidateRemediationOriginsIn(context.Background(), tx, []Observation{original}); !errors.Is(err, ErrOriginsChanged) {
		t.Fatalf("cleared origin admitted: %v", err)
	}
}

func TestRemediationOriginsPreserveExpiredExceptionInterpretation(t *testing.T) {
	t.Parallel()
	store, previous, _ := verdictReceiptFixtureWithOutcome(t, "sh", Excepted)
	observation := Observe(previous, CurrentContext{Check: previous.Reference(), HeadSHA: previous.SHA, ExceptionActive: false})
	tx, err := store.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ValidateRemediationOriginsIn(context.Background(), tx, []Observation{observation}); err != nil {
		t.Fatalf("expired exception hid underlying finding: %v", err)
	}
	if err := ValidateRemediationOriginsIn(context.Background(), tx, []Observation{Recorded(previous)}); !errors.Is(err, ErrOriginsChanged) {
		t.Fatalf("active exception became a finding: %v", err)
	}
}

func TestRemediationOriginsRequireCurrentAIConfirmationAndCheckedReads(t *testing.T) {
	t.Parallel()
	store, previous, _ := verdictReceiptFixture(t, "llm")
	check := func(ctx context.Context) error {
		tx, err := store.db.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		return ValidateRemediationOriginsIn(ctx, tx, []Observation{Recorded(previous)})
	}
	if err := check(context.Background()); !errors.Is(err, ErrOriginsChanged) {
		t.Fatalf("unconfirmed AI finding admitted: %v", err)
	}
	if _, err := store.confirm(context.Background(), confirmationRequest{VerdictID: previous.ID, By: "reviewer"}); err != nil {
		t.Fatal(err)
	}
	if err := check(context.Background()); err != nil {
		t.Fatalf("confirmed finding refused: %v", err)
	}
	dbtest.FailNextStatement(t, store.db, "FROM conformance_adjudications")
	if err := check(context.Background()); err == nil || errors.Is(err, ErrOriginsChanged) {
		t.Fatalf("read failure looked like changed evidence: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := check(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}
