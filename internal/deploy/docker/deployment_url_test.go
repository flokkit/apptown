package docker

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// One boot gives its selected address to every app consumer, but not needs.
// Prepared previews preserve it across serialization even if origins differ.
func TestBootGivesEveryAppConsumerItsDeploymentURL(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"standing", "preview", "unknown"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			c, err := appcontract.Parse([]byte(preparedContract))
			if err != nil {
				t.Fatal(err)
			}
			ws := t.TempDir()
			if err := os.WriteFile(ws+"/Dockerfile", []byte("FROM scratch\n"), 0600); err != nil {
				t.Fatal(err)
			}
			deps := Dependencies{Secrets: liveSecrets{}, Needs: liveNeeds{}, Routes: liveRoutes{}, Probe: upProbe{}, DFImage: testDF, StaticServer: testStaticServer}
			want := "https://selected.example"
			req := BootRequest{Allowed: AuthorisedWhenRequested, Name: "url-app", StackID: "url-stack", Workspace: ws, Scratch: t.TempDir(), Probe: time.Second}
			if target == "standing" {
				req.URL = want
			} else {
				if target == "unknown" {
					want = ""
				}
				prepared, err := PrepareBoot(t.Context(), deps, nil, "app", "preview", ws, c)
				if err != nil {
					t.Fatal(err)
				}
				prepared.URL = want
				raw, err := json.Marshal(prepared)
				if err != nil {
					t.Fatal(err)
				}
				var sent Prepared
				if err := json.Unmarshal(raw, &sent); err != nil {
					t.Fatal(err)
				}
				req.URL = sent.URL
				deps = sent.Dependencies(upProbe{}, testImages())
			}
			client := &recordingClient{}
			req.Docker = Scope{Client: client}
			if _, err := NewHost(deps).Boot(t.Context(), req, c); err != nil {
				t.Fatal(err)
			}
			consumers := map[string]bool{"web": false, "worker": false, "init-migrate": false}
			for _, call := range client.calls {
				for consumer := range consumers {
					if !strings.Contains(call, "-e SECRET_FOR=value-for-"+consumer+" ") {
						continue
					}
					consumers[consumer] = true
					if want == "" {
						if strings.Contains(call, "APPTOWN_URL=") {
							t.Fatalf("unknown address injected: %s", call)
						}
					} else if !strings.Contains(call, "-e APPTOWN_URL="+want+" ") {
						t.Fatalf("%s missing selected URL: %s", consumer, call)
					}
				}
				if strings.Contains(call, "--network-alias postgres ") && strings.Contains(call, "APPTOWN_URL=") {
					t.Fatalf("need received app address: %s", call)
				}
			}
			for consumer, seen := range consumers {
				if !seen {
					t.Errorf("consumer %s never started", consumer)
				}
			}
		})
	}
}
