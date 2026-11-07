package docker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Recovery checks the admitted identity even if readiness was not recorded.
func TestDockerRecoveryReobservesTheAdmittedVersionWithoutReadyProof(t *testing.T) {
	t.Parallel()
	id := deploy.DockerIdentity{Deployment: "4", Generation: 7, Artifact: "selected-artifact"}
	for _, scenario := range []string{"match", "wrong-generation", "wrong-environment", "wrong-artifact", "wrong-image", "not-running", "no-boot-proof", "no-boot-proof-stopped", "cancelled", "gone"} {
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
			case "no-boot-proof-stopped":
				runtime.Running = false
				delete(noted, "dockerReady")
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
			o, err := resume(ctx, dk, deploy.Placement{ID: idstest.N(4), Label: "4"}, 7, noted)
			switch {
			case scenario == "match" || scenario == "no-boot-proof":
				if err != nil || o.Health != "unknown" || o.ImageDigest != "sha256:selected" {
					t.Fatalf("valid recovery %+v %v", o, err)
				}
			case scenario == "gone" || scenario == "no-boot-proof-stopped":
				if !errors.Is(err, deploy.ErrNotApplied) {
					t.Fatalf("missing version cannot retry: %v", err)
				}
			case scenario == "cancelled":
				// Nothing was asked: still unknown.
				if err == nil || deploy.IsTerminal(err) {
					t.Fatalf("a cancelled look settled the attempt: %+v %v", o, err)
				}
			default:
				// The host confirmed that the selected runtime failed or differs.
				if !deploy.IsTerminal(err) {
					t.Fatalf("%s is not a known failure: %+v %v", scenario, o, err)
				}
			}
			if scenario == "no-boot-proof" && calls != 1 {
				t.Fatal("missing boot proof must still inspect the admitted container")
			}
		})
	}
}

// Without a saved pre-apply identity, absence has not been established.
func TestDockerMissingApplyIdentityIsUncertain(t *testing.T) {
	t.Parallel()
	_, err := resume(t.Context(), Scope{}, deploy.Placement{}, 1, nil)
	if err == nil || errors.Is(err, deploy.ErrNotApplied) || deploy.IsTerminal(err) {
		t.Fatalf("missing identity permitted retry or settlement: %v", err)
	}
}
