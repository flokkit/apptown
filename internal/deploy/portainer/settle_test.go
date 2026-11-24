package portainer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// HTTP success alone cannot confirm an apply; only the stack status can.
func TestStackAnswersRequireAnExplicitDeploymentResult(t *testing.T) {
	for _, tc := range []struct {
		name, answer, read string
		code               int
		terminal, hang     bool
	}{
		{name: "missing status", answer: `{"Id":12}`},
		{name: "inactive", answer: `{"Id":12,"Status":2}`},
		{name: "deploying without ID", answer: `{"Status":3}`},
		{name: "failed answer", answer: `{"Id":12,"Status":4,"DeploymentStatus":[{"Message":"starting"},{"Message":"manifest unknown"}]}`, terminal: true},
		{name: "unreadable", answer: `{"Id":12,"Status":3}`, code: 503},
		{name: "malformed read", answer: `{"Id":12,"Status":3}`, read: `{`},
		{name: "wrong read ID", answer: `{"Id":12,"Status":3}`, read: `{"Id":99,"Status":1}`},
		{name: "missing read status", answer: `{"Id":12,"Status":3}`, read: `{"Id":12}`},
		{name: "bounded request", answer: `{"Id":12,"Status":3}`, hang: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "GET" && r.URL.Path == "/api/stacks":
					fmt.Fprint(w, `[]`)
				case r.Method == "POST":
					fmt.Fprint(w, tc.answer)
				case r.Method == "GET" && r.URL.Path == "/api/stacks/12":
					if tc.hang {
						<-r.Context().Done()
						return
					}
					if tc.code > 0 {
						w.WriteHeader(tc.code)
					}
					fmt.Fprint(w, tc.read)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL)
					w.WriteHeader(500)
				}
			}))
			t.Cleanup(server.Close)
			cfg := &Target{PortainerURL: server.URL, Scope: scopeFixtureScope(server.URL)}
			// The outer context is a hang guard; the driver's own deadline must win.
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, err := applyStack(ctx, cfg, 3, "forge-shop", scopeFixtureCompose(shopCompose), idstest.N(7), nil, 20*time.Millisecond, nil)
			if err == nil || deploy.IsTerminal(err) != tc.terminal || ctx.Err() != nil {
				t.Fatalf("unconfirmed response: %v outer%v", err, ctx.Err())
			}
			if tc.terminal && !strings.Contains(err.Error(), "manifest unknown") {
				t.Fatalf("lost last deployment message: %v", err)
			}
		})
	}
}

func TestAnUpdateKeepsItsListedStackID(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/stacks":
			fmt.Fprint(w, `[{"Id":12,"Name":"forge-shop","EndpointId":3}]`)
		case r.URL.Path == "/api/stacks/12/file":
			fmt.Fprintf(w, `{"StackFileContent":%q}`, scopeFixtureCompose(shopCompose))
		case r.Method == "PUT":
			fmt.Fprint(w, `{"Id":99,"Status":1}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(server.Close)
	cfg := &Target{PortainerURL: server.URL, Scope: scopeFixtureScope(server.URL)}
	applied, err := applyStack(t.Context(), cfg, 3, "forge-shop", scopeFixtureCompose(shopCompose), idstest.N(7), nil, 0, nil)
	if err == nil || deploy.IsTerminal(err) || applied.ID != 12 {
		t.Fatalf("changed update identity: %+v %v", applied, err)
	}
}

func TestStackCreateUsesOneRoute(t *testing.T) {
	for _, code := range []int{404, 405} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			t.Parallel()
			creates := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" && r.URL.Path == "/api/stacks" {
					fmt.Fprint(w, `[]`)
					return
				}
				creates++
				if r.URL.Path != "/api/stacks/create/standalone/string" {
					t.Errorf("unexpected create route: %s", r.URL)
				}
				w.WriteHeader(code)
			}))
			t.Cleanup(server.Close)
			cfg := &Target{PortainerURL: server.URL, Scope: scopeFixtureScope(server.URL)}
			_, err := applyStack(t.Context(), cfg, 3, "forge-shop", scopeFixtureCompose(shopCompose), idstest.N(7), nil, 0, nil)
			if err == nil || deploy.IsTerminal(err) || creates != 1 {
				t.Fatalf("unsupported create: calls%d err%v", creates, err)
			}
		})
	}
}
