package portainer

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCurrentStackProofGuardsActualUpdateAndDelete(t *testing.T) {
	for _, action := range []string{"update", "delete"} {
		for _, proof := range []string{"empty", "conflicting", "legacy"} {
			t.Run(action+"/"+proof, func(t *testing.T) {
				id := idstest.N(7)
				var effects atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "GET" {
						effects.Add(1)
						fmt.Fprint(w, `{}`)
						return
					}
					switch r.URL.Path {
					case "/api/stacks":
						fmt.Fprint(w, `[{"Id":4,"Name":"app","EndpointId":3}]`)
					case "/api/stacks/4/file":
						if proof == "empty" {
							fmt.Fprint(w, `{}`)
							return
						}
						content := "services: {}"
						if proof == "conflicting" {
							content = "x-apptown-deployment: " + idstest.N(8).String() + "\n" + content
						}
						_ = json.NewEncoder(w).Encode(map[string]string{"StackFileContent": content})
					default:
						t.Errorf("unexpected read %s", r.URL.Path)
						w.WriteHeader(404)
					}
				}))
				defer server.Close()
				cfg := Target{PortainerURL: server.URL, Scope: scopeFixtureScope(server.URL)}
				legacy := []StackAssociation{{DeploymentID: id, Scope: *cfg.Scope, StackID: 4, StackName: "app"}}
				var err error
				if action == "update" {
					_, err = DeployStack(t.Context(), &cfg, 3, "app", scopeFixtureCompose("services: {}"), id, legacy)
				} else {
					driver := Driver{Config: func(context.Context, ids.ID) (*Target, error) { return &cfg, nil }}
					_, err = driver.Teardown(t.Context(), deploy.Placement{ID: id, Generation: 1, Handle: map[string]any{"portainerAssociations": legacy}}, 1, deploy.TakeDown{Owned: []deploy.Owned{{Kind: "stack", ID: "4", Name: "app"}}})
				}
				if proof == "legacy" {
					if err != nil || effects.Load() != 1 {
						t.Fatalf("valid legacy control: effects=%d err=%v", effects.Load(), err)
					}
				} else if err == nil || effects.Load() != 0 {
					t.Fatalf("invalid current proof permitted %s: effects=%d err=%v", action, effects.Load(), err)
				}
			})
		}
	}
}
