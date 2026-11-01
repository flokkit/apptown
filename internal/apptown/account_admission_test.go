package apptown

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
)

func TestAccountReadFailureRejectsHTTPIdentity(t *testing.T) {
	t.Parallel()
	a, h := internalApp(t)
	who := sessionFrom(t, postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "boss", "boss-password")))
	issued, err := a.Access.IssueToken(context.Background(), identity.NewToken{Purpose: "cli", Name: "test-cli", User: "boss", Role: "admin"}, access.Principal{ID: testPrincipalOf(t, a.DB, "boss"), Kind: "user", Name: "boss", Via: "session"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ via, marker string }{
		{"session", "-- name: SessionByHash"},
		{"token", "-- name: AuthTokenByHash"},
		{"session", "-- name: TouchUser"},
		{"token", "-- name: TouchUser"},
	} {
		t.Run(tc.via+tc.marker, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
			if tc.via == "session" {
				req.AddCookie(who)
			} else {
				req.Header.Set("Authorization", "Bearer "+issued.Plaintext)
			}
			restore := dbtest.NextStatementFailure(a.DB, tc.marker)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			restore()
			// Even public auth/me must distinguish an unavailable lookup from anonymous success.
			if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), `"user":"boss"`) {
				t.Fatalf("account read failure became a user: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestMCPUnavailableCredentialDoesNotChallengeForReplacement(t *testing.T) {
	t.Parallel()
	a, h, who := oauthFixture(t)
	pair := authorizeAndExchange(t, h, who, registerClient(t, h), "apptown:user:read")
	oauthToken := pair["access_token"].(string)
	agent, err := a.Access.IssueToken(t.Context(), identity.NewToken{Purpose: "agent", Name: "test-agent", User: "boss", Role: "admin"}, access.Principal{ID: testPrincipalOf(t, a.DB, "boss"), Kind: "user", Name: "boss", Via: "session"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, token, marker string }{
		{"OAuth token", oauthToken, "-- name: TokenByHashAndKind"},
		{"OAuth delegation", oauthToken, "-- name: DelegationByID :one"},
		{"OAuth account", oauthToken, "-- name: TouchUser"},
		{"personal token", agent.Plaintext, "-- name: AuthTokenByHash"},
		{"personal account", agent.Plaintext, "-- name: TouchUser"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restore := dbtest.NextStatementFailure(a.DB, tc.marker)
			w := mcpWith(h, tc.token, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
			restore()
			if w.Code != http.StatusServiceUnavailable || w.Header().Get("WWW-Authenticate") != "" {
				t.Fatalf("lookup failure asks for replacement credential: %d %v %s", w.Code, w.Header(), w.Body)
			}
		})
	}
	w := mcpWith(h, "unknown", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("invalid credential: %d %v", w.Code, w.Header())
	}
}
