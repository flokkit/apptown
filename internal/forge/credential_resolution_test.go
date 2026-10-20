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

	"encoding/json"
	"slices"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids/idstest"
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

// fakeInfisical answers login and the secrets read, or 503 while failing.
func fakeInfisical(t *testing.T, failing *atomic.Bool) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			w.WriteHeader(503)
			return
		}
		if r.Method == "POST" {
			fmt.Fprint(w, `{"accessToken":"token","expiresIn":3600}`)
			return
		}
		fmt.Fprint(w, `{"secrets":[{"secretKey":"KEY","secretValue":"remote"}]}`)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// Each secrets store answers for itself. With no Infisical credentials the
// infisical integration reads "not set up", never healthy; the built-in
// store reads its key. Set up, Infisical is read now: a failing read is
// not OK though values are cached, and its failure and recovery leave the
// usual receipts. A provider with no check is unchecked.
//
// Kills: the merged "N keys resolved" reading back for any provider.
func TestASecretsStoreAnswersForItself(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	h := connectorHealthFor(a)
	ctx := context.Background()
	if got := h.Health(ctx, "secrets", "infisical", nil); got.OK || !got.Checked || !strings.Contains(got.Detail, "not set up") {
		t.Fatalf("infisical without credentials: %+v", got)
	}
	if got := h.Health(ctx, "secrets", "internal", nil); !got.OK || !got.Checked {
		t.Fatalf("built-in store: %+v", got)
	}
	if got := h.Health(ctx, "secrets", "vault", nil); got.Checked {
		t.Fatalf("a provider with no check was read: %+v", got)
	}

	var failing atomic.Bool
	credentialBootstrap(t, a, fakeInfisical(t, &failing))
	if got := h.Health(ctx, "secrets", "infisical", nil); !got.OK {
		t.Fatalf("infisical set up and answering: %+v", got)
	}
	failing.Store(true)
	moveClock(a, 2*time.Minute) // past the one-minute cache
	if got := h.Health(ctx, "secrets", "infisical", nil); got.OK {
		t.Fatalf("infisical failing behind a warm cache read OK: %+v", got)
	}
	requireAudits(t, a.DB, 1, "infisical.error")
	failing.Store(false)
	if got := h.Health(ctx, "secrets", "infisical", nil); !got.OK {
		t.Fatalf("infisical recovered: %+v", got)
	}
	requireAudits(t, a.DB, 1, "infisical.recovered")
}

func sorted(s []string) []string { slices.Sort(s); return s }

// secretsWiring is the secrets bindings a run's admission recorded.
func secretsWiring(t *testing.T, a *App, run *workflow.Run) []string {
	t.Helper()
	var out []string
	for _, e := range runEvents(t, a, run, "integration.resolved") {
		if e["capability"] == "secrets" {
			out = append(out, fmt.Sprint(e["binding"]))
		}
	}
	return out
}

// manifestSecrets is the secrets bindings the project's manifest lists.
func manifestSecrets(t *testing.T, a *App) []string {
	t.Helper()
	w := authzCall(a.Handler(), "owner", "GET", "/api/v1/projects/game/manifest", "")
	var m struct {
		Bindings []struct{ Capability, Binding string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || w.Code != 200 {
		t.Fatalf("manifest: %d %s %v", w.Code, w.Body.String(), err)
	}
	var out []string
	for _, b := range m.Bindings {
		if b.Capability == "secrets" {
			out = append(out, b.Binding)
		}
	}
	return out
}

// The secrets wiring a project's manifest lists, and a run's admission
// records, is the stores this AppTown can use then: the built-in store
// alone without Infisical credentials, both with them. It is an inventory
// of what was available, not a record of what the run read.
//
// Kills: the oldest secrets integration (infisical) recorded whatever is
// set up; the availability filter dropped from either caller.
func TestRecordedSecretsWiringIsTheAvailableStores(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	authzWorkflow(t, a, workflow.ApprovalConfig{})
	// The stock integrations, as apptownd's start seeds them: infisical is the
	// older of the two secrets integrations.
	a.SyncSeedIntegrations(context.Background())
	start := func(title string) *workflow.Run {
		t.Helper()
		change, err := a.Work.Create(asPerson("owner"), workflow.ChangeDraft{ProjectID: idstest.N(1), Type: "change", Title: title})
		if err != nil {
			t.Fatal(err)
		}
		run, err := a.Work.StartRun(asPerson("owner"), workflow.StartChangeRun{ChangeID: change.ID, Workflow: "implement"})
		if err != nil {
			t.Fatal(err)
		}
		a.Work.Wait()
		return run
	}
	if got := secretsWiring(t, a, start("without Infisical")); strings.Join(got, ",") != "built-in-secrets" {
		t.Fatalf("run without Infisical recorded %v", got)
	}
	if got := manifestSecrets(t, a); strings.Join(got, ",") != "built-in-secrets" {
		t.Fatalf("manifest without Infisical lists %v", got)
	}

	var failing atomic.Bool
	credentialBootstrap(t, a, fakeInfisical(t, &failing))
	run := start("with Infisical")
	want := "built-in-secrets,infisical" // sorted: order is not the guarantee
	if got := secretsWiring(t, a, run); strings.Join(sorted(got), ",") != want {
		t.Fatalf("run with Infisical recorded %v, want %s", got, want)
	}
	if got := manifestSecrets(t, a); strings.Join(sorted(got), ",") != want {
		t.Fatalf("manifest with Infisical lists %v, want %s", got, want)
	}
}
