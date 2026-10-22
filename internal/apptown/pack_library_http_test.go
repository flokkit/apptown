package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestPackLibraryHTTPWarningsProtectionAndManifest(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	// Missing references are a warning, unlike an invalid policy declaration.
	invalid := `{"name":"custom-pack","policies":[{"name":"bad","rule":"not-a-rule"}]}`
	for _, tc := range []struct {
		who, body string
		code      int
	}{{"outsider", `{"name":"custom-pack"}`, 403}, {"boss", `{`, 400}, {"boss", `{"name":"INVALID"}`, 400}, {"boss", invalid, 400},
		// Strict: a field a pack does not have, the retired autoAdopt among
		// them, and a policy defined inline instead of named.
		{"boss", `{"name":"custom-pack","guidence":["x"]}`, 400}, {"boss", `{"name":"custom-pack","autoAdopt":true}`, 400},
		{"boss", `{"name":"custom-pack","policies":[{"name":"deploy-requires-scan","rule":"requireStepBefore","with":{"uses":"deploy","requires":"scan"}}]}`, 400}} {
		w := authzCall(h, tc.who, "POST", "/api/v1/packs", tc.body)
		if w.Code != tc.code {
			t.Fatalf("save refusal: %d %s", w.Code, w.Body.String())
		}
	}
	if _, err := a.Ways.Pack(t.Context(), "custom-pack"); err == nil {
		t.Fatal("invalid pack saved")
	}
	// The editor's shape: a picked policy is its name with an empty rule
	// and options, and the draft carries versionName and the inclusion fields.
	// A version pins what it names, so items the library lacks are refused
	// by name; an unknown toolchain, a starter file, is only a warning.
	w := authzCall(h, "boss", "POST", "/api/v1/packs", `{"name":"custom-pack","description":"Demo setup","versionName":"","includes":"","replaces":{},"drops":[],"guidance":["missing-guide"],"workflows":["missing-flow"],"policies":[{"name":"missing-policy","description":"","rule":"","with":{}}],"toolchain":"missing-tool"}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "missing-guide") || !strings.Contains(w.Body.String(), "missing-flow") || !strings.Contains(w.Body.String(), "missing-policy") {
		t.Fatalf("missing items: %d %s", w.Code, w.Body.String())
	}
	w = authzCall(h, "boss", "POST", "/api/v1/packs", `{"name":"custom-pack","description":"Demo setup","versionName":"","includes":"","replaces":{},"drops":[],"guidance":[],"workflows":[],"policies":[],"toolchain":"missing-tool"}`)
	var saved struct {
		OK       bool
		Warnings []string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || w.Code != 200 || !saved.OK || len(saved.Warnings) != 1 {
		t.Fatalf("warnings: %d %s %v", w.Code, w.Body.String(), err)
	}
	// Update to usable contents before application. Version pinning and later
	// publication behavior are covered by the owner and pack-view tests.
	w = authzCall(h, "boss", "POST", "/api/v1/packs", `{"name":"custom-pack","description":"Demo setup","versionName":"usable"}`)
	if w.Code != 200 {
		t.Fatalf("update pack: %d %s", w.Code, w.Body.String())
	}
	// Saving never moves the recommendation: new projects still get v1
	// until a person recommends the usable version.
	if w := authzCall(h, "boss", "POST", "/api/v1/packs/custom-pack/recommended", `{"version":2}`); w.Code != 200 {
		t.Fatalf("recommend: %d %s", w.Code, w.Body.String())
	}
	if _, err := a.Ways.PublishPack(t.Context(), "protected-pack", "Built in", `{}`, "stock"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path, body string }{{"POST", "/api/v1/packs", `{"name":"protected-pack"}`}, {"DELETE", "/api/v1/packs/protected-pack", `{}`}} {
		w := authzCall(h, "boss", tc.method, tc.path, tc.body)
		if w.Code != 409 {
			t.Fatalf("stock protection: %d %s", w.Code, w.Body.String())
		}
	}
	w = authzCall(h, "owner", "GET", "/api/v1/packs", "")
	var packs []wow.Pack
	if err := json.Unmarshal(w.Body.Bytes(), &packs); err != nil || w.Code != 200 || len(packs) != 2 {
		t.Fatalf("pack list: %d %s %v", w.Code, w.Body.String(), err)
	}
	found := false
	for _, p := range packs {
		if p.Name == "custom-pack" {
			found = true
			if p.Description != "Demo setup" || p.Origin == "stock" {
				t.Fatalf("custom pack projection: %+v", p)
			}
		}
	}
	if !found {
		t.Fatal("custom pack missing")
	}
	base := "/api/v1/projects/game/pack"
	for _, tc := range []struct {
		who, body string
		code      int
	}{{"outsider", `{"name":"custom-pack"}`, 403}, {"owner", `{`, 400}, {"owner", `{"name":"missing"}`, 404}, {"owner", `{"name":"custom-pack"}`, 200}} {
		w := authzCall(h, tc.who, "POST", base, tc.body)
		if w.Code != tc.code {
			t.Fatalf("apply pack: %d %s", w.Code, w.Body.String())
		}
	}
	w = authzCall(h, "owner", "GET", "/api/v1/projects/game/manifest", "")
	var manifest struct{ Packs []struct{ Name string } }
	if err := json.Unmarshal(w.Body.Bytes(), &manifest); err != nil || w.Code != 200 || len(manifest.Packs) != 1 || manifest.Packs[0].Name != "custom-pack" {
		t.Fatalf("manifest application: %d %s %v", w.Code, w.Body.String(), err)
	}
	for _, tc := range []struct {
		who  string
		code int
	}{{"outsider", 403}, {"boss", 200}, {"boss", 404}} {
		w := authzCall(h, tc.who, "DELETE", "/api/v1/packs/custom-pack", "")
		if w.Code != tc.code {
			t.Fatalf("delete custom pack: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestSystemWorkflowHTTPRequiresExplicitSystemDeclarationAndCanReset(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	source := `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: my-land, description: Custom acceptance}
spec:
  kind: pipeline
  stages:
    - name: review
      steps:
        - {name: accept, uses: approval, with: {means: "I checked the change"}}
`
	if _, _, err := a.Ways.PublishWorkflow(t.Context(), source, "boss"); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/system-workflows"
	for _, tc := range []struct {
		who, body string
		code      int
	}{{"owner", `{"stock":"land","name":"my-land"}`, 403}, {"boss", `{`, 400}, {"boss", `{"stock":"unknown"}`, 400}, {"boss", `{"stock":"land","name":"absent"}`, 404}, {"boss", `{"stock":"land","name":"my-land"}`, 422}} {
		w := authzCall(h, tc.who, "PUT", path, tc.body)
		if w.Code != tc.code {
			t.Fatalf("system binding refusal: %d %s", w.Code, w.Body.String())
		}
		if a.Ways.SystemWorkflow(t.Context(), "land") != "land" {
			t.Fatal("rejected binding persisted")
		}
	}
	if _, _, err := a.Ways.PublishWorkflow(t.Context(), strings.Replace(source, "name: my-land,", "name: my-land, system: true,", 1), "boss"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"my-land", ""} {
		w := authzCall(h, "boss", "PUT", path, `{"stock":"land","name":"`+name+`"}`)
		if w.Code != 200 {
			t.Fatalf("bind/reset: %d %s", w.Code, w.Body.String())
		}
		w = authzCall(h, "owner", "GET", path, "")
		var out struct {
			Bindings []struct {
				Stock, Name, Description string
				Custom                   bool
			}
			Candidates []string
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || len(out.Bindings) != 4 {
			t.Fatalf("system workflows: %d %s %v", w.Code, w.Body.String(), err)
		}
		expected := name
		if expected == "" {
			expected = "land"
		}
		found := false
		for _, b := range out.Bindings {
			if b.Stock == "land" {
				found = true
				if b.Name != expected || b.Custom != (name != "") || (name != "" && b.Description != "Custom acceptance") {
					t.Fatalf("binding projection: %+v", b)
				}
			}
		}
		if !found {
			t.Fatal("land binding missing")
		}
	}
}

// The pack list says which packs start from a static app, read from the
// pack's seed contract, so the new-project wizard can leave out services.
func TestPackListSaysWhichStartersAreStatic(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	for _, name := range []string{"html-game", "go-small"} {
		if _, err := a.Ways.PublishPack(t.Context(), name, "stock pack", `{}`, "stock"); err != nil {
			t.Fatal(err)
		}
	}
	w := authzCall(a.Handler(), "owner", "GET", "/api/v1/packs", "")
	var packs []struct {
		Name          string `json:"name"`
		StaticStarter bool   `json:"staticStarter"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &packs); err != nil || w.Code != 200 {
		t.Fatalf("pack list: %d %s %v", w.Code, w.Body.String(), err)
	}
	got := map[string]bool{}
	for _, p := range packs {
		got[p.Name] = p.StaticStarter
	}
	if !got["html-game"] || got["go-small"] {
		t.Fatalf("static starters: %v", got)
	}
}

// A pack that includes a stock pack is saved through the editor's route as
// a declaration and applied as one choice: the project gets the resolved
// contents at the included publication's pins, and starts from the
// included pack's starter files. A replacement of an item the included pack
// lacks is refused and saves nothing.
func TestAnIncludingPackAppliesItsResolvedContents(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	for _, b := range []string{"base/a", "base/b", "team/a"} {
		if _, err := a.Ways.CreateBlock(t.Context(), b, b+" rules", "", "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Ways.PublishPack(t.Context(), "html-game", "stock pack", `{"guidance":["base/a","base/b"]}`, "stock"); err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/packs", `{"name":"team","includes":"html-game","replaces":{"base/a":"nope"}}`); w.Code != 400 {
		t.Fatalf("an unresolvable declaration: %d %s", w.Code, w.Body.String())
	}
	if _, err := a.Ways.Pack(t.Context(), "team"); err == nil {
		t.Fatal("an unresolvable declaration was saved")
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/packs", `{"name":"team","includes":"html-game","replaces":{"base/a":"team/a"},"drops":[{"item":"base/b","reason":"not ours"}]}`); w.Code != 200 {
		t.Fatalf("save an including pack: %d %s", w.Code, w.Body.String())
	}
	team, err := a.Ways.Pack(t.Context(), "team")
	if err != nil || strings.Join(team.Guidance, ",") != "team/a" || team.StarterPack() != "html-game" {
		t.Fatalf("offered contents: %+v %v", team, err)
	}
	// A save sends the whole declaration back; what the editor does not
	// edit is kept, not dropped.
	if w := authzCall(h, "boss", "POST", "/api/v1/packs", `{"name":"keeps","includes":"html-game","clears":["toolchain"],"extensions":["lsp"],"services":[{"name":"db","image":"postgres:16"}]}`); w.Code != 200 {
		t.Fatalf("save with setup: %d %s", w.Code, w.Body.String())
	}
	if kept, err := a.Ways.PackDeclaration(t.Context(), "keeps"); err != nil || strings.Join(kept.Clears, ",") != "toolchain" || strings.Join(kept.Extensions, ",") != "lsp" || len(kept.Services) != 1 {
		t.Fatalf("the declaration's setup was dropped: %+v %v", kept, err)
	}
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/pack", `{"name":"team"}`); w.Code != 200 {
		t.Fatalf("apply: %d %s", w.Code, w.Body.String())
	}
	got, err := a.Adoption.CurrentReceipt(t.Context(), idstest.N(1))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, b := range got.Receipt.Guidance {
		names = append(names, b.Name)
	}
	if strings.Join(names, ",") != "team/a" || got.Receipt.Pack != "team" {
		t.Fatalf("the project took the resolved contents: %+v", got.Receipt)
	}
}

// Recommending a version is a person's act: an administrator's agent token
// is refused by authorization, their command-line token is not. Moving a
// recommendation cuts a follow version of the including pack, which moves
// no project and is not what new projects get: they get the recommended
// version's contents, toolchain and services included.
func TestRecommendingIsAPersonsActAndMovesNoProject(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	if _, err := a.Ways.CreateBlock(t.Context(), "base/a", "rules", "", "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ways.PublishPack(t.Context(), "base", "Base", `{"guidance":["base/a"]}`, ""); err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/packs", `{"name":"team","includes":"base"}`); w.Code != 200 {
		t.Fatalf("save team: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/pack", `{"name":"team"}`); w.Code != 200 {
		t.Fatalf("apply: %d %s", w.Code, w.Body.String())
	}
	before, err := a.Adoption.CurrentReceipt(t.Context(), idstest.N(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ways.PublishPack(t.Context(), "base", "Base", `{"guidance":["base/a"],"toolchain":"go","services":[{"capability":"postgres"}]}`, ""); err != nil {
		t.Fatal(err)
	}
	agent := tokenFor(t, a, "boss-agent", "boss", "admin", access.TokenAgent)
	refusedAsAgent(t, "recommend", bearerCall(a, agent, "POST", "/api/v1/packs/base/recommended", `{"version":2}`))
	if h, _ := a.Ways.PackHistory(t.Context(), "base"); h.Recommended != 1 {
		t.Fatalf("a refused recommendation moved it: %+v", h)
	}
	cli := tokenFor(t, a, "boss-cli", "boss", "admin", access.TokenCLI)
	if w := bearerCall(a, cli, "POST", "/api/v1/packs/base/recommended", `{"version":2}`); w.Code != 200 {
		t.Fatalf("recommend with a command-line token: %d %s", w.Code, w.Body.String())
	}
	team, err := a.Ways.PackHistory(t.Context(), "team")
	if err != nil || team.Latest != 2 || team.Recommended != 1 || team.Versions[0].Spec.Toolchain != "go" {
		t.Fatalf("team follows base v2, unrecommended: %+v %v", team, err)
	}
	after, err := a.Adoption.CurrentReceipt(t.Context(), idstest.N(1))
	if err != nil || after.Revision != before.Revision {
		t.Fatalf("a follow version moved a project: %d → %d %v", before.Revision, after.Revision, err)
	}
	if packs, err := adoption.ProjectPacksIn(t.Context(), a.DB, idstest.N(1)); err != nil || len(packs) != 1 || packs[0].Version != 1 || packs[0].Recommended != 1 {
		t.Fatalf("the project stays on team v1: %+v %v", packs, err)
	}
	w := authzCall(h, "owner", "GET", "/api/v1/packs", "")
	var listed []struct {
		Name      string
		Version   int64
		Toolchain string
		Services  []struct{ Capability string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || w.Code != 200 {
		t.Fatalf("pack list: %d %v", w.Code, err)
	}
	for _, p := range listed {
		if p.Name == "team" && (p.Version != 1 || p.Toolchain != "" || len(p.Services) != 0) {
			t.Fatalf("the list offers team's recommended contents, not its newest: %+v", p)
		}
	}
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/open/pack", `{"name":"team"}`); w.Code != 200 {
		t.Fatalf("apply to a second project: %d %s", w.Code, w.Body.String())
	}
	if packs, err := adoption.ProjectPacksIn(t.Context(), a.DB, idstest.N(2)); err != nil || len(packs) != 1 || packs[0].Version != 1 {
		t.Fatalf("a new project gets the recommended version: %+v %v", packs, err)
	}
}

const keptLaneYAML = `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: %s}
spec:
  kind: pipeline
  role: landing
  stages:
    - name: land
      steps:
        - name: accept
          uses: approval
          with: {means: "Approve the landing"}
`

// Dropping or replacing an included workflow or policy changes the pack,
// not the projects already on it: the update offer names what adopting
// leaves in place, and adopting keeps it, beside the project's own policy.
func TestAdoptingADropKeepsWhatTheProjectHasAndSaysSo(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	if _, _, err := a.Ways.PublishWorkflow(t.Context(), fmt.Sprintf(keptLaneYAML, "old-lane"), "fixture"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"strict", "team-strict"} {
		if err := a.Ways.SaveLibraryPolicy(t.Context(), wow.LibraryPolicy{Name: name, Rule: "requireStepBefore", With: map[string]any{"uses": "deploy", "requires": "scan"}}, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Ways.PublishPack(t.Context(), "base", "Base", `{"workflows":["old-lane"],"policies":[{"name":"strict"}]}`, ""); err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/packs", `{"name":"team","includes":"base"}`); w.Code != 200 {
		t.Fatalf("save team: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/pack", `{"name":"team"}`); w.Code != 200 {
		t.Fatalf("apply: %d %s", w.Code, w.Body.String())
	}
	if err := a.Adoption.InstallPolicy(t.Context(), idstest.N(1), "own-rule", "ours", "requireStepBefore", map[string]any{"uses": "deploy", "requires": "lint"}, "", "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.recordAdoption(a.projectByID(context.Background(), idstest.N(1)), "edit", "fixture installs the project's own policy", "fixture"); err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/packs", `{"name":"team","includes":"base","drops":[{"item":"old-lane","reason":"we land another way"}],"replaces":{"strict":"team-strict"}}`); w.Code != 200 {
		t.Fatalf("save team v2: %d %s", w.Code, w.Body.String())
	}
	w := authzCall(h, "owner", "POST", "/api/v1/projects/game/adoption/prepare", `{"packs":[{"pack":"team","version":2}]}`)
	var cand struct {
		ExpectedRevision int64            `json:"expectedRevision"`
		Packs            []map[string]any `json:"packs"`
		Kept             []string         `json:"kept"`
		Problems         []string         `json:"problems"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &cand); err != nil || w.Code != 200 || len(cand.Problems) != 0 {
		t.Fatalf("prepare: %d %s %v", w.Code, w.Body.String(), err)
	}
	if strings.Join(cand.Kept, ",") != "workflow old-lane,policy strict" {
		t.Fatalf("the offer names what adopting keeps, and not the project's own policy: %v", cand.Kept)
	}
	body, _ := json.Marshal(map[string]any{"expectedRevision": cand.ExpectedRevision, "packs": cand.Packs, "viaChange": false})
	if w := authzCall(h, "boss", "POST", "/api/v1/projects/game/adoption/adopt", string(body)); w.Code != 200 {
		t.Fatalf("adopt: %d %s", w.Code, w.Body.String())
	}
	got, err := a.Adoption.CurrentReceipt(t.Context(), idstest.N(1))
	if err != nil {
		t.Fatal(err)
	}
	lanes := []string{}
	for _, wf := range got.Receipt.Workflows {
		if !wf.System {
			lanes = append(lanes, wf.Name)
		}
	}
	policies := []string{}
	for _, p := range got.Receipt.Policies {
		policies = append(policies, p.Name)
	}
	if !slices.Contains(lanes, "old-lane") || !slices.Contains(policies, "strict") || !slices.Contains(policies, "own-rule") {
		t.Fatalf("adopting kept the lane and both policies: lanes %v policies %v", lanes, policies)
	}
}

// A pack the instance no longer offers takes no new project, through any
// door: applying it, adopting a selection, opening an uplift Change, or an
// agent creating a project with it. A project already on it keeps it and
// may move between its versions (review PI2-01).
func TestAPackNoLongerOfferedTakesNoNewProject(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	for _, name := range []string{"kept-pack", "other-pack"} {
		if _, err := a.Ways.PublishPack(t.Context(), name, name, `{}`, ""); err != nil {
			t.Fatal(err)
		}
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/projects/open/pack", `{"name":"kept-pack"}`); w.Code != 200 {
		t.Fatalf("an adopter while it was offered: %d %s", w.Code, w.Body.String())
	}
	if _, err := a.Ways.PublishPack(t.Context(), "kept-pack", "kept-pack", `{"description":"v2"}`, ""); err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/packs/kept-pack/enabled", `{"enabled":false}`); w.Code != 200 {
		t.Fatalf("stop offering: %d %s", w.Code, w.Body.String())
	}
	revision := func(projectID ids.ID) int64 {
		t.Helper()
		r, err := a.Adoption.CurrentReceipt(t.Context(), projectID)
		if err != nil {
			t.Fatal(err)
		}
		return r.Revision
	}
	before := revision(idstest.N(1))
	refused := func(door string, _ any, code int, body string) {
		t.Helper()
		if code != 409 || !strings.Contains(body, "no longer offered") {
			t.Fatalf("%s took a pack no longer offered: %d %s", door, code, body)
		}
	}
	w := authzCall(h, "boss", "POST", "/api/v1/projects/game/pack", `{"name":"kept-pack"}`)
	refused("applying it", w, w.Code, w.Body.String())
	for _, viaChange := range []bool{false, true} {
		body := fmt.Sprintf(`{"expectedRevision":%d,"packs":[{"pack":"kept-pack","version":1}],"viaChange":%v}`, before, viaChange)
		w := authzCall(h, "boss", "POST", "/api/v1/projects/game/adoption/adopt", body)
		refused(fmt.Sprintf("adopting it (via a Change: %v)", viaChange), w, w.Code, w.Body.String())
	}
	if after := revision(idstest.N(1)); after != before {
		t.Fatalf("a refused adoption recorded revision %d", after)
	}
	agent := mcp_token(t, a, "boss-agent", "boss", "admin")
	if res := mcp_call(t, a, agent, "create_project", map[string]any{"slug": "late", "name": "Late", "pack": "kept-pack"}); res["isError"] != true {
		t.Fatalf("an agent created a project with a pack no longer offered: %v", res)
	}
	// The project already on it moves to its other version.
	open := revision(idstest.N(2))
	body := fmt.Sprintf(`{"expectedRevision":%d,"packs":[{"pack":"kept-pack","version":2}],"viaChange":false}`, open)
	if w := authzCall(h, "boss", "POST", "/api/v1/projects/open/adoption/adopt", body); w.Code != 200 {
		t.Fatalf("an existing adopter moving versions: %d %s", w.Code, w.Body.String())
	}
	if packs, err := adoption.ProjectPacksIn(t.Context(), a.DB, idstest.N(2)); err != nil || len(packs) != 1 || packs[0].Version != 2 {
		t.Fatalf("the existing adopter's membership: %+v %v", packs, err)
	}
}

// A capability a pack declares it requires survives the save into the
// publication, where adoption enforces it.
//
// Kills: a save that built the stored spec without requires, so a pack
// published as needing a scanner could be adopted without one.
func TestASavedPackKeepsTheCapabilitiesItRequires(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	if w := authzCall(a.Handler(), "boss", "POST", "/api/v1/packs", `{"name":"needs-scanner","requires":["scanner"]}`); w.Code != 200 {
		t.Fatalf("save with requires: %d %s", w.Code, w.Body.String())
	}
	v, err := wow.PackVersionIn(t.Context(), a.DB, "needs-scanner", 1)
	var stored wow.PackSpec
	if err != nil || json.Unmarshal([]byte(v.SpecJSON), &stored) != nil || !slices.Equal(stored.Requires, []string{"scanner"}) {
		t.Fatalf("the saved publication lost requires: %+v %v", v, err)
	}
}

// A pack or block saved with apiVersion apptown.dev/v1 is the same
// publication as one without it; another version is refused by name.
func TestASaveMayStateTheFormatVersion(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	h := a.Handler()
	if w := authzCall(h, "boss", "POST", "/api/v1/packs", `{"name":"stated","guidance":[]}`); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	w := authzCall(h, "boss", "POST", "/api/v1/packs", `{"apiVersion":"apptown.dev/v1","name":"stated","guidance":[]}`)
	var saved struct{ Version int64 }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.Version != 1 {
		t.Fatalf("stating v1 made another publication: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/packs", `{"apiVersion":"apptown.dev/v2","name":"stated"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "apptown.dev/v2") {
		t.Fatalf("a v2 pack: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/guidance/blocks", `{"apiVersion":"apptown.dev/v2","name":"team/x","bodyMd":"Be kind."}`); w.Code != 400 || !strings.Contains(w.Body.String(), "apptown.dev/v2") {
		t.Fatalf("a v2 block: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/guidance/blocks", `{"apiVersion":"apptown.dev/v1","name":"team/x","bodyMd":"Be kind."}`); w.Code != 201 {
		t.Fatalf("a v1 block: %d %s", w.Code, w.Body.String())
	}
}
