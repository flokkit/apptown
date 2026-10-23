package integration

import (
	"encoding/pem"
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
	set, err := trust.New(t.TempDir(), map[trust.Use]trust.Bundle{trust.AppTown: app, trust.Deployments: dep})
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
