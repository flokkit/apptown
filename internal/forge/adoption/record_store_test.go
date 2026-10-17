package adoption

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/adoption/internal/adoptionsql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestProjectAdoptionDoorPreservesScopeAndCancellation(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	first := adoptionTestProject(t, db, "first", "First")
	second := adoptionTestProject(t, db, "second", "Second")
	ad, err := newAdoptions(db).Advance(t.Context(), first, AdoptionEdit, "reviewed", "alice", false, func(*database.Tx, *RecordedRevision, time.Time) (Receipt, error) { return EmptyReceipt(), nil })
	if err != nil {
		t.Fatal(err)
	}
	got, err := ProjectAdoptionIn(context.Background(), db, first, ad.ID)
	if err != nil || got.ID != ad.ID || got.Kind != AdoptionEdit {
		t.Fatalf("admitted revision: %+v %v", got, err)
	}
	for _, pid := range []ids.ID{ids.Nil, idstest.N(999999), second} {
		if got, err := ProjectAdoptionIn(context.Background(), db, pid, ad.ID); got != nil || !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("foreign scope %d: %+v %v", pid, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := ProjectAdoptionIn(ctx, db, first, ad.ID); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %+v %v", got, err)
	}
	dbtest.FailNextStatement(t, db, "name: AdoptionByID")
	var failure *pgconn.PgError
	if got, err := ProjectAdoptionIn(context.Background(), db, first, ad.ID); got != nil || errors.Is(err, sql.ErrNoRows) || !errors.As(err, &failure) || failure.Code != "42883" || !strings.Contains(failure.Message, "forge_injected_failure") {
		t.Fatalf("failed read became absence: %+v %v", got, err)
	}
}

func TestAdoptionHistoryDoesNotReturnPartialRecords(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	project := adoptionTestProject(t, db, "history", "History")
	// The newest row is readable; the older receipt is corrupt. A history
	// failure must not masquerade as a shorter, otherwise usable history.
	q := adoptionsql.New(db.SQL())
	if _, err := q.InsertAdoption(context.Background(), adoptionsql.InsertAdoptionParams{ID: ids.New(time.Now()), ProjectID: project, Revision: 1, Kind: AdoptionEdit.String(), Digest: "old", Receipt: []byte(`[]`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertAdoption(context.Background(), adoptionsql.InsertAdoptionParams{ID: ids.New(time.Now()), ProjectID: project, Revision: 2, Kind: AdoptionEdit.String(), Digest: "new", Receipt: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	rows, err := newAdoptions(db).History(t.Context(), project, 10)
	if err == nil || rows == nil || len(rows) != 0 {
		t.Fatalf("corrupt older receipt returned partial history: %+v %v", rows, err)
	}
}
