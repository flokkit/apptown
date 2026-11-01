package portainer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestPortainerExecutionUsesCheckpointedEndpointAndStack(t *testing.T) {
	t.Parallel()
	for _, existing := range []bool{false, true} {
		for _, driver := range []bool{false, true} {
			t.Run(fmt.Sprintf("existing=%v/driver=%v", existing, driver), func(t *testing.T) {
				var mu sync.Mutex
				var checkpoint map[string]any
				mutations := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					if r.Header.Get("X-API-Key") != "secret" {
						t.Error("missing execution credential")
					}
					switch {
					case r.URL.Path == "/api/endpoints":
						fmt.Fprint(w, `[{"Id":3,"Name":"selected"}]`)
					case r.Method == "GET" && r.URL.Path == "/api/stacks":
						if existing {
							fmt.Fprint(w, `[{"Id":9,"Name":"app","EndpointId":8},{"Id":4,"Name":"app","EndpointId":3}]`)
						} else {
							fmt.Fprint(w, `[{"Id":9,"Name":"app","EndpointId":8}]`)
						}
					case r.Method == "GET" && r.URL.Path == "/api/stacks/4/file":
						_ = json.NewEncoder(w).Encode(map[string]string{"StackFileContent": scopeFixtureCompose("services: {}")})
					default:
						mutations++
						wantMethod, wantPath := "POST", "/api/stacks/create/standalone/string"
						if existing {
							wantMethod, wantPath = "PUT", "/api/stacks/4"
						}
						if r.Method != wantMethod || r.URL.Path != wantPath || r.URL.Query().Get("endpointId") != "3" {
							t.Errorf("wrong mutation: %s %s", r.Method, r.URL)
						}
						if !reflect.DeepEqual(checkpoint, map[string]any{"stack": "app", "endpoint": "selected", "endpointId": int64(3), "providerScope": scopeFixtureScope("http://" + r.Host), "deploymentId": idstest.N(7)}) {
							t.Errorf("provider called before durable identity: %v", checkpoint)
						}
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						if body["stackFileContent"] != scopeFixtureCompose("services: {}\nx-original: original compose\n") {
							t.Errorf("changed compose: %v", body)
						}
						// Created and updated alike, the stack carries this
						// installation's mark: the inventory counts only those.
						if !reflect.DeepEqual(body["env"], []any{map[string]any{"name": InstanceEnv, "value": "inst-a"}}) {
							t.Errorf("stack applied without the installation's mark: %v", body["env"])
						}
						fmt.Fprint(w, `{}`)
					}
				}))
				defer server.Close()
				cfg := Target{Scope: scopeFixtureScope(server.URL), PortainerURL: server.URL, PortainerKey: "secret", EnvName: "selected", Installation: "inst-a"}
				save := func(v map[string]any) error { mu.Lock(); defer mu.Unlock(); checkpoint = v; return nil }
				var got deploy.Observation
				var err error
				if driver {
					d := &Driver{Config: func(context.Context, ids.ID) (*Target, error) { return &cfg, nil }}
					got, err = d.Reconcile(context.Background(), deploy.Placement{ID: idstest.N(7), URL: "https://app", Handle: map[string]any{"stack": "app", "owner": "kept"}}, 5, deploy.Artifact{Kind: "compose", Body: []byte(scopeFixtureCompose("services: {}\nx-original: original compose\n")), Hash: deploy.ConfigHash(scopeFixtureCompose("services: {}\nx-original: original compose\n"))}, "key", func(string, ...any) {}, save)
				} else {
					got, err = (execution{deploymentID: idstest.N(7), config: cfg, endpointID: 3, stack: "app", compose: scopeFixtureCompose("services: {}\nx-original: original compose\n"), url: "https://app", log: func(string, ...any) {}}).execute(context.Background(), 5, save)
				}
				mu.Lock()
				defer mu.Unlock()
				if err != nil || mutations != 1 || got.Generation != 5 || got.Rollout != "active" || got.Health != "unknown" || got.URL != "https://app" {
					t.Fatalf("%+v %v mutations=%d", got, err, mutations)
				}
				if driver && got.Handle.(map[string]any)["owner"] != "kept" {
					t.Fatal("lost provider handle fields")
				}
			})
		}
	}
}

func TestPortainerUnreadableInventoryNeverCreatesStack(t *testing.T) {
	t.Parallel()
	for _, body := range []string{"invalid", "null", `[{"Id":4}]`, "truncated", "oversized"} {
		t.Run(body, func(t *testing.T) {
			var mutations atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					mutations.Add(1)
					return
				}
				switch body {
				case "truncated":
					w.Header().Set("Content-Length", "100")
					fmt.Fprint(w, `[]`)
				case "oversized":
					fmt.Fprint(w, strings.Repeat(" ", 1<<20)+`[]`)
				default:
					fmt.Fprint(w, body)
				}
			}))
			defer server.Close()
			_, err := (execution{deploymentID: idstest.N(7), config: Target{Scope: scopeFixtureScope(server.URL), PortainerURL: server.URL, EnvName: "selected"}, endpointID: 3, stack: "app", compose: scopeFixtureCompose("services: {}"), log: func(string, ...any) {}}).execute(context.Background(), 1, func(map[string]any) error { return nil })
			if err == nil || deploy.IsTerminal(err) || mutations.Load() != 0 {
				t.Fatalf("unknown inventory became creation: %v mutations=%d", err, mutations.Load())
			}
		})
	}
}

func TestPortainerCheckpointFailurePreventsHTTP(t *testing.T) {
	t.Parallel()
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; t.Error("HTTP crossed failed checkpoint") }))
	defer server.Close()
	receiptErr := errors.New("receipt unavailable")
	receiptCalled := false
	_, err := (execution{deploymentID: idstest.N(7), config: Target{Scope: scopeFixtureScope(server.URL), PortainerURL: server.URL}, endpointID: 3, stack: "app", compose: scopeFixtureCompose("services: {}"), log: func(string, ...any) {}}).execute(context.Background(), 1, func(map[string]any) error { receiptCalled = true; return receiptErr })
	if !receiptCalled || err == nil || !strings.Contains(err.Error(), receiptErr.Error()) || !deploy.IsTerminal(err) || called {
		t.Fatalf("checkpoint: %v HTTP=%v", err, called)
	}
}

func TestPortainerCancellationInterruptsHTTP(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"listing", "apply"} {
		t.Run(phase, func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if phase == "apply" && r.Method == "GET" {
					fmt.Fprint(w, `[]`)
					return
				}
				io.Copy(io.Discard, r.Body)
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := (execution{deploymentID: idstest.N(7), config: Target{Scope: scopeFixtureScope(server.URL), PortainerURL: server.URL}, endpointID: 3, stack: "app", compose: scopeFixtureCompose("services: {}"), log: func(string, ...any) {}}).execute(ctx, 1, func(map[string]any) error { return nil })
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("provider was not called")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) || deploy.IsTerminal(err) {
					t.Fatalf("cancellation: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("provider ignored cancellation")
			}
		})
	}
}
