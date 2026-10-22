package identity

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A project's pull token is minted for each environment that needs one,
// prefixed so it is recognisable, and stored only as its hash: every token
// minted resolves to its project.
func TestPullTokensAndConsents(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	testAccounts(t, db, "alice", "bob")
	s := New(db, Config{})
	first, err := s.RuntimePullToken(context.Background(), idstest.N(7))
	if err != nil || !strings.HasPrefix(first, PullTokenPrefix) {
		t.Fatalf("mint: %q %v", first, err)
	}
	second, err := s.RuntimePullToken(context.Background(), idstest.N(7))
	if err != nil || second == first {
		t.Fatalf("a second environment's token: %q %v", second, err)
	}
	for _, tok := range []string{first, second} {
		if pid, found, err := s.ProjectOfPullToken(context.Background(), tok); err != nil || !found || pid != idstest.N(7) {
			t.Fatalf("project of token: %v %v %d", err, found, pid)
		}
	}
	if _, found, err := s.ProjectOfPullToken(context.Background(), PullTokenPrefix+"unknown"); err != nil || found {
		t.Fatalf("an unknown token: %v %v", err, found)
	}
	// What is stored is the hash (the table has no token column,
	// dbschema's migration test); presenting the hash is no token.
	if _, found, err := s.ProjectOfPullToken(context.Background(), HashToken(first)); err != nil || found {
		t.Fatalf("the stored hash worked as a token: %v %v", found, err)
	}
	// A token that cannot be stored is an error, never an empty token.
	t.Cleanup(db.FailNextStatement("-- name: InsertPullToken"))
	if tok, err := s.RuntimePullToken(context.Background(), idstest.N(7)); err == nil || tok != "" {
		t.Fatalf("a token not stored was handed out: %q %v", tok, err)
	}
	o := s.oauth
	if err := o.RecordConsent(context.Background(), "h1", "alice", "b1", `{"client_id":"c"}`, time.Minute); err != nil {
		t.Fatal(err)
	}
	if ok, err := o.ConsumeConsent(context.Background(), "h1", "bob", "b1", `{"client_id":"c"}`); err != nil || ok {
		t.Fatalf("another person cannot claim it: %v %v", err, ok)
	}
	if ok, _ := o.ConsumeConsent(context.Background(), "h1", "alice", "b2", `{"client_id":"c"}`); ok {
		t.Fatal("another browser cannot claim it")
	}
	if ok, _ := o.ConsumeConsent(context.Background(), "h1", "alice", "b1", `{"client_id":"other"}`); ok {
		t.Fatal("a modified request cannot claim it")
	}
	if ok, err := o.ConsumeConsent(context.Background(), "h1", "alice", "b1", `{"client_id":"c"}`); err != nil || !ok {
		t.Fatalf("the person, browser and request claim it: %v %v", err, ok)
	}
	if ok, _ := o.ConsumeConsent(context.Background(), "h1", "alice", "b1", `{"client_id":"c"}`); ok {
		t.Fatal("claimed once")
	}
	// A request lives its lifetime by AppTown's clock, which also judges
	// it: with the database's clock moved a minute either way, an expired
	// request is refused and a live one claimed.
	for _, by := range []string{"-1 minute", "1 minute"} {
		moveDatabaseClock(t, db, by)
		if err := o.RecordConsent(context.Background(), "expired"+by, "alice", "b1", `{}`, time.Nanosecond); err != nil {
			t.Fatal(err)
		}
		if ok, _ := o.ConsumeConsent(context.Background(), "expired"+by, "alice", "b1", `{}`); ok {
			t.Fatalf("an expired request was claimed, with the database's clock moved %s", by)
		}
		if err := o.RecordConsent(context.Background(), "live"+by, "alice", "b1", `{}`, 30*time.Second); err != nil {
			t.Fatal(err)
		}
		if ok, err := o.ConsumeConsent(context.Background(), "live"+by, "alice", "b1", `{}`); err != nil || !ok {
			t.Fatalf("a live request was refused, with the database's clock moved %s: %v", by, err)
		}
	}
}

// moveDatabaseClock makes now() in db read the real clock moved by (an
// interval such as "-1 hour") on every connection made from here on, so a
// test can show which clock a rule reads: only the database's is moved.
func moveDatabaseClock(t *testing.T, db *database.DB, by string) {
	t.Helper()
	for _, statement := range []string{
		`CREATE OR REPLACE FUNCTION public.now() RETURNS timestamptz LANGUAGE plpgsql STABLE AS $$ BEGIN RETURN pg_catalog.now() + interval '` + by + `'; END $$`,
		`DO $$ BEGIN EXECUTE format('ALTER DATABASE %I SET search_path = public, pg_catalog', current_database()); END $$`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	db.Pool().SetMaxIdleConns(0)
	if _, err := db.Exec(`DO $$ BEGIN IF now() = pg_catalog.now() THEN RAISE EXCEPTION 'the database''s clock was not moved'; END IF; END $$`); err != nil {
		t.Fatal(err)
	}
}
