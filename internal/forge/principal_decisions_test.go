package forge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids/idstest"
)

// Independent approval compares people, not names: the change's opener,
// renamed, is still its opener and may not approve it; a new account given
// the opener's old name is someone else and may.
func TestIndependentApprovalFollowsThePersonThroughARename(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	fx := fixtureWork(t, a)
	h := a.Handler()
	if err := a.Projects.SetIndependentApproval(verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "boss", Via: "session"}), idstest.N(1), true); err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/users/owner/rename", `{"name":"owner2"}`); w.Code != 200 {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "owner2", "POST", "/api/v1/steps/"+fx.Accept.String()+"/approve", `{"decision":"approve"}`); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "opened this change") {
		t.Fatalf("the renamed opener approved their own change: %d %s", w.Code, w.Body.String())
	}
	// "owner" now names a new person, who is not the opener.
	a.addGrant("user", "owner", access.RoleProjectApprover, "", access.ScopeProject, idstest.N(1).String(), "", "boss", "")
	if w := authzCall(h, "owner", "POST", "/api/v1/steps/"+fx.Accept.String()+"/approve", `{"decision":"approve"}`); w.Code != 200 {
		t.Fatalf("a new person with the opener's old name: %d %s", w.Code, w.Body.String())
	}
}

// A quorum counts people: a person renamed between votes is still the one
// who voted, and cannot vote again under the new name.
func TestAQuorumCountsPeopleNotNames(t *testing.T) {
	t.Parallel()
	a := authzFixtureWithApproval(t, workflow.ApprovalConfig{Quorum: 2})
	fx := fixtureWork(t, a)
	h := a.Handler()
	approve := `{"decision":"approve"}`
	if w := authzCall(h, "owner", "POST", "/api/v1/steps/"+fx.Accept.String()+"/approve", approve); w.Code != 200 {
		t.Fatalf("first vote: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/users/owner/rename", `{"name":"owner2"}`); w.Code != 200 {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "owner2", "POST", "/api/v1/steps/"+fx.Accept.String()+"/approve", approve); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already decided") {
		t.Fatalf("the renamed voter voted again: %d %s", w.Code, w.Body.String())
	}
}

// Approving is a person's own act: their coding agent's token, acting for
// them, cannot give it.
func TestAnAgentTokenCannotApprove(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	fx := fixtureWork(t, a)
	agent, err := a.Access.IssueToken(context.Background(), identity.NewToken{Purpose: access.TokenAgent, Name: "owners-agent", User: "owner"}, access.Principal{Kind: "user", Name: "boss", Via: "session", Role: "admin"})
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

// History names people as they are now: a line a renamed person wrote reads
// with their new name and the one they had then.
func TestTheLedgerNamesARenamedPersonAsTheyAreNow(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/changes", `{"type":"feature","title":"more"}`); w.Code != 201 {
		t.Fatalf("change: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/users/owner/rename", `{"name":"owner2"}`); w.Code != 200 {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	w := authzCall(h, "boss", "GET", "/api/v1/audit?kind=change.created", "")
	var lines []struct{ Actor, ActorShown string }
	if err := json.Unmarshal(w.Body.Bytes(), &lines); err != nil || w.Code != 200 || len(lines) != 1 || lines[0].Actor != "owner" || lines[0].ActorShown != "owner2 (then owner)" {
		t.Fatalf("the ledger: %d %s %v", w.Code, w.Body.String(), err)
	}
}
