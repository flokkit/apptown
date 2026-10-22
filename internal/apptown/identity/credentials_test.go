package identity

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A session is a hashed row with an expiry: resolved while live, removed
// on sight once expired, gone on logout.
func TestSessions(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	testAccounts(t, db, "alice")
	s := New(db, Config{SessionTTL: SessionTTL}).sessions
	tok, err := issueSessionIn(context.Background(), db, "alice", s.ttl, time.Now())
	if err != nil || len(tok) != 64 {
		t.Fatalf("issue: %v %q", err, tok)
	}
	if _, err := queries(db).SessionByHash(context.Background(), tok); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("the plaintext is never stored: %v", err)
	}
	if user, _, ok, err := s.Resolve(context.Background(), tok); err != nil || !ok || user != "alice" {
		t.Fatalf("resolve: %v %v %q", err, ok, user)
	}
	if _, _, ok, err := s.Resolve(context.Background(), "not-a-session"); err != nil || ok {
		t.Fatalf("unknown: %v %v", err, ok)
	}
	// sql-guarantee: Expire the already issued and successfully resolved session without replacing its token; InsertSession cannot update its expiry and deleting it would bypass expiry-triggered cleanup.
	if _, err := db.Exec(`UPDATE sessions SET expires_at=? WHERE token_hash=?`, time.Now().Add(-time.Minute), HashToken(tok)); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, _ := s.Resolve(context.Background(), tok); ok {
		t.Fatal("an expired session does not resolve")
	}
	if sessionStored(t, db, tok) {
		t.Fatal("the expired row is removed on sight")
	}
	var stored int
	// sql-guarantee: No owner read lists sessions; removal on sight must leave the table empty, not replace the expired row with another.
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("the expired row is removed on sight: %d %v", stored, err)
	}
	tok, _ = issueSessionIn(context.Background(), db, "alice", s.ttl, time.Now())
	if err := s.End(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, _ := s.Resolve(context.Background(), tok); ok {
		t.Fatal("logout ends the session")
	}
}

// A personal token: plaintext once, hash stored, its role bounded to
// member unless admin, listed without hashes, revoked with a receipt.
func TestTokens(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	testAccounts(t, db, "alice", "bob", "ops", "root")
	svc := New(db, Config{OpenInstance: true})
	testGrantRoot(t, svc, "ops")
	k := svc.tokens
	tok, err := testIssueToken(svc, "cli", "alice", "owner-of-everything", "ops")
	if err != nil || !strings.HasPrefix(tok, TokenPrefix) {
		t.Fatalf("create: %v %q", err, tok)
	}
	user, role, _, ok, err := k.Resolve(context.Background(), tok)
	if err != nil || !ok || user != "alice" || role != "member" {
		t.Fatalf("resolve: %v %v %q %q", err, ok, user, role)
	}
	if _, err := testIssueToken(svc, "cli", "bob", "admin", "ops"); err == nil {
		t.Fatal("token names are unique")
	}
	adm, _ := testIssueToken(svc, "root-cli", "root", "admin", "ops")
	if _, role, _, _, _ := k.Resolve(context.Background(), adm); role != "admin" {
		t.Fatalf("an admin token keeps its bound: %q", role)
	}
	all, err := svc.Tokens(context.Background(), testOpenPrincipal("ops"))
	if err != nil || len(all) != 2 || all[0].Name != "root-cli" || !all[0].LastUsed.Valid || all[1].RevokedAt.Valid {
		t.Fatalf("list: %v %+v", err, all)
	}
	if ok, err := svc.RevokeToken(context.Background(), "cli", testOpenPrincipal("ops")); err != nil || !ok {
		t.Fatalf("revoke: %v %v", err, ok)
	}
	if ok, err := svc.RevokeToken(context.Background(), "cli", testOpenPrincipal("ops")); err != nil || ok {
		t.Fatalf("revoking twice: %v %v", err, ok)
	}
	if _, _, _, ok, _ := k.Resolve(context.Background(), tok); ok {
		t.Fatal("a revoked token does not resolve")
	}
	if n := len(ledger(t, db, "token.created")) + len(ledger(t, db, "token.revoked")); n != 3 {
		t.Fatalf("receipts: %d", n)
	}
}

// The account's credential facts, and the bootstrap and seed rules.
func TestAccountCredentialFacts(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	d := &directoryStore{db: db}
	if needs, err := d.NeedsBootstrap(context.Background()); err != nil || !needs {
		t.Fatalf("empty instance needs bootstrap: %v %v", err, needs)
	}
	if err := d.SeedAdministrator(context.Background(), "root"); err != nil {
		t.Fatal(err)
	}
	if err := d.SeedAdministrator(context.Background(), "root"); err != nil {
		t.Fatal(err)
	}
	if root, err := RootIn(context.Background(), db, "root", time.Now()); err != nil || !root {
		t.Fatalf("seeded root: %v %v", root, err)
	}
	if needs, _ := d.NeedsBootstrap(context.Background()); needs {
		t.Fatal("explicit root setup must close bootstrap")
	}
	if err := testSaveAccount(db, accountEdit{Name: "root", Role: "admin", PasswordHash: "h"}, "setup"); err != nil {
		t.Fatal(err)
	}
	if needs, _ := d.NeedsBootstrap(context.Background()); needs {
		t.Fatal("a password ends bootstrap")
	}
	if a, err := d.login(context.Background(), "root"); err != nil || a == nil || a.passwordHash != "h" || a.Role != "admin" {
		t.Fatalf("account: %v", err)
	}
	if a, err := d.login(context.Background(), "ghost"); err != nil || a != nil {
		t.Fatalf("unknown account: %v", err)
	}

	if role, found, _ := d.Role(context.Background(), "root"); !found || role != "admin" {
		t.Fatalf("role: %v %q", found, role)
	}
}

// A consequential decision leaves a receipt; a denial also a ledger line
// pointing at it.
func TestRecordDecision(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	allow := access.Decision{Allow: true, GrantIDs: []ids.ID{idstest.N(3)}, Roles: []string{"project-owner@1"}, Reason: "alice may land: Project owner"}
	id, err := recordDecision(context.Background(), db, alice, full, access.CapChangeLand, change, access.Context{Route: "POST /land"}, allow, time.Now())
	if err != nil || id.IsZero() {
		t.Fatalf("allow receipt: %v %d", err, id)
	}
	deny := access.Decision{Code: access.DenyOutsideScope, Reason: "alice may not land here"}
	did, err := recordDecision(context.Background(), db, alice, full, access.CapChangeLand, change, access.Context{Route: "POST /land"}, deny, time.Now())
	if err != nil || did.IsZero() {
		t.Fatalf("deny receipt: %v %d", err, did)
	}
	var outcome, grants string
	// sql-guarantee: The decision receipt's stored encoding is the guarantee, and no owner read returns authorization_decisions rows.
	if err := db.QueryRow(`SELECT outcome, grant_ids_json::text FROM authorization_decisions WHERE id=?`, id).Scan(&outcome, &grants); err != nil || outcome != "allow" || grants != `["`+idstest.N(3).String()+`"]` {
		t.Fatalf("allow row: %v %q %q", err, outcome, grants)
	}
	var lines int
	// sql-guarantee: The ledger line's link to its decision receipt is a column no ledger read exposes.
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE kind='authz.denied' AND authorization_decision_id=? AND project_id=?`, did, idstest.N(7)).Scan(&lines); err != nil || lines != 1 {
		t.Fatalf("the denial's ledger line: %v %d", err, lines)
	}
	if denied := ledger(t, db, "authz.denied"); len(denied) != 1 {
		t.Fatalf("an allowance leaves no ledger line: %+v", denied)
	}
}

// A command-line token made with no role carries its person's own role as
// it is made, so the guide's "acts as you" holds for an administrator; an
// agent token stays member, and a role asked for is kept. The person is the
// token's, not the issuer's: an administrator's token for a member is a
// member's, and stays one when that member is promoted later.
//
// Kills: the member default, which gave the first administrator's CLI token
// no administrator authority (one-server test, 22 Oct).
func TestATokensDefaultCeilingIsItsPersonsRoleWhenMade(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	testAccounts(t, db, "alice", "ops")
	svc := New(db, Config{OpenInstance: true})
	testGrantRoot(t, svc, "ops")
	ops := testOpenPrincipal("ops")
	issue := func(name, user, role, purpose string) string {
		t.Helper()
		issued, err := svc.IssueToken(context.Background(), NewToken{Name: name, User: user, Role: role, Purpose: purpose}, ops)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return issued.Token.Role
	}
	for _, c := range []struct{ name, user, role, purpose, want string }{
		{"ops-cli", "", "", access.TokenCLI, "admin"},
		{"ops-agent", "", "", access.TokenAgent, "member"},
		{"ops-narrow", "", "member", access.TokenCLI, "member"},
		{"alice-cli", "alice", "", access.TokenCLI, "member"},
	} {
		if got := issue(c.name, c.user, c.role, c.purpose); got != c.want {
			t.Errorf("%s: ceiling %q, want %q", c.name, got, c.want)
		}
	}
	testGrantRoot(t, svc, "alice")
	all, err := svc.Tokens(context.Background(), ops)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range all {
		if tok.Name == "alice-cli" && tok.Role != "member" {
			t.Fatalf("promoting alice widened her earlier token: %+v", tok)
		}
	}
}
