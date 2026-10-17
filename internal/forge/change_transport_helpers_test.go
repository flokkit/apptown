package forge

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
)

// Read scenarios belong to bls, the same named person used by their HTTP/MCP
// requests. Creation includes normal ownership, adoption and attribution.
func createReadTestProject(t *testing.T, a *App, slug string) ids.ID {
	t.Helper()
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{ID: nextFixtureProject(t, a.DB), Slug: slug, Name: slug, Actor: "bls"})
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func createReadTestChange(t *testing.T, a *App, projectID ids.ID, title string) ids.ID {
	t.Helper()
	ch, err := a.Work.Create(asPerson("bls"), workflow.ChangeDraft{ProjectID: projectID, Title: title})
	if err != nil {
		t.Fatal(err)
	}
	return ch.ID
}

func startReadTestRun(t *testing.T, a *App, changeID ids.ID, stage schema.Stage) *workflow.RunSnapshot {
	t.Helper()
	installFakeGit(t, a, nil)
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "build", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{stage}}})
	run, err := a.Work.StartRun(asPerson("bls"), workflow.StartChangeRun{ChangeID: changeID, Workflow: "build"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	snapshot, err := a.Work.Snapshot(asPerson("bls"), run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func changeRequest(a *App, actor, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Forge-Actor", actor)
	a.Handler().ServeHTTP(r, req)
	return r
}
