package dbschema

import (
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database"
)

// Migration 161 revokes each token made before purposes and keeps it: a
// used one is still its account's evidence of signing in (its last use
// stays), one already revoked keeps its time, and cli and agent tokens are
// untouched. Afterwards, on a migrated database as on a fresh one, a live
// token must name a purpose, and every token must say which it has.
//
// Kills: deleting the purpose-less rows, revoking a classified token,
// moving an earlier revocation, or a CHECK or default that still admits a
// live purpose-less token.
func TestMigration161RevokesTokensWithoutAPurposeAndKeepsThem(t *testing.T) {
	t.Parallel()
	db := releaseDB(t)
	const boss = "01a1323b-3213-7000-8853-c2d607475a85" // a person in the release's records
	// sql-guarantee: the release's records before the migration; the build
	// that wrote them is not this one, so no owner operation can.
	for _, row := range []struct {
		name, purpose     string
		lastUsed, revoked any
	}{
		{"old-used", "", "2026-09-12T08:00:00Z", nil},
		{"old-revoked", "", nil, "2026-09-20T09:00:00Z"},
		{"cli", "cli", "2026-10-21T10:00:00Z", nil},
		{"agent", "agent", nil, nil},
	} {
		if _, err := db.Exec(`INSERT INTO auth_tokens (name, principal_id, token_hash, created_at, last_used, revoked_at, purpose) VALUES (?, ?, ?, '2026-09-01T00:00:00Z', ?, ?, ?)`,
			row.name, boss, "hash-"+row.name, row.lastUsed, row.revoked, row.purpose); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	type token struct {
		lastUsed, revoked *time.Time
	}
	got := map[string]token{}
	rows, err := db.Query(`SELECT name, last_used, revoked_at FROM auth_tokens`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		var tok token
		if err := rows.Scan(&name, &tok.lastUsed, &tok.revoked); err != nil {
			t.Fatal(err)
		}
		got[name] = tok
	}
	_ = rows.Close()
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	if tok, ok := got["old-used"]; !ok || tok.lastUsed == nil || !tok.lastUsed.Equal(at("2026-09-12T08:00:00Z")) || tok.revoked == nil || !tok.revoked.Equal(at("2026-10-22T00:00:00Z")) {
		t.Fatalf("the used purpose-less token after 161: %+v (kept %v)", tok, ok)
	}
	if tok, ok := got["old-revoked"]; !ok || tok.revoked == nil || !tok.revoked.Equal(at("2026-09-20T09:00:00Z")) {
		t.Fatalf("the revoked purpose-less token after 161: %+v (kept %v)", tok, ok)
	}
	for _, name := range []string{"cli", "agent"} {
		if tok, ok := got[name]; !ok || tok.revoked != nil {
			t.Fatalf("the %s token after 161: %+v (kept %v)", name, tok, ok)
		}
	}
	wantPurposeRequired(t, db, boss)

	fresh := emptyDB(t)
	if _, err := Migrate(t.Context(), fresh); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: a person for the fresh database's token rows; the
	// storage rule is what is under test.
	if _, err := fresh.Exec(`INSERT INTO principals (id, kind, name, created_at) VALUES (?, 'person', 'boss', '2026-10-22T00:00:00Z')`, boss); err != nil {
		t.Fatal(err)
	}
	wantPurposeRequired(t, fresh, boss)
}

// wantPurposeRequired: a live token without a purpose, and a token naming
// none, are refused; a revoked purpose-less one is not.
func wantPurposeRequired(t *testing.T, db *database.DB, principal string) {
	t.Helper()
	// sql-guarantee: the storage rule itself, which no operation can reach
	// around.
	if _, err := db.Exec(`INSERT INTO auth_tokens (name, principal_id, token_hash, created_at, purpose) VALUES ('live-blank', ?, 'hash-live-blank', '2026-10-22T00:00:00Z', '')`, principal); err == nil {
		t.Error("a live token without a purpose was stored")
	}
	if _, err := db.Exec(`INSERT INTO auth_tokens (name, principal_id, token_hash, created_at) VALUES ('unnamed', ?, 'hash-unnamed', '2026-10-22T00:00:00Z')`, principal); err == nil {
		t.Error("a token naming no purpose was stored")
	}
	if _, err := db.Exec(`INSERT INTO auth_tokens (name, principal_id, token_hash, created_at, revoked_at, purpose) VALUES ('revoked-blank', ?, 'hash-revoked-blank', '2026-10-22T00:00:00Z', '2026-10-22T00:00:00Z', '')`, principal); err != nil {
		t.Errorf("a revoked token without a purpose: %v", err)
	}
}
