package forge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
)

// A step has private Docker when it declares docker: true, whatever its
// skills; skill/implementation with docker: false, or no declaration, gives
// none. The three steps share one run and run in turn.
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
	want := map[string]bool{"declared": true, "implementation-declared-false": false, "not-declared": false}
	runID, _ := admittedAgentStep(t, a, `{"prompt":"do it","docker":true}`,
		schema.Step{Name: "implementation-declared-false", Uses: "agent/pi", With: map[string]any{"prompt": "do it", "skills": []string{"skill/implementation"}, "docker": false}},
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

// A run admitted under the old rule (skill/implementation, no docker key)
// keeps its definition through the migration, and its pending implementation
// step and a retry of it are refused with the reason.
func TestAnOldRunsImplementationStepIsRefusedOnDispatchAndRetry(t *testing.T) {
	t.Parallel()
	a := agentTestApp(t)
	a.cfg.agentExecutor = ExecutorDocker
	if _, err := a.Ways.CreateBlock(t.Context(), "skill/implementation", "Implement.", "", "alice"); err != nil {
		t.Fatal(err)
	}
	runID, stepID := admittedAgentStep(t, a, `{"prompt":"do it","skills":["skill/implementation"]}`)
	before, err := workflow.ReadRunIn(context.Background(), a.DB, runID)
	if err != nil {
		t.Fatal(err)
	}
	a.SyncStock(t.Context())
	after, err := workflow.ReadRunIn(context.Background(), a.DB, runID)
	if err != nil || after.DefinitionJSON != before.DefinitionJSON {
		t.Fatalf("the run's definition changed: %v\n%s\n%s", err, before.DefinitionJSON, after.DefinitionJSON)
	}
	f := installFakePi(t, a, func(int, ExecRequest) ExecOutcome { return ExecOutcome{Output: piSays("done")} })
	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
	if status, ev := stepStatus(t, a, stepID); status != workflow.StatusError || ev["reason"] != workflow.OldDockerRuleRefusal {
		t.Fatalf("step %s: %v", status, ev)
	}
	if len(f.requests()) != 0 {
		t.Fatal("the agent was started")
	}
	if _, err := a.Work.RetryStep(asPerson("alice"), workflow.RetryCommand{StepID: stepID}); err == nil || !strings.Contains(err.Error(), workflow.OldDockerRuleRefusal) {
		t.Fatalf("retry: %v", err)
	}
}

// A project that keeps a custom pack in its next adoption after the
// migration, on an older publication of the pack, starts new runs with the
// declaration: the pack's pins moved with the migration.
func TestANewRunAfterKeepingAnOlderPackDeclaresPrivateDocker(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	lane := func(prompt string) string {
		return "apiVersion: forge.flokkit.io/v1\nkind: Workflow\nmetadata:\n  name: build\nspec:\n  kind: pipeline\n  role: working\n  stages:\n    - name: work\n      steps:\n        - name: build\n          uses: agent/pi\n          with:\n            prompt: " + prompt + "\n            skills: [skill/implementation]\n"
	}
	if _, _, err := a.Ways.PublishWorkflow(t.Context(), lane("first"), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ways.PublishPack(t.Context(), "team", "", `{"workflows":["build"]}`, ""); err != nil {
		t.Fatal(err)
	}
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "p", Name: "p", Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	adopt := func(req adoption.Request) {
		t.Helper()
		c, err := a.Adoption.Prepare(t.Context(), p.ID, req)
		if err != nil || len(c.Problems) != 0 {
			t.Fatalf("prepare: %v %+v", err, c)
		}
		if _, err := a.lifecycle.Adopt(adoption.WithSystemAuthority(context.Background(), p.ID), p, c, "alice", "keep team", adoption.OperationSelect); err != nil {
			t.Fatal(err)
		}
	}
	members, err := adoption.ProjectPacksIn(t.Context(), a.DB, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	picks := []adoption.PackPick{{Pack: "team", Version: 1}}
	for _, m := range members {
		picks = append(picks, adoption.PackPick{Pack: m.Pack, Version: m.Version})
	}
	adopt(adoption.Request{Packs: picks})
	// team moves on to v2; the project stays on v1.
	if _, _, err := a.Ways.PublishWorkflow(t.Context(), lane("second"), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ways.CutPacksOnPinChange(t.Context()); err != nil {
		t.Fatal(err)
	}
	a.SyncStock(t.Context())
	// The migration added team v3 for the project's v1 without moving the
	// latest; the packs' sync then publishes the declared latest after it.
	if team, err := a.Ways.Pack(t.Context(), "team"); err != nil || team.LatestVersion != 4 {
		t.Fatalf("team: %+v %v", team, err)
	}
	adopt(adoption.Request{})

	a.Work.Stop()
	ctx := asPerson("alice")
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Title: "build"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "build"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	started, err := workflow.ReadRunIn(context.Background(), a.DB, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if definition := strings.ReplaceAll(started.DefinitionJSON, " ", ""); !strings.Contains(definition, `"docker":true`) || !strings.Contains(definition, `"prompt":"first"`) {
		t.Fatalf("the new run's definition: %s", started.DefinitionJSON)
	}
}
