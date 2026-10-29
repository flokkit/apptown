package apptown

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestTransparentReadRevocationSurvivesStartup(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{AuthMode: "internal", AuthHeader: "", AuthProxySecret: ""})

	if err := a.EnsureAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Access.EnsureUser(context.Background(), "reader"); err != nil {
		t.Fatal(err)
	}
	reader := access.Principal{Kind: "user", Name: "reader"}
	resource := access.Resource{Type: "project", ID: "1", ProjectID: idstest.N(1)}
	if !mayRead(t, a, reader, resource) {
		t.Fatal("initial read grant did not authorize the reader")
	}
	id := testGrantID(t, a.DB, "group", identity.GroupAllUsers, access.RoleAuditor, "", access.ScopeInstance, "")
	if id.IsZero() {
		t.Fatal("first initialization did not grant transparent reads")
	}
	if err := a.EnsureAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	if grants := liveGrants(t, a, access.ScopeInstance, "", "group", identity.GroupAllUsers, access.RoleAuditor); len(grants) != 1 || grants[0].ID != id {
		t.Fatalf("second initialization duplicated transparent reads: %+v", grants)
	}
	if !revokeTestGrant(t, a, id, "admin") {
		t.Fatal("revoke transparent reads")
	}
	// A fresh App on the same database exercises startup without in-memory state.
	restarted := newApp(t, a.DB, Config{AuthMode: "internal"})
	if err := restarted.EnsureAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: revoked grants have no owner read; counting live and revoked rows proves startup recorded no new grant, not even one it revoked.
	completionCount(t, a.DB, 1, `SELECT COUNT(*) FROM grants WHERE subject=$1 AND role_name=$2`, identity.GroupAllUsers, access.RoleAuditor)
	if got := testGrantID(t, restarted.DB, "group", identity.GroupAllUsers, access.RoleAuditor, "", access.ScopeInstance, ""); !got.IsZero() {
		t.Fatalf("startup recreated revoked authority: grant %d", got)
	}
	if mayRead(t, restarted, reader, resource) {
		t.Fatal("reader retained revoked access after restart")
	}
	// Revocation changes the bootstrap default, not the ability to grant explicitly.
	regranted := restarted.addGrant("group", identity.GroupAllUsers, access.RoleAuditor, "", access.ScopeInstance, "", "", "admin", "reopen reads")
	if regranted.IsZero() || regranted == id {
		t.Fatal("explicit regrant did not create a new grant")
	}
	if err := restarted.EnsureAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: revoked grants have no owner read; the revoked default must still be recorded beside the regrant.
	completionCount(t, a.DB, 2, `SELECT COUNT(*) FROM grants WHERE subject=$1 AND role_name=$2`, identity.GroupAllUsers, access.RoleAuditor)
	// sql-guarantee: LiveGrant cannot tell a revoked grant from a missing one.
	completionCount(t, a.DB, 1, `SELECT COUNT(*) FROM grants WHERE id=$1 AND revoked_at IS NOT NULL`, id)
	if !mayRead(t, restarted, reader, resource) {
		t.Fatal("explicit read regrant did not authorize the reader")
	}
}

func testGrantID(t *testing.T, db *database.DB, subjectType, subject, role, capability string, scope access.ScopeKind, scopeID string) ids.ID {
	t.Helper()
	grants, err := identity.GrantsAtIn(context.Background(), db, scope, scopeID)
	if err != nil {
		t.Fatal(err)
	}
	var id ids.ID
	for _, grant := range grants {
		if grant.SubjectType != subjectType || grant.Subject != subject || grant.RoleName != role || string(grant.Capability) != capability || grant.RevokedAt.Valid {
			continue
		}
		if id.IsZero() || grant.ID.Compare(id) < 0 {
			id = grant.ID
		}
	}
	return id
}

func revokeTestGrant(t *testing.T, a *App, id ids.ID, by string) bool {
	t.Helper()
	var changed bool
	if err := a.DB.InTx(context.Background(), func(tx *database.Tx) error {
		var err error
		changed, err = identity.RevokeIn(context.Background(), tx, id, by, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return changed
}

func TestAuthorizationDefaultsFailExplicitlyAndKeepRevocations(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	drop := vetoAuthorizationRecord(t, db, "grants", "INSERT", "RETURN NULL;")
	a := constructApp(t, db, Config{DataDir: t.TempDir(), AuthMode: "internal"})
	if err := a.EnsureAuthorization(context.Background()); err == nil {
		t.Fatal("startup accepted a missing default")
	}
	drop()
	a = constructApp(t, a.DB, Config{DataDir: t.TempDir()})
	if err := a.EnsureAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	grants, err := a.Access.AllGrants(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range grants {
		if grant.Subject != "anonymous" {
			continue
		}
		changed, err := a.Access.Revoke(context.Background(), grant.ID, "test")
		if err != nil || !changed {
			t.Fatalf("revoke anonymous grant %d: changed=%v err=%v", grant.ID, changed, err)
		}
	}
	if err := a.EnsureAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: revoked grants have no owner read; the revocation must survive as a recorded choice.
	completionCount(t, a.DB, 1, `SELECT COUNT(*) FROM grants WHERE subject='anonymous' AND revoked_at IS NOT NULL`)
	live, err := a.Access.AllGrants(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range live {
		if grant.Subject == "anonymous" {
			t.Fatalf("startup restored a revoked anonymous grant: %+v", grant)
		}
	}
}

// A load balancer asks an AppTown in internal mode whether it is up without
// signing in, through the whole handler.
func TestHealthNeedsNoSignIn(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{AuthMode: "internal"})
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 || w.Body.String() != "ok\n" {
		t.Fatalf("health in internal mode: %d %q", w.Code, w.Body.String())
	}
}

// mayRead is the reader's project:read answer, asked as a question.
func mayRead(t *testing.T, a *App, pr access.Principal, res access.Resource) bool {
	t.Helper()
	dec, err := a.Access.Explain(context.Background(), pr, access.Permission{Capability: access.CapProjectRead, Resource: res})
	if err != nil {
		t.Fatal(err)
	}
	return dec.Allow
}
