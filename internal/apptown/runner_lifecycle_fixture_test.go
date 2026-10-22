package apptown

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// admittedRunnerStep creates one real working run and lets its registered
// runner claim the step. When finish is true the same attempt records a pass.
func admittedRunnerStep(t *testing.T, a *App, slug, name, kind string, finish bool) (ids.ID, ids.ID) {
	t.Helper()
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: slug, Name: slug, Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "work", Version: 1}, Spec: schema.Spec{
		Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: name, Uses: kind}}}},
	}})
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Runners = map[string]workflow.Runner{kind: func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if finish {
				if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass}, nil); err != nil {
					t.Error(err)
				}
			}
		}}
	})
	a.Work.Stop()
	ctx := asPerson("alice")
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Title: "runner lifecycle"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "work"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snapshot.Steps {
		if step.Name == name && step.Type == kind && step.Status == workflow.StatusPending {
			if err := a.Work.ExecuteStep(ctx, step.ID); err != nil {
				t.Fatal(err)
			}
			return run.ID, step.ID
		}
	}
	t.Fatalf("pending %s step not materialized: %+v", kind, snapshot.Steps)
	return ids.Nil, ids.Nil
}

func admittedOpenRun(t *testing.T, a *App, slug string) ids.ID {
	t.Helper()
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: slug, Name: slug, Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "work", Version: 1}, Spec: schema.Spec{
		Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}},
	}})
	a.Work.Stop()
	ctx := asPerson("alice")
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Title: "open work"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "work"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	return run.ID
}
