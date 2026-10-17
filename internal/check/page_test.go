package check

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The page probe judges what a visitor who opens the app gets: a server
// error or a host allow-list's refusal (400, 403) is a finding wherever the
// readiness path stands; a sign-in prompt, a redirect off to a sign-in
// provider, or an API-only app's 404 at its root is an app that answers; a redirect within the app is judged where it
// lands; no answer is the review's inability to look.
func TestThePageProbeJudgesWhatAVisitorGets(t *testing.T) {
	t.Parallel()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		switch r.Header.Get("X-App") {
		case "refuses":
			w.WriteHeader(http.StatusForbidden)
		case "disallowed-host":
			w.WriteHeader(http.StatusBadRequest)
		case "api-only":
			w.WriteHeader(http.StatusNotFound)
		case "signs-in":
			w.WriteHeader(http.StatusUnauthorized)
		case "sso":
			http.Redirect(w, r, "https://login.example.org/authorize", http.StatusFound)
		case "crashes-after-redirect":
			if r.URL.Path == "/" {
				http.Redirect(w, r, "/dashboard?tab=1", http.StatusFound)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(app.Close)
	for _, tc := range []struct {
		app     string
		refused bool
		want    string
	}{
		{"refuses", true, "GET / answered 403"},
		{"disallowed-host", true, "GET / answered 400"},
		{"api-only", false, ""},
		{"crashes-after-redirect", true, "GET /dashboard?tab=1 answered 500"},
		{"signs-in", false, ""},
		{"sso", false, ""},
		{"serves", false, ""},
	} {
		t.Run(tc.app, func(t *testing.T) {
			status, path, err := ProbePage(context.Background(), PageProbe{URL: app.URL, Headers: map[string]string{"X-App": tc.app}})
			if err != nil {
				t.Fatal(err)
			}
			why := PageRefusal(status, path)
			if (why != "") != tc.refused || !strings.Contains(why, tc.want) {
				t.Fatalf("%s: status %d at %s judged %q", tc.app, status, path, why)
			}
		})
	}
	if state, detail := JudgePage(context.Background(), "http://127.0.0.1:1"); state != CannotAssess {
		t.Fatalf("an unreachable front door judged %v: %s", state, detail)
	}
	if ps, err := ParseProbe("page"); err != nil || !ps.Page {
		t.Fatalf("the page probe does not parse: %+v %v", ps, err)
	}
}
