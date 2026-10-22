package apptown

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDocumentationHTTP(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	h := a.Handler()
	get := func(path string) (int, string) {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	if c, b := get("/api/v1/docs"); c != 200 || !strings.Contains(b, `"title":"Start here"`) {
		t.Fatalf("docs list: %d %s", c, b)
	}
	if c, _ := get("/api/v1/docs/guides/quickstart"); c != 200 {
		t.Fatalf("a guide doc: %d", c)
	}
}
