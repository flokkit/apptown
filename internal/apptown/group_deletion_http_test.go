package apptown

import (
	"context"
	"slices"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/identity"
)

func TestGroupDeletionHTTPProtectsAuthorityAndSystemGroups(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	if w := authzCall(h, "boss", "POST", "/api/v1/groups", `{"name":"temporary","members":["owner"]}`); w.Code != 200 {
		t.Fatalf("create group: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "outsider", "DELETE", "/api/v1/groups/temporary", ""); w.Code != 403 {
		t.Fatalf("unauthorized delete: %d %s", w.Code, w.Body.String())
	}
	if kind, err := a.Access.GroupKind(context.Background(), "temporary"); err != nil || kind == "" {
		t.Fatalf("denial deleted group: %q %v", kind, err)
	}
	if w := authzCall(h, "boss", "DELETE", "/api/v1/groups/"+identity.GroupAllUsers, ""); w.Code != 409 {
		t.Fatalf("system group: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", "DELETE", "/api/v1/groups/temporary", ""); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", "DELETE", "/api/v1/groups/temporary", ""); w.Code != 404 {
		t.Fatalf("missing group: %d %s", w.Code, w.Body.String())
	}
	if kind, _ := a.Access.GroupKind(context.Background(), "temporary"); kind != "" {
		t.Fatal("successful deletion left group")
	}
}

// Member operations must preserve unrelated members and the description, even
// when a page's old reading no longer includes the latest member.
func TestGroupMemberHTTPChangesOneMemberAndChecksAuthority(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	call := func(actor, method, path, body string, want int) {
		t.Helper()
		w := authzCall(h, actor, method, path, body)
		if w.Code != want {
			t.Fatalf("%s %s as %s: %d %s", method, path, actor, w.Code, w.Body.String())
		}
	}
	call("boss", "POST", "/api/v1/groups", `{"name":"team","description":"Keep this","members":["owner"]}`, 200)
	call("outsider", "POST", "/api/v1/groups/team/members/boss", "{}", 403)
	call("boss", "POST", "/api/v1/groups/team/members/boss", "{}", 200)
	call("boss", "POST", "/api/v1/groups/team/members/outsider", "{}", 200)
	call("boss", "POST", "/api/v1/groups/team/members/outsider", "{}", 200)
	call("outsider", "DELETE", "/api/v1/groups/team/members/owner", "", 403)
	call("boss", "DELETE", "/api/v1/groups/team/members/owner", "", 200)
	groups, err := a.Access.Groups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g.Name == "team" && (g.Description != "Keep this" || !slices.Equal(g.Members, []string{"boss", "outsider"})) {
			t.Fatalf("one-member change replaced other facts: %+v", g)
		}
	}
	call("boss", "POST", "/api/v1/groups/team/members/missing", "{}", 404)
	call("boss", "POST", "/api/v1/groups/missing/members/owner", "{}", 404)
	call("boss", "POST", "/api/v1/groups/"+identity.GroupAllUsers+"/members/owner", "{}", 409)
}
