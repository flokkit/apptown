package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/database"

	"github.com/flokkit/apptown/internal/apptown/wow"
)

// A foreign pack carrying a missing workflow is information, not a one-click
// invitation to move the project to that pack. Any of the project's own
// packs that carries it is offered.
func TestCatalogOffersOnlyOwnPack(t *testing.T) {
	t.Parallel()
	f := newPackPullFixture(t)
	f.publishWorkflow(t, "wanted-lane", false)
	f.publishPack(t, "aaa-wrong-pack", []string{"wanted-lane"})
	f.publishPack(t, "right-pack", []string{"wanted-lane"})
	f.publishPack(t, "empty-pack", nil)
	member := func(packs ...string) {
		t.Helper()
		var picks []adoption.Membership
		for _, p := range packs {
			picks = append(picks, adoption.Membership{Pack: p, Version: 1})
		}
		if err := f.db.InTx(t.Context(), func(tx *database.Tx) error {
			return adoption.SetMembershipIn(t.Context(), tx, f.projectID, picks, "fixture", "bls", time.Now())
		}); err != nil {
			t.Fatal(err)
		}
	}
	member("right-pack", "empty-pack")
	ways := wow.New(f.db, wow.Dependencies{Stock: catalogStock(`catalog:
  - name: uplift/x
    definition: {workflow: wanted-lane, title: t, prompt: b}
`)})
	handler := NewCatalogChanges(ways, f.adoption, f.projects, f.identity, nil, "")
	call := func() *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.CatalogList(w, httptest.NewRequest("GET", "/api/v1/catalog?project=p", nil).WithContext(f.ctx))
		return w
	}
	w := call()
	body := w.Body.String()
	if !strings.Contains(body, `"missingWorkflow":"wanted-lane"`) {
		t.Fatalf("missing workflow not flagged: %s", body)
	}
	if !strings.Contains(body, `"fromPack":"right-pack"`) || strings.Contains(body, "aaa-wrong-pack") {
		t.Fatalf("must offer the project's own pack only: %s", body)
	}
	member("empty-pack")
	w = call()
	if strings.Contains(w.Body.String(), `"fromPack"`) {
		t.Fatalf("foreign packs must not be offered: %s", w.Body.String())
	}
}
