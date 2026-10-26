package digitalocean

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

func TestMaintainedAppPlatformHealthBindsEveryObservation(t *testing.T) {
	cases := []struct {
		name, health string
		fail         bool
	}{
		{"healthy", "healthy", false}, {"native unhealthy", "unhealthy", false}, {"wrong status", "unhealthy", false},
		{"redirect", "unhealthy", false}, {"legacy redirects", "healthy", false}, {"release mismatch", "unhealthy", false},
		{"missing worker", "unknown", true}, {"unavailable", "unknown", true}, {"rate limited", "unknown", true},
		{"wrong active", "unknown", true}, {"wrong app", "unknown", true}, {"pending replacement", "unknown", true}, {"changed after probe", "unknown", true},
		{"changed front door", "unknown", true}, {"wrong artifact", "unknown", true}, {"wrong generation", "unknown", true},
		{"different connector", "unknown", true}, {"different API base", "unknown", true}, {"legacy receipt", "unknown", true},
		{"gated", "unknown", true}, {"canceled", "unknown", true}, {"malformed receipt", "unknown", true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var calls, credentials, frontCalls, appCalls atomic.Int32
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if strings.HasPrefix(r.URL.Path, "/public/") {
					frontCalls.Add(1)
					if r.Header.Get("Authorization") != "" {
						t.Error("API credential reached front door")
					}
					if r.URL.Path == "/public/ready" && (tt.name == "redirect" || tt.name == "legacy redirects") {
						http.Redirect(w, r, "/public/ok", http.StatusFound)
						return
					}
					if tt.name == "wrong status" {
						w.WriteHeader(http.StatusCreated)
						return
					}
					w.WriteHeader(http.StatusOK)
					return
				}
				if r.Header.Get("Authorization") != "Bearer rotated-token" {
					t.Error("API did not use selected credential snapshot")
				}
				if r.URL.Path == "/v2/apps/app-1/health" {
					if tt.name == "unavailable" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					if tt.name == "rate limited" {
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
					components := []map[string]any{{"name": "web", "state": "HEALTHY", "replicas_desired": 1, "replicas_ready": 1}, {"name": "worker", "state": "HEALTHY", "replicas_desired": 1, "replicas_ready": 1}}
					if tt.name == "native unhealthy" {
						components[1]["state"] = "UNHEALTHY"
					}
					if tt.name == "missing worker" {
						components = components[:1]
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"app_health": map[string]any{"components": components}})
					return
				}
				if r.URL.Path != "/v2/apps/app-1" {
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				n := appCalls.Add(1)
				app, active, front := "app-1", "dep-1", server.URL+"/public"
				if tt.name == "wrong app" {
					app = "app-other"
				}
				if tt.name == "wrong active" || (tt.name == "changed after probe" && n > 1) {
					active = "dep-other"
				}
				if tt.name == "changed front door" && n > 1 {
					front += "/changed"
				}
				body := map[string]any{"id": app, "live_url": front, "active_deployment": map[string]any{"id": active, "phase": "ACTIVE"}}
				if tt.name == "pending replacement" {
					body["pending_deployment"] = map[string]any{"id": "dep-other", "phase": "PENDING"}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"app": body})
			}))
			defer server.Close()
			connection := ids.New(time.Now())
			now := time.Now().UTC()
			d := NewDriver(Dependencies{ConnectionID: connection, Client: func(context.Context) (*Client, Config, error) {
				credentials.Add(1)
				return NewClient("rotated-token", server.URL), Config{}, nil
			}}, Options{Now: func() time.Time { return now }})
			receipt := healthReceipt{Version: 1, Connection: connection, APIBase: server.URL, App: "app-1", Deployment: "dep-1", Generation: 3, Artifact: "admitted", Readiness: readinessSnapshot{Version: 1, Path: "/ready", Expect: 200}, Components: []string{"web", "worker"}}
			p := deploy.Placement{Generation: 3, Handle: map[string]any{"appId": "app-1", "deploymentId": "dep-1"}}
			artifact := "admitted"
			ctx := t.Context()
			switch tt.name {
			case "wrong artifact":
				artifact = "changed"
			case "wrong generation":
				p.Generation = 4
			case "different connector":
				d.ConnectionID = ids.New(time.Now())
			case "different API base":
				receipt.APIBase = "https://other.invalid"
			case "gated":
				p.Gate = &deploy.Gate{}
			case "legacy redirects":
				receipt.Readiness.Expect = 0
			case "release mismatch":
				receipt.Release = "expected-release"
			case "malformed receipt":
				receipt.Components = []string{"web", "web"}
			case "canceled":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			if tt.name != "legacy receipt" {
				p.Handle["healthReceipt"] = receipt
			}
			observed, err := d.Sample(ctx, p, artifact, func(ctx context.Context) error { return ctx.Err() })
			if (err != nil) != tt.fail || observed.Health != tt.health {
				t.Fatalf("sample=%+v err=%v want %s fail=%v", observed, err, tt.health, tt.fail)
			}
			if tt.fail {
				if !observed.HealthObservedAt.IsZero() || observed.ArtifactHash != "" {
					t.Fatal("failed observation retained a fresh stamp")
				}
			} else if observed.HealthObservedAt != now || observed.Generation != 3 || observed.ArtifactHash != "admitted" || observed.Running != "dep-1" {
				t.Fatalf("missing exact fresh evidence: %+v", observed)
			}
			switch tt.name {
			case "wrong artifact", "wrong generation", "different connector", "legacy receipt", "gated", "canceled", "malformed receipt":
				if credentials.Load() != 0 || calls.Load() != 0 {
					t.Fatal("invalid receipt resolved credentials or reached provider")
				}
			case "different API base":
				if calls.Load() != 0 {
					t.Fatal("changed destination reached provider")
				}
			}
			if credentials.Load() > 1 {
				t.Fatal("sample reread credential instead of one snapshot")
			}
			if tt.name == "redirect" && frontCalls.Load() != 1 {
				t.Fatal("exact predicate followed redirect")
			}
		})
	}
}

func TestMaintainedAppPlatformHealthRechecksPermitAtEveryBoundary(t *testing.T) {
	for deny := 1; deny <= 6; deny++ {
		t.Run(string(rune('0'+deny)), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if strings.HasSuffix(r.URL.Path, "/health") {
					_, _ = w.Write([]byte(`{"app_health":{"components":[{"name":"web","state":"HEALTHY","replicas_desired":1,"replicas_ready":1}]}}`))
					return
				}
				_, _ = w.Write([]byte(`{"app":{"id":"app","active_deployment":{"id":"dep","phase":"ACTIVE"}}}`))
			}))
			defer server.Close()
			connection := ids.New(time.Now())
			d := NewDriver(Dependencies{ConnectionID: connection, Client: func(context.Context) (*Client, Config, error) { return NewClient("token", server.URL), Config{}, nil }}, Options{})
			receipt := healthReceipt{Version: 1, Connection: connection, APIBase: server.URL, App: "app", Deployment: "dep", Generation: 1, Artifact: "artifact", Readiness: readinessSnapshot{Version: 1}, Components: []string{"web"}}
			p := deploy.Placement{Generation: 1, Handle: map[string]any{"appId": "app", "deploymentId": "dep", "healthReceipt": receipt}}
			count := 0
			denied := errors.New("current permit revoked")
			observed, err := d.Sample(t.Context(), p, "artifact", func(context.Context) error {
				count++
				if count == deny {
					return denied
				}
				return nil
			})
			// No public predicate: five permits (initial, app, report, app, final).
			if deny <= 5 {
				if !errors.Is(err, denied) || observed.Health != "unknown" || !observed.HealthObservedAt.IsZero() {
					t.Fatalf("revocation %d became evidence %+v %v", deny, observed, err)
				}
			} else if err != nil || observed.Health != "healthy" {
				t.Fatalf("control %+v %v", observed, err)
			}
			expected := []int32{0, 0, 1, 2, 3, 3}[deny-1]
			if requests.Load() != expected {
				t.Fatalf("revocation%d sent %d requests want%d", deny, requests.Load(), expected)
			}
		})
	}
}

func TestAppPlatformRecoveryRefusesMixedHealthReceiptBeforeAccess(t *testing.T) {
	for _, name := range []string{"version", "generation", "connection", "app", "deployment", "question", "release", "duplicate component", "null", "unknown field"} {
		t.Run(name, func(t *testing.T) {
			connection := ids.New(time.Now())
			calls := 0
			d := NewDriver(Dependencies{ConnectionID: connection, Client: func(context.Context) (*Client, Config, error) {
				calls++
				return nil, Config{}, errors.New("unexpected credential lookup")
			}}, Options{})
			receipt := healthReceipt{Version: 1, Connection: connection, APIBase: "https://api.example", App: "app", Deployment: "dep", Generation: 1, Artifact: "admitted", Readiness: readinessSnapshot{Version: 1, Path: "/ready", Expect: 200}, Components: []string{"web"}}
			checkpoint := map[string]any{"appId": "app", "appName": "app-name", "deploymentId": "dep", "artifactKind": appSpecV2, "readiness": receipt.Readiness}
			switch name {
			case "version":
				receipt.Version = 2
			case "generation":
				receipt.Generation = 2
			case "connection":
				receipt.Connection = ids.New(time.Now())
			case "app":
				receipt.App = "other"
			case "deployment":
				receipt.Deployment = "other"
			case "question":
				receipt.Readiness.Expect = 201
			case "release":
				receipt.Release = "other"
			case "duplicate component":
				receipt.Components = []string{"web", "web"}
			}
			checkpoint["healthReceipt"] = receipt
			if name == "null" {
				checkpoint["healthReceipt"] = nil
			}
			if name == "unknown field" {
				raw, _ := json.Marshal(receipt)
				var value map[string]any
				if err := json.Unmarshal(raw, &value); err != nil {
					t.Fatal(err)
				}
				value["other"] = true
				checkpoint["healthReceipt"] = value
			}
			observed, err := d.Resume(t.Context(), deploy.Placement{}, 1, checkpoint, func(string, ...any) {})
			if err == nil || calls != 0 || observed.Health == "healthy" {
				t.Fatalf("mixed %s receipt reached credentials/provider: %+v %v calls%d", name, observed, err, calls)
			}
		})
	}
}

func TestAppPlatformHealthDeadlineStopsProviderObservation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	connection := ids.New(time.Now())
	d := NewDriver(Dependencies{ConnectionID: connection, Client: func(context.Context) (*Client, Config, error) { return NewClient("token", server.URL), Config{}, nil }}, Options{})
	receipt := healthReceipt{Version: 1, Connection: connection, APIBase: server.URL, App: "app", Deployment: "dep", Generation: 1, Artifact: "artifact", Readiness: readinessSnapshot{Version: 1}, Components: []string{"web"}}
	p := deploy.Placement{Generation: 1, Handle: map[string]any{"appId": "app", "deploymentId": "dep", "healthReceipt": receipt}}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	got, err := d.Sample(ctx, p, "artifact", func(ctx context.Context) error { return ctx.Err() })
	if !errors.Is(err, context.DeadlineExceeded) || got.Health != "unknown" || !got.HealthObservedAt.IsZero() {
		t.Fatalf("expired provider observation gained evidence: %+v %v", got, err)
	}
}

func TestAppPlatformHealthRechecksPermitBeforePublicRequests(t *testing.T) {
	for _, deny := range []int{4, 5, 6, 7, 8} {
		t.Run(string(rune('0'+deny)), func(t *testing.T) {
			var public, provider atomic.Int32
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/public/") {
					public.Add(1)
					if strings.HasSuffix(r.URL.Path, "/release") {
						_, _ = w.Write([]byte("release"))
					}
					return
				}
				provider.Add(1)
				if strings.HasSuffix(r.URL.Path, "/health") {
					_, _ = w.Write([]byte(`{"app_health":{"components":[{"name":"web","state":"HEALTHY","replicas_desired":1,"replicas_ready":1}]}}`))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"id": "app", "live_url": server.URL + "/public", "active_deployment": map[string]any{"id": "dep", "phase": "ACTIVE"}}})
			}))
			defer server.Close()
			connection := ids.New(time.Now())
			d := NewDriver(Dependencies{ConnectionID: connection, Client: func(context.Context) (*Client, Config, error) { return NewClient("token", server.URL), Config{}, nil }}, Options{})
			receipt := healthReceipt{Version: 1, Connection: connection, APIBase: server.URL, App: "app", Deployment: "dep", Generation: 1, Artifact: "artifact", Readiness: readinessSnapshot{Version: 1, Path: "/ready", Expect: 200}, Release: "release", Components: []string{"web"}}
			p := deploy.Placement{Generation: 1, Handle: map[string]any{"appId": "app", "deploymentId": "dep", "healthReceipt": receipt}}
			calls := 0
			denied := errors.New("current public observation denied")
			got, err := d.Sample(t.Context(), p, "artifact", func(context.Context) error {
				calls++
				if calls == deny {
					return denied
				}
				return nil
			})
			if deny <= 7 {
				if !errors.Is(err, denied) || got.Health != "unknown" || !got.HealthObservedAt.IsZero() {
					t.Fatalf("public revocation%d became evidence: %+v %v", deny, got, err)
				}
			} else if err != nil || got.Health != "healthy" {
				t.Fatalf("positive public control %+v %v", got, err)
			}
			wantPublic := int32(2)
			if deny == 4 {
				wantPublic = 0
			}
			if deny == 5 {
				wantPublic = 1
			}
			wantProvider := int32(3)
			if deny <= 6 {
				wantProvider = 2
			}
			if public.Load() != wantPublic || provider.Load() != wantProvider {
				t.Fatalf("requests after revocation: public%d/%d provider%d/%d", public.Load(), wantPublic, provider.Load(), wantProvider)
			}
		})
	}
}
