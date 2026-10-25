package deployment

import (
	"encoding/json"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestQueuedReleasePrincipalDoesNotRecoverAuthorityFromAName(t *testing.T) {
	original := access.Principal{ID: idstest.N(501), Kind: "user", Name: "alice", Credential: "header:alice"}
	for _, tc := range []struct {
		name      string
		principal access.Principal
		valid     bool
	}{
		{"complete user", original, true},
		{"name only", access.Principal{Kind: "user", Name: "alice"}, false},
		{"legacy name credential", access.Principal{Kind: "user", Name: "alice", Credential: "header:alice"}, false},
		{"UUID without credential", access.Principal{ID: original.ID, Kind: "user", Name: "alice"}, false},
		{"person claiming system", access.Principal{ID: original.ID, Kind: "user", Name: "alice", Credential: "system:release"}, false},
		{"named system binding", operations.System("binding/release"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(operations.AuthorityOf(tc.principal))
			if err != nil {
				t.Fatal(err)
			}
			got, err := releasePrincipal(raw)
			if tc.valid {
				if err != nil || got.ID != tc.principal.ID || got.Credential != tc.principal.Credential {
					t.Fatalf("captured identity changed: %+v %v", got, err)
				}
			} else if err == nil {
				t.Fatalf("incomplete identity accepted: %+v", got)
			}
		})
	}
	for _, raw := range []string{"", "null", "{", "{}"} {
		if got, err := releasePrincipal(json.RawMessage(raw)); err == nil {
			t.Errorf("missing/corrupt snapshot %q accepted as %+v", raw, got)
		}
	}
}
