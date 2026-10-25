package integration

import (
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

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
	_, _, err = gitlabJSON("POST", appURL+"/api/v4/projects/1/deploy_tokens", "integration-secret", map[string]any{"name": "project"})
	if err != nil || !appCalled.Load() {
		t.Errorf("GitLab API refused the AppTown authority: called=%v err=%v", appCalled.Load(), err)
	}
	_, _, err = gitlabJSON("POST", depURL+"/api/v4/projects/1/deploy_tokens", "integration-secret", map[string]any{"name": "project"})
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
			status, _, err := gitlabJSON(http.MethodPost, source.URL+"/api/v4/projects", "fixture-secret", map[string]any{"name": "fixture"})
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
