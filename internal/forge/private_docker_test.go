package forge

import (
	"context"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/forge/wow"
)

// A step has private Docker when it declares docker: true, whatever its
// skills; skill/implementation with docker: false, or with no declaration,
// gives none, and the step runs. The steps share one run and run in turn.
func TestAStepHasPrivateDockerOnlyWhenItDeclaresIt(t *testing.T) {
	t.Parallel()
	a := agentTestApp(t)
	a.cfg.agentExecutor = ExecutorDocker
	if _, err := a.Ways.CreateBlock(t.Context(), "skill/implementation", "Implement.", "", "alice"); err != nil {
		t.Fatal(err)
	}
	// The declared step's private daemon is removed when it ends, so the
	// next step may write the folder.
	daemon := newSurvivingContainers()
	daemon.allowRemoval()
	a.docker = docker.Scope{Client: daemon}
	want := map[string]bool{"declared": true, "implementation-declared-false": false, "implementation-not-declared": false, "not-declared": false}
	runID, _ := admittedAgentStep(t, a, `{"prompt":"do it","docker":true}`,
		schema.Step{Name: "implementation-declared-false", Uses: "agent/pi", With: map[string]any{"prompt": "do it", "skills": []string{"skill/implementation"}, "docker": false}},
		schema.Step{Name: "implementation-not-declared", Uses: "agent/pi", With: map[string]any{"prompt": "do it", "skills": []string{"skill/implementation"}}},
		schema.Step{Name: "not-declared", Uses: "agent/pi", With: map[string]any{"prompt": "do it"}})
	snapshot, err := a.Work.Snapshot(asPerson("alice"), runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snapshot.Steps {
		name := step.Name
		if name == "build" {
			name = "declared"
		}
		called, got := false, false
		installFakePi(t, a, func(_ int, req ExecRequest) ExecOutcome {
			called, got = true, envValue(req, "DOCKER_HOST") != ""
			return ExecOutcome{Output: piSays("done")}
		})
		a.Work.ExecuteStep(a.asEngine(context.Background()), step.ID)
		if !called || got != want[name] {
			t.Errorf("%s: agent ran %v with private Docker %v, want %v", name, called, got, want[name])
		}
	}
	if len(snapshot.Steps) != len(want) {
		t.Fatalf("steps: %+v", snapshot.Steps)
	}
}

// -no-private-docker refuses a step that declares private Docker, with the
// reason, before anything is started.
func TestNoPrivateDockerRefusesADeclaringStep(t *testing.T) {
	t.Parallel()
	a := agentTestApp(t, Config{NoPrivateDocker: true})
	a.cfg.agentExecutor = ExecutorDocker
	daemon := newSurvivingContainers()
	a.docker = docker.Scope{Client: daemon}
	_, stepID := admittedAgentStep(t, a, `{"prompt":"do it","docker":true}`)
	f := installFakePi(t, a, func(int, ExecRequest) ExecOutcome { return ExecOutcome{Output: piSays("done")} })
	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
	status, ev := stepStatus(t, a, stepID)
	if reason, _ := ev["reason"].(string); status != workflow.StatusError || !strings.Contains(reason, "-no-private-docker") {
		t.Fatalf("step %s: %v", status, ev)
	}
	if len(f.requests()) != 0 || len(daemon.live) != 0 {
		t.Fatalf("started %d agent(s) and %d container(s)", len(f.requests()), len(daemon.live))
	}
}

// A workflow that uses skill/implementation and says nothing of docker is
// kept as written when AppTown starts: no version declaring private Docker
// is added for it.
func TestStartingAppTownLeavesAWorkflowsDockerDeclarationAsWritten(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	src := "apiVersion: apptown.dev/v1\nkind: Workflow\nmetadata:\n  name: build\nspec:\n  kind: pipeline\n  role: working\n  stages:\n    - name: work\n      steps:\n        - name: build\n          uses: agent/pi\n          with:\n            prompt: build it\n            skills: [skill/implementation]\n"
	if _, _, err := a.Ways.PublishWorkflow(t.Context(), src, "alice"); err != nil {
		t.Fatal(err)
	}
	a.SyncStock(t.Context())
	w, err := a.Ways.Workflow(t.Context(), wow.WorkflowRef{Name: "build"})
	if err != nil {
		t.Fatal(err)
	}
	if w.Version != 1 || w.YAML != src {
		t.Fatalf("after a start the workflow is v%d:\n%s", w.Version, w.YAML)
	}
}
