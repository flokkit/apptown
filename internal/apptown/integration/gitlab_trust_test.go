package integration

import (
	"context"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/localtls"
	"github.com/flokkit/apptown/internal/trust"
)

// Registry provisioning calls GitLab's project and deploy-token API with
// the integration's PRIVATE-TOKEN: a Git host's API, so it trusts the
// AppTown use's authorities and never one given only for deployments.
//
// Not parallel: it installs the process's trust.
func TestGitLabProvisioningTrustsOnlyTheAppTownUse(t *testing.T) {
	signed := func(called *atomic.Bool) (string, trust.Bundle) {
		ca, err := localtls.Load(t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("PRIVATE-TOKEN") == "integration-secret" {
				called.Store(true)
			}
			w.WriteHeader(http.StatusCreated)
		}))
		srv.TLS = ca.TLSConfig()
		srv.StartTLS()
		t.Cleanup(srv.Close)
		b, err := trust.Parse(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Certificate().Raw}))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), b
	}
	var appCalled, depCalled atomic.Bool
	appURL, app := signed(&appCalled)
	depURL, dep := signed(&depCalled)
	set, err := trust.New(t.TempDir(), map[trust.Use]trust.Bundle{trust.AppTown: app, trust.Deployments: dep}, trust.Bundle{})
	if err != nil {
		t.Fatal(err)
	}
	before := http.DefaultTransport.(*http.Transport).TLSClientConfig
	t.Cleanup(func() {
		http.DefaultTransport.(*http.Transport).TLSClientConfig = before
		trust.Install(nil)
	})
	trust.Install(set)
	_, _, err = gitlabJSON(context.Background(), appURL, "integration-secret", "POST", "/projects/1/deploy_tokens", map[string]any{"name": "project"})
	if err != nil || !appCalled.Load() {
		t.Errorf("GitLab API refused the AppTown authority: called=%v err=%v", appCalled.Load(), err)
	}
	_, _, err = gitlabJSON(context.Background(), depURL, "integration-secret", "POST", "/projects/1/deploy_tokens", map[string]any{"name": "project"})
	if err == nil || depCalled.Load() {
		t.Errorf("GitLab API sent the integration credential to a server trusted only for deployments: called=%v err=%v", depCalled.Load(), err)
	}
}

// Not parallel: the downgrade case installs only its test server's transport.
func TestGitLabProvisioningCredentialsStayAtTheConfiguredEndpoint(t *testing.T) {
	cases := []struct {
		name            string
		status          int
		sameOrigin, tls bool
	}{
		{"success", http.StatusOK, false, false},
		{"provider-failure", http.StatusForbidden, false, false},
		{"same-origin-loop", http.StatusFound, true, false},
		{"https-downgrade", http.StatusFound, false, true},
	}
	for _, status := range []int{301, 302, 303, 307, 308} {
		cases = append(cases, struct {
			name            string
			status          int
			sameOrigin, tls bool
		}{fmt.Sprintf("cross-origin-%d", status), status, false, false})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var destinationCalls, sourceCalls atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				destinationCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer destination.Close()
			source := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sourceCalls.Add(1)
				if got := r.Header.Get("PRIVATE-TOKEN"); got != "fixture-secret" {
					t.Errorf("initial credential = %q", got)
				}
				if tc.status >= 300 && tc.status < 400 {
					target := destination.URL + "/redirected"
					if tc.sameOrigin {
						target = r.URL.Path
					}
					w.Header().Set("Location", target)
				}
				w.WriteHeader(tc.status)
			}))
			if tc.tls {
				source.StartTLS()
			} else {
				source.Start()
			}
			defer source.Close()
			previous := http.DefaultTransport
			if tc.tls {
				http.DefaultTransport = source.Client().Transport
				defer func() { http.DefaultTransport = previous }()
			}
			status, _, err := gitlabJSON(context.Background(), source.URL, "fixture-secret", http.MethodPost, "/projects", map[string]any{"name": "fixture"})
			redirect := tc.status >= 300 && tc.status < 400
			if redirect {
				if err == nil {
					t.Error("redirect reported success")
				}
			} else {
				if err != nil || status != tc.status {
					t.Fatalf("provider response: status=%d err=%v", status, err)
				}
			}
			if got := destinationCalls.Load(); got != 0 {
				t.Errorf("redirect destination received %d requests", got)
			}
			if got := sourceCalls.Load(); got != 1 {
				t.Errorf("configured endpoint received %d requests; want one", got)
			}
		})
	}
}

type provisioningTransport func(*http.Request) (*http.Response, error)

func (f provisioningTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Not parallel: observes the outgoing request through the default transport.
func TestGitLabProvisioningAnswersEachEffectWithinItsOwnDeadline(t *testing.T) {
	before := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = before })
	var contexts []context.Context
	http.DefaultTransport = provisioningTransport(func(r *http.Request) (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			t.Fatalf("provider request inherits canceled caller: %v", err)
		}
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 20*time.Second || time.Until(deadline) < 19*time.Second {
			t.Fatalf("provider deadline: %v, present=%v", deadline, ok)
		}
		if r.URL.EscapedPath() != "/api/v4/projects/group%2Fproject/deploy_tokens" || r.Header.Get("PRIVATE-TOKEN") != "fixture" {
			t.Fatalf("provider identity changed: %s", r.URL)
		}
		contexts = append(contexts, r.Context())
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 2 {
		status, _, err := gitlabJSON(ctx, "https://gitlab.invalid/", "fixture", http.MethodDelete, "/projects/group%2Fproject/deploy_tokens", nil)
		if status != http.StatusNoContent || err != nil {
			t.Fatalf("provider response: %d %v", status, err)
		}
	}
	if len(contexts) != 2 || contexts[0] == contexts[1] {
		t.Fatal("effects do not have independent contexts")
	}
	for _, ctx := range contexts {
		if ctx.Err() != context.Canceled {
			t.Fatal("completed effect retains its deadline timer")
		}
	}
}
