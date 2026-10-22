package projectlifecycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// The project selects an adopted library workflow; system workflows resolve
// through the instance binding without requiring a selected lane.
func TestResolveWorkflowFromLibrary(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, false)
	db := f.db
	f.ways.SyncStockWorkflows(t.Context())
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := f.lifecycle.projects.BySlug(context.Background(), "p")

	if _, err := f.adoption.RecordCurrent(t.Context(), p.ID, adoption.AdoptionEdit, "explicit test configuration", "fixture"); err != nil {
		t.Fatal(err)
	}
	// A lane the project has not selected is refused, in plain words.
	if _, err := f.lifecycle.SelectedWorkflow(t.Context(), &p, "quick-change"); err == nil || !strings.Contains(err.Error(), `"quick-change" is not one of this project's ways of working`) {
		t.Fatalf("unselected lane must be refused: %v", err)
	}
	// Selected → the library's latest, with a stock origin receipt.
	if err := f.adoption.SelectLane(t.Context(), pid, "quick-change", true, "fixture"); err != nil {
		t.Fatal(err)
	}
	f.lifecycle.NoteAdoption(t.Context(), p.ID, "fixture selects quick-change", "fixture")
	rw, err := f.lifecycle.SelectedWorkflow(t.Context(), &p, "quick-change")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rw.Origin, "stock/quick-change@") || rw.Version < 1 || rw.System {
		t.Fatalf("lane receipt wrong: %+v", rw)
	}
	// A system workflow needs no selection and says it is one.
	rw, err = f.lifecycle.SelectedWorkflow(t.Context(), &p, "land")
	if err != nil || !rw.System {
		t.Fatalf("land must resolve as system without selection: %v %+v", err, rw)
	}
	// An unselected or explicitly disabled lane stays unavailable.
	if laneOn(t, f.adoption, pid, "fix-bug") {
		t.Fatal("an unselected lane must not count as selected")
	}
	if err := f.adoption.SelectLane(t.Context(), pid, "fix-bug", false, "fixture"); err != nil {
		t.Fatal(err)
	}
	if laneOn(t, f.adoption, pid, "fix-bug") {
		t.Fatal("an explicit disabled lane must win")
	}
	// An unknown workflow cannot resolve.
	if _, err := f.lifecycle.SelectedWorkflow(t.Context(), &p, "only-here"); err == nil || !strings.Contains(err.Error(), `"only-here" is not one of this project's ways of working`) {
		t.Fatalf("an unknown workflow must not resolve: %v", err)
	}
}

func TestSystemWorkflowBinding(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, false)
	db := f.db
	f.ways.SyncStockWorkflows(t.Context())
	_, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := f.lifecycle.projects.BySlug(context.Background(), "p")
	// An admin's own landing, in the library, marked system.
	custom := `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: our-land, description: land plus compliance, system: true}
spec:
  kind: pipeline
  role: landing
  stages:
    - name: gates
      steps:
        - {name: compliance, uses: approval, with: {approvers: [x], means: "the landing meets compliance"}}
`
	if name, version, err := f.ways.PublishWorkflow(t.Context(), custom, "fixture"); err != nil || name != "our-land" || version != 1 {
		t.Fatalf("publish system workflow: %s@%d %v", name, version, err)
	}
	if err := f.ways.BindSystemWorkflow(t.Context(), "land", "our-land", "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.adoption.RecordCurrent(t.Context(), p.ID, adoption.AdoptionEdit, "explicit test configuration", "fixture"); err != nil {
		t.Fatal(err)
	}
	rw, err := f.lifecycle.SelectedWorkflow(t.Context(), &p, "land")
	if err != nil {
		t.Fatal(err)
	}
	if rw.Name != "our-land" || rw.Origin != "lib/our-land@1" || !rw.System {
		t.Fatalf("binding not honoured: %+v", rw)
	}
}

func laneOn(t *testing.T, s *adoption.Service, projectID ids.ID, name string) bool {
	t.Helper()
	on, err := s.LaneSelected(t.Context(), projectID, name)
	if err != nil {
		t.Fatalf("reading lane %s: %v", name, err)
	}
	return on
}
