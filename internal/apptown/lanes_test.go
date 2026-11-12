package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// seedLibraryWorkflow publishes a workflow and records its procedure version.
func seedLibraryWorkflow(t *testing.T, a *App, projectID ids.ID, name, yaml string) {
	t.Helper()
	got, version, err := a.Ways.PublishWorkflow(t.Context(), yaml, "fixture")
	if err != nil || got != name || version != 1 {
		t.Fatalf("publish fixture workflow: %s@%d, want %s@1: %v", got, version, name, err)
	}
	if err := recordCatalogFixture(a.Adoption, t.Context(), projectID, "fixture"); err != nil {
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

func TestProjectCanStartExtraWorkflowWithoutSelection(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	a := newApp(t, db)
	a.Ways.SyncStockWorkflows(t.Context())
	_, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{ID: idstest.N(1), Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := a.Projects.BySlug(context.Background(), "p")
	recordFixtureAdoptions(t, a)
	_, run, err := a.changeActions.CreateAndStart(a.asAppTown(context.Background(), p.ID, "test"), &p, "feature", "look into it", "what is x", "research")
	if err != nil {
		t.Fatal(err)
	}
	var origin string
	// sql-guarantee: workflow_origin is recorded on the run but no owner read exposes it; the run must record what ran
	_ = db.SQL().QueryRowContext(context.Background(), `SELECT workflow_origin FROM runs WHERE id=$1`, run.ID).Scan(&origin)
	if !strings.HasPrefix(origin, "stock/research@") {
		t.Fatalf("run must record what ran: %q", origin)
	}
	// A project's existing receipt cannot restrict a newly published extra workflow.
	_, source, err := a.Ways.WorkflowVersion(t.Context(), "research", 0)
	if err != nil {
		t.Fatal(err)
	}
	source = strings.Replace(source, "name: research", "name: new-research", 1)
	if _, _, err := a.Ways.PublishWorkflow(t.Context(), source, "fixture"); err != nil {
		t.Fatal(err)
	}
	_, fresh, err := a.changeActions.CreateAndStart(a.asAppTown(t.Context(), p.ID, "test"), &p, "feature", "Another question", "Find out", "new-research")
	if err != nil || fresh == nil || fresh.WorkflowVersion != 1 {
		t.Fatalf("new library workflow requires no selection or adoption edit: %+v %v", fresh, err)
	}
	if _, _, err := a.changeActions.CreateAndStart(a.asAppTown(t.Context(), p.ID, "test"), &p, "feature", "Unknown workflow", "Find out", "missing-workflow"); err == nil {
		t.Fatal("workflow outside the library and packs was accepted")
	}
}

func TestProjectCanRunPackWorkflow(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	a.Ways.SyncStockWorkflows(t.Context())
	_, err := database.Transact(t.Context(), a.DB, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "pack-project", Name: "Pack project"})
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Projects.BySlug(t.Context(), "pack-project")
	if err != nil {
		t.Fatal(err)
	}
	recordFixtureAdoptions(t, a)
	_, research, err := a.Ways.WorkflowVersion(t.Context(), "research", 0)
	if err != nil {
		t.Fatal(err)
	}
	custom := strings.Replace(research, "name: research", "name: pack-research", 1)
	old := wow.New(a.DB, wow.Dependencies{Stock: fstest.MapFS{"pack-research.yaml": &fstest.MapFile{Data: []byte(custom)}}})
	old.SyncStockWorkflows(t.Context())
	a.Ways.SyncStockWorkflows(t.Context())
	if !a.Ways.Withdrawn(wow.KindWorkflow, "stock", "pack-research") {
		t.Fatal("fixture is not withdrawn stock")
	}
	if _, _, err := a.changeActions.CreateAndStart(a.asAppTown(t.Context(), p.ID, "test"), &p, "feature", "Unavailable", "Find out", "pack-research"); err == nil {
		t.Fatal("withdrawn workflow ran without a pack")
	}
	if _, err := a.Ways.PublishPack(t.Context(), "research-pack", "Research", `{"workflows":["pack-research"]}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	pack, err := a.Ways.Pack(t.Context(), "research-pack")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.lifecycle.ApplyPack(adoption.WithSystemAuthority(t.Context(), p.ID), &p, []*wow.Pack{pack}, "fixture"); err != nil {
		t.Fatal(err)
	}
	_, run, err := a.changeActions.CreateAndStart(a.asAppTown(t.Context(), p.ID, "test"), &p, "feature", "Investigate", "Find the answer", "pack-research")
	if err != nil || run == nil || run.WorkflowName != "pack-research" {
		t.Fatalf("pack workflow did not start: %+v %v", run, err)
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
	if c, ok := got["scan-source"]; !ok || !sameJSON(c, `{"on":"source","none":true,"failOn":"high"}`) {
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

// Projects have neither workflow copies nor workflow selection storage.
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
	if err := recordCatalogFixture(a.Adoption, t.Context(), idstest.N(1), "fixture"); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: the retired tables' absence is a schema fact; only the catalogue can say a table does not exist
	completionCount(t, a.DB, 0, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('workflows','workflow_versions','project_lanes')`)
	if !workflowAvailable(t, a.Adoption, idstest.N(1), "quick-change") {
		t.Fatal("selecting the workflow must enable its lane")
	}

	h := a.Handler()
	list := httptest.NewRecorder()
	h.ServeHTTP(list, httptest.NewRequest("GET", "/api/v1/projects/p/lanes", nil))
	if list.Code != 200 || strings.Contains(list.Body.String(), `"selected"`) {
		t.Fatalf("read-only workflow list: %d %s", list.Code, list.Body.String())
	}
	var available struct {
		Lanes []struct {
			Name, Description, Tier, Origin string
			Version, RunsCount              int
		}
		System []struct{ Name string }
	}
	if err := json.Unmarshal(list.Body.Bytes(), &available); err != nil {
		t.Fatal(err)
	}
	foundExtra := false
	for _, wf := range available.Lanes {
		if wf.Tier == "system" || wf.Name == "land" {
			t.Fatalf("system workflow offered: %+v", wf)
		}
		if wf.Name == "research" {
			foundExtra = true
		}
	}
	if !foundExtra {
		t.Fatal("extra workflow absent")
	}
	removed := httptest.NewRecorder()
	h.ServeHTTP(removed, httptest.NewRequest("PUT", "/api/v1/projects/p/lanes", strings.NewReader(`{"name":"research","enabled":false}`)))
	if removed.Code != 404 {
		t.Fatalf("selection API remains: %d", removed.Code)
	}
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
