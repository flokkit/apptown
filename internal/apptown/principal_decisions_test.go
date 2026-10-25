package apptown

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
)

// Approving is a person's own act: their coding agent's token, acting for
// them, cannot give it.
func TestAnAgentTokenCannotApprove(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	fx := fixtureWork(t, a)
	agent, err := a.Access.IssueToken(context.Background(), identity.NewToken{Purpose: access.TokenAgent, Name: "owners-agent", User: "owner"}, access.Principal{ID: testPrincipalOf(t, a.DB, "boss"), Kind: "user", Name: "boss", Via: "session", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/steps/"+fx.Accept.String()+"/approve", strings.NewReader(`{"decision":"approve"}`))
	req.Header.Set("Authorization", "Bearer "+agent.Plaintext)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "own") {
		t.Fatalf("an agent's token approved: %d %s", w.Code, w.Body.String())
	}
}
