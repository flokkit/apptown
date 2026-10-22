package identity

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
)

// Test observations through the owners' own reads, so a renamed column breaks
// compilation instead of silently changing what an assertion means.

// ledger is every receipt of exactly one kind, read through the audit owner.
// The read is bounded, so it refuses to answer when the bound is reached.
func ledger(t *testing.T, q database.Queryer, kind string) []audit.Entry {
	t.Helper()
	all, err := audit.Recent(context.Background(), q, audit.Filter{KindPrefix: kind, Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == audit.MaxLimit {
		t.Fatalf("%d or more %q receipts: a bounded read cannot count them", audit.MaxLimit, kind)
	}
	var out []audit.Entry
	for _, e := range all {
		if kind == "" || e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// liveGrants is every unrevoked grant naming subject, narrowed to role when
// one is given.
func liveGrants(t *testing.T, q database.Queryer, subject, role string) []accesssql.GrantsNamed {
	t.Helper()
	all, err := queries(q).AllGrants(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []accesssql.GrantsNamed
	for _, g := range all {
		if (subject == "" || g.Subject == subject) && (role == "" || g.RoleName == role) {
			out = append(out, g)
		}
	}
	return out
}

// recordedGrants is every grant row naming subject (any subject when empty),
// narrowed to role when one is given, revoked or not. Rollback and history
// checks need it: AllGrants reads only unrevoked grants, so a failed command
// that left a revoked grant behind would look clean through it.
func recordedGrants(t *testing.T, q database.Queryer, subject, role string) []accesssql.GrantsNamed {
	t.Helper()
	// sql-guarantee: no identity read lists revoked grants; a rolled-back or historical grant row must be seen whatever its revocation state.
	rows, err := q.SQL().QueryContext(context.Background(), `SELECT id, subject, role_name, scope_type, scope_id, expires_at, revoked_at FROM grants_named ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []accesssql.GrantsNamed
	for rows.Next() {
		var g accesssql.GrantsNamed
		if err := rows.Scan(&g.ID, &g.Subject, &g.RoleName, &g.ScopeType, &g.ScopeID, &g.ExpiresAt, &g.RevokedAt); err != nil {
			t.Fatal(err)
		}
		if (subject == "" || g.Subject == subject) && (role == "" || g.RoleName == role) {
			out = append(out, g)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// memberships is every recorded membership of username, live or not.
func memberships(t *testing.T, q database.Queryer, username string) []accesssql.GroupMembersNamed {
	t.Helper()
	all, err := queries(q).MembershipsAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []accesssql.GroupMembersNamed
	for _, m := range all {
		if m.Username == username {
			out = append(out, m)
		}
	}
	return out
}

// groupRecorded reports whether a group of that name exists, retired or not.
func groupRecorded(t *testing.T, q database.Queryer, name string) bool {
	t.Helper()
	_, err := queries(q).GroupByName(context.Background(), name)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

// userRecorded reports whether an account of that name exists.
func userRecorded(t *testing.T, q database.Queryer, name string) bool {
	t.Helper()
	_, err := queries(q).UserByName(context.Background(), name)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

// sessionStored reports whether the session issued as token is still held.
func sessionStored(t *testing.T, q database.Queryer, token string) bool {
	t.Helper()
	_, err := queries(q).SessionByHash(context.Background(), HashToken(token))
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

func wantLen[T any](t *testing.T, what string, got []T, want int) {
	t.Helper()
	if len(got) != want {
		t.Fatalf("%s: %d want %d", what, len(got), want)
	}
}

// storedTokens is every personal token row, revoked or not.
func storedTokens(t *testing.T, q database.Queryer) []accesssql.AuthTokensNamed {
	t.Helper()
	all, err := queries(q).AuthTokensAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// tokenReceipts counts the issue and revoke receipts together.
func tokenReceipts(t *testing.T, q database.Queryer) int {
	t.Helper()
	return len(ledger(t, q, "token.created")) + len(ledger(t, q, "token.revoked"))
}
