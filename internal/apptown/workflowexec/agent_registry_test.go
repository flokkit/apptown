package workflowexec

import (
	"context"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/deploy/docker"
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
// no registry, as on a worker, though the instance has a hub: the
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
	if _, err := ex.integrations.Bindings.Upsert(context.Background(), "registry", integration.CapContainerRegistry, "service", `{"url":"registry.example","group":"team","username":"robot","tokenFrom":"HUB_TOKEN"}`, false); err != nil {
		t.Fatal(err)
	}
	const token = "registry-token-canary"
	if err := ex.secrets.SetInstance(t.Context(), "HUB_TOKEN", token, "fixture"); err != nil {
		t.Fatal(err)
	}

	if ri, err := ex.integrations.Registry(context.Background(), snapshot.Run.ProjectID); err != nil || ri == nil || ri.Token != token {
		t.Fatalf("the project's registry: %+v %v", ri, err)
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

func TestPrivateDockerFailureStopsTheAgentStep(t *testing.T) {
	t.Parallel()
	a, _, stepID := admittedAgentStep(t, `{"prompt":"build it","docker":true,"executor":"docker"}`)
	daemon := newDockerFake()
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "run" && slices.Contains(args, "--privileged") {
			return []byte("private daemon refused"), errors.New("refused"), true
		}
		return nil, nil, false
	})
	ex := a.agents
	ex.workshop = agentenv.NewWorkshop(&docker.Scope{Client: daemon}, ex.integrations, ex.progress, a.dir, a.image, "private-daemon", nil)
	calls := 0
	installFakePi(a, func(_ int, _ execution.Request) execution.Outcome {
		calls++
		return execution.Outcome{Output: piSays("done")}
	})
	if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
		t.Fatal(err)
	}
	status, evidence := stepStatus(t, a, stepID)
	if status != workflow.StatusError || !strings.Contains(fmt.Sprint(evidence), "starting the step's private Docker") || calls != 0 {
		t.Fatalf("status=%s evidence=%v agent calls=%d", status, evidence, calls)
	}
}
