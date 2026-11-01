package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/database"
)

// Accounts: a name seen on the wire is recorded as a member; the first
// account bootstraps an administrator once; a real account is kept for the
// record while a bare name may be removed.
func TestAccounts(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	d := &directoryStore{db: db}
	if role, err := d.ensureRole(context.Background(), "drifter"); err != nil || role != "member" {
		t.Fatalf("ensure: %v %q", err, role)
	}
	if role, err := d.ensureRole(context.Background(), "drifter"); err != nil || role != "member" {
		t.Fatalf("ensure again: %v %q", err, role)
	}
	if _, err := New(db, Config{}).Bootstrap(context.Background(), NewAccount{Username: "root", Password: "first-password"}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db, Config{}).Bootstrap(context.Background(), NewAccount{Username: "again", Password: "other-password"}); !errors.Is(err, ErrAlreadyBootstrapped) {
		t.Fatalf("second bootstrap: %v", err)
	}
	if role, _ := d.ensureRole(context.Background(), "root"); role != "admin" {
		t.Fatalf("bootstrap makes an administrator: %q", role)
	}
	grants, _ := ActiveGrantsIn(context.Background(), db, access.Principal{Kind: "user", Name: "root"}, time.Now())
	if !HasRole(grants, access.RoleInstanceAdmin, access.ScopeInstance, "") {
		t.Fatal("bootstrap seeds the sovereign grant")
	}
	users, err := d.users(context.Background())
	if err != nil || len(users) != 2 || users[0].Username != "drifter" || users[0].HasPassword || !users[1].HasPassword || users[1].Role != "admin" {
		t.Fatalf("users: %v %+v", err, users)
	}
	if err := testRemoveName(db, "root", "ops"); !errors.Is(err, errRealAccount) {
		t.Fatalf("a real account stays: %v", err)
	}
	if err := testRemoveName(db, "nobody", "ops"); !errors.Is(err, errNoSuchName) {
		t.Fatalf("unknown name: %v", err)
	}
	if err := testRemoveName(db, "drifter", "ops"); err != nil {
		t.Fatal(err)
	}
	if users, _ := d.users(context.Background()); len(users) != 1 {
		t.Fatal("the bare name is gone")
	}
	if n := len(ledger(t, db, "auth.bootstrap")) + len(ledger(t, db, "user.name-removed")); n != 2 {
		t.Fatalf("receipts: %d", n)
	}
}

// Saving an account: the administrator grant follows the role; the last
// administrator cannot be demoted or disabled; disabling ends sessions and
// is what the decision rule sees.
func TestSaveFollowsTheRole(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	d := &directoryStore{db: db}
	if err := testSaveAccount(db, accountEdit{Name: "root", Role: "admin", PasswordHash: "h"}, "setup"); err != nil {
		t.Fatal(err)
	}
	yes := true
	if err := testSaveAccount(db, accountEdit{Name: "root", Role: "member"}, "ops"); !errors.Is(err, errLastAdministrator) {
		t.Fatalf("demoting the last administrator: %v", err)
	}
	if err := testSaveAccount(db, accountEdit{Name: "root", Role: "admin", Disabled: &yes}, "ops"); !errors.Is(err, errLastAdministrator) {
		t.Fatalf("disabling the last administrator: %v", err)
	}
	if err := testSaveAccount(db, accountEdit{Name: "alice", Role: "admin", PasswordHash: "h2"}, "ops"); err != nil {
		t.Fatal(err)
	}
	if _, err := queries(db).InsertSession(context.Background(), accesssql.InsertSessionParams{ID: ids.New(time.Now()), TokenHash: "t1", Username: "root", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := testSaveAccount(db, accountEdit{Name: "root", Role: "member"}, "ops"); err != nil {
		t.Fatalf("with another administrator the demotion proceeds: %v", err)
	}
	grants, _ := ActiveGrantsIn(context.Background(), db, access.Principal{Kind: "user", Name: "root"}, time.Now())
	if HasRole(grants, access.RoleInstanceAdmin, access.ScopeInstance, "") {
		t.Fatal("the sovereign grant follows the role down")
	}
	if err := testSaveAccount(db, accountEdit{Name: "root", Role: "member", Disabled: &yes}, "ops"); err != nil {
		t.Fatal(err)
	}
	if disabled, err := disabledIn(context.Background(), d.db, "root"); err != nil || !disabled {
		t.Fatalf("disabled: %v %v", err, disabled)
	}
	if disabled, err := disabledIn(context.Background(), d.db, "ghost"); err != nil || !disabled {
		t.Fatalf("an unknown name is refused like a disabled one: %v %v", err, disabled)
	}
	if _, err := queries(db).SessionByHash(context.Background(), "t1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("disabling ends sessions: %v", err)
	}
	var sessions int
	// sql-guarantee: No owner read lists an account's sessions; disabling must leave the account none, not only end the one this test knows.
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sessions_named WHERE username='root'`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("disabling ends sessions: %v %d", err, sessions)
	}
	group := "kids"
	if err := testSaveAccount(db, accountEdit{Name: "alice", Role: "admin", DefaultGroup: &group}, "ops"); err != nil {
		t.Fatal(err)
	}
	if def, err := d.defaultGroup(context.Background(), "alice"); err != nil || def != "kids" {
		t.Fatalf("default group: %v %q", err, def)
	}
}

// Groups: saved whole with their members and a receipt, listed built-in
// first, protected kinds never deleted, a project's roster revoked
// together.
func TestGroups(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	d := &directoryStore{db: db}
	if err := testSaveAccount(db, accountEdit{Name: "root", Role: "admin", PasswordHash: "h"}, "setup"); err != nil {
		t.Fatal(err)
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error { return defaultsIn(context.Background(), tx, false, time.Now()) }); err != nil {
		t.Fatal(err)
	}
	if err := New(db, Config{}).SaveGroup(context.Background(), GroupChange{Name: "kids", Description: "the children", Members: []string{"jude", "root"}}, admittedPrincipal(t, New(db, Config{}), "root")); err != nil {
		t.Fatal(err)
	}
	if err := New(db, Config{}).SaveGroup(context.Background(), GroupChange{Name: "kids", Description: "the children, revised", Members: []string{"jude"}}, admittedPrincipal(t, New(db, Config{}), "root")); err != nil {
		t.Fatal(err)
	}
	groups, err := d.groups(context.Background())
	if err != nil || len(groups) != 3 || groups[0].Kind != "built-in" || groups[1].Name != "root" || groups[1].Kind != "personal" {
		t.Fatalf("groups: %v %+v", err, groups)
	}
	if kids := groups[2]; kids.Description != "the children, revised" || len(kids.Members) != 1 || kids.Members[0] != "jude" {
		t.Fatalf("whole-set membership: %+v", kids)
	}
	if kind, _ := d.GroupKind(context.Background(), "kids"); kind != "ordinary" {
		t.Fatalf("kind: %q", kind)
	}
	if kind, _ := d.GroupKind(context.Background(), "nope"); kind != "" {
		t.Fatalf("unknown group kind: %q", kind)
	}
	if names, _ := d.groupNames(context.Background()); len(names) != 2 || names[0] != "root" || names[1] != "kids" {
		t.Fatalf("creatable groups, personal first: %v", names)
	}
	if err := testDeleteGroup(db, "root", "ops"); !errors.Is(err, errProtectedGroup) {
		t.Fatalf("a personal group stays: %v", err)
	}
	if err := testDeleteGroup(db, GroupAllUsers, "ops"); !errors.Is(err, errProtectedGroup) {
		t.Fatalf("the built-in group stays: %v", err)
	}
	if err := testDeleteGroup(db, "kids", "ops"); err != nil {
		t.Fatal(err)
	}
	if groups, _ := d.groups(context.Background()); len(groups) != 2 {
		t.Fatal("deleted")
	}
	if receipts := len(ledger(t, db, "group.saved")) + len(ledger(t, db, "group.deleted")); receipts != 3 {
		t.Fatalf("receipts: %d", receipts)
	}
}

// Removing a name ends everything it held, in the same transaction, and
// the receipt lists it: a later sighting of the same name (a trusted
// header, an open-mode actor, an old token) starts an account with none of
// the old grants, tokens, memberships or invites.
func TestRemovingANameRevokesWhatItHeld(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{})
	testGrantRoot(t, s, "ops")
	ops := admittedPrincipal(t, s, "ops")
	if _, err := s.EnsureUser(ctx, "carol"); err != nil {
		t.Fatal(err)
	}
	grant, err := s.Grant(ctx, Spec{SubjectType: "user", Subject: "carol", Role: access.RoleProjectOwner, Scope: access.ScopeProject, ScopeID: idstest.N(7).String(), By: "ops"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGroup(ctx, GroupChange{Name: "kids", Members: []string{"carol"}}, ops); err != nil {
		t.Fatal(err)
	}
	tok, err := s.IssueToken(ctx, NewToken{Name: "carol-cli", User: "carol", Purpose: access.TokenCLI}, ops)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := s.Invite(ctx, NewInvite{Username: "carol"}, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveName(ctx, "carol", ops); err != nil {
		t.Fatal(err)
	}
	removed := ledger(t, s.db, "user.name-removed")
	if len(removed) != 1 {
		t.Fatalf("one removal receipt: %+v", removed)
	}
	var listed struct {
		Grants      []ids.ID
		Tokens      []string
		Invites     []ids.ID
		Memberships int64
	}
	if err := json.Unmarshal(removed[0].Payload, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Grants) != 1 || listed.Grants[0] != grant || len(listed.Tokens) != 1 || listed.Tokens[0] != "carol-cli" ||
		len(listed.Invites) != 1 || listed.Invites[0] != inv.Invite.ID || listed.Memberships != 1 {
		t.Fatalf("the receipt lists what was revoked: %s", removed[0].Payload)
	}
	if pr, err := s.PersonalTokenIdentity(ctx, tok.Plaintext, net.ParseIP("127.0.0.1")); err == nil && pr.Name != "" {
		t.Fatalf("the removed name's token still signs in: %+v", pr)
	}
	if got, err := s.InviteFor(ctx, inv.Token); err != nil || got.State != "revoked" {
		t.Fatalf("the removed name's invite is withdrawn: %v %+v", err, got)
	}
	// The name is seen again: a new account, holding nothing of the old one.
	if _, err := s.EnsureUser(ctx, "carol"); err != nil {
		t.Fatal(err)
	}
	grants, err := ActiveGrantsIn(ctx, s.db, access.Principal{Kind: "user", Name: "carol"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range grants {
		if g.SubjectType == "user" || g.Subject == "kids" {
			t.Fatalf("a re-created name inherits a grant: %+v", g)
		}
	}
	groups, err := s.Groups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		for _, m := range g.Members {
			if m == "carol" {
				t.Fatalf("a re-created name inherits membership of %s", g.Name)
			}
		}
	}
}

// An account with no password is still a real account once it has signed
// in or acted (single sign-on and proxy accounts never have a password):
// it is disabled, never removed, so a later sighting of the name finds it
// disabled instead of starting a fresh enabled account. Only a name that
// never signed in or acted can be removed.
func TestAnAccountThatActedIsDisabledNotRemoved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{})
	testGrantRoot(t, s, "ops")
	ops := admittedPrincipal(t, s, "ops")
	for _, name := range []string{"sso", "agent", "browser", "typo"} {
		if _, err := s.EnsureUser(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	// sso acted through a chat: its ledger line names it as the person.
	if err := s.db.InTx(ctx, func(tx *database.Tx) error {
		return audit.At(time.Now()).RecordIn(ctx, tx, "sso via console/abc", idstest.N(1), "change.created", "1", nil)
	}); err != nil {
		t.Fatal(err)
	}
	// agent signed in with a token an administrator issued it.
	tok, err := s.IssueToken(ctx, NewToken{Name: "agent-cli", User: "agent", Purpose: access.TokenCLI}, ops)
	if err != nil {
		t.Fatal(err)
	}
	if pr, err := s.PersonalTokenIdentity(ctx, tok.Plaintext, net.ParseIP("127.0.0.1")); err != nil || pr.Name != "agent" {
		t.Fatalf("token sign-in: %v %+v", err, pr)
	}
	// browser holds a session. No owner operation starts one for an account
	// without a password, so the private transition sets it up.
	if _, err := issueSessionIn(ctx, s.db, "browser", time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	users, err := s.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if want := u.Username != "typo"; u.Acted != want {
			t.Fatalf("People lists %s as acted=%v", u.Username, u.Acted)
		}
	}
	for _, name := range []string{"sso", "agent", "browser"} {
		if err := s.RemoveName(ctx, name, ops); !errors.Is(err, errRealAccount) {
			t.Fatalf("%s acted, so it is kept: %v", name, err)
		}
	}
	yes := true
	if err := s.SaveUser(ctx, AccountChange{Username: "sso", Disabled: &yes}, ops); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureUser(ctx, "sso"); err != nil {
		t.Fatal(err)
	}
	if disabled, err := disabledIn(ctx, s.db, "sso"); err != nil || !disabled {
		t.Fatalf("a disabled account seen again stays disabled: %v %v", err, disabled)
	}
	if err := s.RemoveName(ctx, "typo", ops); err != nil {
		t.Fatalf("a name that never signed in or acted is removed: %v", err)
	}
}

// Authority is decided only for an account that exists and is enabled: a
// name with no account (removed, or never seen) is refused, never treated
// as enabled. AppTown's own names are not people and hold no account.
func TestAnUnknownNameIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{})
	testGrantRoot(t, s, "ops")
	if _, err := s.EnsureUser(ctx, "ghost"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveName(ctx, "ghost", admittedPrincipal(t, s, "ops")); err != nil {
		t.Fatal(err)
	}
	// Everyone signed in reads everything by default: only the missing
	// account refuses the removed name that reading.
	if err := s.db.InTx(ctx, func(tx *database.Tx) error { return defaultsIn(ctx, tx, false, time.Now()) }); err != nil {
		t.Fatal(err)
	}
	ghost := access.Principal{Kind: "user", Name: "ghost", Via: "session"}
	read := access.Permission{Capability: access.CapProjectRead, Resource: access.Resource{Type: "project", ID: idstest.N(7).String(), ProjectID: idstest.N(7)}}
	if dec, err := s.Decide(ctx, admittedPrincipal(t, s, "ops"), read); err != nil || !dec.Allow {
		t.Fatalf("the default read: %v %+v", err, dec)
	}
	dec, err := s.Decide(ctx, ghost, read)
	if err != nil || dec.Allow || dec.Code != access.DenyAccountDisabled {
		t.Fatalf("a name with no account is refused: %v %+v", err, dec)
	}
	var denied *access.Denied
	if err := RequireEnabledIn(ctx, s.db, ghost); !errors.As(err, &denied) || denied.Code != access.DenyAccountDisabled {
		t.Fatalf("the account preflight refuses a name with no account: %v", err)
	}
	console := access.Principal{Kind: "instrument", Instrument: "console", Name: "ghost via console/x", Person: "ghost"}
	if err := RequireEnabledIn(ctx, s.db, console); !errors.As(err, &denied) {
		t.Fatalf("a chat whose person has no account is refused: %v", err)
	}
	if err := RequireEnabledIn(ctx, s.db, admittedPrincipal(t, s, "ops")); err != nil {
		t.Fatalf("an enabled account passes: %v", err)
	}
	for name, want := range map[string]bool{"system": true, "apptown-system": true, "apptown": true, "forge-system": true, "forge": true, "run/12": true, "run/": false, "run/x": false, "ops": false, "ghost": false} {
		if got := SystemActor(name); got != want {
			t.Errorf("SystemActor(%q) = %v", name, got)
		}
	}
}

// A grant to a person records the person's account with it, as the grant
// and member doors do, without counting as a sighting: authority is
// decided only for an account, and People lists everyone who holds some.
func TestAGrantRecordsThePersonsAccount(t *testing.T) {
	t.Parallel()
	s := New(testDB(t), Config{})
	if _, err := s.PutGrant(context.Background(), Spec{SubjectType: "user", Subject: "dana", Role: access.RoleProjectViewer, Scope: access.ScopeProject, ScopeID: idstest.N(3).String(), By: "fixture"}); err != nil {
		t.Fatal(err)
	}
	users, err := s.Users(context.Background())
	if err != nil || len(users) != 1 || users[0].Username != "dana" || users[0].LastSeen.Valid || users[0].Acted {
		t.Fatalf("the grant's person is an account, never seen: %v %+v", err, users)
	}
	if disabled, err := disabledIn(context.Background(), s.db, "dana"); err != nil || disabled {
		t.Fatalf("the granted person may act: %v %v", err, disabled)
	}
}
