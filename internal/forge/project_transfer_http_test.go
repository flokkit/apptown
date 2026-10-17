package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestProjectTransferHTTPRequiresBothSidesAndKeepsHistory(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	h := a.Handler()
	for _, body := range []string{`{"name":"destination"}`, `{"name":"neighbor"}`} {
		if w := authzCall(h, "boss", "POST", "/api/v1/groups", body); w.Code != 200 {
			t.Fatalf("group: %d %s", w.Code, w.Body.String())
		}
	}
	w := authzCall(h, "boss", "POST", "/api/v1/projects", `{"slug":"young"}`)
	var created project.Project
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || w.Code != 201 {
		t.Fatalf("create: %d %s %v", w.Code, w.Body.String(), err)
	}
	// Read the normal creation result rather than assuming a sequence value.
	original, err := a.Projects.BySlug(context.Background(), "young")
	if err != nil {
		t.Fatal(err)
	}
	grant := func(spec identity.Spec) {
		t.Helper()
		if _, err := a.Access.Grant(context.Background(), spec, ""); err != nil {
			t.Fatal(err)
		}
	}
	grant(identity.Spec{SubjectType: "user", Subject: "owner", Role: access.RoleProjectMaintainer, Scope: access.ScopeProject, ScopeID: fmt.Sprint(original.ID), By: "boss"})
	for _, tc := range []struct {
		who, slug, body string
		code            int
	}{
		{"boss", "young", `{`, 400}, {"boss", "young", `{"group":"missing"}`, 404},
		{"outsider", "young", `{"group":"destination"}`, 403}, {"owner", "young", `{"group":"destination"}`, 403},
		{"boss", "game", `{"group":"destination"}`, 409},
	} {
		w := authzCall(h, tc.who, "POST", "/api/v1/projects/"+tc.slug+"/owner", tc.body)
		if w.Code != tc.code {
			t.Fatalf("transfer rejection: %d %s", w.Code, w.Body.String())
		}
		p, err := a.Projects.ByID(context.Background(), original.ID)
		if err != nil || p.OwnerGroup != original.OwnerGroup {
			t.Fatalf("denied transfer changed owner: %+v %v", p, err)
		}
	}
	grant(identity.Spec{SubjectType: "user", Subject: "owner", Capability: string(access.CapProjectCreate), Scope: access.ScopeGroup, ScopeID: "destination", By: "boss"})
	w = authzCall(h, "owner", "POST", "/api/v1/projects/young/owner", `{"group":"destination"}`)
	var out struct {
		OK       bool
		From, To string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || !out.OK || out.From != original.OwnerGroup || out.To != "destination" {
		t.Fatalf("transfer: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	p, err := a.Projects.ByID(context.Background(), original.ID)
	if err != nil || p.OwnerGroup != "destination" {
		t.Fatalf("owner not transferred: %+v %v", p, err)
	}
	game, err := a.Projects.ByID(context.Background(), idstest.N(1))
	if err != nil || game.OwnerGroup == "destination" {
		t.Fatalf("history project moved: %+v %v", game, err)
	}
}
