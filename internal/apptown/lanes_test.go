package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// seedLibraryWorkflow puts a workflow in the library at version 1 and
// selects it for the project — what a test used to do with a row in the
// retired per-project table.
func seedLibraryWorkflow(t *testing.T, a *App, projectID ids.ID, name, yaml string) {
	t.Helper()
	got, version, err := a.Ways.PublishWorkflow(t.Context(), yaml, "fixture")
	if err != nil || got != name || version != 1 {
		t.Fatalf("publish fixture workflow: %s@%d, want %s@1: %v", got, version, name, err)
	}
	if err := a.Adoption.SelectLane(t.Context(), projectID, name, true, "fixture"); err != nil {
		t.Fatal(err)
	}
	p := a.projectByID(context.Background(), projectID)
	if p == nil {
		t.Fatalf("fixture project %d is missing", projectID)
	}
	if _, err := a.recordAdoption(p, "edit", "fixture selects "+name, "fixture"); err != nil {
		t.Fatal(err)
	}
}

func TestRunRecordsWorkflowOrigin(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	a := newApp(t, db)
	a.Ways.SyncStockWorkflows(t.Context())
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{ID: idstest.N(1), Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := a.Projects.BySlug(context.Background(), "p")
	if err := a.Adoption.SelectLane(t.Context(), pid, "research", true, "fixture"); err != nil {
		t.Fatal(err)
	}
	recordFixtureAdoptions(t, a)
	_, run, err := a.changeActions.CreateAndStart(a.asAppTown(context.Background(), p.ID, "test"), &p, "feature", "look into it", "what is x", "research")
	if err != nil {
		t.Fatal(err)
	}
	var origin string
	// sql-guarantee: workflow_origin is recorded on the run but no owner read exposes it; the run must record what ran
	_ = db.QueryRow(`SELECT workflow_origin FROM runs WHERE id=?`, run.ID).Scan(&origin)
	if !strings.HasPrefix(origin, "stock/research@") {
		t.Fatalf("run must record what ran: %q", origin)
	}
}

// Scan points (design/decisions/workflows-as-lanes.md): `uses: scan` with `on:`
// expands at stage entry to one step per attached scanner with that
// target; a point with nothing attached becomes one passing step that
// says so. Attaching a scanner is how a landing gains a scan.
func TestScanPointsExpandToAttachedScanners(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	a := newApp(t, db)
	testAdministrator(t, a, "alice")
	_, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{ID: idstest.N(1), Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	// One image-target scanner attached, no source-target scanner.
	if _, err := a.Integrations.Bindings.Upsert(t.Context(), "trivy", verification.ScannerCapability, "tool", mustJSON(map[string]any{"image": "x", "command": []string{"y"}, "target": "image", "appliesTo": []string{"repository"}}), true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Integrations.Bindings.Upsert(t.Context(), "manual-image", verification.ScannerCapability, "tool", mustJSON(map[string]any{"image": "x", "command": []string{"y"}, "target": "image", "manual": true}), true); err != nil {
		t.Fatal(err)
	}
	spec := `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: land-ish, system: true}
spec:
  kind: pipeline
  role: landing
  stages:
    - name: gates
      steps:
        - {name: scan-source, uses: scan, with: {on: source, failOn: high}}
        - {name: scan-image, uses: scan, with: {on: image}}
`
	doc, err := schema.ParseWorkflowStored([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	recordFixtureAdoptions(t, a)
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land-ish", Version: 1}, Spec: doc.Spec})
	a.Work.Stop()
	ctx := asPerson("alice")
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: idstest.N(1), Title: "Scan the change"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "land-ish"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	runID := run.ID
	got := map[string]string{}
	snapshot, err := a.Work.Snapshot(ctx, runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snapshot.Steps {
		got[step.Name] = string(step.Config)
	}
	if c, ok := got["scan-source"]; !ok || !sameJSON(c, `{"on":"source","none":true}`) {
		t.Fatalf("a source point with nothing attached must become one passing step: %v", got)
	}
	if c, ok := got["scan-image:trivy"]; !ok || !strings.Contains(canonJSON(c), `"scanner":"trivy"`) {
		t.Fatalf("an image point must expand to the attached image scanner: %v", got)
	}
	if _, ok := got["scan-image"]; ok {
		t.Fatal("the point itself must not remain as a step once expanded")
	}
	if _, ok := got["scan-image:manual-image"]; ok {
		t.Fatal("a manual scanner must not be scheduled by a scan point")
	}
}

// A person's rejection starts the rework (Letterbox change 157): the
// stage's agent step is re-dispatched in continue mode with the words,
// and the approval reopens with the rejection kept on record.

// Phase two retired the per-project copy store: selecting writes no copy,
// and a project workflow's history is the library's.
func TestProjectWorkflowStoreIsRetired(t *testing.T) {
	t.Parallel()
	a := testApp(t)

	a.Ways.SyncStockWorkflows(t.Context())
	_, err := database.Transact(context.Background(), a.DB, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{ID: idstest.N(1), Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.lifecycle.SelectLane(t.Context(), idstest.N(1), "quick-change", true, "fixture"); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: the retired tables' absence is a schema fact; only the catalogue can say a table does not exist
	completionCount(t, a.DB, 0, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('workflows','workflow_versions')`)
	if !laneOn(t, a.Adoption, idstest.N(1), "quick-change") {
		t.Fatal("selecting the workflow must enable its lane")
	}

	h := a.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/projects/p/workflows", strings.NewReader(`{"yaml":"x"}`)))
	if rec.Code != 404 {
		t.Fatalf("the removed per-project editor must refuse POST, got %d", rec.Code)
	}
}

func sameJSON(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return a == b
	}
	return reflect.DeepEqual(x, y)
}
