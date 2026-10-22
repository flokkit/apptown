package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
)

// Revealing a built-in value is an admin's act with a receipt: the value
// comes back, the audit log names who read which secret in which
// environment, and a secret held elsewhere is refused with a pointer.
func TestRevealSecretIsRecorded(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	service := secret.NewService(db, secret.Config{})
	projects := project.New(db, nil)
	pid := httpTestProject(t, db, "rv", "Reveal")
	slug := "rv"
	scope := secret.Scope(slug, "main", "env")
	if _, err := service.Vault.Put(t.Context(), scope, "DJANGO_SUPERUSER_PASSWORD", "hunter2", "t"); err != nil {
		t.Fatal(err)
	}
	if err := service.Bindings.Put(t.Context(), pid, secret.Binding{Deployment: "main", Name: "DJANGO_SUPERUSER_PASSWORD", Store: secret.StoreInternal, Ref: scope, Version: "1", Generated: true, UpdatedBy: "t"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/projects/"+slug+"/secrets/main/DJANGO_SUPERUSER_PASSWORD/reveal", nil)
	req.SetPathValue("slug", slug)
	req.SetPathValue("deployment", "main")
	req.SetPathValue("name", "DJANGO_SUPERUSER_PASSWORD")
	rec := httptest.NewRecorder()
	NewSecrets(service, nil, projects, nil, db, "", nil).RevealSecret(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"value":"hunter2"`) {
		t.Fatalf("reveal: %d %s", rec.Code, rec.Body.String())
	}
	n := 0
	for _, receipt := range receiptsOfKind(t, db, "secret.revealed") {
		if receipt.Subject == "DJANGO_SUPERUSER_PASSWORD" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected one audit receipt, got %d", n)
	}
}
