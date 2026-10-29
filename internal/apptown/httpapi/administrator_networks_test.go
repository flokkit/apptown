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
	"github.com/flokkit/apptown/internal/database"
)

type loopbackClient struct{}

func (loopbackClient) ClientIP(*http.Request) net.IP { return net.ParseIP("127.0.0.1") }
func networkCall(h AdministratorNetworks, who, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: who, Via: "session"}, Verified: true}))
	w := httptest.NewRecorder()
	h.AdminNetworks(w, r)
	return w
}

func TestConfigurationHTTPRequiresReadableAuthorityAndMapsErrors(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/api/v1/auth/admin-networks"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			db, accounts := accountHTTPFixture(t)
			h := NewAdministratorNetworks(accounts, loopbackClient{})
			valid, invalid := `{"expected":[],"networks":["192.0.2.0/24"]}`, `{"expected":[],"networks":["bad"]}`
			if w := networkCall(h, "boss", "PUT", path, invalid); w.Code != 400 {
				t.Fatalf("invalid configuration: %d %s", w.Code, w.Body)
			}
			restore := db.FailNextStatement("-- name: UserByName :one")
			w := networkCall(h, "boss", "PUT", path, valid)
			restore()
			if w.Code != 500 {
				t.Fatalf("unread authority: %d %s", w.Code, w.Body)
			}
			adminNetworksAre(t, db, accounts, 0)
			if w := networkCall(h, "boss", "PUT", path, valid); w.Code != 200 {
				t.Fatalf("retry: %d %s", w.Code, w.Body)
			}
		})
	}
}
func TestAuthorizationSettingsRequireWriteAndReceipt(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/api/v1/auth/admin-networks"} {
		for _, table := range []string{"instance_settings", "audit_log"} {
			t.Run(path+"/"+table, func(t *testing.T) {
				db, accounts := accountHTTPFixture(t)
				h := NewAdministratorNetworks(accounts, loopbackClient{})
				body, kind := `{"expected":[],"networks":["192.0.2.0/24"]}`, "auth.admin-networks"
				veto := "RETURN NULL;"
				if table == "audit_log" {
					veto = "IF NEW.kind='" + kind + "' THEN RETURN NULL; END IF; RETURN NEW;"
				}
				drop := vetoHTTPRecord(t, db, table, "INSERT", veto)
				w := networkCall(h, "boss", "PUT", path, body)
				if w.Code != 500 {
					t.Fatalf("missing record: %d %s", w.Code, w.Body)
				}
				adminNetworksAre(t, db, accounts, 0)
				if receipts := receiptsOfKind(t, db, kind); len(receipts) != 0 {
					t.Fatalf("receipt without a change: %+v", receipts)
				}
				drop()
				if w := networkCall(h, "boss", "PUT", path, body); w.Code != 200 {
					t.Fatalf("retry: %d %s", w.Code, w.Body)
				}
				adminNetworksAre(t, db, accounts, 1)
				if receipts := receiptsOfKind(t, db, kind); len(receipts) != 1 {
					t.Fatalf("receipts: %+v", receipts)
				}
			})
		}
	}
}

// adminNetworksAre checks the owner's answer and the stored rows: an empty
// stored value reads the same as none through AdministratorNetworks, so a
// failed change must also leave exactly the expected number of setting rows.
func adminNetworksAre(t *testing.T, db *database.DB, accounts *identity.Service, want int) {
	t.Helper()
	networks, err := accounts.AdministratorNetworks(context.Background())
	if err != nil || len(networks) != want {
		t.Fatalf("administrator networks %v want %d: %v", networks, want, err)
	}
	var rows int
	// sql-guarantee: No owner read lists instance settings, and AdministratorNetworks reads a missing row and an empty stored value alike.
	if err := db.QueryRow(`SELECT count(*) FROM instance_settings WHERE key<>'setup_completed'`).Scan(&rows); err != nil || rows != want {
		t.Fatalf("instance setting rows %d want %d: %v", rows, want, err)
	}
}
