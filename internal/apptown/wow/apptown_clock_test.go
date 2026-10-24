package wow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/flokkit/apptown/stock"
)

// What the ways of working store is stamped by AppTown's clock, not the
// wall clock or the database's: on a clock ninety days behind, the stock a
// release seeds, a person's block and its edit, and a pack's publication all carry that clock's time. An
// adoption resolves a block as of its revision's time by the same clock.
func TestAppTownsClockStampsTheWaysOfWorking(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	at := time.Now().Add(-90 * 24 * time.Hour).UTC().Truncate(time.Microsecond)
	stamped := at.Format(time.RFC3339Nano)
	s := New(testDB(t), Dependencies{Stock: stock.FS, ToolLines: stock.Versions(), Now: func() time.Time { return at }})
	s.SyncStock(ctx)

	if _, err := s.CreateBlock(ctx, "house/clocked", "first", "", "alice"); err != nil {
		t.Fatal(err)
	}
	body := "second"
	created, err := s.BlockByName(ctx, "house/clocked")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditBlock(ctx, created.ID, BlockPatch{BodyMD: &body}, "alice"); err != nil {
		t.Fatal(err)
	}
	blocks, err := s.Blocks(ctx, BlockFilter{})
	if err != nil || len(blocks) < 2 {
		t.Fatalf("blocks: %d %v", len(blocks), err)
	}
	for _, b := range blocks {
		if b.CreatedAt != stamped || b.UpdatedAt != stamped {
			t.Fatalf("block %s stamped %s/%s, not %s", b.Name, b.CreatedAt, b.UpdatedAt, stamped)
		}
		versions, err := s.BlockVersions(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range versions {
			if v.CreatedAt != stamped {
				t.Fatalf("block %s version %d stamped %s, not %s", b.Name, v.Version, v.CreatedAt, stamped)
			}
		}
	}

	spec, err := json.Marshal(PackSpec{Guidance: []string{"house/clocked"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishPack(ctx, "clocked", "Clocked", string(spec), ""); err != nil {
		t.Fatal(err)
	}
	pack, err := s.Pack(ctx, "clocked")
	if err != nil {
		t.Fatal(err)
	}
	publications, err := s.PackPublications(ctx, pack.ID)
	if err != nil || len(publications) != 1 || !publications[0].At.Time.Equal(at) {
		t.Fatalf("a pack publication stamped other than %s: %+v %v", stamped, publications, err)
	}
}
