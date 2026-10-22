package identity

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type countedQueries struct {
	database.Handle
	reads  int
	writes int
}

func (d *countedQueries) SQL() database.Handle {
	return d.Handle.WithExecutor(d)
}
func (d *countedQueries) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	d.writes++
	return d.DBTX.ExecContext(ctx, q, args...)
}
func (d *countedQueries) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	d.reads++
	return d.DBTX.QueryContext(ctx, q, args...)
}
func (d *countedQueries) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	d.reads++
	return d.DBTX.QueryRowContext(ctx, q, args...)
}

func TestGrantLookupIsOneReadAndUsesCurrentAuthority(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	svc := New(db, Config{})
	if err := svc.SeedAdministrator(context.Background(), "boss"); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureDefaults(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	grantsStore := NewGrants(db)
	put := func(in Spec) ids.ID {
		t.Helper()
		id, err := grantsStore.Put(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	user := "query-user"
	grantIDs := make([]ids.ID, 5)
	for i := range grantIDs {
		group := fmt.Sprintf("query-group-%d", i)
		if err := svc.SaveGroup(context.Background(), GroupChange{Name: group, Members: []string{user}}, testPrincipal("boss")); err != nil {
			t.Fatal(err)
		}
		role := access.RoleProjectViewer
		if i == 2 {
			role = access.RoleProjectApprover
		}
		grantIDs[i] = put(Spec{SubjectType: "group", Subject: group, Role: role, Scope: access.ScopeProject, ScopeID: idstest.N(1).String(), By: "test"})
	}
	// A same-named group is not the user, and explicit built-in membership
	// must not duplicate the grant automatically held by signed-in users.
	unrelated := put(Spec{SubjectType: "group", Subject: user, Role: access.RoleProjectOwner, Scope: access.ScopeProject, ScopeID: idstest.N(1).String(), By: "test"})
	if err := svc.SaveGroup(context.Background(), GroupChange{Name: GroupAllUsers, Members: []string{user}}, testPrincipal("boss")); err != nil {
		t.Fatal(err)
	}
	direct := put(Spec{SubjectType: "user", Subject: user, Role: access.RoleProjectContrib, Scope: access.ScopeProject, ScopeID: idstest.N(1).String(), By: "test"})
	counted := &countedQueries{Handle: db.SQL()}
	pr := access.Principal{Kind: "user", Name: user}
	grants, err := ActiveGrantsIn(context.Background(), counted, pr, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if counted.reads != 1 || len(grants) != 7 {
		t.Fatalf("five groups: %d reads, %d grants", counted.reads, len(grants))
	}
	seen := map[ids.ID]bool{}
	for _, g := range grants {
		if seen[g.ID] || g.ID == unrelated {
			t.Fatalf("duplicate or unrelated grant: %+v", g)
		}
		seen[g.ID] = true
	}
	if !seen[direct] || !slices.ContainsFunc(grants, func(g access.Grant) bool {
		return g.RoleName == access.RoleProjectApprover && g.ScopeType == access.ScopeProject && g.ScopeID == idstest.N(1).String()
	}) {
		t.Fatal("direct or inherited authority missing")
	}
	// sql-guarantee: Keep a revoked membership row to test the revoked_at filter independently of absence; existing membership mutations only insert or delete rows.
	if _, err := db.Exec(`UPDATE group_members SET revoked_at=now() WHERE principal_id=? AND group_name='query-group-0'`, principalOf(t, db, user)); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: Keep an expired membership row to test expires_at independently of revocation or absence; no named membership mutation accepts expiry.
	if _, err := db.Exec(`UPDATE group_members SET expires_at=now()-interval '1 second' WHERE principal_id=? AND group_name='query-group-1'`, principalOf(t, db, user)); err != nil {
		t.Fatal(err)
	}
	expireStoredGrant(t, db, grantIDs[2])
	if changed, err := grantsStore.Revoke(context.Background(), grantIDs[3], "boss"); err != nil || !changed {
		t.Fatalf("revoke: %v %v", changed, err)
	}
	counted.reads = 0
	grants, err = ActiveGrantsIn(context.Background(), counted, pr, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if counted.reads != 1 || len(grants) != 3 || slices.ContainsFunc(grants, func(g access.Grant) bool {
		return g.RoleName == access.RoleProjectApprover && g.ScopeType == access.ScopeProject && g.ScopeID == idstest.N(1).String()
	}) {
		t.Fatalf("stale authority retained: reads=%d grants=%+v", counted.reads, grants)
	}
	for _, g := range grants {
		for _, revoked := range grantIDs[:4] {
			if g.ID == revoked {
				t.Fatalf("expired/revoked authority retained: %+v", g)
			}
		}
	}
	anonymous, err := ActiveGrantsIn(context.Background(), counted, access.Principal{Kind: "anonymous"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range anonymous {
		if g.SubjectType != "anonymous" {
			t.Fatalf("anonymous inherited user authority: %+v", g)
		}
	}
	t.Cleanup(db.FailNextStatement("ActiveGrants"))
	if grants, err := ActiveGrantsIn(context.Background(), counted, pr, time.Now()); err == nil || grants != nil {
		t.Fatalf("failed authority read: %+v %v", grants, err)
	}
}
