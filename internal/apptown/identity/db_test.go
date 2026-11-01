package identity

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

var databaseSuite = dbtest.NewSuite(dbschema.Open)

func TestMain(m *testing.M)            { os.Exit(databaseSuite.Run(m)) }
func testDB(t *testing.T) *database.DB { t.Helper(); return databaseSuite.New(t) }

func testPrincipal(name string) access.Principal {
	return access.Principal{Kind: "user", Name: name, Via: "session", Role: "admin"}
}

// admittedPrincipal reads the identity already established by the fixture.
// It never creates an account to make an authorization test pass.
func admittedPrincipal(t *testing.T, s *Service, name string) access.Principal {
	t.Helper()
	accounts, err := s.Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range accounts {
		if account.Username == name {
			pr := testPrincipal(name)
			pr.ID = account.PrincipalID
			return pr
		}
	}
	t.Fatalf("fixture account %q is missing", name)
	return access.Principal{}
}

func admittedOpenPrincipal(t *testing.T, s *Service, name string) access.Principal {
	t.Helper()
	pr := admittedPrincipal(t, s, name)
	pr.Via = "open"
	return pr
}

func testOpenPrincipal(name string) access.Principal {
	return access.Principal{Kind: "user", Name: name, Via: "open"}
}

// Storage fixtures explicitly exercise private transitions; public command
// tests use real principals and configured admission policy instead.
func testSaveAccount(db *database.DB, edit accountEdit, by string) error {
	ctx := context.Background()
	return db.InTx(ctx, func(tx *database.Tx) error {
		now := time.Now()
		return preserveRoot(ctx, tx, now, func() error { return changeAccountIn(ctx, tx, edit, by, now) })
	})
}
func testRemoveName(db *database.DB, name, by string) error {
	return db.InTx(context.Background(), func(tx *database.Tx) error { return removeNameIn(context.Background(), tx, name, by, time.Now()) })
}
func testDeleteGroup(db *database.DB, name, by string) error {
	return db.InTx(context.Background(), func(tx *database.Tx) error { return deleteGroupIn(context.Background(), tx, name, by, time.Now()) })
}

func testIssueToken(s *Service, name, user, role, by string) (string, error) {
	ctx := context.Background()
	in, err := (NewToken{Purpose: "cli", Name: name, User: user, Role: role}).prepare(testPrincipal(by))
	if err != nil {
		return "", err
	}
	var issued issuedToken
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		var err error
		issued, err = issueTokenIn(ctx, tx, in, by, time.Now())
		return err
	})
	if err != nil {
		return "", err
	}
	return issued.Plaintext, nil
}

// testGrantRoot supplies explicit authority without unrelated personal-group
// setup, for tests of ordinary group/account commands.
func testGrantRoot(t *testing.T, s *Service, name string) {
	t.Helper()
	if _, err := s.EnsureUser(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutGrant(context.Background(), Spec{SubjectType: "user", Subject: name, Role: access.RoleInstanceAdmin, Scope: access.ScopeInstance, By: "test-fixture"}); err != nil {
		t.Fatal(err)
	}
}

// testAccounts records the named people's accounts, as a grant or member
// door does: credentials are issued to an account's principal only.
func testAccounts(t *testing.T, db *database.DB, names ...string) {
	t.Helper()
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		for _, name := range names {
			if err := recordPersonIn(context.Background(), tx, name, time.Now()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// principalOf is the principal of name's account, read through the owner.
func principalOf(t *testing.T, q database.Queryer, name string) ids.ID {
	t.Helper()
	u, err := queries(q).UserByName(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return u.PrincipalID
}
