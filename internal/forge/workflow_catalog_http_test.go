package forge

import (
	"encoding/json"
	"strings"
	"testing"

	"flokkit.io/forge/internal/ids/idstest"
)

func TestWorkflowLibraryHTTPSelectionHistoryAndValidation(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	source := `apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: {name: custom-review, description: A review lane}
spec:
  kind: lifecycle
  stages:
    - name: review
      steps:
        - name: accept
          uses: approval
          with: {means: "I reviewed the work"}
`
	body := func(yaml string) string {
		b, err := json.Marshal(map[string]string{"yaml": yaml})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, tc := range []struct {
		who, payload string
		code         int
	}{{"outsider", body(source), 403}, {"boss", `{`, 400}, {"boss", body("not a workflow"), 400}} {
		w := authzCall(h, tc.who, "POST", "/api/v1/workflows/library", tc.payload)
		if w.Code != tc.code {
			t.Fatalf("publish refusal: %d %s", w.Code, w.Body.String())
		}
	}
	// The format a save holds a workflow to is published for editors and
	// agents, and a save refuses what it does not describe.
	if w := authzCall(h, "owner", "GET", "/api/v1/workflows/schema", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"forge.flokkit.io/v1"`) {
		t.Fatalf("workflow schema: %d %.200s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "owner", "POST", "/api/v1/workflows/validate", body(strings.Replace(source, "kind: Workflow", "kind: Workflow\nlabels: [x]", 1))); !strings.Contains(w.Body.String(), "labels") {
		t.Fatalf("an unknown key validated: %s", w.Body.String())
	}
	for _, tc := range []struct {
		payload string
		ok      bool
	}{{body("invalid: ["), false}, {body(source), true}} {
		w := authzCall(h, "owner", "POST", "/api/v1/workflows/validate", tc.payload)
		var out struct {
			OK     bool
			Name   string
			Stages []struct {
				Name  string
				Steps []string
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.OK != tc.ok {
			t.Fatalf("editor validation: %d %s %v", w.Code, w.Body.String(), err)
		}
		if tc.ok && (out.Name != "custom-review" || len(out.Stages) != 1 || out.Stages[0].Name != "review" || len(out.Stages[0].Steps) != 1 || out.Stages[0].Steps[0] != "accept (approval)") {
			t.Fatalf("editor preview: %+v", out)
		}
	}
	publish := func(yaml string, version int) {
		t.Helper()
		w := authzCall(h, "boss", "POST", "/api/v1/workflows/library", body(yaml))
		var out struct {
			Name    string
			Version int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.Name != "custom-review" || out.Version != version {
			t.Fatalf("publish: %d %s %v", w.Code, w.Body.String(), err)
		}
	}
	publish(source, 1)
	if w := authzCall(h, "outsider", "PUT", "/api/v1/projects/game/lanes", `{"name":"custom-review","enabled":true}`); w.Code != 403 {
		t.Fatalf("foreign selection: %d", w.Code)
	}
	if w := authzCall(h, "owner", "PUT", "/api/v1/projects/game/lanes", `{"name":"missing","enabled":true}`); w.Code != 404 {
		t.Fatalf("missing selection: %d", w.Code)
	}
	if w := authzCall(h, "owner", "PUT", "/api/v1/projects/game/lanes", `{"name":"custom-review","enabled":true}`); w.Code != 200 {
		t.Fatalf("select: %d %s", w.Code, w.Body.String())
	}
	// A deprecated workflow is not turned on anew, and the refusal names its
	// replacement from the description (as change-request names quick-change).
	retired := strings.Replace(strings.Replace(source, "name: custom-review, description: A review lane", "name: old-review, deprecated: true, description: Merged into custom-review — nothing new should use it", 1), "I reviewed the work", "I reviewed it", 1)
	if w := authzCall(h, "boss", "POST", "/api/v1/workflows/library", body(retired)); w.Code != 200 {
		t.Fatalf("publish deprecated: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "owner", "PUT", "/api/v1/projects/game/lanes", `{"name":"old-review","enabled":true}`); w.Code != 422 || !strings.Contains(w.Body.String(), "deprecated") || !strings.Contains(w.Body.String(), "Merged into custom-review") {
		t.Fatalf("deprecated selection: %d %s", w.Code, w.Body.String())
	}
	if laneOn(t, a.Adoption, idstest.N(1), "old-review") {
		t.Fatal("a refused deprecated workflow was selected")
	}
	if w := authzCall(h, "owner", "PUT", "/api/v1/projects/game/lanes", `{"name":"old-review","enabled":false}`); w.Code != 200 {
		t.Fatalf("turning a deprecated workflow off stays allowed: %d %s", w.Code, w.Body.String())
	}
	check := func(version int, stage string) {
		t.Helper()
		w := authzCall(h, "owner", "GET", "/api/v1/workflows/library", "")
		var library []struct {
			Name          string
			LatestVersion int
			Stages        []string
			UsedBy        []struct{ Project, Origin string }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &library); err != nil || w.Code != 200 {
			t.Fatalf("library: %d %s %v", w.Code, w.Body.String(), err)
		}
		found := false
		for _, wf := range library {
			if wf.Name == "custom-review" {
				found = true
				if wf.LatestVersion != version || len(wf.Stages) != 1 || wf.Stages[0] != stage || len(wf.UsedBy) != 1 || wf.UsedBy[0].Project != "game" {
					t.Fatalf("selected library projection: %+v", wf)
				}
			}
		}
		if !found {
			t.Fatal("published workflow absent from library")
		}
		w = authzCall(h, "owner", "GET", "/api/v1/projects/game/workflows", "")
		var selected []struct {
			Name      string
			Version   int
			RunsCount int
			Summary   *struct{ Stages []struct{ Name string } }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &selected); err != nil || w.Code != 200 {
			t.Fatalf("selected list: %d %s %v", w.Code, w.Body.String(), err)
		}
		found = false
		for _, wf := range selected {
			if wf.Name == "custom-review" {
				found = true
				if wf.Version != version || wf.RunsCount != 0 || wf.Summary == nil || len(wf.Summary.Stages) != 1 || wf.Summary.Stages[0].Name != stage {
					t.Fatalf("selected workflow: %+v", wf)
				}
			}
		}
		if !found {
			t.Fatal("selected workflow absent")
		}
	}
	check(1, "review")
	publish(strings.Replace(source, "name: review", "name: finish", 1), 2)
	check(2, "finish")
	w := authzCall(h, "owner", "GET", "/api/v1/projects/game/workflows/custom-review/history", "")
	var history []struct {
		Version int
		YAML    string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil || w.Code != 200 || len(history) != 2 || history[0].Version != 2 || history[1].Version != 1 || !strings.Contains(history[1].YAML, "name: review") {
		t.Fatalf("history: %d %s %v", w.Code, w.Body.String(), err)
	}
	if w := authzCall(h, "outsider", "DELETE", "/api/v1/workflows/library/custom-review", ""); w.Code != 403 {
		t.Fatalf("foreign delete: %d", w.Code)
	}
	if w := authzCall(h, "owner", "PUT", "/api/v1/projects/game/lanes", `{"name":"custom-review","enabled":false}`); w.Code != 200 {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	if laneOn(t, a.Adoption, idstest.N(1), "custom-review") {
		t.Fatal("disabled lane remained selected")
	}
	if w := authzCall(h, "boss", "DELETE", "/api/v1/workflows/library/custom-review", ""); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, _, err := a.Ways.WorkflowVersion(t.Context(), "custom-review", 0); err == nil {
		t.Fatal("deleted definition remains")
	}
	// Absence is classified by the library, so the handler needs no word
	// about the store to answer it.
	if w := authzCall(h, "boss", "DELETE", "/api/v1/workflows/library/custom-review", ""); w.Code != 404 {
		t.Fatalf("deleting twice: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "owner", "GET", "/api/v1/projects/game/workflows/custom-review/history", ""); w.Code != 404 {
		t.Fatalf("history of a deleted workflow: %d %s", w.Code, w.Body.String())
	}
}
