//go:build integration

package forge

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/schema"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/workflow"
)

// The list is restricted to this test's unique label; a local smoke must never
// sweep a developer's other containers. All observations and removals are real.
func TestIntegrationReaperRetainsActiveExecution(t *testing.T) {
	label := fmt.Sprintf("forge.reaper-smoke=%d", time.Now().UnixNano())
	client := dockerClientFunc(func(ctx context.Context, host string, req docker.Command) ([]byte, error) {
		if len(req.Args) > 0 && req.Args[0] == "ps" {
			req.Args = append(req.Args, "--filter", "label="+label)
		}
		return (docker.CLI{}).Run(ctx, host, req)
	})
	a, _, step := attemptFixture(t, "script", Config{DockerClient: client})
	var execution *workflow.Execution
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Runners = map[string]workflow.Runner{"script": func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Fatal(err)
			}
			execution = task
		}}
	})
	a.Work.Stop()
	if err := a.Work.ExecuteStep(a.asEngine(t.Context()), step.ID); err != nil {
		t.Fatal(err)
	}
	if execution == nil {
		t.Fatal("runner was not entered")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	daemon := docker.Scope{Client: docker.CLI{}}
	args := []string{"run", "-d", "--entrypoint", "/bin/sh", "--label", label, "--label", "forge.class=step", "--label", fmt.Sprintf("forge.owner=apptown-step-%d-smoke", step.ID), "--label", "forge.created=" + time.Now().Add(-3*time.Hour).UTC().Format(time.RFC3339), "apptown-agent", "-c", "sleep 300"}
	output, err := daemon.Command(ctx, docker.Command{Args: args})
	if err != nil {
		t.Fatalf("start smoke container: %s: %v", output, err)
	}
	id := strings.TrimSpace(string(output))
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = daemon.Command(cleanup, docker.Command{Args: []string{"rm", "-f", id}})
	})

	if reaped := a.reaper.Containers(ctx, false); len(reaped) != 0 {
		t.Fatalf("active execution reaped: %v", reaped)
	}
	running, err := daemon.Command(ctx, docker.Command{Args: []string{"inspect", "-f", "{{.State.Running}}", id}})
	if err != nil || strings.TrimSpace(string(running)) != "true" {
		t.Fatalf("active container lost: %s %v", running, err)
	}
	if err := execution.Finish(workflow.EncodeOutcome(workflow.StatusPass, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if reaped := a.reaper.Containers(ctx, false); len(reaped) != 1 {
		t.Fatalf("finished container retained: %v", reaped)
	}
	output, err = daemon.Command(ctx, docker.Command{Args: []string{"inspect", id}})
	if err == nil || !strings.Contains(strings.ToLower(string(output)), "no such object") {
		t.Fatalf("finished container still present or inspection failed unexpectedly: %s %v", output, err)
	}
}

// Exact resource names are remapped into a unique test namespace before reaching
// the real daemon. The move must remove only the selected run's disposable data.
func TestIntegrationCompletedRunResourceCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	prefix := fmt.Sprintf("forge-cleanup-smoke-%d", time.Now().UnixNano())
	names := []string{prefix + "-selected", prefix + "-other"}
	var actual string
	client := dockerClientFunc(func(_ context.Context, host string, req docker.Command) ([]byte, error) {
		switch strings.Join(req.Args, " ") {
		case "rm -f -v " + actual:
			// A preview is disposable: it goes with its anonymous volumes
			// (docker.Scope.RemoveDisposable).
			req.Args = []string{"rm", "-f", "-v", names[0]}
		case "volume rm " + docker.Current.DataVolume(actual):
			req.Args = []string{"volume", "rm", names[0] + "-data"}
		default:
			return nil, fmt.Errorf("unexpected unscoped command: %v", req.Args)
		}
		return (docker.CLI{}).Run(ctx, host, req)
	})
	a := testApp(t, Config{DockerClient: client})
	_, caller, change := changeScenarioIn(t, a, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "verify", Steps: []schema.Step{{Name: "work", Uses: "script"}}}}})
	run, err := a.Work.StartRun(caller, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "test"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	actual = docker.PreviewRunName(run.ID)
	daemon := docker.Scope{Client: docker.CLI{}}
	for _, name := range names {
		volume := name + "-data"
		if out, err := daemon.Command(ctx, docker.Command{Args: []string{"volume", "create", volume}}); err != nil {
			t.Fatalf("create volume: %s %v", out, err)
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			_, _ = daemon.Command(cleanup, docker.Command{Args: []string{"rm", "-f", name}})
			_, _ = daemon.Command(cleanup, docker.Command{Args: []string{"volume", "rm", volume}})
		})
		if out, err := daemon.Command(ctx, docker.Command{Args: []string{"run", "-d", "--name", name, "--entrypoint", "/bin/sh", "-v", volume + ":/data", "apptown-agent", "-c", "sleep 300"}}); err != nil {
			t.Fatalf("start resource: %s %v", out, err)
		}
	}

	workResourcesFor(a).Run(caller, run.ID)
	for _, args := range [][]string{{"inspect", names[0]}, {"volume", "inspect", names[0] + "-data"}} {
		out, err := daemon.Command(ctx, docker.Command{Args: args})
		if err == nil || !strings.Contains(strings.ToLower(string(out)), "no such") {
			t.Fatalf("selected resource survived or lookup failed unexpectedly: %s %v", out, err)
		}
	}
	for _, args := range [][]string{{"inspect", names[1]}, {"volume", "inspect", names[1] + "-data"}} {
		if out, err := daemon.Command(ctx, docker.Command{Args: args}); err != nil {
			t.Fatalf("unrelated resource lost: %s %v", out, err)
		}
	}
}
