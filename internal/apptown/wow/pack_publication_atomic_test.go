package wow

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/stock"
)

func TestPackPublicationsAreAtomicAndImmutable(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ways := New(db, Dependencies{Stock: stock.FS})
	ways.SyncStock(t.Context())
	spec, err := json.Marshal(PackSpec{Guidance: []string{"prose/terse-style"}})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, db, "INSERT INTO pack_versions")
	if _, err := ways.PublishPack(t.Context(), "atomic", "Atomic", string(spec), ""); err == nil {
		t.Fatal("publication failure hidden")
	}
	if _, err := ways.Pack(t.Context(), "atomic"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a failed publication left a pack behind: %v", err)
	}
	if v, err := ways.PublishPack(t.Context(), "atomic", "Atomic", string(spec), ""); err != nil || v != 1 {
		t.Fatalf("publication: %d %v", v, err)
	}
	var original string
	// sql-guarantee: the whole stored version row, compared byte for byte after a stock sync; immutability of the stored publication is the guarantee
	if err := db.QueryRow(`SELECT to_jsonb(v)::text FROM pack_versions v JOIN packs p ON p.id=v.pack_id WHERE p.name='atomic'`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	changed, err := json.Marshal(PackSpec{Guidance: []string{"arch/boring-technology"}})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, db, "name: AdvancePackVersion")
	if _, err := ways.PublishPack(t.Context(), "atomic", "Atomic", string(changed), ""); err == nil {
		t.Fatal("pointer failure hidden")
	}
	pack, err := ways.Pack(t.Context(), "atomic")
	if err != nil {
		t.Fatal(err)
	}
	if publications, err := ways.PackPublications(t.Context(), pack.ID); err != nil || len(publications) != 1 || pack.LatestVersion != 1 {
		t.Fatalf("a failed pointer advance left a publication behind: %+v latest=%d %v", publications, pack.LatestVersion, err)
	}
	if v, err := ways.PublishPack(t.Context(), "atomic", "Atomic", string(changed), ""); err != nil || v != 2 {
		t.Fatalf("new publication: %d %v", v, err)
	}
	ways.SyncStockPacks(t.Context())
	var after string
	// sql-guarantee: the whole stored version row, compared byte for byte after a stock sync; immutability of the stored publication is the guarantee
	if err := db.QueryRow(`SELECT to_jsonb(v)::text FROM pack_versions v JOIN packs p ON p.id=v.pack_id WHERE p.name='atomic' AND v.version=1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != original {
		t.Fatal("stock sync rewrote publication")
	}
}
