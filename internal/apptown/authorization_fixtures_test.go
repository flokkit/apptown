package apptown

import (
	"context"
	"net/http"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func vetoAuthorizationRecord(t *testing.T, db *database.DB, table, operation, body string) func() {
	t.Helper()
	completionExec(t, db, `CREATE FUNCTION veto_authorization_record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
	completionExec(t, db, `CREATE TRIGGER veto_authorization_record BEFORE `+operation+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_authorization_record()`)
	return func() { completionExec(t, db, `DROP TRIGGER veto_authorization_record ON `+table) }
}

// testAdministrator is explicit authority for HTTP tests about another concern.
// Authorization tests supply their own scoped grants instead.
func testAdministrator(t *testing.T, a *App, user string) {
	t.Helper()
	if err := a.Access.SeedAdministrator(context.Background(), user); err != nil {
		t.Fatal(err)
	}
}

// testAccounts records each name as an account through the identity owner,
// for fixtures whose callers act as people without a grant door recording
// them: authority is decided only for an enabled account.
func testAccounts(t *testing.T, db *database.DB, names ...string) {
	t.Helper()
	ids := identity.New(db, identity.Config{})
	for _, name := range names {
		if _, err := ids.EnsureUser(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
}

// testPrincipalOf is the principal of name's account.
func testPrincipalOf(t *testing.T, db *database.DB, name string) ids.ID {
	t.Helper()
	who, err := identity.ActorIn(context.Background(), db, name)
	if err != nil || who.Principal.IsZero() {
		t.Fatalf("%s has no principal: %v", name, err)
	}
	return who.Principal
}

// testRequestAs models a transport-established identity without granting it any
// authority. Handler tests still provide their domain grants in their fixtures.
func testRequestAs(r *http.Request, user string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authCtxKey{}, &authIdentity{User: user, Via: "session"}))
}
