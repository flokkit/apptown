package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
)

type scannerSnapshotFunc func(context.Context, database.Queryer) ([]Scanner, error)

func (f scannerSnapshotFunc) SnapshotIn(ctx context.Context, q database.Queryer) ([]Scanner, error) {
	return f(ctx, q)
}

// The integration adapter is tested at AppTown's boundary. Work must pass its
// existing transaction to the adapter and refuse entry if that inventory fails.
func TestStageEntryRequiresItsTransactionalScannerInventory(t *testing.T) {
	t.Parallel()
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty inventory", true: "failed inventory"}[unavailable], func(t *testing.T) {
			t.Parallel()
			db, run, _ := completionFixture(t, "working")
			spec := &schema.Spec{Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "security", Uses: "scan", With: map[string]any{"on": "source"}}}}}}
			sentinel := errors.New("scanner inventory unavailable")
			called := false
			err := db.InTx(context.Background(), func(tx *database.Tx) error {
				source := scannerSnapshotFunc(func(ctx context.Context, q database.Queryer) ([]Scanner, error) {
					called = true
					if q != tx {
						t.Error("stage inventory did not use admitted transaction")
					}
					if unavailable {
						return nil, sentinel
					}
					return nil, nil
				})
				_, err := NewStages(source, func(string) bool { return true }).EnterIn(context.Background(), tx, run.ID, spec, "finish", time.Now())
				return err
			})
			if !called {
				t.Fatal("stage bypassed inventory")
			}
			if unavailable {
				if !errors.Is(err, sentinel) {
					t.Fatalf("inventory error lost: %v", err)
				}
				wantStepCount(t, db, 0, func(s *Step) bool { return s.RunID == run.ID })
				wantRunEventCount(t, db, run.ID, 0, anyEvent)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				wantStepCount(t, db, 1, func(s *Step) bool { return s.RunID == run.ID && jsonPathEqual(s.Config, "none", "true") })
			}
		})
	}
}
