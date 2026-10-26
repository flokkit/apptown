package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/deploy/digitalocean/dofake"
)

func TestAppPlatformMaintainedHealthUsesCompletedOwnerReceipt(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy=", legacy), func(t *testing.T) {
			w := newSyncWorld(t)
			s := w.s
			if err := s.providers.secrets.SetInstance(t.Context(), "DIGITALOCEAN_ACCESS_TOKEN", "fixture-token", "fixture"); err != nil {
				t.Fatal(err)
			}
			api := &dofake.API{}
			api.ServeFrontDoor(true)
			handler := api.Handler()
			var samples, inventoryState atomic.Int32
			var recordedDeployment atomic.Value
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/apps/") && strings.HasSuffix(r.URL.Path, "/healthz") {
					if r.Header.Get("Authorization") != "" {
						t.Error("provider credential reached application health")
					}
					out.WriteHeader(http.StatusOK)
					return
				}

				if strings.HasSuffix(r.URL.Path, "/health") && strings.HasPrefix(r.URL.Path, "/v2/apps/") {
					samples.Add(1)
					_ = json.NewEncoder(out).Encode(map[string]any{"app_health": map[string]any{"components": []map[string]any{{"name": "web", "state": "HEALTHY", "replicas_desired": 1, "replicas_ready": 1}}}})
					return
				}
				if state := inventoryState.Load(); state != 0 && r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/apps/") {
					if state == 1 {
						out.WriteHeader(http.StatusNotFound)
						return
					}
					phase, active := "ACTIVE", recordedDeployment.Load().(string)
					if state == 2 {
						phase = "ERROR"
					}
					if state == 3 {
						active = "replacement"
					}
					_ = json.NewEncoder(out).Encode(map[string]any{"app": map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/v2/apps/"), "active_deployment": map[string]any{"id": active, "phase": phase}}})
					return
				}
				handler.ServeHTTP(out, r)
			}))
			defer server.Close()
			s.providers.digitalOcean = digitalocean.Options{APIBase: server.URL, PollInterval: time.Millisecond, PushImage: func(*digitalocean.Driver, context.Context, *digitalocean.Client, string, string, string, string, func(string, ...any)) (string, error) {
				return "sha256:" + strings.Repeat("a", 64), nil
			}}
			pid := createTestProject(t, s.db, "health-app", "Health app")
			target := w.target(t, "do-syd")
			dep, err := s.Deployments.EnsureStanding(t.Context(), pid, "cloud", target.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "non-production"})
			if err != nil {
				t.Fatal(err)
			}
			driver, err := s.providers.Driver(t.Context(), target, dep)
			if err != nil {
				t.Fatal(err)
			}
			actual, ok := driver.(*digitalOceanRelease)
			if !ok {
				t.Fatalf("production factory omitted maintained wrapper: %T", driver)
			}
			connection, err := w.integrations.Bindings.ByName(t.Context(), "do-syd")
			if err != nil {
				t.Fatal(err)
			}
			if actual.ConnectionID != connection.ID {
				t.Fatal("driver did not retain resolved connector identity")
			}
			contract, err := appcontract.Parse([]byte("app:\n  web:\n    image: web:1\n    public: true\n    health: {readiness: {http: /healthz, expect: 200}}\n"))
			if err != nil {
				t.Fatal(err)
			}
			src := deploy.Source{Contract: contract, Images: map[string]string{"web": "local-web"}, Revision: "selected"}
			art, err := actual.Render(t.Context(), dep.Placement(), src, nil, secret.Delivery{}, func(string, ...any) {})
			if err != nil {
				t.Fatal(err)
			}
			art.Source = &src
			start := deploymentStartFor(dep, "health-proof", "selected")
			start.Inputs.ArtifactHash = art.Hash
			at, _, err := s.Outcomes.Admit(t.Context(), start)
			if err != nil {
				t.Fatal(err)
			}
			observed, err := driver.Reconcile(t.Context(), dep.Placement(), at.Generation, art, "health-proof", func(string, ...any) {}, s.Outcomes.Checkpoint(t.Context(), at))
			if err != nil {
				t.Fatal(err)
			}
			handle, ok := observed.Handle.(map[string]any)
			if !ok {
				t.Fatal("no final handle")
			}
			if _, ok := handle["healthReceipt"]; !ok {
				t.Fatal("provider completion lost receipt")
			}
			if legacy {
				delete(handle, "healthReceipt")
			}
			if _, err := s.Outcomes.Complete(t.Context(), Completion{Attempt: at, Observed: observed}); err != nil {
				t.Fatal(err)
			}
			dep, err = s.Deployments.ByID(t.Context(), dep.ID)
			if err != nil {
				t.Fatal(err)
			}
			record := listingRecord{deployment: dep, target: target}
			if err := s.ObserveRuntime(t.Context()); err != nil {
				t.Fatal(err)
			}
			if samples.Load() != 0 {
				t.Fatal("read-only inventory sampled health")
			}
			if got := s.observations.read(record, s.now()); got.Health == "healthy" {
				t.Fatalf("inventory converted historical completion to current health: %+v", got)
			}
			if err := s.refreshReadiness(t.Context()); err != nil {
				t.Fatal(err)
			}
			got := s.observations.read(record, s.now())
			if legacy {
				if samples.Load() != 0 || got.Health == "healthy" || !got.HealthObservedAt.IsZero() {
					t.Fatalf("legacy completion gained health: %+v samples%d", got, samples.Load())
				}
				return
			}
			if samples.Load() != 1 || got.Health != "healthy" || got.HealthGeneration != at.Generation || got.HealthArtifact != art.Hash || got.HealthObservedAt.IsZero() {
				t.Fatalf("completed exact health absent: %+v samples%d", got, samples.Load())
			}
			recordedDeployment.Store(dep.Handle["deploymentId"].(string))
			for state := int32(1); state <= 3; state++ {
				inventoryState.Store(state)
				if err := s.ObserveRuntime(t.Context()); err != nil {
					t.Fatal(err)
				}
				got := s.observations.read(record, s.now())
				if got.Health == "healthy" || !got.HealthObservedAt.IsZero() || got.HealthArtifact != "" {
					t.Fatalf("newer contradictory inventory%d inherited sample: %+v", state, got)
				}
				if samples.Load() != 1 {
					t.Fatal("inventory contradiction launched a diagnostic")
				}
			}
			inventoryState.Store(0)
			if err := s.ObserveRuntime(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := s.observations.read(record, s.now()); got.Health != "healthy" {
				t.Fatalf("matching current inventory lost retained fresh sample: %+v", got)
			}
			before := samples.Load()
			for range 3 {
				_ = s.observations.read(record, s.now())
			}
			if samples.Load() != before {
				t.Fatal("listing launched health observations")
			}
			if _, err := s.Deployments.HoldAfterRestore(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := s.refreshReadiness(t.Context()); err != nil {
				t.Fatal(err)
			}
			if samples.Load() != before {
				t.Fatal("restore hold reached provider health")
			}
		})
	}
}
