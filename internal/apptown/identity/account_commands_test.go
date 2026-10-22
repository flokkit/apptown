package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"golang.org/x/crypto/bcrypt"
)

func TestAccountCommandValidation(t *testing.T) {
	t.Parallel()
	// Invalid edits do not need a database and cannot mutate an account.
	s := New(nil, Config{OpenInstance: true})
	// A name is checked against the account it names, inside the command
	// (names_test.go): an existing account is changed whatever its syntax.
	for _, in := range []AccountChange{
		{Username: ""},
		{Username: "alice", Role: "emperor"},
		{Username: "alice", Password: "short"},
	} {
		var invalid *AccountInputError
		if _, err := in.prepare(); !errors.As(err, &invalid) {
			t.Fatalf("invalid account command was not rejected: %v", err)
		}
	}
	for _, in := range []NewAccount{
		{Username: "Bad Name", Password: "long-enough"},
		{Username: "alice", Password: "short"},
	} {
		var invalid *AccountInputError
		if _, err := in.prepare(); !errors.As(err, &invalid) {
			t.Fatalf("invalid bootstrap command was not rejected: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.SaveUser(ctx, AccountChange{Username: "alice"}, testOpenPrincipal("ops")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled account command: %v", err)
	}
	if _, err := s.Bootstrap(ctx, NewAccount{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled bootstrap: %v", err)
	}
}

func TestAccountCommandOwnsPasswordAndDefaults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{OpenInstance: true})
	testGrantRoot(t, s, "ops")
	group := "  family  "
	in := AccountChange{Username: "alice", Password: "new-password", DefaultGroup: &group}
	if err := s.SaveUser(ctx, in, testOpenPrincipal("ops")); err != nil {
		t.Fatal(err)
	}
	if group != "  family  " {
		t.Fatal("preparing the command mutated its caller's default group")
	}
	a, err := s.directory.login(ctx, "alice")
	if err != nil || a == nil {
		t.Fatalf("saved account missing: %v", err)
	}
	cost, err := bcrypt.Cost([]byte(a.passwordHash))
	if err != nil || cost != bcrypt.DefaultCost || a.passwordHash == in.Password {
		t.Fatal("password was not stored as a default-cost bcrypt hash")
	}
	if a.Role != "member" || a.DefaultGroup != "family" || a.checkPassword(in.Password) != nil {
		t.Fatal("password or defaults did not reach the account")
	}
	signed, err := s.SignIn(ctx, PasswordLogin{Username: "alice", Password: in.Password})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUser(ctx, AccountChange{Username: "alice"}, testOpenPrincipal("ops")); err != nil {
		t.Fatal(err)
	}
	if pr, err := s.SessionIdentity(ctx, signed.Token); err != nil || pr.Name != "alice" {
		t.Fatalf("omitted password ended the session: %v", err)
	}
	if err := s.SaveUser(ctx, AccountChange{Username: "alice", Password: "replacement-password"}, testOpenPrincipal("ops")); err != nil {
		t.Fatal(err)
	}
	if pr, err := s.SessionIdentity(ctx, signed.Token); err != nil || pr.Name != "" {
		t.Fatalf("reset retained the old session: %v", err)
	}
	if _, err := s.SignIn(ctx, PasswordLogin{Username: "alice", Password: in.Password}); !errors.Is(err, ErrCredentials) {
		t.Fatalf("old password still admitted: %v", err)
	}
	if _, err := s.SignIn(ctx, PasswordLogin{Username: "alice", Password: "replacement-password"}); err != nil {
		t.Fatal(err)
	}
	var payloads []string
	for _, receipt := range ledger(t, s.db, "user.saved") {
		payloads = append(payloads, string(receipt.Payload))
	}
	receipts := strings.Join(payloads, ",")
	if receipts == "" {
		t.Fatal("account saves left no receipt")
	}
	if strings.Contains(receipts, in.Password) || strings.Contains(receipts, "replacement-password") || strings.Contains(receipts, "$2") {
		t.Fatal("account receipt contains password material")
	}
}

func TestBootstrapSessionFailureRollsBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{SessionTTL: time.Hour})
	in := NewAccount{Username: "first-admin", Password: "first-password"}
	restore := s.db.FailNextStatement("-- name: InsertSession")
	signed, err := s.Bootstrap(ctx, in)
	restore()
	var issue *SessionIssueError
	if !errors.As(err, &issue) || signed.Token != "" || signed.Principal.Name != "" {
		t.Fatalf("failed session returned a bootstrap identity: %v", err)
	}
	if needs, err := s.NeedsBootstrap(ctx); err != nil || !needs {
		t.Fatalf("failed bootstrap closed setup: needs=%v err=%v", needs, err)
	}
	for _, table := range []string{"users", "groups", "group_members", "grants", "audit_log", "sessions"} {
		var n int
		// sql-guarantee: Rollback must leave no row of any kind, including ones no owner read lists (a session with no token handed back).
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("failed bootstrap left %s rows: %d, %v", table, n, err)
		}
	}
	signed, err = s.Bootstrap(ctx, in)
	if err != nil || signed.Token == "" || signed.Principal.Name != in.Username || signed.Principal.Role != "admin" {
		t.Fatalf("bootstrap could not be retried: %v", err)
	}
	pr, err := s.SessionIdentity(ctx, signed.Token)
	if err != nil || pr.Name != in.Username || pr.Role != "admin" {
		t.Fatalf("committed session does not admit its administrator: %v", err)
	}
	stored, err := queries(s.db).SessionByHash(ctx, HashToken(signed.Token))
	if err != nil {
		t.Fatal(err)
	}
	if expires := stored.ExpiresAt; stored.Username != in.Username || time.Until(expires) > time.Hour || time.Until(expires) < 59*time.Minute {
		t.Fatal("bootstrap session did not use the configured lifetime and stored hash")
	}
}

func TestCompletedSetupCannotReopenAfterAuthorityLoss(t *testing.T) {
	t.Parallel()
	s := New(testDB(t), Config{})
	if err := s.SeedAdministrator(context.Background(), "root"); err != nil {
		t.Fatal(err)
	}
	// Simulate expired or externally removed authority, beyond the last-root guard.
	// sql-guarantee: Remove even the last root grant after setup; Revoke refuses this state and no named query deletes all authority. The persisted setup marker must still prevent a second bootstrap.
	if _, err := s.db.Exec(`DELETE FROM grants`); err != nil {
		t.Fatal(err)
	}
	if _, err := queries(s.db).DeleteUser(context.Background(), "root"); err != nil {
		t.Fatal(err)
	}
	if needs, err := s.NeedsBootstrap(context.Background()); err != nil || needs {
		t.Fatalf("setup reopened: %v %v", needs, err)
	}
	if _, err := s.Bootstrap(context.Background(), NewAccount{Username: "intruder", Password: "new-password"}); !errors.Is(err, ErrAlreadyBootstrapped) {
		t.Fatalf("new root admitted: %v", err)
	}
}

func TestSetupMarkerFailureRollsBackRootSeed(t *testing.T) {
	t.Parallel()
	s := New(testDB(t), Config{})
	restore := s.db.FailNextStatement("-- name: PutInstanceSetting")
	err := s.SeedAdministrator(context.Background(), "root")
	restore()
	if err == nil {
		t.Fatal("root seed succeeded without setup marker")
	}
	if root, err := RootIn(context.Background(), s.db, "root", time.Now()); err != nil || root {
		t.Fatalf("partial root seed: %v %v", root, err)
	}
	if needs, err := s.NeedsBootstrap(context.Background()); err != nil || !needs {
		t.Fatalf("failed setup closed bootstrap: %v %v", needs, err)
	}
}

// The host operator's break-glass gives an existing account a new password
// through the administrator's account change: the old password and sessions
// stop working, one receipt names the operator, authority and disabled state
// are untouched, and an unknown name is refused without creating an account.
func TestOperatorPasswordResetIsTheAccountChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{})
	testGrantRoot(t, s, "ops")
	if err := s.SaveUser(ctx, AccountChange{Username: "root", Password: "forgotten-password", Role: "admin"}, testPrincipal("ops")); err != nil {
		t.Fatal(err)
	}
	disabled := true
	if err := s.SaveUser(ctx, AccountChange{Username: "away", Password: "away-password", Disabled: &disabled}, testPrincipal("ops")); err != nil {
		t.Fatal(err)
	}
	signed, err := s.SignIn(ctx, PasswordLogin{Username: "root", Password: "forgotten-password"})
	if err != nil {
		t.Fatal(err)
	}

	password, err := s.ResetPassword(ctx, "root")
	if err != nil || len(password) < 32 {
		t.Fatalf("reset: %d characters, %v", len(password), err)
	}
	if pr, err := s.SessionIdentity(ctx, signed.Token); err != nil || pr.Name != "" {
		t.Fatalf("reset kept the old session: %v", err)
	}
	if _, err := s.SignIn(ctx, PasswordLogin{Username: "root", Password: "forgotten-password"}); !errors.Is(err, ErrCredentials) {
		t.Fatalf("old password still admitted: %v", err)
	}
	if signed, err := s.SignIn(ctx, PasswordLogin{Username: "root", Password: password}); err != nil || signed.Principal.Role != "admin" {
		t.Fatalf("new password: role %q, %v", signed.Principal.Role, err)
	}
	receipts, err := audit.Recent(ctx, s.db, audit.Filter{KindPrefix: "user.saved", Subject: "root"})
	if err != nil || len(receipts) != 2 || receipts[0].Actor != OperatorActor {
		t.Fatalf("receipts %+v, %v", receipts, err)
	}
	if strings.Contains(string(receipts[0].Payload), password) {
		t.Fatal("receipt contains the password")
	}

	if _, err := s.ResetPassword(ctx, "away"); err != nil {
		t.Fatal(err)
	}
	if off, err := s.Disabled(ctx, "away"); err != nil || !off {
		t.Fatalf("reset re-enabled a disabled account: %v %v", off, err)
	}
	if role, _, err := s.Role(ctx, "away"); err != nil || role != "member" {
		t.Fatalf("reset changed a member's role: %q %v", role, err)
	}

	if _, err := s.ResetPassword(ctx, "nobody"); !errors.Is(err, ErrNoSuchName) {
		t.Fatalf("unknown name: %v", err)
	}
	if _, found, err := s.Role(ctx, "nobody"); err != nil || found {
		t.Fatalf("unknown name created an account: %v %v", found, err)
	}
}

// Disabling and enabling without a role leaves authority exactly as it was:
// an administrator through a group gains no direct grant, and a direct
// administrator keeps theirs.
func TestAccountStateChangeWithoutRoleKeepsAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{})
	if err := s.SeedAdministrator(ctx, "backup"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "dana"} {
		if err := s.SaveUser(ctx, AccountChange{Username: name, Password: "long-enough-1"}, testPrincipal("backup")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveGroup(ctx, GroupChange{Name: "operators", Members: []string{"alice"}}, testPrincipal("backup")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Grant(ctx, Spec{SubjectType: "group", Subject: "operators", Role: access.RoleInstanceAdmin, Scope: access.ScopeInstance, By: "backup"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUser(ctx, AccountChange{Username: "dana", Role: "admin"}, testPrincipal("backup")); err != nil {
		t.Fatal(err)
	}
	direct := func(name string) int {
		grants, err := s.GrantsAt(ctx, access.ScopeInstance, "")
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, g := range grants {
			if g.SubjectType == "user" && g.Subject == name && g.RoleName == access.RoleInstanceAdmin {
				n++
			}
		}
		return n
	}
	for _, c := range []struct {
		name   string
		direct int
	}{{"alice", 0}, {"dana", 1}} {
		for _, disabled := range []bool{true, false} {
			if err := s.SaveUser(ctx, AccountChange{Username: c.name, Disabled: &disabled}, testPrincipal("backup")); err != nil {
				t.Fatalf("%s disabled=%v: %v", c.name, disabled, err)
			}
			if got := direct(c.name); got != c.direct {
				t.Errorf("%s disabled=%v: %d direct administrator grants, want %d", c.name, disabled, got, c.direct)
			}
		}
		if root, err := RootIn(ctx, s.db, c.name, time.Now()); err != nil || !root {
			t.Errorf("%s is no longer an administrator after disable and enable: %v %v", c.name, root, err)
		}
	}
}
