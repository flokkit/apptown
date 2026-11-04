package dbschema

import (
	"github.com/flokkit/apptown/internal/ids"
	"testing"
	"time"
)

func TestMigration205KeepsHistoricalAmountsWithoutInventingSources(t *testing.T) {
	db := releaseDB(t)
	if _, err := applyMigrations(t.Context(), db, migrations[:len(migrations)-1]); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: this is the old ledger shape; current writers include source counts.
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO token_usage(id,at,kind,total,cost_micros) VALUES ($1,now(),'price-source-fixture',100,1200),($2,now(),'price-source-fixture',50,0)`, ids.New(time.Now()), ids.New(time.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := applyMigrations(t.Context(), db, migrations); err != nil {
		t.Fatal(err)
	}
	var total, cost, reported, catalog, unpriced int64
	// sql-guarantee: migration keeps amounts, and cannot assign historical catalog/provider provenance.
	err := db.SQL().QueryRowContext(t.Context(), `SELECT SUM(total),SUM(cost_micros),SUM(provider_reported),SUM(catalog),SUM(unpriced) FROM token_usage WHERE kind='price-source-fixture'`).Scan(&total, &cost, &reported, &catalog, &unpriced)
	if err != nil || total != 150 || cost != 1200 || reported != 0 || catalog != 0 || unpriced != 2 {
		t.Fatalf("migration totals %d %d sources %d %d %d: %v", total, cost, reported, catalog, unpriced, err)
	}
}
