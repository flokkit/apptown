package dbschema

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestMigration171AssociatesLocalIdentityWithoutRemoteProof(t *testing.T) {
	db := releaseDB(t)
	var previous []migration
	var change migration
	for _, m := range migrations {
		if m.n < 171 {
			previous = append(previous, m)
		}
		if m.n == 171 {
			change = m
		}
	}
	if len(change.stmts) == 0 {
		t.Fatal("missing provider scope migration")
	}
	if _, err := applyMigrations(t.Context(), db, previous); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: released rows predate connection identity; current owners cannot create this migration input.
	if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO integrations(id,name,capability,provider,config_json,created_at,updated_at) VALUES ($1,'scope-fixture','deploy','portainer','{}','2026-10-01T00:00:00Z','2026-10-01T00:00:00Z')`, idstest.N(180)); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: released direct-ref and named target records before migration171.
	if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO targets(id,name,kind,provider,address_json,created_at,updated_at) VALUES ($1,'scope-named','portainer','scope-fixture','{}','2026-10-01T00:00:00Z','2026-10-01T00:00:00Z'),($2,'scope-direct','portainer','','{"urlFrom":"URL","apiKeyFrom":"KEY"}','2026-10-01T00:00:00Z','2026-10-01T00:00:00Z'),($3,'scope-missing','portainer','missing-scope-connection','{}','2026-10-01T00:00:00Z','2026-10-01T00:00:00Z')`, idstest.N(181), idstest.N(182), idstest.N(183)); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: historical repository receipts preserve their original connector text.
	if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO owned_repositories(id,project_id,connector,registry,repository,recorded_at) SELECT $1,id,'scope-fixture','fixture-registry','owned-repo','2026-10-01T00:00:00Z' FROM projects ORDER BY id LIMIT 1`, idstest.N(184)); err != nil {
		t.Fatal(err)
	}
	through171 := append(append([]migration(nil), previous...), change)
	before := providerMigrationRows(t, db, false)
	// Failure after both DDL and backfill must restore schema and original rows.
	dbtest.FailNextStatement(t, db, "ALTER TABLE release_requests ADD COLUMN provider_binding_json")
	if _, err := applyMigrations(t.Context(), db, through171); err == nil {
		t.Fatal("injected final-statement failure was hidden")
	}
	if version, err := schemaVersion(context.Background(), db); err != nil || version != 170 {
		t.Fatalf("failed migration version %d: %v", version, err)
	}
	if after := providerMigrationRows(t, db, false); after != before {
		t.Fatal("failed migration changed original targets")
	}
	var columns int
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT count(*) FROM information_schema.columns WHERE table_name='targets' AND column_name='connection_id'`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("partial target DDL survived: %d %v", columns, err)
	}
	if _, err := applyMigrations(t.Context(), db, through171); err != nil {
		t.Fatal(err)
	}
	if after := providerMigrationRows(t, db, true); after != before {
		t.Fatal("association rewrote original target fields")
	}
	for _, n := range []int{181, 182, 183} {
		var connection sql.NullString
		var scope sql.NullString
		if err := db.SQL().QueryRowContext(context.Background(), `SELECT connection_id::text,provider_scope_json::text FROM targets WHERE id=$1`, idstest.N(uint64(n))).Scan(&connection, &scope); err != nil {
			t.Fatal(err)
		}
		want := n == 181
		if connection.Valid != want || (want && connection.String != idstest.N(180).String()) || scope.Valid {
			t.Fatalf("target %d: connection=%+v scope=%+v", n, connection, scope)
		}
	}
	var repositoryConnection, connector string
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT connection_id::text,connector FROM owned_repositories WHERE id=$1`, idstest.N(184)).Scan(&repositoryConnection, &connector); err != nil || repositoryConnection != idstest.N(180).String() || connector != "scope-fixture" {
		t.Fatalf("repository identity: %s %s %v", repositoryConnection, connector, err)
	}
	if n, err := applyMigrations(t.Context(), db, through171); err != nil || n != 0 {
		t.Fatalf("replay applied %d: %v", n, err)
	}
}

func providerMigrationRows(t *testing.T, db *database.DB, associated bool) string {
	t.Helper()
	expression := "to_jsonb(t)"
	if associated {
		expression += " - 'connection_id' - 'provider_scope_json'"
	}
	var raw []byte
	if err := db.SQL().QueryRowContext(context.Background(), fmt.Sprintf(`SELECT jsonb_agg(%s ORDER BY t.id) FROM targets t`, expression)).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(canonical)
}
