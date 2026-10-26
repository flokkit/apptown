package portainer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
)

func TestReleaseObservationRequiresExactComponentsAndActualImages(t *testing.T) {
	for _, mode := range []string{"exact", "old-generation", "foreign-deployment", "wrong-artifact", "wrong-image", "missing-digest", "duplicate", "missing", "init-running", "init-failed", "worker-starting", "unavailable", "changed-scope"} {
		t.Run(mode, func(t *testing.T) {
			_, r := readinessFixture(t)
			artifact := strings.Repeat("f", 64)
			labels := func(service string) map[string]string {
				return map[string]string{composeProject: "stack-a", composeService: service, releaseComponent: service, releaseDeployment: r.Deployment.String(), releaseGeneration: "8", releaseArtifact: artifact}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				path := request.URL.Path
				switch {
				case path == "/api/endpoints/3/docker/containers/json":
					if mode == "unavailable" {
						w.WriteHeader(503)
						return
					}
					if !strings.Contains(request.URL.Query().Get("filters"), "stack-a") {
						t.Error("observation did not limit placement")
					}
					rows := []map[string]any{}
					for _, c := range r.Components {
						if mode == "missing" && c.Service == "worker" {
							continue
						}
						rows = append(rows, map[string]any{"Id": c.Service, "Labels": labels(c.Service)})
					}
					// A sibling placement never supplies or duplicates this placement's app.
					rows = append(rows, map[string]any{"Id": "sibling", "Labels": map[string]string{composeProject: "stack-b", composeService: "app"}})
					if mode == "duplicate" {
						rows = append(rows, map[string]any{"Id": "other-app", "Labels": labels("app")})
					}
					_ = json.NewEncoder(w).Encode(rows)
				case strings.Contains(path, "/containers/"):
					service := strings.TrimSuffix(strings.TrimPrefix(path, "/api/endpoints/3/docker/containers/"), "/json")
					l := labels(service)
					image := r.Components[0].Image
					if mode == "old-generation" {
						l[releaseGeneration] = "7"
					}
					if mode == "foreign-deployment" {
						l[releaseDeployment] = "elsewhere"
					}
					if mode == "wrong-artifact" {
						l[releaseArtifact] = "other"
					}
					if mode == "wrong-image" {
						image = "registry.example/app@sha256:" + strings.Repeat("b", 64)
					}
					state := map[string]any{"Running": true, "Status": "running", "ExitCode": 0, "Health": map[string]string{"Status": "healthy"}}
					if service == "init-schema" {
						state["Running"] = false
						state["Status"] = "exited"
						if mode == "init-running" {
							state["Running"] = true
						}
						if mode == "init-failed" {
							state["ExitCode"] = 17
						}
					}
					if service == "worker" && mode == "worker-starting" {
						state["Health"] = map[string]string{"Status": "starting"}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": service, "Image": "sha256:actual", "Config": map[string]any{"Image": image, "Labels": l}, "State": state})
				case path == "/api/endpoints/3/docker/images/sha256:actual/json":
					digests := []string{r.Components[0].Image}
					if mode == "missing-digest" {
						digests = nil
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:actual", "RepoDigests": digests})
				default:
					t.Errorf("unexpected observation %s", path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			scope := Scope{Version: 1, APIBase: server.URL, EndpointID: 3, DaemonID: "daemon"}
			expected := &expectedRelease{Readiness: r, Generation: 8, ArtifactHash: artifact, SubmittedHash: "submitted", Scope: scope}
			cfg := &Target{PortainerURL: server.URL, Scope: &scope}
			if mode == "changed-scope" {
				expected.Scope.DaemonID = "other"
			}
			got, err := observeRelease(t.Context(), cfg, deploy.Placement{ID: r.Deployment, Generation: 8}, "stack-a", expected)
			if mode == "exact" {
				if err != nil || got.Rollout != "active" || got.Health != "unknown" || got.Running != r.Components[0].Image {
					t.Fatalf("exact component sample is not yet public readiness: %+v %v", got, err)
				}
			} else if err == nil {
				t.Fatalf("accepted %s as %+v", mode, got)
			}
		})
	}
}
