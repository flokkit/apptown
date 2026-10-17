package workflow

import (
	"context"
	"errors"
	"testing"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/ids"
)

type lawRulesFunc func(context.Context, database.Queryer, ids.ID, *schema.Spec) ([]LawViolation, error)

func (f lawRulesFunc) ViolationsIn(ctx context.Context, q database.Queryer, id ids.ID, spec *schema.Spec) ([]LawViolation, error) {
	return f(ctx, q, id, spec)
}
func TestLawOwnerUsesLockedScopeAndTransaction(t *testing.T) {
	t.Parallel()
	q, run, _ := activityFixture(t)
	q.db.Pool().SetMaxOpenConns(1)
	fail := true
	calls := 0
	failure := errors.New("rules unavailable")
	owner := NewLaws(q.db, lawRulesFunc(func(ctx context.Context, db database.Queryer, id ids.ID, spec *schema.Spec) ([]LawViolation, error) {
		calls++
		if id != run.ProjectID {
			t.Fatalf("project %d", id)
		}
		if stage, _ := spec.Stage(run.CurrentStage); stage == nil {
			t.Fatal("missing pinned declaration")
		}
		current, err := readRun(ctx, db, run.ID)
		if err != nil {
			return nil, err
		}
		if current.CurrentStage != run.CurrentStage {
			t.Fatal("wrong observation")
		}
		if fail {
			return nil, failure
		}
		return []LawViolation{{Policy: "scan", Rule: "requires", Reading: "scan", Detail: "missing scan"}}, nil
	}))
	ctx := context.Background()
	stale := *run
	stale.RetryRevision++
	if _, err := owner.records.evaluate(ctx, &stale, false); !errors.Is(err, ErrRunMoved) {
		t.Fatalf("stale observation: %v", err)
	}
	if calls != 0 {
		t.Fatal("observed rules before checking scope")
	}
	if err := owner.Refresh(ctx, run); err != nil || calls != 0 {
		t.Fatalf("created an unrequested gate: %v", err)
	}
	if _, err := owner.records.evaluate(ctx, run, false); !errors.Is(err, failure) {
		t.Fatalf("read failure: %v", err)
	}
	wantStepCount(t, q.db, 0, func(s *Step) bool { return s.Type == "policy/current" })
	wantEventCount(t, q.db, 0, anyEvent)
	fail = false
	step, err := owner.records.evaluate(ctx, run, false)
	if err != nil || step == nil || step.Status != StatusNeedsHuman {
		t.Fatalf("gate %+v %v", step, err)
	}
	if _, err = owner.records.evaluate(ctx, run, false); err != nil {
		t.Fatal(err)
	}
	wantStepCount(t, q.db, 1, func(s *Step) bool { return s.Type == "policy/current" })
	wantEventCount(t, q.db, 1, ofKind("law.changed"))
	wantReceiptCount(t, q.db, 1, receiptKind("landing.paused-by-law"))
}
