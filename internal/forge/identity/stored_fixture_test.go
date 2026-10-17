package identity

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/identity/internal/accesssql"
	"flokkit.io/forge/internal/ids"
)

// Credential admission probes need the account disabled without executing the
// administrator command that refuses losing the last root or ends its sessions.
func disableStoredAccount(t *testing.T, db *database.DB, name string) {
	t.Helper()
	q := queries(db)
	row, err := q.UserByName(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.SaveUser(context.Background(), accesssql.SaveUserParams{Username: row.Username, PrincipalID: row.PrincipalID, PasswordHash: row.PasswordHash, CreatedAt: row.CreatedAt, LastSeen: row.LastSeen, DefaultGroup: row.DefaultGroup, DisabledAt: sql.NullTime{Time: time.Now().UTC(), Valid: true}}); err != nil {
		t.Fatal(err)
	}
}

func storeOrdinaryGroup(t *testing.T, db *database.DB, name string) {
	t.Helper()
	if _, err := queries(db).SaveGroup(context.Background(), accesssql.SaveGroupParams{Name: name, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func expireStoredGrant(t *testing.T, db *database.DB, id ids.ID) {
	t.Helper()
	if _, err := queries(db).SetGrantExpiry(context.Background(), accesssql.SetGrantExpiryParams{ID: id, ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
}

// Keep the revoked and expired row for projection tests that must distinguish
// inactive authority from a missing record.
func revokeExpiredStoredGrant(t *testing.T, db *database.DB, r accesssql.GrantsNamed) {
	t.Helper()
	_, err := queries(db).RevokeGrant(context.Background(), accesssql.RevokeGrantParams{
		ID: r.ID, SubjectType: r.SubjectType, RoleName: r.RoleName, Capability: r.Capability,
		ScopeType: r.ScopeType, ScopeID: r.ScopeID, Origin: r.Origin, GrantedBy: r.GrantedBy, Reason: r.Reason,
		CreatedAt: r.CreatedAt, ReviewAt: r.ReviewAt, ExpiresAt: sql.NullTime{Time: time.Now().Add(-24 * time.Hour), Valid: true}, RevokedAt: sql.NullTime{Time: time.Now(), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func saveStoredDelegation(t *testing.T, db *database.DB, r accesssql.DelegationsNamed) {
	t.Helper()
	if _, err := queries(db).SaveDelegation(context.Background(), accesssql.SaveDelegationParams{
		ID: r.ID, ClientID: r.ClientID, ClientName: r.ClientName, Audience: r.Audience, Kind: r.Kind,
		ScopesJson: r.ScopesJson, GroupBoundsJson: r.GroupBoundsJson, ProjectBoundsJson: r.ProjectBoundsJson,
		AuthTime: r.AuthTime, AuthMethod: r.AuthMethod, CreatedAt: r.CreatedAt, LastUsed: r.LastUsed, ExpiresAt: r.ExpiresAt, RevokedAt: r.RevokedAt,
	}); err != nil {
		t.Fatal(err)
	}
}
