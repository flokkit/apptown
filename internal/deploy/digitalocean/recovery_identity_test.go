package digitalocean

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A lost response is recovered only from the exact generation in a deployment's
// spec. Lists may be paginated; the app's current active ID is never a substitute.
func TestLostApplyIdentityRequiresProviderGenerationEvidence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"active", "failed", "unrelated-active", "unmarked", "absent", "unavailable", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			place := deploy.Placement{ID: idstest.N(77)}
			name := doAppName(deploy.HostKey(place.ID))
			mark, err := json.Marshal(recoveryMark{Generation: 3, ArtifactKind: appSpecV2, Readiness: readinessSnapshot{Version: 1}})
			if err != nil {
				t.Fatal(err)
			}
			spec := AppSpec{Name: name, Envs: []doEnv{{Key: recoveryEnv, Value: string(mark)}}}
			if mode == "unmarked" {
				spec.Envs = nil
			}
			phase := "ACTIVE"
			if mode == "failed" {
				phase = "ERROR"
			}
			matched := doDeployment{ID: "selected", Phase: phase, Spec: spec}
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					writes++
					t.Errorf("recovery performed %s", r.Method)
					w.WriteHeader(500)
					return
				}
				var answer any
				switch r.URL.Path {
				case "/v2/apps":
					if mode == "unavailable" {
						w.WriteHeader(503)
						return
					}
					apps := []any{}
					if mode != "absent" {
						apps = append(apps, map[string]any{"id": "app", "spec": map[string]any{"name": name}})
					}
					answer = map[string]any{"apps": apps}
				case "/v2/apps/app/deployments":
					if r.URL.Query().Get("page") == "2" {
						deps := []doDeployment{matched}
						if mode == "ambiguous" {
							other := matched
							other.ID = "duplicate"
							deps = append(deps, other)
						}
						answer = map[string]any{"deployments": deps}
					} else {
						answer = map[string]any{"deployments": []any{map[string]any{"id": "old", "phase": "ACTIVE", "spec": map[string]any{"name": name}}}, "links": map[string]any{"pages": map[string]any{"next": "/v2/apps/app/deployments?page=2"}}}
					}
				case "/v2/apps/app/deployments/selected":
					answer = map[string]any{"deployment": matched}
				case "/v2/apps/app":
					active := "selected"
					if mode == "unrelated-active" {
						active = "unrelated"
					}
					answer = map[string]any{"app": map[string]any{"id": "app", "active_deployment": map[string]any{"id": active, "phase": "ACTIVE"}}}
				default:
					t.Errorf("unexpected request: %s", r.URL)
					w.WriteHeader(500)
					return
				}
				if err := json.NewEncoder(w).Encode(answer); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			driver := NewDriver(Dependencies{Client: func(context.Context) (*Client, Config, error) {
				return &Client{Base: server.URL, HTTP: server.Client()}, Config{}, nil
			}}, Options{})
			for _, checkpoint := range []map[string]any{{}, {"appName": name, "appId": "app"}} {
				got, err := driver.Resume(t.Context(), place, 3, checkpoint, func(string, ...any) {})
				switch mode {
				case "active":
					if err != nil || got.Generation != 3 || got.Rollout != "active" {
						t.Fatalf("matching generation: %+v %v", got, err)
					}
				case "failed":
					if !deploy.IsTerminal(err) {
						t.Fatalf("provider failure was not terminal: %v", err)
					}
				case "absent":
					if !errors.Is(err, deploy.ErrNotApplied) {
						t.Fatalf("provider-confirmed absence: %v", err)
					}
				default:
					if err == nil || errors.Is(err, deploy.ErrNotApplied) || deploy.IsTerminal(err) {
						t.Fatalf("unknown outcome became retryable or settled: %+v %v", got, err)
					}
				}
			}
			if writes != 0 {
				t.Fatal("observation repeated provider effects")
			}
		})
	}
}
