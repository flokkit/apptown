package apptown

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/stock"
)

func TestDependencyCatalogHTTPValidatesAuthorityAndKeepsStockReadOnly(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	if _, _, err := a.Dependencies.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	body := func(yaml string) string {
		raw, err := json.Marshal(map[string]string{"yaml": yaml})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	source := "capability: search-test\ntitle: Search\nlocal:\n  image: search:1\n  port: 9200\n"
	for _, tc := range []struct {
		who, body string
		code      int
	}{{"outsider", body(source), 403}, {"boss", `{`, 400}, {"boss", body("capability: bad\n"), 400}, {"boss", body("capability: postgres\nlocal:\n  image: overridden\n  port: 5432\n"), 409}} {
		if w := authzCall(h, tc.who, "POST", "/api/v1/dependencies", tc.body); w.Code != tc.code {
			t.Fatalf("refused dependency: %d %s", w.Code, w.Body.String())
		}
	}
	if _, err := a.Dependencies.ByCapability(t.Context(), "search-test"); err == nil {
		t.Fatal("denied save created dependency")
	}
	w := authzCall(h, "boss", "POST", "/api/v1/dependencies", body(source))
	var saved struct {
		OK         bool
		Capability string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || w.Code != 200 || !saved.OK || saved.Capability != "search-test" {
		t.Fatalf("saved: %d %s %v", w.Code, w.Body.String(), err)
	}
	w = authzCall(h, "boss", "GET", "/api/v1/dependencies", "")
	var out struct{ Dependencies []dependency.Kind }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("catalog: %d %s %v", w.Code, w.Body.String(), err)
	}
	found := false
	for _, d := range out.Dependencies {
		if d.Capability == "search-test" {
			found = true
			if d.Local.Image != "search:1" {
				t.Fatalf("wrong declaration: %+v", d)
			}
		}
	}
	if !found {
		t.Fatal("saved dependency absent from catalog")
	}
}
