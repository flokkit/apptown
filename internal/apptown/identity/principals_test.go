package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/fault"
)

// A removed name is free, and whoever takes it next is a new principal that
// inherits nothing: the old principal's grant and token do not reach the
// new account, and the old principal stays on record, removed.
func TestARemovedNameTakenAgainInheritsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{})
	testGrantRoot(t, s, "ops")
	if _, err := s.Grant(ctx, Spec{SubjectType: "user", Subject: "carol", Role: access.RoleProjectCreator, Scope: access.ScopeInstance, By: "ops"}, ""); err != nil {
		t.Fatal(err)
	}
	old, err := queries(s.db).UserByName(ctx, "carol")
	if err != nil {
		t.Fatal(err)
	}
	token, err := testIssueToken(s, "carols", "carol", "member", "ops")
	if err != nil {
		t.Fatal(err)
	}
	if err := testRemoveName(s.db, "carol", "ops"); err != nil {
		t.Fatal(err)
	}
	// A trusted header names carol again: a new account, a new principal.
	if _, err := s.EnsureUser(ctx, "carol"); err != nil {
		t.Fatal(err)
	}
	now, err := queries(s.db).UserByName(ctx, "carol")
	if err != nil || now.PrincipalID == old.PrincipalID {
		t.Fatalf("the name's new account is the old principal: %s %v", now.PrincipalID, err)
	}
	if was, err := queries(s.db).PrincipalByID(ctx, old.PrincipalID); err != nil || !was.RemovedAt.Valid || was.Name != "carol" {
		t.Fatalf("the old principal is not on record as removed: %+v %v", was, err)
	}
	if grants := liveGrants(t, s.db, "carol", ""); len(grants) != 0 {
		t.Fatalf("the new carol inherited grants: %+v", grants)
	}
	if pr, err := s.PersonalTokenIdentity(ctx, token, nil); err == nil && pr.Name != "" {
		t.Fatalf("the old carol's token reached the new account: %+v", pr)
	}
}

// A rename keeps the person: their grant and token are theirs under the new
// name, the old name holds nothing and is free, and a taken name is refused.
func TestARenameKeepsThePersonsAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{})
	testGrantRoot(t, s, "ops")
	testAccounts(t, s.db, "dave")
	if _, err := s.Grant(ctx, Spec{SubjectType: "user", Subject: "carol", Role: access.RoleProjectCreator, Scope: access.ScopeInstance, By: "ops"}, ""); err != nil {
		t.Fatal(err)
	}
	token, err := testIssueToken(s, "carols", "carol", "member", "ops")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(ctx, "carol", "dave", testPrincipal("ops")); fault.KindOf(err) != fault.Conflict {
		t.Fatalf("a rename to a taken name: %v", err)
	}
	if err := s.Rename(ctx, "carol", "caroline", testPrincipal("ops")); err != nil {
		t.Fatal(err)
	}
	if grants := liveGrants(t, s.db, "caroline", access.RoleProjectCreator); len(grants) != 1 {
		t.Fatalf("the renamed person's grant: %+v", grants)
	}
	if grants := liveGrants(t, s.db, "carol", ""); len(grants) != 0 {
		t.Fatalf("the old name holds grants: %+v", grants)
	}
	if pr, err := s.PersonalTokenIdentity(ctx, token, nil); err != nil || pr.Name != "caroline" || pr.ID.IsZero() {
		t.Fatalf("the renamed person's token: %+v %v", pr, err)
	}
}

// A caller admitted before a rename keeps acting as the person it was
// admitted as: an operation captured from carol's session is still hers
// after she is renamed, a later account with her old name lends it nothing,
// and disabling her stops it.
func TestACapturedCallerStaysThePersonItWasAdmittedAs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{SessionTTL: SessionTTL})
	testGrantRoot(t, s, "ops")
	testAccounts(t, s.db, "carol")
	tok, err := issueSessionIn(ctx, s.db, "carol", SessionTTL, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, sessionID, ok, err := s.sessions.Resolve(ctx, tok)
	if err != nil || !ok {
		t.Fatalf("session: %v %v", ok, err)
	}
	carol, err := queries(s.db).UserByName(ctx, "carol")
	if err != nil {
		t.Fatal(err)
	}
	captured := access.Principal{ID: carol.PrincipalID, Kind: "user", Name: "carol", Via: "session", Credential: SessionCredential(sessionID)}
	create := access.Permission{Capability: access.CapProjectCreate, Resource: access.Resource{Type: "instance"}}
	if err := s.Rename(ctx, "carol", "caroline", testPrincipal("ops")); err != nil {
		t.Fatal(err)
	}
	// The new carol may create projects; the captured caller may not.
	if _, err := s.Grant(ctx, Spec{SubjectType: "user", Subject: "carol", Role: access.RoleProjectCreator, Scope: access.ScopeInstance, By: "ops"}, ""); err != nil {
		t.Fatal(err)
	}
	var denied *access.Denied
	if err := s.RequireStill(ctx, captured, create); !errors.As(err, &denied) {
		t.Fatalf("the captured caller borrowed the new carol's grant: %v", err)
	}
	// Given to caroline, it is hers; disabled, she is stopped.
	if _, err := s.Grant(ctx, Spec{SubjectType: "user", Subject: "caroline", Role: access.RoleProjectCreator, Scope: access.ScopeInstance, By: "ops"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RequireStill(ctx, captured, create); err != nil {
		t.Fatalf("the renamed person's own grant: %v", err)
	}
	disableStoredAccount(t, s.db, "caroline")
	if err := s.RequireStill(ctx, captured, create); err == nil {
		t.Fatal("a disabled person's captured caller still acts")
	}
}

// Administering people is judged on the admitted administrator, not on the
// account now holding their old name: renamed, alice still administers;
// once she is disabled, a new administrator named alice lends her nothing.
func TestAdministrationJudgesTheAdmittedPrincipal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{SessionTTL: SessionTTL})
	testGrantRoot(t, s, "ops")
	testGrantRoot(t, s, "alice")
	alice, err := queries(s.db).UserByName(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	admitted := access.Principal{ID: alice.PrincipalID, Kind: "user", Name: "alice", Via: "session"}
	if err := s.Rename(ctx, "alice", "alicia", testPrincipal("ops")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUser(ctx, AccountChange{Username: "one"}, admitted); err != nil {
		t.Fatalf("renamed, the admitted administrator was refused: %v", err)
	}
	testGrantRoot(t, s, "alice")
	disableStoredAccount(t, s.db, "alicia")
	var denied *access.Denied
	if err := s.SaveUser(ctx, AccountChange{Username: "two"}, admitted); !errors.As(err, &denied) {
		t.Fatalf("the disabled administrator acted on the new alice's account: %v", err)
	}
}
