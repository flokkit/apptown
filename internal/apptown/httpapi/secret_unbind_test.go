package httpapi

import (
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
)

func TestSecretUnbindHTTPDistinguishesReadFailureFromAbsence(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	secrets := secret.NewService(db, secret.Config{})
	projects := project.New(db, nil)
	id := httpTestProject(t, db, "secret-owner", "Secret owner")
	b := secret.Binding{Deployment: "main", Name: "TOKEN", Store: secret.StoreInternal, Ref: "project:secret-owner", Version: "2", UpdatedBy: "alice"}
	for _, value := range []string{"private-old-value", "private-current-value"} {
		if _, err := secrets.Vault.Put(t.Context(), b.Ref, b.Name, value, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	if err := secrets.Bindings.Put(t.Context(), id, b); err != nil {
		t.Fatal(err)
	}
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("DELETE", "/secrets/main/TOKEN", nil)
		r.SetPathValue("slug", "secret-owner")
		r.SetPathValue("deployment", b.Deployment)
		r.SetPathValue("name", b.Name)
		w := httptest.NewRecorder()
		NewSecrets(secrets, nil, projects, nil, db, "", nil).UnbindSecret(w, r)
		return w
	}
	t.Cleanup(db.FailNextStatement("-- name: BindingByName :one"))
	if w := request(); w.Code != 500 {
		t.Fatalf("read failure: %d %s", w.Code, w.Body)
	}
	if bound, err := secrets.Bindings.For(t.Context(), id, b.Deployment); err != nil || len(bound) != 1 || bound[b.Name].Ref != b.Ref {
		t.Fatalf("binding after failure: %+v %v", bound, err)
	}
	if w := request(); w.Code != 200 {
		t.Fatalf("unbind: %d %s", w.Code, w.Body)
	}
	if w := request(); w.Code != 404 {
		t.Fatalf("absent binding: %d %s", w.Code, w.Body)
	}
}
