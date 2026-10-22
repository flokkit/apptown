package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// Policy vocabulary is checked at the HTTP door and persisted by Adoption.
func TestPolicyCreateAndList(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	authority := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := authority.SeedAdministrator(context.Background(), "bls"); err != nil {
		t.Fatal(err)
	}
	projects := project.New(db, authority)
	if _, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	}); err != nil {
		t.Fatal(err)
	}
	ways := wow.New(db, wow.Dependencies{})
	adopt := adoption.New(db, adoption.Dependencies{Ways: ways})
	handler := NewProjectPolicies(projects, authority, ways, adopt, db)
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "bls", Via: "test"}, Receipt: "bls", Verified: true})
	call := func(method, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1/projects/p/policies", strings.NewReader(body)).WithContext(ctx)
		r.SetPathValue("slug", "p")
		w := httptest.NewRecorder()
		if method == "GET" {
			handler.ListPolicies(w, r)
		} else {
			handler.CreatePolicy(w, r)
		}
		return w
	}
	w := call("POST", `{"name":"scan-before-deploy","rule":"requireStepBefore","with":{"uses":"deploy","requires":"scan"}}`)
	if w.Code >= 300 {
		t.Fatalf("create policy: %d %s", w.Code, w.Body.String())
	}
	rec := call("GET", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "scan-before-deploy") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if w := call("POST", `{"name":"x","rule":"madeUpRule"}`); w.Code < 400 {
		t.Fatalf("unknown rule must be refused: %d %s", w.Code, w.Body.String())
	}
}
