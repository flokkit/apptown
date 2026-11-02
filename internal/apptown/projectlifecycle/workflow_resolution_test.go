package projectlifecycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

// Library workflows resolve without project selection. System workflows use
// the instance binding.
func TestResolveWorkflowFromLibrary(t *testing.T) {
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

	if _, err := f.adoption.RecordCurrent(t.Context(), p.ID, adoption.AdoptionEdit, "explicit test configuration", "fixture"); err != nil {
		t.Fatal(err)
	}
	rw, err := f.lifecycle.ResolveWorkflow(t.Context(), &p, "quick-change")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rw.Origin, "stock/quick-change@") || rw.Version < 1 || rw.System {
		t.Fatalf("lane receipt wrong: %+v", rw)
	}
	dbtest.FailNextStatement(t, db, "-- name: CurrentAdoption :one")
	if _, err := f.lifecycle.ResolveWorkflow(t.Context(), &p, "quick-change"); err == nil {
		t.Fatal("unavailable adoption receipt silently selected a workflow")
	}
	// A system workflow needs no selection and says it is one.
	rw, err = f.lifecycle.ResolveWorkflow(t.Context(), &p, "land")
	if err != nil || !rw.System {
		t.Fatalf("land must resolve as system without selection: %v %+v", err, rw)
	}
	// An unknown workflow cannot resolve.
	if _, err := f.lifecycle.ResolveWorkflow(t.Context(), &p, "only-here"); err == nil {
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
	rw, err := f.lifecycle.ResolveWorkflow(t.Context(), &p, "land")
	if err != nil {
		t.Fatal(err)
	}
	if rw.Name != "our-land" || rw.Origin != "lib/our-land@1" || !rw.System {
		t.Fatalf("binding not honoured: %+v", rw)
	}
}
