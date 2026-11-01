package adoption

import (
	"context"
	"database/sql"
	"testing"

	"github.com/flokkit/apptown/internal/database/query"
	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/strs"
)

// Keep the actual catalog reader and candidate parser in this test's path while
// allowing the publication query's database round trips to be counted.
type publicationCatalog struct {
	catalog
	q database.Queryer
}

func (publicationCatalog) Withdrawn(wow.Kind, string, string) bool { return false }

func (c publicationCatalog) PackPublication(ctx context.Context, name string, version int64) (wow.PackVersion, error) {
	return wow.PackVersionIn(ctx, c.q, name, version)
}

type publicationQueries struct {
	query.Handle
	reads int
}

func (q *publicationQueries) SQL() query.Handle { return dbtest.WithExecutor(q.Handle, q) }

func (q *publicationQueries) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	q.reads++
	return q.Executor.QueryContext(ctx, query, args...)
}

func (q *publicationQueries) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	q.reads++
	return q.Executor.QueryRowContext(ctx, query, args...)
}

func TestCandidatePublicationPreservesExactContents(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	library := wow.NewLibrary(db)
	if err := library.StockInsert(t.Context(), wow.StockBlock{Name: "rule", Body: "v1"}); err != nil {
		t.Fatal(err)
	}
	ways := wow.New(db, wow.Dependencies{})
	if version, err := ways.PublishPack(t.Context(), "pinned", "Pinned", mustJSON(candidatePackSpec{Platform: true, Guidance: []string{"rule"}}), ""); err != nil || version != 1 {
		t.Fatalf("publish v1: %d %v", version, err)
	}
	if version, err := library.StockUpdate(t.Context(), wow.StockBlock{Name: "rule", Body: "v2"}); err != nil || version != 2 {
		t.Fatalf("publish rule v2: %d %v", version, err)
	}
	if version, err := ways.PublishPack(t.Context(), "pinned", "Pinned", mustJSON(candidatePackSpec{Guidance: []string{"rule"}}), ""); err != nil || version != 2 {
		t.Fatalf("publish v2: %d %v", version, err)
	}
	packRecord, err := ways.Pack(t.Context(), "pinned")
	if err != nil {
		t.Fatal(err)
	}
	pack := packRecord.ID

	counted := &publicationQueries{Handle: db.SQL()}
	builder := candidateBuilder{ways: publicationCatalog{q: counted}}
	version, err := builder.packPublication(t.Context(), "pinned", 1)
	if err != nil || !version.Spec.Platform || len(version.Spec.Guidance) != 1 || version.Spec.Guidance[0] != "rule" || version.Pins.Blocks["rule"] != 1 || counted.reads != 1 {
		t.Fatalf("exact pinned publication: %+v, reads=%d, err=%v", version, counted.reads, err)
	}
	for _, tc := range []struct{ name, want string }{{"missing", `no pack named "missing"`}, {"pinned", "pinned has no version 9"}} {
		if _, err := builder.packPublication(t.Context(), tc.name, 9); err == nil || err.Error() != tc.want {
			t.Fatalf("absent publication: %v, want %s", err, tc.want)
		}
	}
	// sql-guarantee: corrupt the pins of the already selected immutable publication; PublishPack can only append a valid version.
	if _, err := db.SQL().ExecContext(context.Background(), `UPDATE pack_versions SET pins_json='{"blocks":"bad"}' WHERE pack_id=$1 AND version=1`, pack); err != nil {
		t.Fatal(err)
	}
	if _, err := builder.packPublication(t.Context(), "pinned", 1); err == nil {
		t.Fatal("invalid publication pins accepted")
	}
}

// mustJSON encodes a fixture; a fixture that cannot be encoded is a broken test.
func mustJSON(v any) string {
	text, err := strs.JSON(v)
	if err != nil {
		panic(err)
	}
	return text
}
