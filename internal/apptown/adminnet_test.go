package apptown

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/dbtest"
)

func TestClientIPBehindProxy(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.168.1.50:1234"
	r.Header.Set("X-Forwarded-For", "8.8.8.8") // a LAN host lying about itself
	if got := (proxyPolicy{}).ClientIP(r).String(); got != "192.168.1.50" {
		t.Errorf("direct connection: headers must be ignored, got %s", got)
	}
	r.RemoteAddr = "127.0.0.1:1234" // Traefik on the host
	r.Header.Set("X-Real-IP", "203.0.113.9")
	if got := (proxyPolicy{}).ClientIP(r).String(); got != "203.0.113.9" {
		t.Errorf("behind a trusted proxy: X-Real-IP names the caller, got %s", got)
	}
	r.Header.Del("X-Real-IP")
	r.Header.Set("X-Forwarded-For", "10.9.9.9, 203.0.113.9, 127.0.0.1")
	if got := (proxyPolicy{}).ClientIP(r).String(); got != "203.0.113.9" {
		t.Errorf("rightmost non-proxy X-Forwarded-For entry is the caller, got %s", got)
	}
}

func TestAdminNetworksGateSignIn(t *testing.T) {
	t.Parallel()
	a, h := internalApp(t)
	admin := sessionFrom(t, postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "boss", "boss-password")))
	if w := postJSON(h, "/api/v1/users", map[string]string{"username": "kid", "password": "kid-password"}, admin); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	from := func(ip, path string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(mustJSON(body)))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = ip + ":4321"
		for _, c := range cookies {
			req.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	// Setting the fence from outside it is refused.
	req := httptest.NewRequest("PUT", "/api/v1/auth/admin-networks", strings.NewReader(`{"expected":[],"networks":["192.168.1.0/24"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.5:4321"
	req.AddCookie(admin)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "lock you out") {
		t.Fatalf("lock-out guard: %d %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest("PUT", "/api/v1/auth/admin-networks", strings.NewReader(`{"expected":[],"networks":["192.168.1.0/24"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.168.1.7:4321"
	req.AddCookie(admin)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("set from inside: %d %s", w.Code, w.Body.String())
	}
	// An administrator from outside cannot sign in; a member can; loopback always can.
	if w := from("203.0.113.9", "/api/v1/auth/login", map[string]string{"username": "boss", "password": "boss-password"}); w.Code != 403 || !strings.Contains(w.Body.String(), "192.168.1.0/24") {
		t.Errorf("admin from outside: %d %s", w.Code, w.Body.String())
	}
	if w := from("203.0.113.9", "/api/v1/auth/login", map[string]string{"username": "kid", "password": "kid-password"}); w.Code != 200 {
		t.Errorf("member from outside: %d %s", w.Code, w.Body.String())
	}
	if w := from("192.168.1.20", "/api/v1/auth/login", map[string]string{"username": "boss", "password": "boss-password"}); w.Code != 200 {
		t.Errorf("admin from inside: %d %s", w.Code, w.Body.String())
	}
	if w := from("127.0.0.1", "/api/v1/auth/login", map[string]string{"username": "boss", "password": "boss-password"}); w.Code != 200 {
		t.Errorf("admin from loopback: %d %s", w.Code, w.Body.String())
	}
	// An administrator token from outside is refused too.
	tok := mcp_token(t, a, "t", "boss", "admin")
	req = httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.RemoteAddr = "203.0.113.9:4321"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("admin token from outside: %d", w.Code)
	}
	if n := auditCount(t, a.DB, "auth.admin-login-refused"); n < 2 {
		t.Errorf("refusals are security events on the ledger: %d", n)
	}
}

func TestAuthorizationPolicyReadFailureDoesNotWidenAccess(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	if _, err := a.Access.SetAdministratorNetworks(context.Background(), access.Principal{Kind: "user", Name: "boss", Via: "session"}, &identity.AdministratorNetworks{}, identity.AdministratorNetworks{"192.0.2.0/24"}, net.ParseIP("192.0.2.1")); err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, a.DB, "-- name: InstanceSetting")
	if a.adminAllowedFrom(context.Background(), net.ParseIP("203.0.113.1")) {
		t.Fatal("failed network-policy read allowed an administrator from anywhere")
	}
}
