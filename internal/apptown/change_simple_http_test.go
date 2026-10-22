package apptown

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// The one-sentence flow: a sentence becomes a change with a run, an intent
// artifact, and a workflow picked from what's installed — or a plain
// refusal when nothing simple is installed.
func TestKidFlowSimpleChange(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "bls")

	plainTestProject(t, a.DB, "game", "G")
	p, _ := a.Projects.BySlug(context.Background(), "game")
	if err := a.lifecycle.InitializeRepository(t.Context(), &p, "", ""); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()

	// Nothing simple installed: a plain-words 422, not a mystery.
	if w := postJSON(h, "/api/v1/projects/game/changes/simple", map[string]string{"text": "make the cat blue"}); w.Code != 422 {
		t.Fatalf("no simple workflow must 422: %d %s", w.Code, w.Body.String())
	}
	// Empty text is asked for, not tolerated.
	quick := strings.Replace(lifecycleYAML, "name: flow", "name: quick-change", 1)
	if name, version, err := a.Ways.PublishWorkflow(t.Context(), quick, "fixture"); err != nil || name != "quick-change" || version != 1 {
		t.Fatalf("publish quick-change: %s@%d %v", name, version, err)
	}
	a.lifecycle.SelectLane(t.Context(), idstest.N(1), "quick-change", true, "fixture")
	if w := postJSON(h, "/api/v1/projects/game/changes/simple", map[string]string{"text": "  "}); w.Code != 400 {
		t.Fatalf("empty text must 400: %d", w.Code)
	}

	w := postJSON(h, "/api/v1/projects/game/changes/simple", map[string]string{"text": "make the cat blue. it is currently red."})
	if w.Code != 201 {
		t.Fatalf("kid flow: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"workflow":"quick-change"`) {
		t.Fatalf("picked workflow: %s", body)
	}
	if !strings.Contains(body, `"title":"make the cat blue"`) {
		t.Fatalf("title should be the first sentence: %s", body)
	}
	var created struct {
		Run workflow.Run `json:"run"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Run.ID.IsZero() {
		t.Fatalf("no run was returned: %v", err)
	}
	artifacts, err := a.Work.RunArtifacts(asPerson("bls"), created.Run.ID, workflow.ArtifactSelection{Kinds: []string{"intent"}})
	if err != nil || len(artifacts) != 1 || !strings.Contains(artifacts[0].Body, "fallback rules") {
		t.Fatalf("intent must say how the workflow was chosen: %+v %v", artifacts, err)
	}
}
