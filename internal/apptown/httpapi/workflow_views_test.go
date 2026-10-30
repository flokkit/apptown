package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func TestHandleGetChangeCockpit(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	if _, err := workflow.AddArtifactIn(f.Context, f.DB, f.RunID, "change-report", "r", "", "the story so far", time.Now()); err != nil {
		t.Fatal(err)
	}
	decisions := adrs.New(f.DB, access.Checked{Authority: identity.Scoped(f.Access, "adrs"), Projects: f.Projects}, nil, nil)
	decision, err := decisions.Create(f.Context, adrs.Create{ProjectID: f.ProjectID, ChangeID: f.ChangeID, Title: "pick sqlite", Tags: "db", BodyMD: "because"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decisions.Edit(f.Context, adrs.Edit{ID: decision.ID, Decision: adrs.Accept}); err != nil {
		t.Fatal(err)
	}
	w := workflowChangeRead(t, f, fmt.Sprint(f.ChangeID))
	if w.Code != 200 {
		t.Fatalf("get change: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Change      map[string]any
		ProjectSlug string
		Detail      map[string]any
		Adrs        []map[string]any
		Runs        []map[string]any
		Report      string
		State       string
		StateDetail string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ProjectSlug != "shop" || body.Change["state"] != "in_progress" || body.State != "waiting on you" || body.Detail == nil || body.Detail["run"] == nil || len(body.Runs) != 1 || body.Runs[0]["workflowName"] != "test" || len(body.Adrs) != 1 || body.Adrs[0]["title"] != "pick sqlite" || body.Report != "the story so far" {
		t.Fatalf("cockpit projection: %+v", body)
	}
}

func TestHandleGetChangeNotFound(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	if w := workflowChangeRead(t, f, "0000000f-423f-7000-8000-000000000000"); w.Code != 404 {
		t.Fatalf("unknown change: %d", w.Code)
	}
}

func TestHandleBoardColumnsAndCap(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, false)
	for i := 0; i < 11; i++ {
		change, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: fmt.Sprintf("s%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.DB.InTx(context.Background(), func(tx *database.Tx) error {
			_, err := workflow.TransitionChangeIn(f.Context, tx, f.ProjectID, change.ID, workflow.ChangeMove{Action: workflow.ShipChange}, time.Now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest("GET", "/api/v1/projects/shop/board", nil).WithContext(f.Context)
	r.SetPathValue("slug", "shop")
	w := httptest.NewRecorder()
	NewChanges(f.Work, f.Projects, f.Access, f.DB, nil).Board(w, r)
	if w.Code != 200 {
		t.Fatalf("board: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Columns []struct {
			State string
			Total int
			Items []map[string]any
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	cols := map[string]struct{ total, shown int }{}
	order := []string{}
	for _, c := range body.Columns {
		cols[c.State] = struct{ total, shown int }{c.Total, len(c.Items)}
		order = append(order, c.State)
	}
	want := []string{"in_progress", "landing", "shipped", "abandoned"}
	if len(order) != len(want) {
		t.Fatalf("column count/order: %v", order)
	}
	for i, state := range want {
		if order[i] != state {
			t.Fatalf("column order: %v", order)
		}
	}
	if cols["shipped"].total != 11 || cols["shipped"].shown != 8 || cols["in_progress"].total != 1 || cols["in_progress"].shown != 1 || cols["landing"].shown != 0 {
		t.Fatalf("column totals/caps: %+v", cols)
	}
}

func TestHandleDashboardSplit(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	projectB, err := database.Transact(context.Background(), f.DB, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(f.Context, tx, time.Now(), project.Spec{Slug: "dash-b", Name: "Dash B"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.DB.InTx(context.Background(), func(tx *database.Tx) error {
		if err := adoption.InitializeIn(f.Context, tx, projectB, 0, ""); err != nil {
			return err
		}
		return adoption.RecordIn(f.Context, tx, projectB, 1, adoption.AdoptionCreate, "dashboard fixture", adoption.EmptyReceipt(), "fixture", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	f.Work.Stop()
	specB := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "build", Uses: "agent/pi"}}}}}
	f.Work = workflow.New(context.Background(), f.DB, workflow.Dependencies{
		Authority:    access.Checked{Authority: identity.Scoped(f.Access, "workflow"), Projects: f.Projects},
		Repositories: &scm.RepositoryLocks{},
		Automatic:    []string{"agent/pi"},
		Runners: map[string]workflow.Runner{"agent/pi": func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
			}
		}},
		Resolve: func(_ context.Context, _ workflow.StartRequest, source workflow.StartSource) (workflow.PreparedRun, error) {
			return workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "dashboard-build", Version: 1}, Spec: specB, AdoptionID: source.AdoptionID}, nil
		},
	})
	t.Cleanup(f.Work.Stop)
	changeB, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: projectB, Title: "in flight"})
	if err != nil {
		t.Fatal(err)
	}
	runB, err := f.Work.StartRun(f.Context, workflow.StartChangeRun{ChangeID: changeB.ID, Workflow: "dashboard-build"})
	if err != nil {
		t.Fatal(err)
	}
	f.Work.Wait()
	r := httptest.NewRequest("GET", "/api/v1/dashboard", nil).WithContext(f.Context)
	w := httptest.NewRecorder()
	decisions := adrs.New(f.DB, access.Checked{Authority: identity.Scoped(f.Access, "adrs"), Projects: f.Projects}, nil, nil)
	NewDashboard(f.Work, decisions, operations.New(f.DB, nil, nil, nil)).Dashboard(w, r)
	if w.Code != 200 {
		t.Fatalf("dashboard: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		PendingApprovals []map[string]any
		ActiveRuns       []map[string]any
		RecentAdrs       []map[string]any
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.PendingApprovals) != 1 || body.PendingApprovals[0]["runId"] != f.RunID.String() || body.PendingApprovals[0]["stepName"] != "accept" {
		t.Fatalf("pending approval: %v", body.PendingApprovals)
	}
	if len(body.ActiveRuns) != 1 || body.ActiveRuns[0]["runId"] != runB.ID.String() {
		t.Fatalf("active split: %v", body.ActiveRuns)
	}
}
