package identity

import (
	"testing"

	"flokkit.io/forge/internal/forge/identity/internal/accesssql"
)

// A stored redirect list or scope list that does not decode allows nothing:
// the decoder fills part of a slice before a type error, and a partial list
// is not what was registered or consented to.
func TestAnOAuthListThatDoesNotDecodeAllowsNothing(t *testing.T) {
	t.Parallel()
	if c := client(accesssql.OauthClient{ClientID: "c", RedirectUris: `["https://ok.example/cb", 17]`}); len(c.RedirectURIs) != 0 {
		t.Fatalf("a partly decoded redirect list: %v", c.RedirectURIs)
	}
	if d := connection(accesssql.DelegationsNamed{ScopesJson: []byte(`["apptown:change:create", 17]`)}); len(d.Scopes) != 0 {
		t.Fatalf("partly decoded scopes: %v", d.Scopes)
	}
	if c := client(accesssql.OauthClient{RedirectUris: `["https://ok.example/cb"]`}); len(c.RedirectURIs) != 1 {
		t.Fatalf("a readable list: %v", c.RedirectURIs)
	}
}
