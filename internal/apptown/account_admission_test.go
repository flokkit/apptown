package apptown

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
)

func TestAccountReadFailureRejectsHTTPIdentity(t *testing.T) {
	t.Parallel()
	a, h := internalApp(t)
	who := sessionFrom(t, postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "boss", "boss-password")))
	issued, err := a.Access.IssueToken(context.Background(), identity.NewToken{Purpose: "cli", Name: "test-cli", User: "boss", Role: "admin"}, access.Principal{Kind: "user", Name: "boss", Via: "session"})
	if err != nil {
		t.Fatal(err)
	}
	for _, via := range []string{"session", "token"} {
		t.Run(via, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
			if via == "session" {
				req.AddCookie(who)
			} else {
				req.Header.Set("Authorization", "Bearer "+issued.Plaintext)
			}
			restore := a.DB.FailNextStatement("-- name: TouchUser")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			restore()
			// auth/me is public, but a failed read cannot report an authenticated user.
			if strings.Contains(w.Body.String(), `"user":"boss"`) {
				t.Fatalf("account read failure became a user: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
