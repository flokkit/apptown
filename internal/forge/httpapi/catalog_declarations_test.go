package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/wow"
)

func TestCatalogDeclarationHTTPKeepsVersionsPromotionAndRejectedEdits(t *testing.T) {
	t.Parallel()
	db, accounts := accountHTTPFixture(t)
	ways := wow.New(db, wow.Dependencies{})
	h := NewCatalogDeclarations(ways, accounts)
	call := func(handler http.HandlerFunc, who, query, body string) *httptest.ResponseRecorder {
		ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: who, Via: "session"}, Receipt: who, Verified: true})
		r := httptest.NewRequest("POST", "/"+query, strings.NewReader(body)).WithContext(ctx)
		r.SetPathValue("name", "repair/controls")
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	create := `{"name":"repair/controls","description":"original","definition":{"workflow":"implement","title":"Repair controls","prompt":"Keep behavior"}}`
	for _, handler := range []http.HandlerFunc{h.CatalogPatch, h.CatalogPromote} {
		if w := call(handler, "boss", "", `{"note":"checked","description":"must-not-create"}`); w.Code != 404 {
			t.Fatalf("missing declaration edit: %d %s", w.Code, w.Body.String())
		}
	}

	for _, tc := range []struct {
		who, body string
		code      int
	}{{"kid", create, 403}, {"boss", `{`, 400}, {"boss", `{"name":"repair/controls","definition":{}}`, 400}} {
		w := call(h.CatalogCreate, tc.who, "", tc.body)
		if w.Code != tc.code {
			t.Fatalf("create rejection: %d %s", w.Code, w.Body.String())
		}
		if rows, err := ways.CatalogChanges(t.Context()); err != nil || len(rows) != 0 {
			t.Fatalf("invalid declaration persisted: %+v %v", rows, err)
		}
	}
	if w := call(h.CatalogCreate, "boss", "", create); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if w := call(h.CatalogCreate, "boss", "", create); w.Code != 409 {
		t.Fatalf("duplicate: %d %s", w.Code, w.Body.String())
	}
	if w := call(h.CatalogPromote, "boss", "", `{}`); w.Code != 400 {
		t.Fatalf("empty promotion: %d %s", w.Code, w.Body.String())
	}
	if w := call(h.CatalogPromote, "kid", "", `{"note":"untrusted"}`); w.Code != 403 {
		t.Fatalf("foreign promotion: %d %s", w.Code, w.Body.String())
	}
	unpromoted, err := ways.CatalogChange(t.Context(), "repair/controls")
	if err != nil || unpromoted.PromotedVersion != 0 {
		t.Fatalf("denied promotion changed publication: %+v %v", unpromoted, err)
	}
	if w := call(h.CatalogPromote, "boss", "", `{"note":"watched evaluation run 7"}`); w.Code != 200 {
		t.Fatalf("promote: %d %s", w.Code, w.Body.String())
	}
	read := func(query string, version int, title, status string) {
		t.Helper()
		w := call(h.CatalogGet, "boss", query, "")
		var out struct {
			ShownVersion int
			Status       string
			Definition   wow.CatalogDef
			Change       wow.CatalogChange
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.ShownVersion != version || out.Definition.Title != title || out.Status != status {
			t.Fatalf("read version: %d %s %v", w.Code, w.Body.String(), err)
		}
	}
	read("", 1, "Repair controls", "catalogued")
	// Invalid definition and metadata travel together: validation must precede
	// either mutation, otherwise a 400 silently edits the displayed description.
	w := call(h.CatalogPatch, "boss", "", `{"description":"must not stick","definition":{}}`)
	if w.Code != 400 {
		t.Fatalf("invalid patch: %d %s", w.Code, w.Body.String())
	}
	current, err := ways.CatalogChange(t.Context(), "repair/controls")
	if err != nil || current.Description != "original" || current.LatestVersion != 1 {
		t.Fatalf("rejected patch persisted: %+v %v", current, err)
	}
	same := `{"definition":{"workflow":"implement","title":"Repair controls","prompt":"Keep behavior"}}`
	if w := call(h.CatalogPatch, "boss", "", same); w.Code != 200 {
		t.Fatalf("unchanged patch: %d %s", w.Code, w.Body.String())
	}
	current, err = ways.CatalogChange(t.Context(), "repair/controls")
	if err != nil || current.LatestVersion != 1 {
		t.Fatalf("identical definition cut version: %+v %v", current, err)
	}
	update := `{"description":"second","definition":{"workflow":"implement","title":"Repair input","prompt":"Keep behavior"}}`
	if w := call(h.CatalogPatch, "kid", "", update); w.Code != 403 {
		t.Fatalf("unauthorized patch: %d", w.Code)
	}
	t.Cleanup(db.FailNextStatement("-- name: InsertCatalogVersion"))
	if w := call(h.CatalogPatch, "boss", "", update); w.Code != 500 {
		t.Fatalf("failed atomic edit reported success: %d %s", w.Code, w.Body.String())
	}
	current, err = ways.CatalogChange(t.Context(), "repair/controls")
	if err != nil || current.Description != "original" || current.LatestVersion != 1 {
		t.Fatalf("failed HTTP edit persisted metadata: %+v %v", current, err)
	}
	if w := call(h.CatalogPatch, "boss", "", update); w.Code != 200 {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	read("", 2, "Repair input", "draft")
	read("?version=1", 1, "Repair controls", "draft")
	for _, tc := range []struct {
		q    string
		code int
	}{{"?version=bad", 400}, {"?version=999", 404}} {
		if w := call(h.CatalogGet, "boss", tc.q, ""); w.Code != tc.code {
			t.Fatalf("version rejection: %d %s", w.Code, w.Body.String())
		}
	}
}
