package wow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/dbtest"
)

func TestBlockEditKeepsTagsProseChecksAndReceiptTogether(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"-- name: BlockVersion :one", "INSERT INTO guidance_block_versions", "-- name: AdvanceBlockVersion", "INSERT INTO audit_log", "-- name: BlockByID :one"} {
		t.Run(fault, func(t *testing.T) {
			db := testDB(t)
			s := New(db, Dependencies{})
			ctx := context.Background()
			q := wowsql.New(db.SQL())
			block, err := s.CreateBlock(ctx, "edit", "old", "old", "alice")
			if err != nil {
				t.Fatal(err)
			}
			before, err := q.BlockByID(ctx, block.ID)
			if err != nil {
				t.Fatal(err)
			}
			body, tags := "new", "new"
			checks := []check.Declaration{{AppliesTo: []string{"repository"}, Name: "works", Expects: "works", Sh: "true"}}
			dbtest.FailNextStatement(t, db, fault)
			if out, err := s.EditBlock(ctx, block.ID, BlockPatch{Tags: &tags, BodyMD: &body, Checks: &checks}, "alice"); err == nil || out != nil {
				t.Fatalf("failed edit returned success: %+v %v", out, err)
			}
			after, err := q.BlockByID(ctx, block.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("partial metadata/version: %+v %v", after, err)
			}
			if _, err := q.BlockVersion(ctx, wowsql.BlockVersionParams{BlockID: block.ID, Version: 2}); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("failed version survived: %v", err)
			}
		})
	}
}
func TestBlockEditsPreserveConcurrentOmittedFieldsAndStock(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := New(db, Dependencies{})
	ctx := context.Background()
	q := wowsql.New(db.SQL())
	block, err := s.CreateBlock(ctx, "edit", "old", "old", "alice")
	if err != nil {
		t.Fatal(err)
	}
	body := "new"
	checks := []check.Declaration{{AppliesTo: []string{"repository"}, Name: "works", Expects: "works", Sh: "true"}}
	var wg sync.WaitGroup
	for _, patch := range []BlockPatch{{BodyMD: &body}, {Checks: &checks}} {
		wg.Go(func() {
			if _, err := s.EditBlock(ctx, block.ID, patch, "alice"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	row, err := q.BlockByID(ctx, block.ID)
	if err != nil || row.LatestVersion != 3 {
		t.Fatalf("version: %+v %v", row, err)
	}
	version, err := q.BlockVersion(ctx, wowsql.BlockVersionParams{BlockID: block.ID, Version: 3})
	if err != nil || version.BodyMd != body {
		t.Fatalf("prose lost: %+v %v", version, err)
	}
	var got []check.Declaration
	if err := json.Unmarshal(version.ChecksJson, &got); err != nil || !reflect.DeepEqual(checks, got) {
		t.Fatalf("checks lost: %+v %v", got, err)
	}
	out, err := s.EditBlock(ctx, block.ID, BlockPatch{BodyMD: &body, Checks: &checks}, "alice")
	if err != nil || out.LatestVersion != 3 {
		t.Fatalf("no-op published: %+v %v", out, err)
	}
	if err := s.library.StockInsert(ctx, StockBlock{Name: "stock", Body: "shipped"}); err != nil {
		t.Fatal(err)
	}
	stock, err := s.BlockByName(ctx, "stock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditBlock(ctx, stock.ID, BlockPatch{BodyMD: &body}, "alice"); !errors.Is(err, ErrStockBlock) {
		t.Fatalf("stock changed: %v", err)
	}
}
