package check_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/check"
)

// Address evidence follows redirects and document bases, distinguishes app
// namespaces from third parties, and never passes a page it could not inspect.
func TestPageOwnAddressJudgesResolvedURLs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, location string
		code                 int
		want                 check.Outcome
		detail               string
	}{
		{name: "relative assets", body: `<link rel="stylesheet" href="app.css"><script src="app.js"></script>`, want: check.Conforms},
		{name: "own absolute assets", body: `<link rel="stylesheet" href="//FRONT/app.css"><script src="http://FRONT/app.js"></script>`, want: check.Conforms},
		{name: "old preview stylesheet", body: `<link rel="alternate STYLESHEET" href="https://old.preview.test/a.css">`, want: check.DoesNotConform, detail: "https://old.preview.test/a.css"},
		{name: "separate main namespace", body: `<script src="https://blog.main.test/a.js"></script>`, want: check.DoesNotConform, detail: "https://blog.main.test/a.js"},
		{name: "eval namespace", body: `<script src="//copy.eval.test/a.js"></script>`, want: check.DoesNotConform, detail: "copy.eval.test/a.js"},
		{name: "exact namespace", body: `<script src="https://app.example/a.js"></script>`, want: check.DoesNotConform, detail: "app.example/a.js"},
		{name: "CDN", body: `<script src="https://cdn.example/a.js"></script>`, want: check.Conforms},
		{name: "namespace boundary", body: `<script src="https://notpreview.test/a.js"></script>`, want: check.Conforms},
		{name: "stale base", body: `<base href="https://old.preview.test/"><link rel="stylesheet" href="app.css"><script src="app.js"></script>`, want: check.DoesNotConform, detail: "https://old.preview.test/app.css"},
		{name: "first base only", body: `<base href="/assets/"><base href="https://old.preview.test/"><script src="app.js"></script>`, want: check.Conforms},
		{name: "external redirect", location: "https://signin.example/login", code: 302, want: check.DoesNotConform, detail: "https://signin.example/login"},
		{name: "server failure", code: 500, want: check.CannotAssess},
		{name: "refused", code: 403, want: check.CannotAssess},
		{name: "redirect loop", location: "/", code: 302, want: check.CannotAssess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/" {
					t.Errorf("unexpected asset fetch %s", r.URL)
					http.NotFound(w, r)
					return
				}
				if tc.location != "" {
					w.Header().Set("Location", tc.location)
				}
				code := tc.code
				if code == 0 {
					code = 200
				}
				w.WriteHeader(code)
				fmt.Fprint(w, strings.ReplaceAll(tc.body, "FRONT", r.Host))
			}))
			t.Cleanup(srv.Close)
			got, detail := check.JudgePageOwnAddress(t.Context(), srv.URL, []string{"*.preview.test", "*.main.test", "*.eval.test", "app.example", "127.0.0.1"})
			if got != tc.want || !strings.Contains(detail, tc.detail) {
				t.Fatalf("got %s: %s", got, detail)
			}
			if got == check.DoesNotConform && !strings.Contains(detail, "expected 127.0.0.1") {
				t.Fatalf("missing expected host: %s", detail)
			}
		})
	}
}

func TestPageOwnAddressResolvesEachRedirectAndFinalDocument(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.Redirect(w, r, "/nested/first", http.StatusFound)
		case "/nested/first":
			w.Header().Set("Location", "second")
			w.WriteHeader(http.StatusFound)
		case "/nested/second":
			fmt.Fprint(w, `<base href="assets/"><script src="app.js"></script>`)
		default:
			t.Errorf("resolved to wrong path %s", r.URL)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	if got, detail := check.JudgePageOwnAddress(t.Context(), srv.URL, nil); got != check.Conforms || !strings.Contains(detail, "/nested/second") {
		t.Fatalf("%s: %s", got, detail)
	}
}
