package inference

import (
	"context"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"testing"
)

// testAdmittedPrincipal reads a fixture's existing person without creating
// authority or silently repairing a missing account.
func testAdmittedPrincipal(t *testing.T, people *identity.Service, name string) access.Principal {
	t.Helper()
	accounts, err := people.Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range accounts {
		if account.Username == name {
			return access.Principal{ID: account.PrincipalID, Kind: "user", Name: name, Via: "session", Role: account.Role}
		}
	}
	t.Fatalf("fixture account %q is missing", name)
	return access.Principal{}
}
