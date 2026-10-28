package portainer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids"
	"gopkg.in/yaml.v3"
)

func TestHealthyReleaseRequiresAppliedExactProbeAndPostProbeIdentity(t *testing.T) {
	for _, mode := range []string{"healthy", "explicit-streak", "wrong-status", "replacement-during-probe", "restart-during-probe", "failed-init", "worker-unhealthy", "provider-unavailable", "changed-target", "revoked-before-probe", "checkpoint-after-apply", "transport-cleanup", "lost-apply-response"} {
		t.Run(mode, func(t *testing.T) {
			compose, r := readinessFixture(t)
			if mode == "explicit-streak" {
				r.Version = 2
				r.Public.Policy = &appcontract.HealthPolicy{Explicit: true, Period: time.Second, Timeout: time.Second, SuccessThreshold: 2, FailureThreshold: 1}
			}
			original, err := WithReadiness(compose, r)
			if err != nil {
				t.Fatal(err)
			}
			applied := false
			applies := 0
			probeCount := 0
			revoked := false
			preflight := 0
			stack := ""
			submitted := ""
			labels := map[string]map[string]string{}
			idFor := func(service string) string {
				ch := "a"
				if service == "worker" {
					ch = "b"
				}
				if service == "init-schema" {
					ch = "c"
				}
				if service == "app" && mode == "replacement-during-probe" && probeCount%2 == 1 {
					ch = "d"
				}
				return strings.Repeat(ch, 64)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				path := request.URL.Path
				switch {
				case path == "/api/stacks" && request.Method == "GET":
					if applied && mode == "provider-unavailable" {
						w.WriteHeader(503)
						return
					}
					if !applied {
						fmt.Fprint(w, "[]")
						return
					}
					_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": 19, "Name": stack, "EndpointId": 3}})
				case path == "/api/stacks/19/file":
					_ = json.NewEncoder(w).Encode(map[string]string{"StackFileContent": submitted})
				case path == "/api/stacks/create/standalone/string":
					if preflight == 0 {
						t.Error("stack applied without diagnostic capability preflight")
					}
					var body struct {
						Name    string
						Compose string `json:"stackFileContent"`
					}
					if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					submitted = body.Compose
					stack = body.Name
					applied = true
					applies++
					var doc struct {
						Services map[string]struct {
							Labels map[string]string `yaml:"labels"`
						} `yaml:"services"`
					}
					if err := yaml.Unmarshal([]byte(submitted), &doc); err != nil {
						t.Error(err)
						return
					}
					for name, service := range doc.Services {
						labels[name] = service.Labels
					}
					if mode == "lost-apply-response" {
						w.WriteHeader(503)
						return
					}
					fmt.Fprint(w, `{"Id":19}`)
				case path == "/api/endpoints/3/docker/containers/json":
					rows := []map[string]any{}
					for _, c := range r.Components {
						l := map[string]string{composeProject: stack, composeService: c.Service}
						rows = append(rows, map[string]any{"Id": idFor(c.Service), "Labels": l, "State": "running", "Image": c.Image})
					}
					_ = json.NewEncoder(w).Encode(rows)
				case strings.Contains(path, "/containers/"):
					id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/endpoints/3/docker/containers/"), "/json")
					service := ""
					for _, c := range r.Components {
						if idFor(c.Service) == id {
							service = c.Service
						}
					}
					l := map[string]string{composeProject: stack, composeService: service}
					for k, v := range labels[service] {
						l[k] = v
					}
					state := map[string]any{"StartedAt": time.Unix(1000, 0).UTC(), "Running": true, "Status": "running", "ExitCode": 0, "Health": map[string]string{"Status": "healthy"}}
					if service == "app" && mode == "restart-during-probe" && probeCount%2 == 1 {
						state["StartedAt"] = time.Unix(1001, 0).UTC()
					}
					if service == "init-schema" {
						state["Running"] = false
						state["Status"] = "exited"
						if mode == "failed-init" {
							state["ExitCode"] = 17
						}
					}
					if service == "worker" && mode == "worker-unhealthy" {
						state["Health"] = map[string]string{"Status": "unhealthy"}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "Image": "sha256:actual", "Config": map[string]any{"Image": r.Components[0].Image, "Labels": l}, "State": state})
				case path == "/api/endpoints/3/docker/images/sha256:actual/json":
					if mode == "revoked-before-probe" {
						revoked = true
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:actual", "RepoDigests": []string{r.Components[0].Image}})
				default:
					t.Errorf("unexpected request %s %s", request.Method, path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			scope := Scope{Version: 1, APIBase: server.URL, EndpointID: 3, DaemonID: "daemon"}
			now := time.Unix(1234, 0).UTC()
			d := Driver{TargetID: r.Target, ReadinessTimeout: 100 * time.Millisecond, Now: func() time.Time { return now }, Allowed: func(context.Context) error {
				if revoked {
					return errors.New("release permission revoked")
				}
				return nil
			}, Config: func(context.Context, ids.ID) (*Target, error) {
				copy := scope
				if applied && mode == "changed-target" {
					copy.DaemonID = "other"
				}
				return &Target{PortainerURL: server.URL, Scope: &copy}, nil
			}}
			if mode == "explicit-streak" {
				d.ReadinessTimeout = 5 * time.Second
			}
			d.Diagnostics = func(context.Context, Target) (Diagnostic, func() error, error) {
				daemon := containerresulttest.Daemon{Answers: func(c containerresulttest.Command) (containerresulttest.Answer, bool) {
					answer := "204 1"
					if c.Script == "printf apptown-readiness-v1" {
						preflight++
						answer = "apptown-readiness-v1"
					} else {
						probeCount++
						if mode == "wrong-status" {
							answer = "503 1"
						}
					}
					return containerresulttest.Answer{Answer: []byte(answer)}, true
				}}
				return Diagnostic{Command: func(ctx context.Context, env []string, args ...string) ([]byte, error) {
					out, err, ok := daemon.RunEnv(args, env)
					if !ok {
						return nil, fmt.Errorf("unexpected diagnostic command %v", args)
					}
					return out, err
				}}, func() error {
					if mode == "transport-cleanup" {
						return errors.New("proxy cleanup unavailable")
					}
					return nil
				}, nil
			}
			checkpoint := map[string]any{}
			save := func(values map[string]any) error {
				for k, v := range values {
					checkpoint[k] = v
				}
				if values["stackApplied"] == true && mode == "checkpoint-after-apply" {
					return errors.New("applied receipt unavailable")
				}
				return nil
			}
			placement := deploy.Placement{ID: r.Deployment}
			got, err := d.Reconcile(t.Context(), placement, 1, deploy.Artifact{Kind: "compose", Body: []byte(original), Hash: deploy.ConfigHash(strings.TrimSpace(original))}, "request", func(string, ...any) {}, save)
			if mode == "healthy" || mode == "explicit-streak" {
				if err != nil || got.RuntimeToken != strings.Repeat("a", 64)+"/1970-01-01T00:16:40Z" || got.Health != "healthy" || got.HealthObservedAt != now || got.ArtifactHash != deploy.ConfigHash(strings.TrimSpace(original)) {
					t.Fatalf("healthy witness: %+v %v", got, err)
				}
				if mode == "explicit-streak" && (probeCount != 2 || got.ReadinessPolicy == nil || got.ReadinessPolicy.SuccessThreshold != 2) {
					t.Fatalf("explicit policy bypassed: probes%d observation%+v", probeCount, got)
				}
				got, err = d.Resume(t.Context(), placement, 1, checkpoint, func(string, ...any) {})
				if err != nil || got.Health != "healthy" || applies != 1 {
					t.Fatalf("resume repeated or lost the effect: %+v %v applies%d", got, err, applies)
				}
			} else if mode == "lost-apply-response" {
				if err == nil {
					t.Fatal("lost apply response was accepted")
				}
				got, err = d.Resume(t.Context(), placement, 1, checkpoint, func(string, ...any) {})
				if err != nil || got.Health != "healthy" || applies != 1 {
					t.Fatalf("observing lost response: %+v %v applies%d", got, err, applies)
				}
			} else if err == nil || got.Health == "healthy" {
				t.Fatalf("%s falsely healthy: %+v %v", mode, got, err)
			}
			if applies != 1 {
				t.Fatalf("applied%d times", applies)
			}
			if (mode == "revoked-before-probe" || mode == "checkpoint-after-apply" || mode == "changed-target") && probeCount > 0 {
				t.Fatal("probe ran after refusal")
			}
			if mode == "wrong-status" && !deploy.IsTerminal(err) {
				t.Fatalf("known readiness failure not terminal: %v", err)
			}
		})
	}
}

func TestAdmittedArtifactMismatchRefusesBeforeDiagnosticOrRecoveryIO(t *testing.T) {
	compose, r := readinessFixture(t)
	original, err := WithReadiness(compose, r)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Version: 1, APIBase: "http://unused.example", EndpointID: 3, DaemonID: "daemon"}
	factory := 0
	config := 0
	driver := Driver{TargetID: r.Target, Allowed: func(context.Context) error { return nil }, Config: func(context.Context, ids.ID) (*Target, error) { config++; return &Target{Scope: &scope}, nil }, Diagnostics: func(context.Context, Target) (Diagnostic, func() error, error) {
		factory++
		return Diagnostic{}, nil, errors.New("unexpected diagnostic")
	}}
	if _, err := driver.Reconcile(t.Context(), deploy.Placement{ID: r.Deployment}, 1, deploy.Artifact{Kind: "compose", Body: []byte(original), Hash: "different"}, "request", func(string, ...any) {}, func(map[string]any) error { t.Fatal("mismatched artifact checkpointed"); return nil }); err == nil || !deploy.IsTerminal(err) {
		t.Fatalf("artifact mismatch: %v", err)
	}
	if factory != 0 {
		t.Fatal("diagnostic opened for another artifact")
	}
	_, expected, err := stampRelease(original, r.Target, 1, scope)
	if err != nil {
		t.Fatal(err)
	}
	driver.ExpectedArtifact = "different"
	config = 0
	checkpoint := map[string]any{"releaseReadiness": expected, "stack": "stack-a"}
	if _, err := driver.Resume(t.Context(), deploy.Placement{ID: r.Deployment}, 1, checkpoint, func(string, ...any) {}); err == nil {
		t.Fatal("recovery trusted checkpoint without admitted artifact")
	}
	if _, err := driver.Sample(t.Context(), deploy.Placement{ID: r.Deployment, Generation: 1, Handle: checkpoint}); err == nil {
		t.Fatal("maintenance trusted checkpoint without admitted artifact")
	}
	if config != 0 || factory != 0 {
		t.Fatalf("mismatched identity reached provider: config%d factory%d", config, factory)
	}
}
