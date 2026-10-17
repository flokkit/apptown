package forge

import (
	"context"
	"encoding/json"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestModelConfigurationHTTPKeepsProfileCellsAndProjectOverridesSeparate(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	for _, tc := range []struct {
		who, body string
		code      int
	}{
		{"outsider", `{"slot":"chat","pattern":"stolen"}`, 403},
		{"boss", `{`, 400}, {"boss", `{"slot":"bad/slot"}`, 400}, {"boss", `{"slot":"chat","profile":"BAD"}`, 400}, {"boss", `{"slot":"chat","project":"missing"}`, 404},
	} {
		if w := authzCall(h, tc.who, "PUT", "/api/v1/model-slots", tc.body); w.Code != tc.code {
			t.Fatalf("invalid binding: %d %s", w.Code, w.Body.String())
		}
	}
	before, err := a.Models.Cells(context.Background())
	if err != nil || len(before) != 0 {
		t.Fatalf("rejection created cells: %+v %v", before, err)
	}
	for _, body := range []string{`{"slot":"chat","pattern":"default-model"}`, `{"slot":"chat","profile":"economy","pattern":"small-model"}`, `{"slot":"chat","project":"game","profile":"ignored","pattern":"project-model"}`} {
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
	var wire struct {
		Overrides []struct{ Project, Slot, Pattern string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &wire); err != nil || len(wire.Overrides) != 1 || wire.Overrides[0].Project != "game" || wire.Overrides[0].Slot != "chat" || wire.Overrides[0].Pattern != "project-model" {
		t.Fatalf("override projection: %s %v", w.Body.String(), err)
	}
	for _, body := range []string{`{"slot":"chat","project":"game","pattern":""}`, `{"slot":"chat","profile":"economy","pattern":""}`} {
		if w := authzCall(h, "boss", "PUT", "/api/v1/model-slots", body); w.Code != 200 {
			t.Fatalf("remove cell: %d %s", w.Code, w.Body.String())
		}
	}
	cells, err := a.Models.Cells(context.Background())
	if err != nil || cells["chat"]["standard"] != "default-model" || cells["chat"]["economy"] != "" {
		t.Fatalf("removed wrong cell: %+v %v", cells, err)
	}
	overrides, err := a.Models.Overrides(context.Background())
	if err != nil || len(overrides) != 0 {
		t.Fatalf("override survived: %+v %v", overrides, err)
	}
}

// Roles get what their descriptions promise (decision D8): the Integration
// administrator ("manages connectors, service and dependency kinds, model
// bindings") saves connectors and sets model slots, and is still refused an
// instance administrator's acts; a Project owner sets their own project's
// model profile and not another's.
//
// Kills: connector and model routes kept for instance administrators, which
// made both roles' capabilities dead.
func TestRolesManageWhatTheirDescriptionsPromise(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	a.addGrant("user", "integrator", access.RoleIntegrationAdmin, "", access.ScopeInstance, "", "fixture", "boss", "")
	a.addGrant("user", "proprietor", access.RoleProjectOwner, "", access.ScopeProject, idstest.N(1).String(), "fixture", "boss", "")
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
