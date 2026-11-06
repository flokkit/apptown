package portainer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"gopkg.in/yaml.v3"
)

func TestStoppedStackScalesOnlyAppPartsAndRetainsNeedsAndSettings(t *testing.T) {
	compose, ready := readinessFixture(t)
	compose += "  # unchanged needs\n"
	var file map[string]any
	if err := yaml.Unmarshal([]byte(compose), &file); err != nil {
		t.Fatal(err)
	}
	services := file["services"].(map[string]any)
	services["postgres"] = map[string]any{"image": "postgres:16", "environment": map[string]any{"PASSWORD": "secret"}}
	raw, err := yaml.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := scaleAppParts(string(raw), ready, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := yaml.Unmarshal([]byte(stopped), &got); err != nil {
		t.Fatal(err)
	}
	out := got["services"].(map[string]any)
	for _, name := range []string{"app", "worker"} {
		if out[name].(map[string]any)["deploy"].(map[string]any)["replicas"] != 0 {
			t.Fatalf("%s was started: %+v", name, out[name])
		}
	}
	for _, name := range []string{"postgres", "init-schema"} {
		if !reflect.DeepEqual(services[name], out[name]) {
			t.Fatalf("%s changed", name)
		}
	}
	started, err := scaleAppParts(stopped, ready, 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(started, "replicas: 0") {
		t.Fatal(started)
	}
}
func TestPortainerRuntimeActionsTouchOnlyCurrentAppContainers(t *testing.T) {
	for _, action := range []string{"stop", "start", "restart"} {
		t.Run(action, func(t *testing.T) {
			_, ready := readinessFixture(t)
			ready.Components = ready.Components[:2]
			scope := Scope{Version: 1, EndpointID: 9, DaemonID: "daemon"}
			expected := &expectedRelease{Readiness: ready, Generation: 2, ArtifactHash: "artifact", SubmittedHash: "submitted", Scope: scope}
			state := "running"
			if action == "start" {
				state = "exited"
			}
			labels := func(name string) map[string]string {
				return map[string]string{composeProject: "stack", composeService: name, releaseDeployment: ready.Deployment.String(), releaseGeneration: "2", releaseArtifact: "artifact", releaseComponent: name}
			}
			var acts []string
			workerState := ""
			missingWorker := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				path := strings.TrimPrefix(r.URL.Path, "/api/endpoints/9/docker/")
				switch {
				case path == "containers/json":
					var list []map[string]any
					for _, name := range []string{"app", "worker", "postgres"} {
						if name == "worker" && missingWorker {
							continue
						}
						partState := state
						if name == "worker" && workerState != "" {
							partState = workerState
						}
						// Needs never participate in runtime confirmation.
						if name == "postgres" {
							partState = "unknown"
						}
						list = append(list, map[string]any{"Id": name, "Labels": labels(name), "State": partState})
					}
					_ = json.NewEncoder(w).Encode(list)
				case r.Method == "POST":
					bits := strings.Split(path, "/")
					if len(bits) != 3 || bits[0] != "containers" || bits[1] == "postgres" || (bits[2] != "start" && bits[2] != "stop") {
						t.Errorf("unexpected effect %s", path)
					}
					acts = append(acts, bits[2]+":"+bits[1])
					if bits[2] == "stop" {
						state = "exited"
					} else {
						state = "running"
					}
					w.WriteHeader(204)
				case strings.HasPrefix(path, "containers/"):
					name := strings.Split(path, "/")[1]
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": name, "Image": "sha256:actual", "Config": map[string]any{"Image": ready.Components[0].Image, "Labels": labels(name)}, "State": map[string]any{"Running": state == "running", "Status": state, "Health": map[string]string{"Status": "healthy"}}})
				case strings.HasPrefix(path, "images/"):
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:actual", "RepoDigests": []string{ready.Components[0].Image}})
				default:
					t.Errorf("unexpected request %s", path)
					http.Error(w, "unexpected", 500)
				}
			}))
			defer server.Close()
			cfg := &Target{Scope: &scope, PortainerURL: server.URL}
			d := &Driver{Config: func(context.Context, ids.ID) (*Target, error) { return cfg, nil }}
			p := deploy.Placement{ID: ready.Deployment, Generation: 2, Handle: map[string]any{"stack": "stack", "releaseReadiness": expected}}
			if err := d.Runtime(t.Context(), p, action); err != nil {
				t.Fatal(err)
			}
			want := []string{fmt.Sprintf("%s:app", action), fmt.Sprintf("%s:worker", action)}
			if action == "restart" {
				want = []string{"stop:app", "stop:worker", "start:app", "start:worker"}
			}
			if !reflect.DeepEqual(acts, want) {
				t.Fatal(acts)
			}
			p.Stopped = action == "stop"
			if _, err := d.ObserveRuntime(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			workerState = "exited"
			if p.Stopped {
				workerState = "running"
			}
			if _, err := d.ObserveRuntime(t.Context(), p); err == nil {
				t.Fatal("confirmed partial runtime action")
			}
			workerState = "unknown"
			if _, err := d.ObserveRuntime(t.Context(), p); err == nil {
				t.Fatal("confirmed unassessable worker")
			}
			if !p.Stopped {
				missingWorker = true
				if _, err := d.ObserveRuntime(t.Context(), p); err == nil {
					t.Fatal("confirmed start with missing worker")
				}
			}
		})
	}
}

func TestStoppedPortainerReconcileUpdatesCodeWithoutStartingAppParts(t *testing.T) {
	compose, ready := readinessFixture(t)
	compose, err := WithReadiness(compose, ready)
	if err != nil {
		t.Fatal(err)
	}
	saved := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/stacks":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == "POST" && r.URL.Path == "/api/stacks/create/standalone/string":
			var body struct{ StackFileContent string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			saved = body.StackFileContent
			_, _ = w.Write([]byte(`{"Id":19}`))
		case r.Method == "GET" && r.URL.Path == "/api/stacks/19/file":
			_ = json.NewEncoder(w).Encode(map[string]string{"StackFileContent": saved})
		case r.Method == "GET" && r.URL.Path == "/api/endpoints/9/docker/containers/json":
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected provider call: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", 500)
		}
	}))
	defer server.Close()
	cfg := &Target{Scope: &Scope{Version: 1, APIBase: server.URL, EndpointID: 9, DaemonID: "daemon"}, PortainerURL: server.URL}
	d := &Driver{TargetID: ready.Target, Config: func(context.Context, ids.ID) (*Target, error) { return cfg, nil }, Allowed: func(context.Context) error { return nil }, Diagnostics: func(context.Context, Target) (Diagnostic, func() error, error) {
		t.Fatal("a stopped release must not probe the app")
		return Diagnostic{}, nil, nil
	}}
	art := deploy.Artifact{Kind: "compose", Body: []byte(compose), Hash: deploy.ConfigHash(strings.TrimSpace(compose))}
	noted := map[string]any{}
	o, err := d.Reconcile(t.Context(), deploy.Placement{ID: ready.Deployment, Stopped: true}, 2, art, "next", func(string, ...any) {}, func(values map[string]any) error {
		for k, v := range values {
			noted[k] = v
		}
		return nil
	})
	if err != nil || o.Rollout != "stopped" || o.Generation != 2 || saved == "" {
		t.Fatalf("stopped release: %+v %v", o, err)
	}
	var file struct {
		Services map[string]struct{ Deploy struct{ Replicas *int } }
	}
	if err := yaml.Unmarshal([]byte(saved), &file); err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"app", "worker"} {
		n := file.Services[part].Deploy.Replicas
		if n == nil || *n != 0 {
			t.Fatalf("%s was not held stopped: %s", part, saved)
		}
	}
	d.ExpectedArtifact = art.Hash
	if _, err := d.Resume(t.Context(), deploy.Placement{ID: ready.Deployment, Stopped: true}, 2, noted, func(string, ...any) {}); err != nil {
		t.Fatalf("stopped release recovery: %v", err)
	}
	delete(noted, "stackApplied")
	if _, err := d.Resume(t.Context(), deploy.Placement{ID: ready.Deployment, Stopped: true}, 2, noted, func(string, ...any) {}); err == nil {
		t.Fatal("recovery invented an applied stopped release")
	}
}
