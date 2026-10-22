package identity

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
)

func TestAdministrationRequiresAuthorityInsideEveryCommand(t *testing.T) {
	t.Parallel()
	for _, op := range []string{"save-user", "remove-name", "save-group", "delete-group"} {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			s := New(testDB(t), Config{})
			if err := s.SeedAdministrator(context.Background(), "boss"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.EnsureUser(context.Background(), "member"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.EnsureUser(context.Background(), "bare"); err != nil {
				t.Fatal(err)
			}
			if err := s.SaveGroup(context.Background(), GroupChange{Name: "kids"}, testPrincipal("boss")); err != nil {
				t.Fatal(err)
			}
			beforeUsers, err := s.Users(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			beforeGroups, err := s.Groups(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			switch op {
			case "save-user":
				err = s.SaveUser(context.Background(), AccountChange{Username: "new-user"}, testPrincipal("member"))
			case "remove-name":
				err = s.RemoveName(context.Background(), "bare", testPrincipal("member"))
			case "save-group":
				err = s.SaveGroup(context.Background(), GroupChange{Name: "kids", Members: []string{"new-member"}}, testPrincipal("member"))
			case "delete-group":
				err = s.DeleteGroup(context.Background(), "kids", testPrincipal("member"))
			}
			var denied *access.Denied
			if !errors.As(err, &denied) {
				t.Fatalf("command admitted a member without its HTTP guard: %v", err)
			}
			afterUsers, err := s.Users(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			afterGroups, err := s.Groups(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeUsers, afterUsers) || !reflect.DeepEqual(beforeGroups, afterGroups) {
				t.Fatal("denied command changed its target")
			}
			if receipts := ledger(t, s.db, ""); len(receipts) != 1 {
				t.Fatalf("denied command wrote a mutation receipt: %+v", receipts)
			}
		})
	}
}

func TestAdministrationPolicyPreservesAccountAndCredentialBounds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                   string
		open                   bool
		pr                     access.Principal
		role                   string
		disabled, grant, allow bool
	}{
		{name: "current administrator", pr: testPrincipal("boss"), role: "admin", allow: true},
		{name: "proxy administrator", pr: access.Principal{Kind: "user", Name: "boss", Via: "proxy"}, role: "admin", allow: true},
		{name: "stale administrator", pr: testPrincipal("boss"), role: "member"},
		{name: "disabled administrator", pr: testPrincipal("boss"), role: "admin", disabled: true},
		{name: "member with identity grant", pr: testPrincipal("boss"), role: "member", grant: true},
		{name: "missing account", pr: testPrincipal("boss")},
		{name: "missing principal"},
		{name: "missing verified channel", pr: access.Principal{Kind: "user", Name: "boss", Role: "admin"}, role: "admin"},
		{name: "bounded member token", pr: access.Principal{Kind: "user", Name: "boss", Via: "token", Role: "member"}, role: "admin"},
		{name: "administrator token", pr: access.Principal{Kind: "user", Name: "boss", Via: "token", Role: "admin"}, role: "admin", allow: true},
		{name: "administrator cli token", pr: access.Principal{Kind: "user", Name: "boss", Via: "token", Role: "admin", Purpose: access.TokenCLI}, role: "admin", allow: true},
		{name: "administrator agent token cannot administer", pr: access.Principal{Kind: "user", Name: "boss", Via: "token", Role: "admin", Purpose: access.TokenAgent}, role: "admin"},
		{name: "oauth cannot gain administration", pr: access.Principal{Kind: "user", Name: "boss", Via: "oauth", Scopes: access.ScopeNames()}, role: "admin"},
		{name: "instrument cannot gain administration", open: true, pr: access.Principal{Kind: "instrument", Name: "boss", Via: "console", Instrument: "console", Role: "admin"}},
		{name: "unseeded open anonymous", open: true, pr: access.Principal{Kind: "anonymous", Name: "anonymous", Via: "open"}},
		{name: "unseeded open person", open: true, pr: testOpenPrincipal("first")},
		{name: "unseeded open member token", open: true, pr: access.Principal{Kind: "user", Name: "first", Via: "token", Role: "member"}, role: "member"},
		{name: "closed anonymous", pr: access.Principal{Kind: "anonymous", Name: "anonymous", Via: "open"}},
		{name: "closed open-channel administrator", pr: testOpenPrincipal("boss"), role: "admin"},
		{name: "seeded open administrator", open: true, pr: testOpenPrincipal("boss"), role: "admin", allow: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := New(testDB(t), Config{OpenInstance: tc.open})
			if tc.role != "" {
				if err := testSaveAccount(s.db, accountEdit{Name: tc.pr.Name, Role: tc.role}, "setup"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.disabled {
				disableStoredAccount(t, s.db, tc.pr.Name)
			}
			if tc.grant {
				if _, err := s.Grant(context.Background(), Spec{SubjectType: "user", Subject: tc.pr.Name, Capability: string(access.CapIdentityUsers), Scope: access.ScopeInstance, By: "setup"}, ""); err != nil {
					t.Fatal(err)
				}
			}
			err := s.SaveGroup(context.Background(), GroupChange{Name: "managed", Members: []string{"new-person"}}, tc.pr)
			if tc.allow {
				if err != nil {
					t.Fatalf("legitimate administrator refused: %v", err)
				}
				if saved := ledger(t, s.db, "group.saved"); len(saved) != 1 || saved[0].Actor != tc.pr.Name {
					t.Fatalf("attribution: %+v", saved)
				}
			} else {
				var denied *access.Denied
				if !errors.As(err, &denied) && !errors.Is(err, access.ErrPrincipalRequired) {
					t.Fatalf("expected authorization refusal, got %v", err)
				}
				if groupRecorded(t, s.db, "managed") || userRecorded(t, s.db, "new-person") || len(ledger(t, s.db, "group.saved")) != 0 {
					t.Fatal("refusal mutated state")
				}
			}
		})
	}
}

func TestAdministrationRequiresReadableCurrentFacts(t *testing.T) {
	t.Parallel()
	for _, marker := range []string{"-- name: UserByName :one", "-- name: AdminUsers :many"} {
		t.Run(marker, func(t *testing.T) {
			t.Parallel()
			s := New(testDB(t), Config{OpenInstance: true})
			restore := s.db.FailNextStatement(marker)
			err := s.SaveGroup(context.Background(), GroupChange{Name: "must-not-exist"}, testOpenPrincipal("first"))
			restore()
			var denied *access.Denied
			if err == nil || errors.As(err, &denied) {
				t.Fatalf("read failure became a policy answer: %v", err)
			}
			groups, err := s.Groups(context.Background())
			if err != nil || len(groups) != 0 {
				t.Fatalf("failed read allowed mutation: %+v %v", groups, err)
			}
		})
	}
}

func TestOpenCommandsCannotCreateInitialAuthority(t *testing.T) {
	t.Parallel()
	s := New(testDB(t), Config{OpenInstance: true})
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, name := range []string{"first", "second"} {
		go func(name string) {
			<-start
			results <- s.SaveUser(context.Background(), AccountChange{Username: name, Role: "admin"}, testOpenPrincipal("installer"))
		}(name)
	}
	close(start)
	winners := 0
	for range 2 {
		err := <-results
		if err == nil {
			winners++
		} else {
			var denied *access.Denied
			if !errors.As(err, &denied) {
				t.Error(err)
			}
		}
	}
	if winners != 0 {
		t.Fatalf("ordinary commands created %d administrators", winners)
	}
	if n, err := enabledRoots(context.Background(), s.db, time.Now()); err != nil || n != 0 {
		t.Fatalf("administrator count: %d %v", n, err)
	}
}

func TestAdministrationRechecksDemotionAndConfiguredMode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{OpenInstance: true})
	testGrantRoot(t, s, "first")
	if err := s.SaveGroup(ctx, GroupChange{Name: "before"}, testOpenPrincipal("first")); err != nil {
		t.Fatal(err)
	}
	s = New(s.db, Config{OpenInstance: false})
	if err := s.SaveGroup(ctx, GroupChange{Name: "closed"}, testOpenPrincipal("first")); err == nil {
		t.Fatal("accepted an identity from a disabled transport mode")
	}
	for _, name := range []string{"boss", "other"} {
		if err := s.SeedAdministrator(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	pr := testPrincipal("boss")
	if err := s.SaveUser(ctx, AccountChange{Username: "boss", Role: "member"}, testPrincipal("other")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGroup(ctx, GroupChange{Name: "stale"}, pr); err == nil {
		t.Fatal("stale principal retained administrative authority")
	}
}
