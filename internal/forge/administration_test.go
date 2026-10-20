package forge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/identity"
)

func TestAdministrativeCommandsPreserveOpenAndProxyModes(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"open", "trusted-header", "internal"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			a := testApp(t, Config{AuthMode: mode, AuthHeader: "X-Test-User", AuthProxySecret: "test-proxy-secret"})

			h := a.Handler()
			call := func(who string, trusted bool) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "/api/v1/groups", strings.NewReader(`{"name":"managed"}`))
				r.Header.Set("X-AppTown-Actor", who)
				if trusted {
					r.Header.Set("X-Test-User", who)
					r.Header.Set("X-AppTown-Proxy-Secret", "test-proxy-secret")
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			if mode == "internal" {
				if w := call("boss", false); w.Code != 401 {
					t.Fatalf("internal actor header admitted: %d", w.Code)
				}
				return
			}
			if mode == "open" {
				if w := call("installer", false); w.Code != 403 {
					t.Fatalf("unseeded open command: %d %s", w.Code, w.Body)
				}
			} else if w := call("boss", false); w.Code != http.StatusUnauthorized {
				t.Fatalf("untrusted proxy: %d", w.Code)
			}
			if err := a.Access.SeedAdministrator(context.Background(), "boss"); err != nil {
				t.Fatal(err)
			}
			if w := call("member", mode == "trusted-header"); w.Code != 403 {
				t.Fatalf("member administrator: %d %s", w.Code, w.Body)
			}
			if w := call("boss", mode == "trusted-header"); w.Code != 200 {
				t.Fatalf("administrator refused: %d %s", w.Code, w.Body)
			}
			testAdministrator(t, a, "backup")
			disabled := true
			if err := a.Access.SaveUser(context.Background(), identity.AccountChange{Username: "boss", Disabled: &disabled}, access.Principal{Kind: "user", Name: "backup", Via: "session"}); err != nil {
				t.Fatal(err)
			}
			if w := call("boss", mode == "trusted-header"); w.Code != 403 {
				t.Fatalf("disabled administrator admitted: %d %s", w.Code, w.Body)
			}
		})
	}
}
