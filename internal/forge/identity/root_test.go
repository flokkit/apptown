package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestRootMembershipChangesPreserveAnEnabledAdministrator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{})
	if err := s.SeedAdministrator(ctx, "backup"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGroup(ctx, GroupChange{Name: "operators", Members: []string{"alice"}}, testPrincipal("backup")); err != nil {
		t.Fatal(err)
	}
	id, err := s.Grant(ctx, Spec{SubjectType: "group", Subject: "operators", Role: access.RoleInstanceAdmin, Scope: access.ScopeInstance, By: "backup"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUser(ctx, AccountChange{Username: "backup", Role: "member"}, testPrincipal("backup")); err != nil {
		t.Fatal(err)
	}
	for _, command := range []func() error{
		func() error { return s.SaveGroup(ctx, GroupChange{Name: "operators"}, testPrincipal("alice")) },
		func() error { return s.DeleteGroup(ctx, "operators", testPrincipal("alice")) },
		func() error { _, err := s.Revoke(ctx, id, "alice"); return err },
	} {
		if err := command(); !errors.Is(err, ErrLastAdministrator) {
			t.Fatalf("last root: %v", err)
		}
		root, err := RootIn(ctx, s.db, "alice", time.Now())
		if err != nil || !root {
			t.Fatalf("root lost after refused command: %v %v", root, err)
		}
	}
	if err := s.SeedAdministrator(ctx, "backup"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGroup(ctx, GroupChange{Name: "operators"}, testPrincipal("backup")); err != nil {
		t.Fatal(err)
	}
	if root, err := RootIn(ctx, s.db, "alice", time.Now()); err != nil || root {
		t.Fatalf("membership removal not observed: %v %v", root, err)
	}
}

func TestRootSeedRequiresItsAccountAndGrant(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"users", "grants"} {
		t.Run(table, func(t *testing.T) {
			t.Parallel()
			s := New(testDB(t), Config{})
			drop := vetoGroupWrite(t, s.db, table, "INSERT")
			if err := s.SeedAdministrator(context.Background(), "root"); err == nil {
				t.Fatal("incomplete root seed accepted")
			}
			users, err := s.Users(context.Background())
			if err != nil || len(users) != 0 {
				t.Fatalf("partial account seed: %v %v", users, err)
			}
			grants, err := s.AllGrants(context.Background())
			if err != nil || len(grants) != 0 {
				t.Fatalf("partial root grant: %v %v", grants, err)
			}
			drop()
			if err := s.SeedAdministrator(context.Background(), "root"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRootGrantDoesNotOverrideCredentialBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		pr   access.Principal
		want bool
	}{
		{"session ignores claimed role", access.Principal{Kind: "user", Name: "alice", Via: "session", Role: "member"}, true},
		{"administrator token", access.Principal{Kind: "user", Name: "alice", Via: "token", Role: "admin"}, true},
		{"member token", access.Principal{Kind: "user", Name: "alice", Via: "token", Role: "member"}, false},
		{"token without ceiling", access.Principal{Kind: "user", Name: "alice", Via: "token"}, false},
		{"oauth scope", access.Principal{Kind: "user", Name: "alice", Via: "oauth", Scopes: access.ScopeNames()}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := &fakeFacts{grants: map[string][]access.Grant{"alice": {role(idstest.N(1), "user", "alice", access.RoleInstanceAdmin, access.ScopeInstance, "")}}}
			got := access.Decide(tc.pr, access.DelegationOf(tc.pr), access.CapIdentityAuth, access.Resource{Type: "instance"}, access.Context{}, facts)
			if got.Allow != tc.want {
				t.Fatalf("%s: %+v", tc.name, got)
			}
		})
	}
}

func TestExpiringTheLastRootGrantIsRefused(t *testing.T) {
	t.Parallel()
	s := New(testDB(t), Config{})
	if err := s.SeedAdministrator(context.Background(), "root"); err != nil {
		t.Fatal(err)
	}
	_, err := s.Grant(context.Background(), Spec{SubjectType: "user", Subject: "root", Role: access.RoleInstanceAdmin, Scope: access.ScopeInstance, By: "root"}, "2000-01-01T00:00:00Z")
	if !errors.Is(err, ErrLastAdministrator) {
		t.Fatalf("expired last root: %v", err)
	}
	if root, err := RootIn(context.Background(), s.db, "root", time.Now()); err != nil || !root {
		t.Fatalf("root lost: %v %v", root, err)
	}
}
