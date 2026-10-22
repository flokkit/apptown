package inference

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/ids"
)

// A workload is admitted only for a person with an enabled account, or for
// one of AppTown's own names. A name with no account (a removed person,
// a run owner never seen) is refused like a disabled one, never treated as
// enabled; a workload acting for a person is checked as that person.
func TestWorkloadAccountsFailClosed(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	dir := identity.New(db, identity.Config{})
	for _, name := range []string{"ops", "alice", "bob"} {
		if _, err := dir.EnsureUser(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dir.PutGrant(context.Background(), identity.Spec{SubjectType: "user", Subject: "ops", Role: access.RoleInstanceAdmin, Scope: access.ScopeInstance, By: "fixture"}); err != nil {
		t.Fatal(err)
	}
	yes := true
	if err := dir.SaveUser(context.Background(), identity.AccountChange{Username: "bob", Disabled: &yes}, access.Principal{Kind: "user", Name: "ops", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	a := Identity{DB: db}
	person := func(name string) ids.ID {
		who, err := identity.ActorIn(context.Background(), db, name)
		if err != nil {
			t.Fatal(err)
		}
		return who.Principal
	}
	alice, bob := person("alice"), person("bob")
	for _, tc := range []struct {
		who   identity.Workload
		admit bool
	}{
		{identity.Workload{Actor: "alice", Principal: alice}, true},
		{identity.Workload{Actor: "alice"}, false}, // a person's workload with no principal
		{identity.Workload{Actor: "bob", Principal: bob}, false},
		{identity.Workload{Actor: "ghost"}, false},
		{identity.Workload{Actor: "system"}, true},
		{identity.Workload{Actor: ""}, true},
		{identity.Workload{Actor: "apptown-system"}, true},
		{identity.Workload{Actor: "run/7"}, true},
		{identity.Workload{Actor: "system", Person: "ghost"}, false},
		{identity.Workload{Actor: "alice via console/c", Person: "alice", Principal: alice}, true},
	} {
		if _, err := a.admitAccount(context.Background(), tc.who); (err == nil) != tc.admit {
			t.Errorf("%+v: admitted=%v, want %v", tc.who, err == nil, tc.admit)
		}
	}
}

// A workload acts for the principal that started or opened it: renamed,
// that person's chat or step keeps working; a later account given the old
// name lends it nothing once the original person is disabled.
func TestAWorkloadStaysThePersonItWasStartedBy(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	dir := identity.New(db, identity.Config{})
	ctx := context.Background()
	for _, name := range []string{"ops", "alice"} {
		if _, err := dir.EnsureUser(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dir.PutGrant(ctx, identity.Spec{SubjectType: "user", Subject: "ops", Role: access.RoleInstanceAdmin, Scope: access.ScopeInstance, By: "fixture"}); err != nil {
		t.Fatal(err)
	}
	ops := access.Principal{Kind: "user", Name: "ops", Via: "session"}
	alice, err := identity.ActorIn(ctx, db, "alice")
	if err != nil {
		t.Fatal(err)
	}
	a := Identity{DB: db}
	for _, kind := range []string{"step", "console"} {
		who := identity.Workload{Kind: kind, Actor: "alice", Principal: alice.Principal}
		if kind == "console" {
			who = identity.Workload{Kind: kind, Actor: "alice via console/c", Person: "alice", Principal: alice.Principal}
		}
		if _, err := a.admitAccount(ctx, who); err != nil {
			t.Fatalf("%s before the rename: %v", kind, err)
		}
	}
	if err := dir.Rename(ctx, "alice", "alicia", ops); err != nil {
		t.Fatal(err)
	}
	if _, err := dir.EnsureUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	started := identity.Workload{Kind: "step", Actor: "alice", Principal: alice.Principal}
	if _, err := a.admitAccount(ctx, started); err != nil {
		t.Fatalf("renamed, the person's step was refused: %v", err)
	}
	yes := true
	if err := dir.SaveUser(ctx, identity.AccountChange{Username: "alicia", Disabled: &yes}, ops); err != nil {
		t.Fatal(err)
	}
	if _, err := a.admitAccount(ctx, started); err == nil {
		t.Fatal("the disabled person's step was admitted on the new alice's account")
	}
}
