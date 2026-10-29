package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/database"
)

func addUser(t *testing.T, db *database.DB, name, role string) {
	t.Helper()
	svc := New(db, Config{})
	if role == "admin" {
		if err := svc.SeedAdministrator(context.Background(), name); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := svc.EnsureUser(context.Background(), name); err != nil {
		t.Fatal(err)
	}
}

// A grant sits at an instance, a group, a project or a release target, and
// a role only at its own scope: a grant anywhere else would confer nothing.
func TestGrantsSitOnlyWhereTheyCanApply(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	addUser(t, db, "alice", "member")
	g := NewGrants(db)
	for _, spec := range []Spec{
		{SubjectType: "user", Subject: "alice", Capability: string(access.CapChangeRead), Scope: "change", ScopeID: idstest.N(7).String(), By: "ops"},
		{SubjectType: "user", Subject: "alice", Capability: string(access.CapRunRead), Scope: "run", ScopeID: idstest.N(7).String(), By: "ops"},
		{SubjectType: "user", Subject: "alice", Capability: string(access.CapConformanceRead), Scope: "dimension", ScopeID: "security", By: "ops"},
		{SubjectType: "user", Subject: "alice", Role: access.RoleProjectViewer, Scope: access.ScopeInstance, By: "ops"},
	} {
		if _, err := g.Add(context.Background(), spec, ""); err == nil {
			t.Fatalf("a grant at %s with %q was recorded", spec.Scope, spec.Role+spec.Capability)
		}
	}
	if _, err := g.Add(context.Background(), Spec{SubjectType: "user", Subject: "alice", Role: access.RoleReleaseManager, Scope: access.ScopeTarget, ScopeID: "*", By: "ops"}, ""); err != nil {
		t.Fatalf("a release manager is granted at a release target: %v", err)
	}
}

// An identical live grant is returned, never duplicated; a revoked one
// leaves history and a fresh grant is a new row; revocation is receipted.
func TestGrantsAreNeverDuplicatedOrEdited(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	addUser(t, db, "alice", "member")
	g := NewGrants(db)
	spec := Spec{SubjectType: "user", Subject: "alice", Role: access.RoleProjectViewer, Scope: access.ScopeProject, ScopeID: idstest.N(7).String(), By: "ops"}
	id1, err := g.Add(context.Background(), spec, "")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := g.Add(context.Background(), spec, "")
	if err != nil || id2 != id1 {
		t.Fatalf("identical live grant must be returned: %v %d %d", err, id1, id2)
	}
	if _, err := g.Add(context.Background(), Spec{SubjectType: "user", Subject: "alice", Role: "nope", Scope: access.ScopeProject, ScopeID: idstest.N(7).String()}, ""); err == nil {
		t.Fatal("an unknown role is refused")
	}
	if ok, err := g.Revoke(context.Background(), id1, "ops"); err != nil || !ok {
		t.Fatalf("revoke: %v %v", err, ok)
	}
	if ok, err := g.Revoke(context.Background(), id1, "ops"); err != nil || ok {
		t.Fatalf("revoking twice is a no-op: %v %v", err, ok)
	}
	if ok, err := g.Revoke(context.Background(), idstest.N(9999), "ops"); err != nil || ok {
		t.Fatalf("revoking nothing is a no-op: %v %v", err, ok)
	}
	id3, err := g.Add(context.Background(), spec, "2030-01-01T00:00:00Z")
	if err != nil || id3 == id1 {
		t.Fatalf("after a revocation a new grant is a new row: %v %d", err, id3)
	}
	live, err := ActiveGrantsIn(context.Background(), db, access.Principal{Kind: "user", Name: "alice"}, time.Now())
	if err != nil || len(live) != 1 || live[0].ID != id3 || !live[0].ExpiresAt.Valid {
		t.Fatalf("active: %v %+v", err, live)
	}
	all, err := AllGrantsIn(context.Background(), db)
	if err != nil || len(all) != 1 {
		t.Fatalf("all live: %v %d", err, len(all))
	}
	added := 0
	for _, receipt := range ledger(t, db, "grant.added") {
		if receipt.ProjectID == idstest.N(7) {
			added++
		}
	}
	if revoked := len(ledger(t, db, "grant.revoked")); added != 2 || revoked != 1 {
		t.Fatalf("receipts: added=%d revoked=%d", added, revoked)
	}
	if _, _, found, err := g.Live(context.Background(), id1); err != nil || found {
		t.Fatal("a revoked grant is not live")
	}
	if scope, scopeID, found, _ := g.Live(context.Background(), id3); !found || scope != access.ScopeProject || scopeID != idstest.N(7).String() {
		t.Fatal("a live grant names its scope")
	}
}

// A person's active grants are their own, their groups' and everyone's;
// expired grants and revoked memberships do not count.
func TestActiveGrantsFollowMembership(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	addUser(t, db, "alice", "member")
	storeOrdinaryGroup(t, db, "kids")
	storeOrdinaryGroup(t, db, "old")
	if _, err := queries(db).InsertMembership(context.Background(), accesssql.InsertMembershipParams{GroupName: "kids", Username: "alice", CreatedBy: "ops", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: A revoked membership must exist beside its live group grant to prove it confers no authority; InsertMembership only creates live rows and deleting membership would bypass this filter.
	if _, err := db.Exec(`INSERT INTO group_members (group_name, principal_id, created_by, created_at, revoked_at) VALUES ('old', ?, 'ops', ?, ?)`, principalOf(t, db, "alice"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	g := NewGrants(db)
	for _, s := range []Spec{
		{SubjectType: "group", Subject: "kids", Role: access.RoleGroupMember, Scope: access.ScopeGroup, ScopeID: "kids"},
		{SubjectType: "group", Subject: "old", Role: access.RoleGroupOwner, Scope: access.ScopeGroup, ScopeID: "old"},
		{SubjectType: "group", Subject: GroupAllUsers, Role: access.RoleAuditor, Scope: access.ScopeInstance},
		{SubjectType: "user", Subject: "bob", Role: access.RoleProjectOwner, Scope: access.ScopeProject, ScopeID: idstest.N(1).String()},
	} {
		if _, err := g.Put(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.Add(context.Background(), Spec{SubjectType: "user", Subject: "alice", Role: access.RoleProjectViewer, Scope: access.ScopeProject, ScopeID: idstest.N(2).String()}, "2000-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	live, err := ActiveGrantsIn(context.Background(), db, access.Principal{Kind: "user", Name: "alice"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var subjects []string
	for _, l := range live {
		subjects = append(subjects, l.Subject)
	}
	if len(live) != 2 || !HasRole(live, access.RoleGroupMember, access.ScopeGroup, "kids") || !HasRole(live, access.RoleAuditor, access.ScopeInstance, "") {
		t.Fatalf("alice's live grants: %v", subjects)
	}
	anon, err := ActiveGrantsIn(context.Background(), db, access.Principal{Kind: "anonymous", Name: "anonymous"}, time.Now())
	if err != nil || len(anon) != 0 {
		t.Fatalf("anonymous has nothing here: %v %d", err, len(anon))
	}
	at, err := GrantsAtIn(context.Background(), db, access.ScopeGroup, "kids")
	if err != nil || len(at) != 1 {
		t.Fatalf("at scope: %v %d", err, len(at))
	}
}

// The defaults: the built-in group, every legacy administrator sovereign
// with a personal group, the open-mode reader granted once and revoked
// when the mode closes, the transparent-reads default once.
func TestDefaultsAndTheLastAdministrator(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	addUser(t, db, "root", "admin")
	addUser(t, db, "alice", "member")
	seed := func(open bool) {
		t.Helper()
		if err := db.InTx(context.Background(), func(tx *database.Tx) error { return DefaultsIn(context.Background(), tx, open, time.Now()) }); err != nil {
			t.Fatal(err)
		}
	}
	seed(true)
	root, _ := ActiveGrantsIn(context.Background(), db, access.Principal{Kind: "user", Name: "root"}, time.Now())
	if !HasRole(root, access.RoleInstanceAdmin, access.ScopeInstance, "") || !HasRole(root, access.RoleGroupOwner, access.ScopeGroup, "root") || !HasRole(root, access.RoleAuditor, access.ScopeInstance, "") {
		t.Fatalf("the administrator's defaults: %+v", root)
	}
	anon, _ := ActiveGrantsIn(context.Background(), db, access.Principal{Kind: "anonymous"}, time.Now())
	if !HasRole(anon, access.RoleAuditor, access.ScopeInstance, "") {
		t.Fatal("open mode: anonymous reads")
	}
	seed(true)
	all, _ := AllGrantsIn(context.Background(), db)
	if len(all) != 4 {
		t.Fatalf("seeding twice adds nothing: %d", len(all))
	}
	seed(false)
	anon, _ = ActiveGrantsIn(context.Background(), db, access.Principal{Kind: "anonymous"}, time.Now())
	if len(anon) != 0 {
		t.Fatal("closing the mode revokes the open reader")
	}
	seed(true)
	anon, _ = ActiveGrantsIn(context.Background(), db, access.Principal{Kind: "anonymous"}, time.Now())
	if len(anon) != 0 {
		t.Fatal("a revoked open reader is a recorded choice; reopening does not grant it again")
	}
	// The last administrator cannot be demoted.
	err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return RevokeAdminGrantsIn(context.Background(), tx, "root", "ops", time.Now())
	})
	if !errors.Is(err, ErrLastAdministrator) {
		t.Fatalf("last administrator: %v", err)
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return AdminGrantIn(context.Background(), tx, "alice", "ops", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return RevokeAdminGrantsIn(context.Background(), tx, "root", "ops", time.Now())
	}); err != nil {
		t.Fatalf("with another administrator, revocation proceeds: %v", err)
	}
	role, _, err := RoleIn(context.Background(), db, "root", time.Now())
	if err != nil || role != "member" {
		t.Fatalf("current grants determine root: %v %q", err, role)
	}
	if g, err := database.Transact(context.Background(), db, func(tx *database.Tx) (string, error) { return SovereignGroupIn(context.Background(), tx, time.Now()) }); err != nil || g != "alice" {
		t.Fatalf("the sovereign group is the first administrator's: %v %q", err, g)
	}
}

// Project ownership grants only the named creator; root needs no synthetic
// ownership. The creator also approves, because stock workflows name the
// approver role and a new project would otherwise have nobody to land it.
func TestProjectOwnership(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	addUser(t, db, "root", "admin")
	addUser(t, db, "alice", "member")
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return ProjectOwnershipIn(context.Background(), tx, idstest.N(5), "alice", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return ProjectOwnershipIn(context.Background(), tx, idstest.N(6), "anonymous", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	at5, _ := GrantsAtIn(context.Background(), db, access.ScopeProject, idstest.N(5).String())
	at6, _ := GrantsAtIn(context.Background(), db, access.ScopeProject, idstest.N(6).String())
	if len(at5) != 2 || !HasRole(at5, access.RoleProjectOwner, access.ScopeProject, idstest.N(5).String()) || !HasRole(at5, access.RoleProjectApprover, access.ScopeProject, idstest.N(5).String()) || len(at6) != 0 {
		t.Fatalf("ownership: %d %d", len(at5), len(at6))
	}
	if v, err := InstanceSettingIn(context.Background(), db, "nothing"); err != nil || v != "" {
		t.Fatalf("unset setting: %v %q", err, v)
	}
}

// A personal group is minted once and never repaired.
func TestPersonalGroup(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	addUser(t, db, "alice", "member")
	g := NewGrants(db)
	name, err := g.PersonalGroup(context.Background(), "alice", "apptown-system")
	if err != nil || name != "alice" {
		t.Fatalf("first: %v %q", err, name)
	}
	if again, err := g.PersonalGroup(context.Background(), "alice", "apptown-system"); err != nil || again != "alice" {
		t.Fatalf("again: %v %q", err, again)
	}
	storeOrdinaryGroup(t, db, "bob")
	if name, err := g.PersonalGroup(context.Background(), "bob", "apptown-system"); err != nil || name != "bob-personal" {
		t.Fatalf("a taken name steps aside: %v %q", err, name)
	}
	recorded, err := queries(db).GrantsAtScope(context.Background(), accesssql.GrantsAtScopeParams{ScopeType: "group", ScopeID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range recorded {
		if changed, err := g.Revoke(context.Background(), grant.ID, "apptown-system"); err != nil || !changed {
			t.Fatalf("revoke recorded choice: %v %v", changed, err)
		}
	}
	if _, err := g.PersonalGroup(context.Background(), "alice", "apptown-system"); err != nil {
		t.Fatalf("a revoked owner grant still names the group as recorded: %v", err)
	}
}
