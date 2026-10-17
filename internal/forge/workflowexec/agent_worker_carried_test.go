package workflowexec

import (
	"context"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
)

// A step on a worker reads what earlier steps of its run wrote where they
// declared it, as it would in the run's folder: the plan step's
// .forge/PLAN.md is sent to the step that carries out the plan and put in
// its tree unless the tree has that file already. The latest step to write
// a path wins. Another step's output at the step's own path is never sent
// to it; the step's own report from its earlier attempt is, with the order
// to update it rather than replace it (a rework's report still describes
// the whole change), except on a restart, whose work goes back to the base.
func TestAStepOnAWorkerIsGivenEarlierStepsOutputs(t *testing.T) {
	f := agentOnWorkerFixture(t, nil)
	db := f.exec.db
	directory := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := directory.SeedAdministrator(context.Background(), "planner"); err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "planner", Via: "session"}, Verified: true})
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{
		{Name: "draft-plan", Uses: AgentKind, With: map[string]any{"prompt": "plan", "output": map[string]any{"kind": "plan", "file": ".forge/PLAN.md"}}},
		{Name: "redraft-plan", Uses: AgentKind, With: map[string]any{"prompt": "plan again", "output": map[string]any{"kind": "plan", "name": "plan", "file": ".forge/PLAN.md"}}},
		{Name: "first-execution", Uses: AgentKind, With: map[string]any{"prompt": "execute", "output": map[string]any{"kind": "exec-report", "file": ".forge/EXEC.md"}}},
		{Name: "execute-plan", Uses: AgentKind, With: map[string]any{"prompt": "execute", "output": map[string]any{"kind": "exec-report", "file": ".forge/EXEC.md"}}},
	}}}}
	work := workflow.New(context.Background(), db, workflow.Dependencies{
		Authority: access.Checked{Authority: directory, Projects: project.New(db, directory)},
		DataDir:   f.dir, Repositories: &scm.RepositoryLocks{},
		Resolve: func(_ context.Context, _ workflow.StartRequest, source workflow.StartSource) (workflow.PreparedRun, error) {
			return workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "plan-execute", Version: 1}, Spec: spec, AdoptionID: source.AdoptionID}, nil
		},
		Automatic: []string{AgentKind},
		Runners:   map[string]workflow.Runner{AgentKind: func(*workflow.Execution) {}},
	})
	t.Cleanup(work.Stop)
	change, err := work.Create(ctx, workflow.ChangeDraft{ProjectID: f.call.run.ProjectID, Title: "Carry out the plan"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "plan-execute"})
	if err != nil {
		t.Fatal(err)
	}
	work.Wait()
	// Two steps wrote the plan; the later one's is what the run's folder
	// holds.
	for name, body := range map[string]string{"draft-plan": "the first plan", "plan": "the approved plan"} {
		if _, err := work.AttachArtifact(ctx, run.ID, workflow.ArtifactDraft{Kind: "plan", Name: name, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"first-execution": "another step's report", "execute-plan": "an earlier attempt's report"} {
		if _, err := work.AttachArtifact(ctx, run.ID, workflow.ArtifactDraft{Kind: "exec-report", Name: name, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	steps, err := work.RunSteps(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var execute *workflow.Step
	for _, s := range steps {
		if s.Name == "execute-plan" {
			execute = s
		}
	}
	if execute == nil {
		t.Fatalf("no execute step: %+v", steps)
	}
	execute.Attempt = 1
	f.call.run.ID, f.call.step = run.ID, execute
	f.call.source.RunID, f.call.source.RootRunID = run.ID, run.ID

	req := f.build(t)
	inputs := inputsByPath(t, req.spec)
	in, ok := inputs["/forge/in/carried/.forge/PLAN.md"]
	if !ok || string(inputContent(t, in)) != "the approved plan" {
		t.Fatalf("the plan was not sent: %+v", in)
	}
	if in, ok := inputs["/forge/in/carried/.forge/EXEC.md"]; !ok || string(inputContent(t, in)) != "an earlier attempt's report" {
		t.Errorf("the step's own earlier report was not sent to it: %+v", in)
	}
	if argv := strings.Join(req.spec.Argv, " "); !strings.Contains(argv, "Your report from the earlier attempt is in .forge/EXEC.md. UPDATE it") {
		t.Errorf("the agent is not told to update its earlier report: %s", argv)
	}
	entry := req.spec.Argv[2]
	if !strings.Contains(entry, "if [ ! -e '/work/.forge/PLAN.md' ] && [ ! -L '/work/.forge/PLAN.md' ]; then\n  mkdir -p '/work/.forge'\n  cp '/forge/in/carried/.forge/PLAN.md' '/work/.forge/PLAN.md'\nfi\n") {
		t.Errorf("the entry does not put the plan in the tree:\n%s", entry)
	}

	// A restart goes back to the base: no earlier report comes with it.
	f.call.step.Config = []byte(`{"prompt":"execute","output":{"kind":"exec-report","file":".forge/EXEC.md"},"retryMode":"restart"}`)
	for at := range inputsByPath(t, f.build(t).spec) {
		if strings.Contains(at, "EXEC.md") {
			t.Errorf("a restart was sent an earlier report: %s", at)
		}
	}
}

// A change that caught up with a moved main is given that main as its base
// branch on a worker, so a rework compares its work with the main it
// contains, not the one it started from; main's later commits, not yet
// merged, are not its base.
func TestAWorkerAgentsBaseIsTheMainItsChangeContains(t *testing.T) {
	f := agentOnWorkerFixture(t, nil)
	git := func(args ...string) string {
		t.Helper()
		cmd := gittest.Command(args...)
		cmd.Dir = f.hub
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	started := f.call.commit
	git("checkout", "-q", "-b", "change-00000000-0029-7000-8000-000000000000")
	git("commit", "-q", "--allow-empty", "-m", "the change")
	git("checkout", "-q", "main")
	git("commit", "-q", "--allow-empty", "-m", "main moved")
	moved := git("rev-parse", "HEAD")
	git("checkout", "-q", "change-00000000-0029-7000-8000-000000000000")
	git("merge", "-q", "--no-edit", "main")
	caughtUp := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	git("commit", "-q", "--allow-empty", "-m", "main moved again")

	source := f.call.source
	source.ChangeBaseSHA = started
	if got := agentGit(context.Background(), source, caughtUp, f.hub); *got != (execution.AgentGit{Branch: "change-00000000-0029-7000-8000-000000000000", Base: "main", BaseCommit: moved}) {
		t.Fatalf("the base of a change that caught up: %+v (started %s, caught up with %s)", got, started[:8], moved[:8])
	}
}
