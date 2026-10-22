package apptown

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// Keep the Work constructed by New: rebuilding it after startup would hide
// method values that captured an unconstructed runner owner.
func TestStartupBindsConstructedVerificationRunners(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "startup", Name: "Startup", Actor: "apptown-system"})
	if err != nil {
		t.Fatal(err)
	}
	seedLibraryWorkflow(t, a, p.ID, "verify-startup", `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: verify-startup}
spec:
  kind: pipeline
  role: working
  stages:
    - name: verify
      steps:
        - name: scan
          uses: scan
          with: {on: source}
        - name: guidance
          uses: guidance/conformance
`)
	ctx := a.asEngine(t.Context())
	ch, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Title: "Verify startup"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Stop()
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "verify-startup"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snap, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil || len(snap.Steps) != 2 {
		t.Fatalf("admission: %+v %v", snap, err)
	}
	for _, step := range snap.Steps {
		a.Work.ExecuteStep(ctx, step.ID)
	}
	snap, err = a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snap.Steps {
		if step.Status != workflow.StatusPass || !strings.Contains(string(step.Evidence), "note") {
			t.Errorf("startup runner %s did not record its result: %+v", step.Type, step)
		}
	}
}
