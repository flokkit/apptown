package apptown

import (
	"context"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/access"
)

func TestTransportCallerSeparatesProvenanceAndReceipt(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("X-AppTown-Actor", "root")
	c, ok := access.CallerFrom(callerContext(r))
	if !ok || c.Verified || c.Receipt != "root" || c.Principal.Kind != "anonymous" || c.Principal.Name != "anonymous" {
		t.Fatalf("header became authority: %+v", c)
	}
	r = testRequestAs(r, "person")
	c, _ = access.CallerFrom(callerContext(r))
	if !c.Verified || c.Principal.Name != "person" || c.Receipt != "person" {
		t.Fatalf("transport provenance: %+v", c)
	}
	c, _ = access.CallerFrom(callerMCPContext(context.Background(), nil, "person via client"))
	if c.Verified {
		t.Fatal("MCP receipt authenticated caller")
	}
	c, _ = access.CallerFrom(callerMCPContext(context.Background(), &authIdentity{User: "person", Via: "oauth", Scopes: []string{"apptown:user:read"}}, "person via client"))
	if !c.Verified || c.Principal.Name != "person" || c.Receipt != "person via client" {
		t.Fatalf("MCP attribution: %+v", c)
	}
}

func TestActorOnlyRequestCannotAcquireRouteOrRosterAuthority(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	mux, err := newRouteMux(a, appRoutes(a))
	if err != nil {
		t.Fatal(err)
	}
	// This route normally sits behind AuthMiddleware. Calling it without
	// admission must not turn a known member's actor header into authority.
	w := authzCall(mux, "owner", "POST", "/api/v1/projects/game/changes", `{"title":"actor-only"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unadmitted route: %d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("X-AppTown-Actor", "owner")
	w = httptest.NewRecorder()
	if a.requireProjectMember(w, r, idstest.N(1), "game") || w.Code != http.StatusForbidden {
		t.Fatal("actor header passed roster admission")
	}
	// Normal open-mode admission still supplies the member's real identity.
	reached := false
	r.URL.Path = "/api/v1/projects/game/changes"
	a.AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = a.requireProjectMember(w, r, idstest.N(1), "game")
	})).ServeHTTP(httptest.NewRecorder(), r)
	if !reached {
		t.Fatal("admitted open-mode member refused")
	}
	r = r.WithContext(context.WithValue(r.Context(), authCtxKey{}, &authIdentity{
		PrincipalID: testPrincipalOf(t, a.DB, "owner"), User: "owner via console/c", Via: "console",
		ProjectID: idstest.N(1), ConsoleID: "c",
	}))
	if !a.requireProjectMember(httptest.NewRecorder(), r, idstest.N(1), "game") {
		t.Fatal("verified console lost its person's membership")
	}
}
