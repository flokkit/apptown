package integration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/apptown/integration/internal/integrationsql"
)

func TestBindingSelectionPreservesOrderVersionsAndTransactions(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	bindings := NewWithClock(db, nil)
	const raw = `{"url":"https://errors.invalid/","tokenFrom":["ERRORS_TOKEN"],"unknown":9007199254740993}`
	for _, name := range []string{"z-first", "a-second"} {
		for version := 1; version <= 7; version++ {
			config := fmt.Sprintf(`{"revision":%d}`, version)
			if version == 7 {
				config = raw
			}
			if got, err := bindings.Upsert(t.Context(), name, "errors", "test", config, false); err != nil || got != version {
				t.Fatalf("%s version %d: got %d: %v", name, version, got, err)
			}
		}
	}

	selected, err := Selected(context.Background(), db, "errors", "")
	if err != nil || selected.Name != "z-first" || selected.Provider != "test" || selected.Version != 7 || !strings.Contains(selected.ConfigJSON, "9007199254740993") {
		t.Fatalf("default snapshot: %+v %v", selected, err)
	}
	if _, err := Selected(context.Background(), db, "model", "z-first"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("capability mismatch: %v", err)
	}
	if _, err := Selected(context.Background(), db, "errors", "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing binding: %v", err)
	}
	all, err := ForCapability(context.Background(), db, "errors")
	if err != nil || len(all) != 2 || all[0].Name != "z-first" || all[1].Name != "a-second" {
		t.Fatalf("binding order: %+v %v", all, err)
	}

	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := integrationsql.New(tx.SQL()).BindingByName(context.Background(), "z-first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := integrationsql.New(tx.SQL()).SetBindingConfiguration(context.Background(), integrationsql.SetBindingConfigurationParams{ID: current.ID, ConfigJson: current.ConfigJson, Version: 8, UpdatedAt: current.UpdatedAt}); err != nil {
		t.Fatal(err)
	}
	inside, err := Selected(context.Background(), tx, "errors", "z-first")
	if err != nil {
		t.Fatal(err)
	}
	outside, err := Selected(context.Background(), db, "errors", "z-first")
	if err != nil || inside.Version != 8 || outside.Version != 7 {
		t.Fatalf("transaction escaped: inside=%+v outside=%+v err=%v", inside, outside, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(dbtest.NextStatementFailure(db, "-- name: Bindings :many"))
	if _, err := Selected(context.Background(), db, "errors", ""); err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failure became absence: %v", err)
	}
}

func TestBindingListDiscardsPartialScan(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	bindings := NewWithClock(db, nil)
	for _, name := range []string{"z-first", "a-second"} {
		if _, err := bindings.Upsert(t.Context(), name, "errors", "test", `{}`, false); err != nil {
			t.Fatal(err)
		}
	}
	// This malformed owner row is the representation under test: a later scan
	// failure must discard every earlier valid binding.
	if _, err := db.SQL().ExecContext(context.Background(), `ALTER TABLE integrations RENAME TO integration_rows`); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: the view that serves one malformed version row to the owner's scan; DDL for the representation under test, not a read
	if _, err := db.SQL().ExecContext(context.Background(), `CREATE VIEW integrations AS SELECT id,name,capability,provider,config_json,
 CASE WHEN name='a-second' THEN 'invalid' ELSE version::text END AS version, managed, created_at, updated_at FROM integration_rows`); err != nil {
		t.Fatal(err)
	}
	if got, err := ForCapability(context.Background(), db, "errors"); err == nil || got != nil {
		t.Fatalf("partial binding list escaped: %+v %v", got, err)
	}
}
