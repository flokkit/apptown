package adoption

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption/internal/adoptionsql"
	"flokkit.io/forge/internal/ids"
)

// Selection follows the current pointer, never the largest historical row,
// and observes a caller's uncommitted transaction rather than the pool.
func TestCurrentAdoptionUsesOneSnapshotAndCallerTransaction(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	id := policyProject(t, db, "receipt-snapshot")
	if err := db.InTx(context.Background(), func(tx *database.Tx) error { return InitializeIn(t.Context(), tx, id, 1, "", "") }); err != nil {
		t.Fatal(err)
	}
	for revision := 1; revision <= 2; revision++ {
		if _, err := adoptionsql.New(db.SQL()).InsertAdoption(context.Background(), adoptionsql.InsertAdoptionParams{ID: ids.New(time.Unix(int64(revision), 0)),
			ProjectID: id, Revision: int64(revision), Kind: AdoptionEdit.String(), Digest: "digest", Actor: "alice",
			At: time.Unix(int64(revision), 0), Receipt: json.RawMessage(fmt.Sprintf(`{"resolver":%d}`, revision)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	counted := &policyCountedQueries{Handle: db.SQL()}
	got, err := CurrentReceiptIn(t.Context(), counted, id)
	if err != nil || got == nil || got.Revision != 1 || got.Receipt.Resolver != 1 || counted.reads != 1 {
		t.Fatalf("current receipt: %+v reads=%d %v", got, counted.reads, err)
	}
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := adoptionsql.New(tx.SQL()).SaveCurrentAdoption(context.Background(), adoptionsql.SaveCurrentAdoptionParams{ProjectID: id, Revision: 2, ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	inside, err := CurrentReceiptIn(t.Context(), tx, id)
	if err != nil || inside == nil || inside.Revision != 2 || inside.Receipt.Resolver != 2 {
		t.Fatalf("inside transaction: %+v %v", inside, err)
	}
	outside, err := CurrentReceiptIn(t.Context(), db, id)
	if err != nil || outside == nil || outside.Revision != 1 || outside.Receipt.Resolver != 1 {
		t.Fatalf("outside transaction: %+v %v", outside, err)
	}
}
