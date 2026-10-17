package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
)

func newOverviewHTTPFixture(t *testing.T) workflowHTTPFixture {
	t.Helper()
	f := newWorkflowHTTPFixture(t, true)
	if _, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "still cooking"}); err != nil {
		t.Fatal(err)
	}
	shipped, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "shipped one"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"build", "land"} {
		run, err := f.Work.StartRun(f.Context, workflow.StartChangeRun{ChangeID: shipped.ID, Workflow: name})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.DB.InTx(context.Background(), func(tx *database.Tx) error {
			return workflow.TransitionRunIn(f.Context, tx, run, workflow.RunDone, run.CurrentStage, time.Now())
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.DB.InTx(context.Background(), func(tx *database.Tx) error {
		_, err := workflow.TransitionChangeIn(f.Context, tx, f.ProjectID, shipped.ID, workflow.ChangeMove{Action: workflow.ShipChange}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func workflowOverviewCall(t *testing.T, f workflowHTTPFixture) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/v1/projects/shop/overview", nil).WithContext(f.Context)
	r.SetPathValue("slug", "shop")
	w := httptest.NewRecorder()
	NewProjectOverview(f.Projects, f.Work, t.TempDir()).ProjectOverview(w, r)
	return w
}

func workflowActivityCall(f workflowHTTPFixture) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/v1/activity", nil).WithContext(f.Context)
	w := httptest.NewRecorder()
	NewActivity(f.Work, deployment.New(r.Context(), f.DB, deployment.Dependencies{Authority: access.Checked{Authority: identity.New(f.DB, identity.Config{Projects: project.NewCatalogue(f.DB)}), Projects: f.Projects}})).Activity(w, r)
	return w
}

func TestHandleProjectOverview(t *testing.T) {
	t.Parallel()
	f := newOverviewHTTPFixture(t)
	w := workflowOverviewCall(t, f)
	if w.Code != 200 {
		t.Fatalf("overview: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Repo    any `json:"repo"`
		Active  []map[string]any
		Open    []map[string]any
		History []map[string]any
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Repo != nil || len(body.Active) != 1 || body.Active[0]["title"] != "original" || body.Active[0]["state"] != "waiting on you" || len(body.Open) != 1 || body.Open[0]["title"] != "still cooking" || len(body.History) != 1 || int(body.History[0]["runs"].(float64)) != 2 || body.History[0]["workflows"] != "build → land" {
		t.Fatalf("overview partitions: %+v", body)
	}
	// Each row names its change (and a running run its run) by number, in
	// the order the fixture opened them: the run's change, then the open
	// one, then the shipped one.
	if body.Active[0]["changeNumber"] != float64(1) || body.Active[0]["runNumber"] != float64(1) || body.Open[0]["changeNumber"] != float64(2) || body.History[0]["changeNumber"] != float64(3) {
		t.Fatalf("overview numbers: %+v", body)
	}
}

func TestOverviewWorkReadFailuresAreVisible(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ view, fragment string }{
		{"overview", "CROSS JOIN LATERAL"},
		{"overview", "SELECT c.id,c.number,c.title,c.state"},
		{"activity", "FROM steps s JOIN runs r"},
		{"activity", "CROSS JOIN LATERAL"},
	} {
		t.Run(fmt.Sprintf("%s/%s", tc.view, tc.fragment), func(t *testing.T) {
			t.Parallel()
			f := newOverviewHTTPFixture(t)
			t.Cleanup(f.DB.FailNextStatement(tc.fragment))
			var w *httptest.ResponseRecorder
			if tc.view == "overview" {
				w = workflowOverviewCall(t, f)
			} else {
				w = workflowActivityCall(f)
			}
			if w.Code != 500 {
				t.Fatalf("failed Work read hidden: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestHandleActivity(t *testing.T) {
	t.Parallel()
	f := newOverviewHTTPFixture(t)
	f.Work.Stop()
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", OnEnter: []schema.Step{{Name: "agent", Uses: "agent/pi"}}}}}
	f.Work = workflow.New(context.Background(), f.DB, workflow.Dependencies{
		Authority:    access.Checked{Authority: identity.Scoped(f.Access, "workflow"), Projects: f.Projects},
		Repositories: &scm.RepositoryLocks{},
		Automatic:    []string{"agent/pi"},
		Runners: map[string]workflow.Runner{"agent/pi": func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
			}
		}},
		Resolve: func(_ context.Context, request workflow.StartRequest, source workflow.StartSource) (workflow.PreparedRun, error) {
			return workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: request.WorkflowName, Version: 1}, Spec: spec, AdoptionID: source.AdoptionID}, nil
		},
	})
	t.Cleanup(f.Work.Stop)
	agentChange, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "agent work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Work.StartRun(f.Context, workflow.StartChangeRun{ChangeID: agentChange.ID, Workflow: "agent"}); err != nil {
		t.Fatal(err)
	}
	f.Work.Wait()
	service := deployment.New(context.Background(), f.DB, deployment.Dependencies{Authority: access.Checked{Authority: identity.New(f.DB, identity.Config{Projects: project.NewCatalogue(f.DB)}), Projects: f.Projects}})
	target, err := service.Targets.Put(context.Background(), &deployment.Target{Name: "overview", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Deployments.EnsureStanding(t.Context(), f.ProjectID, "staging", target.ID, (deployment.Attributes{}).WithDefaults()); err != nil {
		t.Fatal(err)
	}
	w := workflowActivityCall(f)
	if w.Code != 200 {
		t.Fatalf("activity: %d %s", w.Code, w.Body.String())
	}
	var body struct{ Agents, Active, History, Deployments []map[string]any }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Agents) != 1 || body.Agents[0]["step"] != "agent" || len(body.Active) != 2 || len(body.History) != 1 || body.History[0]["project"] != "shop" || len(body.Deployments) != 1 || body.Deployments[0]["name"] != "staging" || body.Deployments[0]["running"] != nil || body.Deployments[0]["observation"] != "unknown" {
		t.Fatalf("activity partitions: %+v", body)
	}
}

func TestWorkflowActivityRequiredReadsFailAtHTTP(t *testing.T) {
	t.Parallel()
	for _, view := range []string{"runs", "overview", "activity", "run"} {
		t.Run(view, func(t *testing.T) {
			t.Parallel()
			f := newOverviewHTTPFixture(t)
			statement := "WITH RECURSIVE activity_tree"
			if view == "run" {
				statement = "-- name: RunSteps"
			}
			t.Cleanup(f.DB.FailNextStatement(statement))
			var out *httptest.ResponseRecorder
			switch view {
			case "overview":
				out = workflowOverviewCall(t, f)
			case "activity":
				out = workflowActivityCall(f)
			case "runs":
				r := httptest.NewRequest("GET", "/api/v1/runs", nil).WithContext(f.Context)
				out = httptest.NewRecorder()
				NewRuns(f.Work).ListRuns(out, r)
			case "run":
				r := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/runs/%d", f.RunID), nil).WithContext(f.Context)
				r.SetPathValue("id", fmt.Sprint(f.RunID))
				out = httptest.NewRecorder()
				NewRuns(f.Work).GetRun(out, r)
			}
			if out.Code != 500 {
				t.Fatalf("required read failure: %d %s", out.Code, out.Body.String())
			}
		})
	}
}
