package portainer

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"context"

	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"gopkg.in/yaml.v3"
)

func readinessFixture(t *testing.T) (string, Readiness) {
	t.Helper()
	image := "registry.example/app@sha256:" + strings.Repeat("a", 64)
	r := Readiness{URL: "https://app.example", Version: 1, Deployment: idstest.N(7), Target: idstest.N(8), Revision: "revision", Components: []Component{{Service: "app", Image: image}, {Service: "worker", Image: image, ShellHealth: true}, {Service: "init-schema", Image: image, Init: true}}, Public: Probe{ToolImage: image, Port: 9123, Path: "/ready?mode=live", Expect: 204}}
	compose, err := MarkCompose("services:\n  app:\n    image: "+image+"\n  worker:\n    image: "+image+"\n    healthcheck:\n      test: [CMD, true]\n  init-schema:\n    image: "+image+"\n", r.Deployment)
	if err != nil {
		t.Fatal(err)
	}
	return compose, r
}

func TestReadinessIsBoundToRenderedImagesAndDeployment(t *testing.T) {
	compose, r := readinessFixture(t)
	got, err := WithReadiness(compose, r)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Readiness Readiness `yaml:"x-apptown-readiness"`
	}
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Readiness.Target != r.Target || doc.Readiness.Deployment != r.Deployment || doc.Readiness.Public.Port != 9123 || len(doc.Readiness.Components) != 3 {
		t.Fatalf("lost admitted expectation: %+v", doc.Readiness)
	}
	if _, err := WithReadiness(got, r); err == nil {
		t.Fatal("replaced immutable expectation")
	}
	for name, mutate := range map[string]func(*Readiness){
		"wrong deployment": func(r *Readiness) { r.Deployment = idstest.N(9) },
		"missing target":   func(r *Readiness) { r.Target = ids.ID{} },
		"tag image":        func(r *Readiness) { r.Components[0].Image = "app:latest" },
		"wrong digest":     func(r *Readiness) { r.Components[0].Image = "registry.example/app@sha256:" + strings.Repeat("b", 64) },
		"duplicate":        func(r *Readiness) { r.Components = append(r.Components, r.Components[0]) },
		"external URL":     func(r *Readiness) { r.Public.Path = "https://elsewhere.example/ready" },
		"scheme relative":  func(r *Readiness) { r.Public.Path = "//elsewhere.example/ready" },
		"invalid port":     func(r *Readiness) { r.Public.Port = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			compose, r := readinessFixture(t)
			mutate(&r)
			if _, err := WithReadiness(compose, r); err == nil {
				t.Fatal("accepted altered expectation")
			}
		})
	}
}

func TestExecutionLabelsBindGenerationAndOriginalArtifactWithoutSelfHash(t *testing.T) {
	compose, r := readinessFixture(t)
	original, err := WithReadiness(compose, r)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Version: 1, APIBase: "http://portainer.example", EndpointID: 3, DaemonID: "daemon-a"}
	stamped, expected, err := stampRelease(original, r.Target, 8, scope)
	if err != nil {
		t.Fatal(err)
	}
	if expected.Generation != 8 || expected.SubmittedHash == expected.ArtifactHash || expected.ArtifactHash != deploy.ConfigHash(strings.TrimSpace(original)) {
		t.Fatalf("wrong artifact identity: %+v", expected)
	}
	var doc struct {
		Services map[string]struct {
			Labels map[string]string `yaml:"labels"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(stamped), &doc); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app", "worker", "init-schema"} {
		labels := doc.Services[name].Labels
		if labels[releaseGeneration] != "8" || labels[releaseComponent] != name || labels[releaseDeployment] != r.Deployment.String() || labels[releaseArtifact] != expected.ArtifactHash {
			t.Fatalf("wrong labels %s: %v", name, labels)
		}
	}
	if _, _, err := stampRelease(stamped, r.Target, 9, scope); err == nil {
		t.Fatal("reused execution-stamped artifact")
	}
	if _, _, err := stampRelease(original, idstest.N(9), 8, scope); err == nil {
		t.Fatal("changed target")
	}
	saved, err := savedRelease(map[string]any{"releaseReadiness": expected})
	if err != nil || saved.Generation != 8 {
		t.Fatalf("checkpoint: %+v %v", saved, err)
	}
	if got, legacy, err := stampRelease(compose, r.Target, 8, scope); err != nil || legacy != nil || got != compose {
		t.Fatalf("historical bytes: %v %v", legacy, err)
	}
}

func TestReadinessCheckpointPrecedesEffectsAndNamesSubmittedBytes(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("checkpoint-fails=%v", fail), func(t *testing.T) {
			compose, r := readinessFixture(t)
			original, err := WithReadiness(compose, r)
			if err != nil {
				t.Fatal(err)
			}
			var saved *expectedRelease
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				calls++
				if saved == nil {
					t.Error("provider reached before durable expectation")
				}
				switch request.URL.Path {
				case "/api/stacks":
					fmt.Fprint(w, "[]")
				case "/api/stacks/create/standalone/string":
					var body struct {
						Compose string `json:"stackFileContent"`
					}
					if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if saved == nil || deploy.ConfigHash(strings.TrimSpace(body.Compose)) != saved.SubmittedHash || !strings.Contains(body.Compose, releaseGeneration) {
						t.Error("submitted bytes differ from checkpoint")
					}
					fmt.Fprint(w, `{"Id":19}`)
				default:
					t.Errorf("unexpected provider request %s", request.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			cfg := Target{PortainerURL: server.URL, Scope: &Scope{Version: 1, APIBase: server.URL, EndpointID: 3, DaemonID: "daemon"}}
			receiptErr := errors.New("checkpoint unavailable")
			daemon := containerresulttest.Daemon{Answers: func(containerresulttest.Command) (containerresulttest.Answer, bool) {
				return containerresulttest.Answer{Answer: []byte("apptown-readiness-v1")}, true
			}}
			diagnostic := Diagnostic{allowed: func(context.Context) error { return nil }, Command: func(ctx context.Context, env []string, args ...string) ([]byte, error) {
				out, err, ok := daemon.RunEnv(args, env)
				if !ok {
					return nil, fmt.Errorf("unexpected diagnostic %v", args)
				}
				return out, err
			}}
			got, err := (execution{diagnostic: &diagnostic, allowed: diagnostic.allowed, targetID: r.Target, deploymentID: r.Deployment, config: cfg, endpointID: 3, stack: "stack-a", compose: original, log: func(string, ...any) {}}).execute(t.Context(), 8, func(identity map[string]any) error {
				if _, ok := identity["releaseReadiness"]; !ok {
					if identity["stackId"] != int64(19) || identity["stackApplied"] != true {
						t.Errorf("wrong application receipt: %v", identity)
					}
					return nil
				}
				var err error
				saved, err = savedRelease(identity)
				if err != nil {
					return err
				}
				if fail {
					return receiptErr
				}
				return nil
			})
			if fail {
				if err == nil || !strings.Contains(err.Error(), receiptErr.Error()) || !deploy.IsTerminal(err) || calls != 0 {
					t.Fatalf("checkpoint failure allowed effects: %d %v", calls, err)
				}
				return
			}
			if err != nil || got.Health != "unknown" {
				t.Fatalf("apply does not establish health: %+v %v", got, err)
			}
			handle, ok := got.Handle.(map[string]any)
			if !ok {
				t.Fatal("missing provider handle")
			}
			retained, err := savedRelease(handle)
			if err != nil || retained == nil || retained.SubmittedHash != saved.SubmittedHash {
				t.Fatalf("lost expected release: %+v %v", retained, err)
			}
		})
	}
}
