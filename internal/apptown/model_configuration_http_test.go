package apptown

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestModelConfigurationHTTPUsesProfilesOnly(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	for _, tc := range []struct {
		who, body string
		code      int
	}{
		{"outsider", `{"slot":"chat","pattern":"stolen"}`, 403},
		{"boss", `{`, 400}, {"boss", `{"slot":"bad/slot"}`, 400}, {"boss", `{"slot":"chat","profile":"BAD"}`, 400}, {"boss", `{"slot":"chat","project":"missing"}`, 400},
	} {
		if w := authzCall(h, tc.who, "PUT", "/api/v1/model-slots", tc.body); w.Code != tc.code {
			t.Fatalf("invalid binding: %d %s", w.Code, w.Body.String())
		}
	}
	// Only the test AppTown's own default model is set.
	before, err := a.Models.Cells(context.Background())
	if err != nil || len(before) != 1 || len(before["main-strong"]) != 1 {
		t.Fatalf("rejection created cells: %+v %v", before, err)
	}
	for _, body := range []string{`{"slot":"chat","pattern":"default-model"}`, `{"slot":"chat","profile":"economy","pattern":"small-model"}`} {
		if w := authzCall(h, "boss", "PUT", "/api/v1/model-slots", body); w.Code != 200 {
			t.Fatalf("binding: %d %s", w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct {
		who, body string
		code      int
	}{{"outsider", `{"profile":"economy"}`, 403}, {"boss", `{"profile":"BAD"}`, 400}, {"boss", `{"profile":"economy"}`, 200}} {
		if w := authzCall(h, tc.who, "PUT", "/api/v1/projects/game/model-profile", tc.body); w.Code != tc.code {
			t.Fatalf("project profile: %d %s", w.Code, w.Body.String())
		}
	}
	p, err := a.Projects.ByID(context.Background(), idstest.N(1))
	if err != nil || p.ModelProfile != "economy" {
		t.Fatalf("saved profile: %+v %v", p, err)
	}
	w := authzCall(h, "boss", "GET", "/api/v1/model-slots", "")
	var out struct {
		Slots []struct {
			Slot     string
			Patterns map[string]string
		}
		Projects []struct{ Slug, Profile string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("matrix: %d %s %v", w.Code, w.Body.String(), err)
	}
	found := false
	for _, s := range out.Slots {
		if s.Slot == "chat" {
			found = true
			if s.Patterns["standard"] != "default-model" || s.Patterns["economy"] != "small-model" {
				t.Fatalf("profile cells: %+v", s)
			}
		}
	}
	if !found {
		t.Fatal("chat missing")
	}
	matched := false
	for _, p := range out.Projects {
		if p.Slug == "game" {
			matched = p.Profile == "economy"
		}
	}
	if !matched {
		t.Fatalf("saved project profile missing from matrix: %+v", out.Projects)
	}
	for _, project := range []string{`"game"`, `"missing"`, `""`, `null`} {
		w := authzCall(h, "boss", "PUT", "/api/v1/model-slots", `{"slot":"chat","project":`+project+`,"pattern":"project-model"}`)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "a project that needs different models gets its own profile") {
			t.Fatalf("project must be refused with profile advice: %d %s", w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`{"slot":"chat","profile":"economy","pattern":""}`} {
		if w := authzCall(h, "boss", "PUT", "/api/v1/model-slots", body); w.Code != 200 {
			t.Fatalf("remove cell: %d %s", w.Code, w.Body.String())
		}
	}
	cells, err := a.Models.Cells(context.Background())
	if err != nil || cells["chat"]["standard"] != "default-model" || cells["chat"]["economy"] != "" {
		t.Fatalf("removed wrong cell: %+v %v", cells, err)
	}

}

// Connector and model capabilities do what they name (decision D8): a
// person granted the connector, model-binding and model-slot capabilities
// saves connectors and sets model slots, and is still refused an instance
// administrator's acts; a Project owner sets their own project's model
// profile and not another's.
//
// Kills: connector and model routes kept for instance administrators, which
// made both roles' capabilities dead.
func TestConnectorAndModelCapabilitiesManageWhatTheyName(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	for _, c := range []access.Capability{access.CapIntegrationConnector, access.CapModelsBindings, access.CapModelsSlots} {
		a.addGrant("user", "integrator", "", string(c), access.ScopeInstance, "", "boss", "")
	}
	a.addGrant("user", "proprietor", access.RoleProjectOwner, "", access.ScopeProject, idstest.N(1).String(), "boss", "")
	h := a.Handler()
	for _, tc := range []struct {
		who, method, path, body string
		code                    int
	}{
		{"integrator", "POST", "/api/v1/integrations", `{"name":"gpu","capability":"model","provider":"pi","config":{"baseUrl":"https://gpu.example/v1","concurrency":2}}`, 200},
		{"integrator", "PUT", "/api/v1/model-slots", `{"slot":"chat","pattern":"default-model"}`, 200},
		{"integrator", "DELETE", "/api/v1/integrations/gpu", ``, 200},
		{"integrator", "PUT", "/api/v1/projects/game/model-profile", `{"profile":"economy"}`, 403},
		{"integrator", "POST", "/api/v1/projects", `{"slug":"mine","name":"Mine"}`, 403},
		{"proprietor", "PUT", "/api/v1/projects/game/model-profile", `{"profile":"economy"}`, 200},
		{"proprietor", "PUT", "/api/v1/projects/open/model-profile", `{"profile":"economy"}`, 403},
		{"proprietor", "PUT", "/api/v1/model-slots", `{"slot":"chat","pattern":"x"}`, 403},
	} {
		if w := authzCall(h, tc.who, tc.method, tc.path, tc.body); w.Code != tc.code {
			t.Errorf("%s %s %s: %d %s, want %d", tc.who, tc.method, tc.path, w.Code, w.Body.String(), tc.code)
		}
	}
}

// A project-only owner can read and move; a project viewer can read only.
func TestProjectModelsReadUsesProjectAuthority(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	revokeReadingForAll(t, a)
	a.addGrant("user", "proprietor", access.RoleProjectOwner, "", access.ScopeProject, idstest.N(1).String(), "boss", "")
	a.addGrant("user", "reader", "", string(access.CapProjectRead), access.ScopeProject, idstest.N(1).String(), "boss", "")
	if err := a.Models.Set(t.Context(), "main-strong", "high", "vendor/high", "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := a.Models.Set(t.Context(), "custom", "standard", "vendor/custom", "fixture"); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	for _, who := range []string{"proprietor", "reader"} {
		w := authzCall(h, who, "GET", "/api/v1/projects/game/model-profile", "")
		var out model.ProjectModels
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("%s project read: %d %s", who, w.Code, w.Body.String())
		}
		if out.Profile != "standard" {
			t.Fatalf("assignment: %+v", out)
		}
		found := map[string]bool{}
		for _, task := range out.Tasks {
			if task.Slot == "chat" && task.Models["high"].Pattern == "vendor/high" && task.Models["high"].Slot == "main-strong" {
				found["chat"] = true
			}
			if task.Slot == "custom" && task.Models["standard"].Pattern == "vendor/custom" {
				found["custom"] = true
			}
		}
		if !found["chat"] || !found["custom"] {
			t.Fatalf("resolved tasks: %+v", out)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 3 || fields["profile"] == nil || fields["profiles"] == nil || fields["tasks"] == nil {
			t.Fatalf("unscoped fields: %s", w.Body.String())
		}
		for _, path := range []string{"/api/v1/model-slots"} {
			if w := authzCall(h, who, "GET", path, ""); w.Code != 403 {
				t.Fatalf("%s unscoped %s: %d %s", who, path, w.Code, w.Body.String())
			}
		}
	}
	for _, tc := range []struct {
		who  string
		code int
	}{{"proprietor", 200}, {"reader", 403}} {
		if w := authzCall(h, tc.who, "PUT", "/api/v1/projects/game/model-profile", `{"profile":"high"}`); w.Code != tc.code {
			t.Fatalf("%s move: %d %s", tc.who, w.Code, w.Body.String())
		}
	}
	w := authzCall(h, "reader", "GET", "/api/v1/projects/game/model-profile", "")
	var moved model.ProjectModels
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &moved) != nil || moved.Profile != "high" {
		t.Fatalf("assignment after switch: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "outsider", "GET", "/api/v1/projects/game/model-profile", ""); w.Code != 403 {
		t.Fatalf("outsider read: %d %s", w.Code, w.Body.String())
	}
}
