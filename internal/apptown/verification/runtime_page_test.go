package verification

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/check"
)

// The runtime "page" probe asks the app's root through its front door, the
// address a visitor uses, so an app whose readiness path passes while every
// real page is refused (Rails' /up skips host authorization) is a finding.
func TestTheRuntimePageProbeAsksTheFrontDoorsRoot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		root int
		want check.Outcome
	}{{http.StatusForbidden, check.DoesNotConform}, {http.StatusOK, check.Conforms}} {
		t.Run(http.StatusText(tc.root), func(t *testing.T) {
			front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/up" {
					w.WriteHeader(http.StatusOK)
					return
				}
				w.WriteHeader(tc.root)
			}))
			t.Cleanup(front.Close)
			if got := (Runtime{}).Probe(context.Background(), guidanceTarget{FrontDoor: front.URL, Naked: "http://127.0.0.1:1"}, "page"); got.Outcome != tc.want {
				t.Fatalf("root answering %d judged %+v", tc.root, got)
			}
		})
	}
}
