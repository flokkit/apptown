package apptown

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

func engineProject(t *testing.T, a *App, slug string) *project.Project {
	t.Helper()
	id, err := database.Transact(context.Background(), a.DB, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{ID: nextFixtureProject(t, a.DB), Slug: slug, Name: strings.ToUpper(slug)})
	})
	if err != nil {
		t.Fatal(err)
	}
	p := a.projectByID(context.Background(), id)
	if p == nil {
		t.Fatalf("created project %d is missing", id)
	}
	return p
}

func engineChange(t *testing.T, a *App, p *project.Project) *workflow.Change {
	t.Helper()
	ctx := a.asAppTown(context.Background(), p.ID, "test")
	ch, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Type: "feature", Title: "t"})
	if err == nil {
		ch, err = a.Work.Begin(ctx, ch.ID)
	}
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

func engineLandingChange(t *testing.T, a *App, p *project.Project) *workflow.Change {
	t.Helper()
	landingYAML := `
apiVersion: apptown.dev/v1
kind: Workflow
metadata: { name: land }
spec:
  kind: lifecycle
  role: landing
  stages:
    - name: ready
      steps:
        - name: prepared
          uses: script
          with: { run: "true" }
        - name: hold
          uses: approval
          with: { means: "the source is ready to merge" }
      gate: { require: [prepared] }
    - name: landed
      steps:
        - name: record
          uses: approval
          with: { means: "the merge was recorded" }
`
	if name, version, err := a.Ways.PublishWorkflow(t.Context(), landingYAML, "fixture"); err != nil || name != "land" || version != 1 {
		t.Fatalf("publish landing workflow: %s@%d err=%v", name, version, err)
	}
	a.lifecycle.SelectLane(t.Context(), p.ID, "land", true, "fixture")
	return engineChange(t, a, p)
}

func engineLandingRun(t *testing.T, a *App, p *project.Project, ch *workflow.Change) *workflow.Run {
	t.Helper()
	// Pause automatic stage advancement so these tests can drive the merge effect.
	a.Work.Stop()
	ctx := a.asAppTown(context.Background(), p.ID, "test")
	// Started by a person who may land: these tests drive the merge itself,
	// which rests on that fact (workflow/assent.go).
	run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "land"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var prepared ids.ID
	for _, step := range snapshot.Steps {
		if step.Name == "prepared" {
			prepared = step.ID
		}
	}
	if prepared.IsZero() {
		t.Fatal("landing preparation step was not materialized")
	}
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), prepared); err != nil {
		t.Fatal(err)
	}
	return run
}

const lifecycleYAML = `
apiVersion: apptown.dev/v1
kind: Workflow
metadata: { name: flow }
spec:
  kind: lifecycle
  stages:
    - name: decide
      steps:
        - name: first-ok
          uses: approval
          with: { approvers: [bls], means: "the direction is right" }
    - name: confirm
      steps:
        - name: second-ok
          uses: approval
          with: { approvers: [bls], means: "the result is right" }
`

// Observe repository ancestry independently of the merge driver's implementation.
func gitAncestorForTest(t *testing.T, repo, from, to string) (bool, error) {
	t.Helper()
	cmd := gittest.Command("-C", repo, "merge-base", "--is-ancestor", from, to)
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}
