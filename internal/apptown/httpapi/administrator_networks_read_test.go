package httpapi

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
)

type networkTestAddress struct{}

func (networkTestAddress) ClientIP(r *http.Request) net.IP {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return net.ParseIP(host)
}

func TestAdminNetworksGETRequiresReadablePolicy(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	accounts := identity.New(db, identity.Config{})
	if err := accounts.SeedAdministrator(context.Background(), "boss"); err != nil {
		t.Fatal(err)
	}
	h := NewAdministratorNetworks(accounts, networkTestAddress{})
	call := func(method, address, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/auth/admin-networks", strings.NewReader(body))
		req.RemoteAddr = address + ":1234"
		req = req.WithContext(access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "boss", Via: "session"}, Verified: true}))
		w := httptest.NewRecorder()
		h.AdminNetworks(w, req)
		return w
	}
	if w := call("PUT", "192.0.2.1", `{"expected":[],"networks":["192.0.2.0/24"]}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	restore := db.FailNextStatement("-- name: InstanceSetting :one")
	w := call("GET", "192.0.2.1", "")
	restore()
	if w.Code != 500 {
		t.Fatalf("unreadable policy: %d %s", w.Code, w.Body)
	}
	w = call("GET", "203.0.113.1", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"youInside":false`) || !strings.Contains(w.Body.String(), "192.0.2.0/24") {
		t.Fatalf("policy view: %d %s", w.Code, w.Body)
	}
}
