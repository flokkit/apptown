package adoption

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

func TestAdoptionRuntimeSourcesPropagateFailedWorkflowCatalog(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	pid := adoptionTestProject(t, db, "catalog-failure", "Catalog failure")
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return RecordIn(t.Context(), tx, pid, 1, AdoptionCreate, "initial", EmptyReceipt(), "alice", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	s := New(db, Dependencies{Ways: wow.New(db, wow.Dependencies{})})
	prior, err := s.CurrentReceipt(t.Context(), pid)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, db, "name: WorkflowCatalog")
	if got, err := s.Prepare(t.Context(), pid, Request{}); got != nil || !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("failed catalog produced a candidate with workflows omitted: %+v %v", got, err)
	}
	dbtest.FailNextStatement(t, db, "name: WorkflowCatalog")
	if _, err := s.Union(t.Context(), pid, prior); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("failed catalog produced a successful union: %v", err)
	}
	if cur := currentOf(t, db, pid); cur.Revision != 1 {
		t.Fatalf("failed catalog moved the aggregate: %+v", cur)
	}
	if revs := revisionsOf(t, db, pid); len(revs) != 1 {
		t.Fatalf("failed catalog recorded a revision: %+v", revs)
	}
}
