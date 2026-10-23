package workflowexec

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
)

// An agent step's private Docker on AppTown's own machine is logged in to
// no registry, as on a worker, though the project has one attached: the
// step's Docker client is given no credential, the workshop adds no
// registry to the step's environment, and the agent is not told to push.
// Release images are built and published by the landing.
//
// Kills: a registry credential staged for the step's Docker client; the
// push instructions in its steering.
func TestALocalAgentsPrivateDockerIsLoggedInToNoRegistry(t *testing.T) {
	t.Parallel()
	a, runID, stepID := admittedAgentStep(t, `{"prompt":"build it","docker":true,"executor":"docker"}`)
	snapshot, err := a.work.Snapshot(a.ctx, runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	ex := a.agentExecutor(t)
	if _, err := ex.integrations.Bindings.Upsert(context.Background(), "registry", integration.CapContainerRegistry, "service", `{"url":"registry.example"}`, false); err != nil {
		t.Fatal(err)
	}
	const token = "registry-token-canary"
	if err := ex.integrations.Attachments.Attach(context.Background(), snapshot.Run.ProjectID, integration.CapContainerRegistry, "registry", `{"repo":"registry.example/team/agents","username":"robot","token":"`+token+`"}`, integration.Entered, "test", nil); err != nil {
		t.Fatal(err)
	}
	if ri := ex.integrations.RegistryFor(context.Background(), snapshot.Run.ProjectID); ri == nil || ri.Token != token {
		t.Fatalf("the project's registry: %+v", ri)
	}
	var steering string
	var env []string
	installFakePi(a, func(n int, req execution.Request) execution.Outcome {
		body, _ := os.ReadFile(filepath.Join(req.Workspace, ".apptown", "steering.md"))
		steering, env = string(body), req.Env
		if _, err := os.Stat(filepath.Join(req.Workspace, ".apptown", "pi-home", ".docker", "config.json")); !os.IsNotExist(err) {
			t.Errorf("a Docker client config was staged for the step: %v", err)
		}
		_ = filepath.WalkDir(req.Workspace, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if b, _ := os.ReadFile(path); strings.Contains(string(b), token) {
					t.Errorf("%s carries the registry's token", path)
				}
			}
			return nil
		})
		if err := os.WriteFile(filepath.Join(req.Workspace, "work.txt"), []byte("done\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return execution.Outcome{Output: piSays("done")}
	})
	if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
		t.Fatal(err)
	}
	if status, evidence := stepStatus(t, a, stepID); status != workflow.StatusPass {
		t.Fatalf("the step is %s: %v", status, evidence)
	}
	if !slices.Contains(env, "DOCKER_HOST=tcp://dind:2375") {
		t.Fatalf("the step was not given its private Docker: %v", env)
	}
	if strings.Contains(steering, "docker push") || strings.Contains(steering, "already\nlogged in") {
		t.Errorf("the agent is told to push:\n%s", steering)
	}
	if !strings.Contains(steering, "logged in to none") {
		t.Errorf("the agent is not told its Docker is logged in nowhere:\n%s", steering)
	}
}
