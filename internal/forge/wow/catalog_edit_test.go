package wow

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sync"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/wow/internal/wowsql"
)

func TestCatalogEditKeepsDescriptionVersionAndReceiptTogether(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"-- name: CatalogVersion :one", "INSERT INTO catalog_change_versions", "-- name: AdvanceCatalogVersion", "INSERT INTO audit_log", "-- name: CatalogByName :one"} {
		t.Run(fault, func(t *testing.T) {
			db := testDB(t)
			s := New(db, Dependencies{})
			ctx := context.Background()
			q := wowsql.New(db.SQL())
			def := CatalogDef{Workflow: "implement", Type: "chore", Prompt: "old"}
			c, err := s.CreateCatalogChange(ctx, "edit", "old", def, "alice")
			if err != nil {
				t.Fatal(err)
			}
			before, err := q.CatalogByName(ctx, c.Name)
			if err != nil {
				t.Fatal(err)
			}
			next := def
			next.Prompt = "new"
			description := "new"
			dbtest.FailNextStatement(t, db, fault)
			if out, err := s.EditCatalog(ctx, c.ID, CatalogPatch{Description: &description, Definition: &next}, "alice"); err == nil || out != nil {
				t.Fatalf("failed edit returned success: %+v %v", out, err)
			}
			after, err := q.CatalogByName(ctx, c.Name)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("partial metadata/version: %+v %v", after, err)
			}
			if _, err := q.CatalogVersion(ctx, wowsql.CatalogVersionParams{CatalogID: c.ID, Version: 2}); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("failed version survived: %v", err)
			}
		})
	}
}
func TestConcurrentIdenticalCatalogEditsPublishOneVersion(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := New(db, Dependencies{})
	ctx := context.Background()
	c, err := s.CreateCatalogChange(ctx, "edit", "old", CatalogDef{Workflow: "implement", Type: "chore", Prompt: "old"}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PromoteCatalogChange(ctx, c.Name, "verified", "alice"); err != nil {
		t.Fatal(err)
	}
	next := CatalogDef{Workflow: "implement", Type: "chore", Prompt: "new"}
	description := "new"
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if _, err := s.EditCatalog(ctx, c.ID, CatalogPatch{Description: &description, Definition: &next}, "alice"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	row, err := wowsql.New(db.SQL()).CatalogByName(ctx, c.Name)
	if err != nil || row.LatestVersion != 2 || row.PromotedVersion != 1 || row.Description != "new" {
		t.Fatalf("edit publication: %+v %v", row, err)
	}
}
