package apptown

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestMemberRoutesPreserveAuthorizationAndAttribution(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	a.addGrant("user", "owner", access.RoleProjectOwner, "", access.ScopeProject, idstest.N(1).String(), "fixture", "boss", "")
	h := a.Handler()
	for _, tc := range []struct {
		name, who, path, body string
		code                  int
	}{
		{"denial precedes malformed input", "outsider", "game", "{", 403},
		{"missing project", "outsider", "missing", "{", 404},
		{"malformed input", "owner", "game", "{", 400},
		{"missing grantee", "owner", "game", `{"role":"project-viewer"}`, 400},
		{"wrong scope role", "owner", "game", `{"grantee":"new-member","role":"instance-admin"}`, 400},
		{"anonymous subject", "owner", "game", `{"grantee":"anonymous","role":"project-viewer"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := authzCall(h, tc.who, "POST", "/api/v1/projects/"+tc.path+"/members", tc.body)
			if w.Code != tc.code {
				t.Fatalf("response: %d %s", w.Code, w.Body.String())
			}
			if tc.code == 403 && w.Header().Get("X-AppTown-Deny-Code") == "" {
				t.Fatal("denial code lost")
			}
		})
	}
	// An independent persistence check protects against refused commands mutating
	// the account or grant before returning the expected HTTP error.
	if accountExists(t, a, "new-member") {
		t.Fatal("refused request created the account")
	}
	if n := len(ledgerEntries(t, a, ids.Nil, "project.member-added")); n != 0 {
		t.Fatalf("refused request left %d receipts", n)
	}
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/members", `{"grantee":"user:new-member","role":"project-viewer"}`); w.Code != 200 {
		t.Fatalf("scoped owner: %d %s", w.Code, w.Body.String())
	}
	if n := len(liveGrants(t, a, access.ScopeProject, idstest.N(1).String(), "user", "new-member", "project-viewer")); n != 1 {
		t.Fatalf("%d live viewer grants", n)
	}
	if receipts := ledgerEntries(t, a, idstest.N(1), "project.member-added"); len(receipts) != 1 || receipts[0].Actor != "owner" || receipts[0].Subject != "user:new-member" || receipts[0].ProjectID != idstest.N(1) {
		t.Fatalf("member-added receipts: %+v", receipts)
	}
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/members", `{"grantee":"user:run/4","role":"project-viewer"}`); w.Code != 400 || accountExists(t, a, "run/4") {
		t.Fatalf("an unusable member name: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "owner", "DELETE", "/api/v1/projects/game/members/nobody-here", ""); w.Code != 404 {
		t.Fatalf("missing member: %d %s", w.Code, w.Body.String())
	}
}

func TestMemberRouteRejectsSpoofedProxyIdentity(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{AuthMode: "trusted-header", AuthHeader: "X-SSO-User", AuthProxySecret: "hush"})
	testAdministrator(t, a, "boss")
	h := a.Handler()
	req := httptest.NewRequest("POST", "/api/v1/projects/missing/members", strings.NewReader(`{"grantee":"intruder","role":"project-owner"}`))
	req.Header.Set("X-SSO-User", "boss")
	req.Header.Set("X-AppTown-Actor", "boss")
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	if out.Code != 401 {
		t.Fatalf("spoof bypassed authentication: %d %s", out.Code, out.Body.String())
	}
	if accountExists(t, a, "intruder") {
		t.Fatal("spoofed request created the account")
	}
}
