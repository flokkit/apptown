package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTheEntryScriptIsReadFromTheBuiltPage(t *testing.T) {
	built := `<!doctype html><html><head><script type="module" crossorigin src="/assets/index-CKcQVKuj.js"></script>` +
		`<link rel="stylesheet" crossorigin href="/assets/index-Ab12.css"></head><body><div id="root"></div></body></html>`
	if got := entryIn([]byte(built)); got != "/assets/index-CKcQVKuj.js" {
		t.Fatalf("entry = %q", got)
	}
	if got := entryIn([]byte(`<html><body>no script</body></html>`)); got != "" {
		t.Fatalf("a page with no entry script names none, got %q", got)
	}
}

func TestEveryResponseNamesTheServedPagesEntry(t *testing.T) {
	if Entry() == "" {
		t.Skip("no frontend built into this test binary (run make web)")
	}
	h := WithEntry(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/me", nil))
	if got := rec.Header().Get(EntryHeader); got != Entry() {
		t.Fatalf("%s = %q, want %q", EntryHeader, got, Entry())
	}
}
