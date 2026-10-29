package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/identity"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
)

func accountManagementFixture(t *testing.T) (*database.DB, http.Handler) {
	t.Helper()
	db, service := accountHTTPFixture(t)
	h := NewAccounts(service)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/users", h.SaveUser)
	mux.HandleFunc("DELETE /api/v1/users/{name}", h.DeleteUser)
	mux.HandleFunc("POST /api/v1/groups", h.SaveGroup)
	return db, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(access.WithCaller(r.Context(), access.Caller{Principal: testAdmittedPrincipal(t, service, "boss"), Verified: true, Receipt: "boss"}))
		mux.ServeHTTP(w, r)
	})
}
func postAccountJSON(h http.Handler, path string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestGroupSaveRejectsFailedMemberObservation(t *testing.T) {
	t.Parallel()
	db, handler := accountManagementFixture(t)
	_ = db
	drop := vetoHTTPRecord(t, db, "users", "INSERT", `IF NEW.username='new-member' THEN RAISE EXCEPTION 'member observation failed'; END IF; RETURN NEW;`)
	in := map[string]any{"name": "kids", "members": []string{"new-member"}}
	w := postAccountJSON(handler, "/api/v1/groups", in)
	if w.Code != 500 {
		t.Fatalf("failed member observation accepted: %d %s", w.Code, w.Body)
	}
	accounts := identity.New(db, identity.Config{})
	if groupNamed(t, accounts, "kids") != nil || accountNamed(t, accounts, "new-member") {
		t.Fatal("failed member observation recorded the group or member")
	}
	var groups int
	// sql-guarantee: Groups lists only unretired groups and no exported read sees a retired one, so only the table proves the failed save left no group row at all.
	if err := db.SQL().QueryRowContext(context.Background(), `SELECT count(*) FROM groups WHERE name='kids'`).Scan(&groups); err != nil || groups != 0 {
		t.Fatalf("failed member observation left a group row: %d %v", groups, err)
	}
	drop()
	w = postAccountJSON(handler, "/api/v1/groups", in)
	if w.Code != 200 {
		t.Fatalf("retry: %d %s", w.Code, w.Body)
	}
	if kids := groupNamed(t, accounts, "kids"); kids == nil || !reflect.DeepEqual(kids.Members, []string{"new-member"}) || !accountNamed(t, accounts, "new-member") {
		t.Fatalf("retry did not record the membership: %+v", kids)
	}
}

// Passwordless names may be removed; real accounts are kept for the record.
func TestDeleteUserGuards(t *testing.T) {
	t.Parallel()
	db, handler := accountManagementFixture(t)
	_ = db
	if w := postAccountJSON(handler, "/api/v1/users", map[string]string{"username": "real", "password": "real-password"}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}

	req := httptest.NewRequest("DELETE", "/api/v1/users/real", nil)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "kept for the record") {
		t.Fatalf("real accounts must be undeletable: %d %s", w.Code, w.Body.String())
	}

	// A passwordless name (open-mode sediment) goes quietly.
	if w := postAccountJSON(handler, "/api/v1/groups", map[string]any{"name": "g", "members": []string{"ghost"}}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	req = httptest.NewRequest("DELETE", "/api/v1/users/ghost", nil)

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("passwordless name should be removable: %d %s", w.Code, w.Body.String())
	}
}

// Account management refuses malformed input in plain words.
func TestSaveUserValidation(t *testing.T) {
	t.Parallel()
	db, handler := accountManagementFixture(t)
	_ = db
	cases := []struct {
		body map[string]string
		want int
	}{
		{map[string]string{"username": "Bad Name", "password": "long-enough"}, 400},
		{map[string]string{"username": "ok-name", "password": "short"}, 400},
		{map[string]string{"username": "ok-name", "role": "emperor"}, 400},
		{map[string]string{"username": "ok-name", "password": "long-enough"}, 200},
	}
	for i, c := range cases {
		if w := postAccountJSON(handler, "/api/v1/users", c.body); w.Code != c.want {
			t.Errorf("case %d: got %d want %d (%s)", i, w.Code, c.want, w.Body.String())
		}
	}
}

func groupNamed(t *testing.T, accounts *identity.Service, name string) *identity.Group {
	t.Helper()
	groups, err := accounts.Groups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := range groups {
		if groups[i].Name == name {
			return &groups[i]
		}
	}
	return nil
}

func accountNamed(t *testing.T, accounts *identity.Service, name string) bool {
	t.Helper()
	users, err := accounts.Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		if user.Username == name {
			return true
		}
	}
	return false
}
