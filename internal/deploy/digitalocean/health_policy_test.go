package digitalocean

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids"
)

func TestAppPlatformRendersAndRetainsExplicitHTTPPolicy(t *testing.T) {
	for _, gated := range []bool{false, true} {
		t.Run(map[bool]string{false: "standing", true: "gated"}[gated], func(t *testing.T) {
			d, _ := gatedDriver(t, nil)
			c := gatedContract(t)
			c.PublicPart().Intent.Readiness = &appcontract.Probe{HTTP: "/ready", Expect: 200, Period: "20s", Timeout: "10s", SuccessThreshold: 3, FailureThreshold: 4, ChecksDeps: true}
			c.PublicPart().Intent.Liveness = &appcontract.Probe{HTTP: "/live", Period: "5s", Timeout: "2s", FailureThreshold: 7, SuccessThreshold: 1}
			p := deploy.Placement{Name: "main"}
			if gated {
				p.Gate = &deploy.Gate{Image: gateRef, Secret: strings.Repeat("ab", 32), AppTown: "https://forge.example", Marker: testMarker}
			}
			art, err := d.Render(t.Context(), p, deploy.Source{Images: map[string]string{"web": "app:1", "worker": "worker:1"}, Init: map[string]string{"migrate": "migrate:1"}, Contract: c}, nil, scopedRenderSecrets{}, func(string, ...any) {})
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := decodeAppArtifact(art)
			if err != nil {
				t.Fatal(err)
			}
			for _, part := range decoded.Spec.Services {
				if part.Name == "web" {
					h, l := part.HealthCheck, part.LivenessHealthCheck
					if h == nil || h.HTTPPath != "/ready" || h.PeriodSeconds != 20 || h.TimeoutSeconds != 10 || h.FailureThreshold != 4 || h.SuccessThreshold != 3 {
						t.Fatalf("readiness: %+v", h)
					}
					if l == nil || l.HTTPPath != "/live" || l.PeriodSeconds != 5 || l.TimeoutSeconds != 2 || l.FailureThreshold != 7 || l.SuccessThreshold != 1 {
						t.Fatalf("liveness: %+v", l)
					}
				}
				if part.Name == GateComponent && (part.HealthCheck.PeriodSeconds != 10 || part.HealthCheck.TimeoutSeconds != 5 || part.HealthCheck.SuccessThreshold != 0 || part.LivenessHealthCheck != nil) {
					t.Fatalf("gate policy changed: %+v", part)
				}
			}
			q := decoded.Readiness
			if q.Version != 2 || q.Policy == nil || !q.Policy.Explicit || q.Policy.Period != 20*time.Second || q.Policy.Timeout != 10*time.Second || q.Policy.SuccessThreshold != 3 || !q.Policy.ChecksDeps {
				t.Fatalf("admitted policy missing: %+v", q)
			}
			replay, err := checkpointReadiness(map[string]any{"artifactKind": art.Kind, "readiness": q})
			if err != nil || !sameReadiness(q, replay) {
				t.Fatalf("checkpoint roundtrip: %+v %v", replay, err)
			}
			changed := q
			copy := *q.Policy
			copy.ChecksDeps = false
			changed.Policy = &copy
			other, err := encodeAppArtifact(decoded.Spec, changed)
			if err != nil || other.Hash == art.Hash {
				t.Fatal("asserted policy omitted from artifact identity")
			}
		})
	}
}

func TestAppPlatformPolicyRefusesBeforeProviderAndKeepsLegacyBytes(t *testing.T) {
	for _, bad := range []string{"worker-ready", "worker-live", "exec", "tcp", "private-expect", "live-success", "live-checks-deps", "period", "timeout", "boot-fit"} {
		t.Run(bad, func(t *testing.T) {
			c := gatedContract(t)
			p := c.PublicPart()
			p.Intent.Readiness = &appcontract.Probe{HTTP: "/ready"}
			switch bad {
			case "worker-ready":
				c.Parts = append(c.Parts, appcontract.Part{Name: "late", Image: "x", Intent: &appcontract.Health{Readiness: &appcontract.Probe{HTTP: "/ready"}}})
			case "worker-live":
				c.Parts = append(c.Parts, appcontract.Part{Name: "late", Image: "x", Intent: &appcontract.Health{Liveness: &appcontract.Probe{HTTP: "/live"}}})
			case "exec":
				p.Intent.Readiness = &appcontract.Probe{Exec: []any{"true"}}
			case "tcp":
				p.Intent.Readiness = &appcontract.Probe{TCP: 8080}
			case "private-expect":
				c.Parts = append(c.Parts, appcontract.Part{Name: "late", Port: 8081, Image: "x", Intent: &appcontract.Health{Readiness: &appcontract.Probe{HTTP: "/ready", Expect: 200}}})
			case "live-success":
				p.Intent.Liveness = &appcontract.Probe{HTTP: "/live", SuccessThreshold: 2}
			case "live-checks-deps":
				p.Intent.Liveness = &appcontract.Probe{HTTP: "/live", ChecksDeps: true}
			case "period":
				p.Intent.Readiness.Period = "1s"
			case "timeout":
				p.Intent.Readiness.Timeout = "11s"
			case "boot-fit":
				p.Intent.Readiness.Period = "30s"
				p.Intent.Readiness.Timeout = "10s"
				p.Intent.Readiness.SuccessThreshold = 4
			}
			calls := 0
			d := NewDriver(Dependencies{Client: func(context.Context) (*Client, Config, error) { calls++; return nil, Config{}, nil }}, Options{})
			src := deploy.Source{Contract: c, Images: map[string]string{"web": "app:1", "worker": "worker:1", "late": "x"}}
			if _, err := d.Render(t.Context(), deploy.Placement{}, src, nil, scopedRenderSecrets{}, func(string, ...any) {}); err == nil || calls != 0 {
				t.Fatalf("invalid policy reached provider: %v calls%d", err, calls)
			}
			if err := d.Preflight(t.Context(), deploy.Placement{}, src, scopedRenderSecrets{}, func(string, ...any) {}); err == nil || calls != 0 {
				t.Fatalf("preflight accepted: %v calls%d", err, calls)
			}
		})
	}
	c := gatedContract(t)
	h, err := serviceHealth(c, false)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(h["web"].readiness)
	if string(body) != `{"http_path":"/healthz","port":8080,"initial_delay_seconds":5,"period_seconds":10,"timeout_seconds":5,"failure_threshold":6}` {
		t.Fatalf("omitted provider defaults changed: %s", body)
	}
	q, err := readinessFor(c, false)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(q)
	if string(body) != `{"version":1,"path":"/healthz","expect":0}` {
		t.Fatalf("legacy readiness bytes changed: %s", body)
	}
}

func TestRecordedExplicitAppPlatformPolicyRefusesMalformedOrDowngradedValues(t *testing.T) {
	policy := readinessDefaults()
	policy.Explicit = true
	q := readinessSnapshot{Version: 2, Path: "/ready", Expect: 200, Policy: &policy}
	for _, bad := range []string{"missing", "downgrade", "unknown", "timeout", "empty-path", "nonexplicit"} {
		t.Run(bad, func(t *testing.T) {
			b, _ := json.Marshal(q)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			switch bad {
			case "missing":
				delete(m, "policy")
			case "downgrade":
				m["version"] = 1
			case "unknown":
				m["policy"].(map[string]any)["extra"] = true
			case "timeout":
				m["policy"].(map[string]any)["timeout"] = float64(11 * time.Second)
			case "empty-path":
				m["path"] = ""
			case "nonexplicit":
				m["policy"].(map[string]any)["explicit"] = false
			}
			b, _ = json.Marshal(m)
			if _, err := decodeReadiness(b); err == nil {
				t.Fatalf("bad receipt accepted: %s", b)
			}
		})
	}
}

func TestMaintainedAppPlatformSampleHonorsAdmittedProbeTimeout(t *testing.T) {
	for _, tc := range []struct {
		name           string
		delay, timeout time.Duration
		healthy        bool
	}{{"six seconds within ten", 6 * time.Second, 10 * time.Second, true}, {"probe timeout", 2 * time.Second, time.Second, false}} {
		t.Run(tc.name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/ready":
					if r.Header.Get("Authorization") != "" {
						t.Error("provider credential reached app")
					}
					timer := time.NewTimer(tc.delay)
					defer timer.Stop()
					select {
					case <-timer.C:
						w.WriteHeader(200)
					case <-r.Context().Done():
					}
				case "/v2/apps/app/health":
					_ = json.NewEncoder(w).Encode(map[string]any{"app_health": map[string]any{"components": []map[string]any{{"name": "web", "state": "HEALTHY", "replicas_desired": 1, "replicas_ready": 1}}}})
				default:
					_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"id": "app", "live_url": server.URL, "active_deployment": map[string]any{"id": "running", "phase": "ACTIVE"}}})
				}
			}))
			defer server.Close()
			id := ids.New(time.Now())
			policy := readinessDefaults()
			policy.Explicit = true
			policy.Timeout = tc.timeout
			policy.SuccessThreshold = 2
			r := healthReceipt{Version: 1, Connection: id, APIBase: server.URL, App: "app", Deployment: "running", Generation: 3, Artifact: "hash", Readiness: readinessSnapshot{Version: 2, Path: "/ready", Expect: 200, Policy: &policy}, Components: []string{"web"}}
			d := NewDriver(Dependencies{ConnectionID: id, Client: func(context.Context) (*Client, Config, error) { return NewClient("secret", server.URL), Config{}, nil }}, Options{})
			got, err := d.Sample(t.Context(), deploy.Placement{Generation: 3, Handle: map[string]any{"appId": "app", "deploymentId": "running", "healthReceipt": r}}, "hash", func(ctx context.Context) error { return ctx.Err() })
			if tc.healthy {
				if err != nil || got.Health != "healthy" || got.ReadinessPolicy == nil || *got.ReadinessPolicy != policy || got.RuntimeToken != "running" {
					t.Fatalf("admitted sample failed: %+v %v", got, err)
				}
			} else if err == nil || !got.HealthObservedAt.IsZero() || got.Health != "unknown" {
				t.Fatalf("timed out probe stamped health: %+v %v", got, err)
			}
		})
	}
}
