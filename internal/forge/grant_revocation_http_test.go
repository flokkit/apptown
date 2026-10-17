package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestGrantRevocationHTTPRemovesOnlySelectedAuthority(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	h := a.Handler()
	beforeGrants, err := a.Access.GrantsAt(t.Context(), access.ScopeProject, idstest.N(1).String())
	if err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "outsider", "POST", "/api/v1/authz/grants", `{"subject":"kid","role":"project-contributor","scope":"project","scopeId":"00000000-0001-7000-8000-000000000000"}`); w.Code != 403 {
		t.Fatalf("unauthorized grant: %d %s", w.Code, w.Body.String())
	}
	afterGrants, err := a.Access.GrantsAt(t.Context(), access.ScopeProject, idstest.N(1).String())
	if err != nil || len(beforeGrants) != len(afterGrants) {
		t.Fatalf("denied grant persisted: %+v %v", afterGrants, err)
	}
	w := authzCall(h, "owner", "POST", "/api/v1/authz/grants", `{"subject":"kid","role":"project-contributor","scope":"project","scopeId":"00000000-0001-7000-8000-000000000000","reason":"temporary contribution"}`)
	var grant struct{ ID ids.ID }
	if err := json.Unmarshal(w.Body.Bytes(), &grant); err != nil || w.Code != 201 || grant.ID.IsZero() {
		t.Fatalf("grant: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	create := func(slug string) int {
		return authzCall(h, "kid", "POST", "/api/v1/projects/"+slug+"/changes", `{"title":"a contribution","propose":true}`).Code
	}
	if got := create("game"); got != 201 {
		t.Fatalf("granted contributor could not create: %d", got)
	}
	before, err := a.Work.Changes(asPerson("owner"), idstest.N(1), workflow.ChangeList{})
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/authz/grants/%d", grant.ID)
	for _, tc := range []struct {
		who, path string
		code      int
	}{{"owner", "/api/v1/authz/grants/bad", 400}, {"owner", "/api/v1/authz/grants/0000000f-423f-7000-8000-000000000000", 404}, {"outsider", path, 403}} {
		w := authzCall(h, tc.who, "DELETE", tc.path, "")
		if w.Code != tc.code {
			t.Fatalf("revoke rejection: %d %s", w.Code, w.Body.String())
		}
	}
	if _, _, live, err := a.Access.LiveGrant(context.Background(), grant.ID); err != nil || !live {
		t.Fatalf("denial revoked grant: live=%v %v", live, err)
	}
	w = authzCall(h, "owner", "DELETE", path, "")
	if w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	if got := create("game"); got != 403 {
		t.Fatalf("revoked contribution still admitted: %d", got)
	}
	after, err := a.Work.Changes(asPerson("owner"), idstest.N(1), workflow.ChangeList{})
	if err != nil || len(after) != len(before) {
		t.Fatalf("revoked request created work: %d -> %d %v", len(before), len(after), err)
	}
	if got := create("open"); got != 201 {
		t.Fatalf("revoking game removed independent open authority: %d", got)
	}
	if w := authzCall(h, "owner", "DELETE", path, ""); w.Code != 404 {
		t.Fatalf("repeat revoke: %d %s", w.Code, w.Body.String())
	}
	// Root's last-administrator protection maps to a conflict without demotion.
	grants, err := a.Access.AllGrants(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var rootID ids.ID
	for _, g := range grants {
		if g.Subject == "boss" && g.RoleName == access.RoleInstanceAdmin {
			rootID = g.ID
		}
	}
	if rootID.IsZero() {
		t.Fatal("missing administrator grant")
	}
	w = authzCall(h, "boss", "DELETE", fmt.Sprintf("/api/v1/authz/grants/%d", rootID), "")
	if w.Code != 409 {
		t.Fatalf("last administrator: %d %s", w.Code, w.Body.String())
	}
	testAccountRole(t, a, "boss", "admin")
}
