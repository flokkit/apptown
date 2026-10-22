package docker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestDockerRecoveryRequiresMatchingBootAndRuntimeProof(t *testing.T) {
	t.Parallel()
	id := deploy.DockerIdentity{Deployment: "4", Generation: 7, Artifact: "selected-artifact"}
	for _, scenario := range []string{"match", "wrong-generation", "wrong-environment", "wrong-artifact", "wrong-image", "not-running", "no-boot-proof", "cancelled", "gone"} {
		t.Run(scenario, func(t *testing.T) {
			runtime := Runtime{Image: "sha256:selected", Running: true, Labels: id.Labels()}
			noted := id.Checkpoint("selected-container")
			noted["dockerReady"] = deploy.Observation{Generation: 7, Rollout: "active", Health: "healthy", ImageDigest: "sha256:selected", URL: "https://selected", Handle: map[string]any{"container": "selected-container", "hostPort": 1234}}
			switch scenario {
			case "wrong-generation":
				runtime.Labels["apptown.generation"] = "8"
			case "wrong-environment":
				runtime.Labels["apptown.deployment"] = "5"
			case "wrong-artifact":
				runtime.Labels["apptown.artifact"] = "different"
			case "wrong-image":
				runtime.Image = "sha256:different"
			case "not-running":
				runtime.Running = false
			case "no-boot-proof":
				delete(noted, "dockerReady")
			}
			calls := 0
			dk := Scope{Client: &fakeClient{reply: func(req Command) ([]byte, error) {
				calls++
				if req.Args[len(req.Args)-1] != "selected-container" {
					t.Fatal("inspected another container")
				}
				if scenario == "gone" {
					return []byte("Error response from daemon: No such object: selected-container"), errors.New("exit status 1")
				}
				return json.Marshal(runtime)
			}}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancelled" {
				cancel()
			}
			o, err := Resume(ctx, dk, deploy.Placement{HostPrefix: "apptown-", ID: idstest.N(4), Label: "4"}, 7, noted)
			switch {
			case scenario == "match":
				if err != nil || o.Health != "unknown" || o.ImageDigest != "sha256:selected" {
					t.Fatalf("valid recovery %+v %v", o, err)
				}
			case scenario == "gone":
				// The daemon says the booted container is gone: a known failure.
				if !deploy.IsTerminal(err) {
					t.Fatalf("a gone container is not a known failure: %v", err)
				}
			case err == nil || deploy.IsTerminal(err):
				t.Fatalf("unknown outcome incorrectly accepted/rejected: %+v %v", o, err)
			}
			if scenario == "no-boot-proof" && calls != 0 {
				t.Fatal("missing boot proof must not inspect a guessed container")
			}
		})
	}
}
