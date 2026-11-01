package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A person's name may not take a form AppTown reads as something else:
// a chat, a run or a connection ("chat/…", "run/…", "console/…"), someone
// acting through something ("… via …"), AppTown's own names, or text
// that does not survive display (control characters, outer spaces).
func TestValidNameRefusesFormsAppTownParses(t *testing.T) {
	t.Parallel()
	for name, ok := range map[string]bool{
		"alice": true, "alice@example.com": true, "Alice Smith": true, "o'brien": true,
		"": false, " alice": false, "alice ": false, "alice\n": false, "al\x00ice": false, "al\x7fice": false,
		"bob via console/x": false, "bob via git ticket": false, "chat/3": false, "run/12": false, "console/abc": false, "eval/2": false,
		"system": false, "apptown-system": false,
	} {
		err := ValidName(name)
		if (err == nil) != ok {
			t.Errorf("ValidName(%q) = %v, want ok=%v", name, err, ok)
		}
		if err != nil && fault.KindOf(err) != fault.Invalid {
			t.Errorf("ValidName(%q) is not classified invalid: %v", name, err)
		}
	}
}

// Every door that records a name refuses one AppTown would read as
// something else, and records nothing.
func TestEveryNameDoorRefusesAnUnusableName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{})
	testGrantRoot(t, s, "ops")
	bad := "bob via console/x"
	if _, err := s.EnsureUser(ctx, bad); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("a sighting records an unusable name: %v", err)
	}
	if err := s.SeedAdministrator(ctx, "run/1"); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("the startup administrator seed takes an unusable name: %v", err)
	}
	if err := s.AddProjectMember(ctx, idstest.N(7), "chat/3", access.RoleProjectViewer, "ops"); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("a project member takes an unusable name: %v", err)
	}
	if _, err := s.Grant(ctx, Spec{SubjectType: "user", Subject: "system", Role: access.RoleProjectOwner, Scope: access.ScopeProject, ScopeID: idstest.N(7).String(), By: "ops"}, ""); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("a grant names an unusable person: %v", err)
	}
	if _, err := s.IssueToken(ctx, NewToken{Name: "t", User: " ops", Purpose: access.TokenCLI}, admittedPrincipal(t, s, "ops")); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("a token is issued to an unusable name: %v", err)
	}
	users, err := s.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Username != "ops" {
		t.Fatalf("a refused name was recorded: %+v", users)
	}
	grants, err := s.GrantsAt(ctx, access.ScopeProject, idstest.N(7).String())
	if err != nil || len(grants) != 0 {
		t.Fatalf("a refused name was granted: %v %+v", err, grants)
	}
}

// AppTown's own names (and the other forms ValidName refuses) cannot
// become a person through the account, first-account, invite or group
// doors either: a workload acting as "system" is admitted without an
// account check, so a person by that name would keep authority after being
// disabled.
func TestReservedNamesCannotBecomePeopleThroughAccountDoors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, name := range []string{"system", "apptown-system"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fresh := New(testDB(t), Config{})
			if _, err := fresh.Bootstrap(ctx, NewAccount{Username: name, Password: "first-password"}); fault.KindOf(err) != fault.Invalid {
				t.Fatalf("the first account takes a reserved name: %v", err)
			}
			s := New(testDB(t), Config{})
			testGrantRoot(t, s, "ops")
			ops := admittedPrincipal(t, s, "ops")
			if err := s.SaveUser(ctx, AccountChange{Username: name, Password: "a-password"}, ops); fault.KindOf(err) != fault.Invalid {
				t.Fatalf("an account is created with a reserved name: %v", err)
			}
			if _, err := s.Invite(ctx, NewInvite{Username: name}, ops); fault.KindOf(err) != fault.Invalid {
				t.Fatalf("an invite names a reserved name: %v", err)
			}
			if err := s.SaveGroup(ctx, GroupChange{Name: "kids", Members: []string{"zoe", name}}, ops); fault.KindOf(err) != fault.Invalid {
				t.Fatalf("a group takes a reserved name as a member: %v", err)
			}
			if _, err := s.PutGrant(ctx, Spec{SubjectType: "user", Subject: name, Role: access.RoleProjectViewer, Scope: access.ScopeProject, ScopeID: idstest.N(7).String(), By: "ops"}); fault.KindOf(err) != fault.Invalid {
				t.Fatalf("the shared grant writer names a reserved person: %v", err)
			}
			users, err := s.Users(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range users {
				if u.Username == name || u.Username == "zoe" {
					t.Fatalf("a refused door recorded %s", u.Username)
				}
			}
		})
	}
}

// An account that already exists is disabled whatever its name: a single
// sign-on name such as an email address, or a row recorded before names were
// checked. Disabling is how an account that acted is offboarded, so it must
// never be refused for the name's syntax; a name AppTown would read as
// something else may only be disabled.
func TestAnExistingAccountIsDisabledWhateverItsName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{})
	testGrantRoot(t, s, "ops")
	ops := admittedPrincipal(t, s, "ops")
	yes := true
	// A proxy-style name, seen on the wire and acting.
	sso := "Alice.Smith@example.com"
	if _, err := s.EnsureUser(ctx, sso); err != nil {
		t.Fatal(err)
	}
	if err := s.db.InTx(ctx, func(tx *database.Tx) error {
		return audit.At(time.Now()).RecordIn(ctx, tx, sso, idstest.N(1), "change.created", "1", nil)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveName(ctx, sso, ops); !errors.Is(err, errRealAccount) {
		t.Fatalf("an account that acted is kept: %v", err)
	}
	if err := s.SaveUser(ctx, AccountChange{Username: sso, Disabled: &yes}, ops); err != nil {
		t.Fatalf("disabling a single sign-on account: %v", err)
	}
	if _, err := s.EnsureUser(ctx, sso); err != nil {
		t.Fatal(err)
	}
	if disabled, err := disabledIn(ctx, s.db, sso); err != nil || !disabled {
		t.Fatalf("a sighting after disabling leaves it disabled: %v %v", err, disabled)
	}
	read := access.Permission{Capability: access.CapProjectRead, Resource: access.Resource{Type: "project", ID: idstest.N(1).String(), ProjectID: idstest.N(1)}}
	if dec, err := s.Decide(ctx, access.Principal{Kind: "user", Name: sso, Via: "proxy"}, read); err != nil || dec.Allow || dec.Code != access.DenyAccountDisabled {
		t.Fatalf("the disabled account's authority is refused: %v %+v", err, dec)
	}
	// A new account still takes a slug.
	if err := s.SaveUser(ctx, AccountChange{Username: "Carol Smith", Password: "a-password"}, ops); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("a new account takes a name that is not a slug: %v", err)
	}
	// A row recorded before names were checked. No operation records such a
	// name any more, so the test writes the old row directly.
	// sql-guarantee: ValidName now refuses this name at every door; only a pre-existing row can carry it.
	if _, err := s.db.SQL().ExecContext(context.Background(), `WITH p AS (INSERT INTO principals (id, kind, name, created_at) VALUES ($1, 'person', 'bob via console/x', $2), ($3, 'person', 'system', $4) RETURNING id, name)
		INSERT INTO users (username, principal_id, created_at) SELECT name, id, $5 FROM p`, ids.New(time.Now()), time.Now(), ids.New(time.Now()), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bob via console/x", "system"} {
		if err := s.SaveUser(ctx, AccountChange{Username: name, Password: "a-password"}, ops); fault.KindOf(err) != fault.Invalid {
			t.Fatalf("%s is given a password: %v", name, err)
		}
		if err := s.SaveUser(ctx, AccountChange{Username: name, Role: "admin"}, ops); fault.KindOf(err) != fault.Invalid {
			t.Fatalf("%s is made an administrator: %v", name, err)
		}
		group := "kids"
		for _, with := range []AccountChange{
			{Username: name, Disabled: &yes, Password: "a-password"},
			{Username: name, Disabled: &yes, Role: "member"},
			{Username: name, Disabled: &yes, DefaultGroup: &group},
		} {
			if err := s.SaveUser(ctx, with, ops); fault.KindOf(err) != fault.Invalid {
				t.Fatalf("%s is changed beyond disabling: %+v %v", name, with, err)
			}
		}
		if err := s.SaveUser(ctx, AccountChange{Username: name, Disabled: &yes}, ops); err != nil {
			t.Fatalf("disabling the old row %s: %v", name, err)
		}
		if disabled, err := disabledIn(ctx, s.db, name); err != nil || !disabled {
			t.Fatalf("%s disabled: %v %v", name, err, disabled)
		}
	}
}
