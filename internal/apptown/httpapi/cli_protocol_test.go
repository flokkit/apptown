package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/version"
)

// AppTown names its version on every answer, serves a CLI whose protocol
// it accepts and one that states none (a browser, an MCP client, a CLI from
// before protocols), and refuses any other CLI by name before it reaches a
// route.
//
// Kills: an AppTown that serves any CLI whatever protocol it speaks.
func TestAppTownAcceptsOnlyTheCLIProtocolsInItsRange(t *testing.T) {
	t.Parallel()
	reached := 0
	h := CLIProtocol(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++ }))
	call := func(protocol string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/projects", nil)
		if protocol != "" {
			r.Header.Set(version.CLIProtocolHeader, protocol)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, p := range []string{"", "1"} {
		if w := call(p); w.Code != 200 || w.Header().Get(version.Header) == "" {
			t.Errorf("protocol %q: %d %v", p, w.Code, w.Header())
		}
	}
	before := reached
	for _, p := range []string{"2", "00000000-0000-7000-8000-000000000000", "two"} {
		w := call(p)
		if w.Code != http.StatusUpgradeRequired || !strings.Contains(w.Body.String(), "accepts CLI protocols 1 to 1") || w.Header().Get(version.Header) == "" {
			t.Errorf("protocol %q: %d %s", p, w.Code, w.Body.String())
		}
	}
	if reached != before {
		t.Fatalf("a refused CLI reached a route")
	}
}
