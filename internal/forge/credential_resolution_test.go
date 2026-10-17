package forge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/audit"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/forge/deployment"
)

func credentialBootstrap(t *testing.T, a *App, url string) {
	t.Helper()
	body := fmt.Sprintf("INFISICAL_URL=%s\nINFISICAL_CLIENT_ID=identity\nINFISICAL_CLIENT_SECRET=bootstrap\nINFISICAL_PROJECT_ID=project\n", url)
	if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
func awaitCredential(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("credential request did not stop")
	}
}

func TestProviderCredentialResolutionUsesObservationContext(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{deployment.KindDOApp, deployment.KindDockerHost} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			a := testApp(t)
			entered, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(entered)
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer func() { close(release); server.Close() }()
			credentialBootstrap(t, a, server.URL)
			if _, err := a.Integrations.Bindings.Save(context.Background(), "cloud", digitalocean.CapCloudAccount, "digitalocean", `{"tokenFrom":"KEY"}`, "fixture"); err != nil {
				t.Fatal(err)
			}
			target := &deployment.Target{Name: "host", Kind: kind, Origin: deployment.OriginAttached, Provider: "cloud", Address: map[string]any{"host": "ssh://deploy@host.example", "keyFrom": "KEY"}}
			// A provider driver is built for a deployment of a project
			// that is there: it names its resources after the project.
			projectID := createReadTestProject(t, a, "credentials")
			driver, err := a.targetProviders.Driver(context.Background(), target, &deployment.Deployment{ProjectID: projectID, Name: "main"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var observeErr error
			go func() {
				_, observeErr = driver.Observe(ctx, deploy.Placement{Generation: 1, Container: "app", Handle: map[string]any{"appId": "app-1"}})
				close(stopped)
			}()
			awaitCredential(t, entered)
			cancel()
			awaitCredential(t, stopped)
			if !errors.Is(observeErr, context.Canceled) {
				t.Fatalf("credential cancellation: %v", observeErr)
			}
			requireAudits(t, a.DB, 0, "infisical.error")
			if _, err := os.Stat(filepath.Join(a.cfg.dataDir, "ssh")); !os.IsNotExist(err) {
				t.Fatalf("cancelled lookup materialized a key: %v", err)
			}
		})
	}
}

func TestCredentialRefreshHealthReceiptsAndFileFallback(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	var failing atomic.Bool
	failing.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			w.WriteHeader(503)
			fmt.Fprint(w, "private-response-secret")
			return
		}
		if r.Method == "POST" {
			fmt.Fprint(w, `{"accessToken":"token","expiresIn":3600}`)
			return
		}
		fmt.Fprint(w, `{"secrets":[{"secretKey":"KEY","secretValue":"remote"}]}`)
	}))
	defer server.Close()
	credentialBootstrap(t, a, server.URL)
	for range 2 {
		if _, err := a.Secrets.WithContext(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	requireAudits(t, a.DB, 1, "infisical.error")
	receipts, err := audit.Recent(context.Background(), a.DB, audit.Filter{Limit: audit.MaxLimit})
	if err != nil || len(receipts) == audit.MaxLimit {
		t.Fatalf("ledger: %d receipts, %v", len(receipts), err)
	}
	for _, receipt := range receipts {
		if strings.Contains(string(receipt.Payload), "private-response-secret") {
			t.Fatalf("a receipt carries the provider's response: %s", receipt.Payload)
		}
	}
	failing.Store(false)
	values, err := a.Secrets.WithContext(context.Background())
	if err != nil || values["KEY"] != "remote" {
		t.Fatalf("recovered: %v", err)
	}
	requireAudits(t, a.DB, 1, "infisical.recovered")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if values, err := a.Secrets.WithContext(ctx); !errors.Is(err, context.Canceled) || values != nil {
		t.Fatalf("cancelled caller consumed cache: %v", err)
	}
}
