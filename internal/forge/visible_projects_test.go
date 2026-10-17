package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/adrs"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/usage"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestVisibleProjectAggregatesKeepSectionsScopedWithoutReadReceipts(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	// Close the fixture's second public project as well as transparent reads.
	grants, err := a.Access.GrantsAt(context.Background(), access.ScopeProject, idstest.N(2).String())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range grants {
		if g.Subject == identity.GroupAllUsers {
			if _, err := a.Access.Revoke(context.Background(), g.ID, "boss"); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, who := range []string{"project-reader", "run-reader", "no-projects"} {
		if _, err := a.Access.EnsureUser(context.Background(), who); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []identity.Spec{
		{SubjectType: "user", Subject: "project-reader", Capability: string(access.CapProjectRead), Scope: access.ScopeProject, ScopeID: idstest.N(1).String()},
		{SubjectType: "user", Subject: "run-reader", Capability: string(access.CapRunRead), Scope: access.ScopeProject, ScopeID: idstest.N(1).String()},
	} {
		if _, err := a.Access.Grant(context.Background(), g, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, pid := range []ids.ID{idstest.N(1), idstest.N(2)} {
		if _, err := a.ADRs.Create(asPerson("boss"), adrs.Create{ProjectID: pid, Title: fmt.Sprintf("decision-project-%d", pid), BodyMD: fmt.Sprintf("private-body-%d", pid)}); err != nil {
			t.Fatal(err)
		}
	}
	ch, err := a.Work.Create(asPerson("boss"), workflow.ChangeDraft{ProjectID: idstest.N(2), Title: "foreign-work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.StartRun(asPerson("boss"), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "implement"}); err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	if _, err := a.Ways.PublishPack(t.Context(), "visibility-pack", "Shared pack", `{}`, ""); err != nil {
		t.Fatal(err)
	}
	pack, err := a.Ways.Pack(t.Context(), "visibility-pack")
	if err != nil {
		t.Fatal(err)
	}
	for _, pid := range []ids.ID{idstest.N(1), idstest.N(2)} {
		if _, err := a.lifecycle.ApplyPack(asPerson("boss"), a.projectByID(context.Background(), pid), pack, "boss"); err != nil {
			t.Fatal(err)
		}
	}
	tokens := map[string]string{}
	for _, who := range []string{"owner", "project-reader", "run-reader", "no-projects", "boss"} {
		role := "member"
		if who == "boss" {
			role = "admin"
		}
		tokens[who] = mcp_token(t, a, who, who, role)
		// Opening the MCP session is audited once; reads inside it are not.
		mcp_session(a, tokens[who])
	}
	auditBefore, err := audit.Recent(context.Background(), a.DB, audit.Filter{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	// If an aggregate records a decision, it consumes this failure. The explicit
	// detail request afterwards must still encounter it (HTTP logs receipt errors).
	clear := a.DB.FailNextStatement("-- name: InsertDecision")
	defer clear()
	h := a.Handler()
	for _, who := range []string{"owner", "project-reader", "run-reader", "no-projects", "boss"} {
		wantProject := who == "owner" || who == "project-reader" || who == "boss"
		wantWork := who == "owner" || who == "run-reader" || who == "boss"
		all := who == "boss"
		w := authzCall(h, who, "GET", "/api/v1/projects", "")
		var projects []struct {
			Slug        string
			ActiveItems int
			AdrCount    int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &projects); err != nil || w.Code != 200 {
			t.Fatalf("%s projects: %d %s %v", who, w.Code, w.Body.String(), err)
		}
		want := 0
		if wantProject {
			want = 1
		}
		if all {
			want = 2
		}
		if len(projects) != want {
			t.Fatalf("%s projects: %+v", who, projects)
		}
		for _, p := range projects {
			if !all && p.Slug != "game" {
				t.Fatalf("foreign project: %+v", p)
			}
			if p.AdrCount != 1 {
				t.Fatalf("count: %+v", p)
			}
			if !wantWork && p.ActiveItems != 0 {
				t.Fatalf("run activity leaked: %+v", p)
			}
		}
		w = authzCall(h, who, "GET", "/api/v1/projects?limit=1", "")
		var page struct {
			Projects []struct {
				ID   ids.ID
				Slug string
			}
			Total int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || page.Total != want || len(page.Projects) != min(1, want) {
			t.Fatalf("%s project page: %d %s %v", who, w.Code, w.Body.String(), err)
		}
		w = authzCall(h, who, "GET", "/api/v1/projects?recent=1", "")
		var recent []struct {
			ID   ids.ID
			Slug string
		}
		if err := json.Unmarshal(w.Body.Bytes(), &recent); err != nil || w.Code != 200 || len(recent) != min(1, want) {
			t.Fatalf("%s recent page: %d %s %v", who, w.Code, w.Body.String(), err)
		}
		if len(recent) > 0 && !all && recent[0].Slug != "game" {
			t.Fatalf("foreign recency leaked: %+v", recent)
		}
		w = authzCall(h, who, "GET", "/api/v1/projects?limit=1&offset=100", "")
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || page.Total != want || len(page.Projects) != 0 {
			t.Fatalf("%s offset/total: %d %s %v", who, w.Code, w.Body.String(), err)
		}
		w = authzCall(h, who, "GET", "/api/v1/dashboard", "")
		var dash struct {
			PendingApprovals []workflow.PendingApproval
			ActiveRuns       []workflow.ActiveRun
			RecentADRs       []adrs.Record
		}
		if err := json.Unmarshal(w.Body.Bytes(), &dash); err != nil || w.Code != 200 {
			t.Fatalf("%s dashboard: %d %s %v", who, w.Code, w.Body.String(), err)
		}
		if len(dash.RecentADRs) != want {
			t.Fatalf("%s ADR section: %+v", who, dash)
		}
		for _, row := range dash.RecentADRs {
			if !all && row.ProjectID != idstest.N(1) {
				t.Fatalf("foreign ADR: %+v", row)
			}
		}
		workCount := len(dash.PendingApprovals) + len(dash.ActiveRuns)
		if (workCount > 0) != wantWork {
			t.Fatalf("%s Work section: %+v", who, dash)
		}
		for _, row := range dash.PendingApprovals {
			if !all && row.ProjectSlug != "game" {
				t.Fatalf("foreign approval: %+v", row)
			}
		}
		for _, row := range dash.ActiveRuns {
			if !all && row.ProjectSlug != "game" {
				t.Fatalf("foreign run: %+v", row)
			}
		}
		w = authzCall(h, who, "GET", "/api/v1/adrs", "")
		var records []adrs.Record
		if err := json.Unmarshal(w.Body.Bytes(), &records); err != nil || w.Code != 200 || len(records) != want {
			t.Fatalf("%s library: %d %s %v", who, w.Code, w.Body.String(), err)
		}
		for _, row := range records {
			if !all && row.ProjectID != idstest.N(1) {
				t.Fatalf("foreign library record: %+v", row)
			}
		}
		w = authzCall(h, who, "GET", "/api/v1/wow", "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "visibility-pack") || strings.Contains(w.Body.String(), `"game"`) != wantProject || strings.Contains(w.Body.String(), `"open"`) != all {
			t.Fatalf("%s fleet: %d %s", who, w.Code, w.Body.String())
		}
		result := mcp_call(t, a, tokens[who], "list_projects", nil)
		body, _ := json.Marshal(result)
		if result["isError"] == true || strings.Contains(string(body), `"slug":"game"`) != wantProject || strings.Contains(string(body), `"slug":"open"`) != all {
			t.Fatalf("%s MCP: %s", who, body)
		}
	}
	auditAfter, err := audit.Recent(context.Background(), a.DB, audit.Filter{Limit: 500})
	if err != nil || !reflect.DeepEqual(auditBefore, auditAfter) {
		t.Fatalf("aggregate wrote audit: %v", err)
	}
	// Calling the kernel's explicit receipt operation must encounter the still-
	// armed fault. A swallowed HTTP recording error cannot make this test pass.
	if _, err := a.Access.Decide(context.Background(), access.Principal{Kind: "user", Name: "no-projects"}, access.Permission{Capability: access.CapProjectRead, Resource: access.Resource{Type: "project", ProjectID: idstest.N(1)}}); err == nil || !strings.Contains(err.Error(), "forge_injected_failure") {
		t.Fatalf("aggregate consumed decision tripwire: %v", err)
	}
	clear()
	if w := authzCall(h, "no-projects", "GET", "/api/v1/projects/game", ""); w.Code != 403 {
		t.Fatalf("detail refusal: %d %s", w.Code, w.Body.String())
	}
	auditAfter, err = audit.Recent(context.Background(), a.DB, audit.Filter{KindPrefix: "authz.denied", Limit: 500})
	if err != nil || len(auditAfter) == 0 {
		t.Fatalf("explicit detail lost denial receipt: %+v %v", auditAfter, err)
	}
	// Lookup failures are failures, not successfully empty/partial pages.
	for _, path := range []string{"/api/v1/projects", "/api/v1/dashboard", "/api/v1/adrs", "/api/v1/wow"} {
		clear := a.DB.FailNextStatement("-- name: ProjectAuthorityCatalogue")
		w := authzCall(h, "owner", "GET", path, "")
		clear()
		if w.Code != 500 || strings.Contains(w.Body.String(), "private-body-") {
			t.Fatalf("%s hid lookup failure: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, query := range []string{"-- name: FleetMissingSelections", "-- name: PackVersions"} {
		clear := a.DB.FailNextStatement(query)
		w := authzCall(h, "owner", "GET", "/api/v1/wow", "")
		clear()
		if w.Code != 500 || strings.Contains(w.Body.String(), `"packs":`) {
			t.Fatalf("fleet returned partial data after %s: %d %s", query, w.Code, w.Body.String())
		}
	}
	clear = a.DB.FailNextStatement("-- name: ProjectAuthorityCatalogue")
	w := mcp_post(a, tokens["owner"], "", mcp_rpc(9, "tools/call", map[string]any{"name": "list_projects", "arguments": map[string]any{}}))
	clear()
	var failed struct {
		Error  *rpcError
		Result json.RawMessage
	}
	if err := json.Unmarshal(w.Body.Bytes(), &failed); err != nil || failed.Error == nil || failed.Error.Code != -32603 || len(failed.Result) > 0 {
		t.Fatalf("MCP hid lookup failure: %s %v", w.Body.String(), err)
	}

}

func TestProjectPageRejectsInvalidBounds(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	for _, q := range []string{"limit=0", "limit=101", "limit=oops", "limit=1&offset=-1", "limit=1&offset=oops", "recent=0", "recent=101", "recent=1&limit=1"} {
		if w := authzCall(a.Handler(), "boss", "GET", "/api/v1/projects?"+q, ""); w.Code != 400 {
			t.Fatalf("%s: %d %s", q, w.Code, w.Body.String())
		}
	}
}

// An eval's throwaway copy is no project in the lists: the project list
// and page and the conformance estate leave it out, and its spend shows on
// the usage page as the evals line, never under its own name.
func TestEvalCopiesAreLeftOutOfProjectListsAndSpendOnTheEvalsLine(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	real, copy := createReadTestProject(t, a, "shop"), createReadTestProject(t, a, "shop-copy")
	evalCopyForTest(t, a, copy)
	for _, id := range []ids.ID{real, copy} {
		if err := a.Projects.SetDefaultBranch(context.Background(), id, "main"); err != nil {
			t.Fatal(err)
		}
	}
	ctx := asPerson("bls")
	visible, err := a.Projects.Visible(ctx)
	if err != nil || len(visible) != 1 || visible[0].ID != real {
		t.Fatalf("the project list: %+v %v", visible, err)
	}
	page, err := a.Projects.Browse(ctx, project.Browse{Limit: 10})
	if err != nil || page.Total != 1 || len(page.Projects) != 1 || page.Projects[0].ID != real {
		t.Fatalf("the project page: %+v %v", page, err)
	}
	testAccounts(t, a.DB, "admin")
	estate, err := a.conformance.View(asPerson("admin"), ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range estate.Projects {
		if p.Slug == "shop-copy" {
			t.Fatalf("the conformance estate lists the copy: %+v", estate.Projects)
		}
	}
	for _, id := range []ids.ID{real, copy} {
		if err := a.Usage.Ledger.Record(ctx, usage.Entry{Kind: "step", ProjectID: id, Actor: "bls"}, usage.Totals{Total: 10, CostMicros: 5}, ""); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := a.Usage.Ledger.Summary(ctx, 30, ids.Nil, "")
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]int64{}
	for _, l := range rep.ByProject {
		keys[l.Key] = l.Total
	}
	if keys["evals"] != 10 || keys["shop"] != 10 || keys["shop-copy"] != 0 || rep.Totals.Total != 20 {
		t.Fatalf("usage by project: %v, total %d", keys, rep.Totals.Total)
	}
}

// The eval routes ask the evals owner, which asks current grants: someone
// who does not manage packs is refused starting, continuing and deleting
// (reading is the auditor's read); a manager is shown what
// starting means and must send it back; an eval that does not exist is
// not found.
func TestEvalRoutesAskTheEvalsOwner(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	h := a.Handler()
	for _, r := range []struct{ method, path, body string }{
		{"GET", "/api/v1/evals/start", ""}, {"POST", "/api/v1/evals", `{"changes":["00000000-0001-7000-8000-000000000000"],"profile":"standard","budgetDollars":1,"shown":"x"}`},
		{"POST", "/api/v1/evals/00000000-0001-7000-8000-000000000000/continue", "{}"}, {"DELETE", "/api/v1/evals/00000000-0001-7000-8000-000000000000", ""},
	} {
		if w := authzCall(h, "outsider", r.method, r.path, r.body); w.Code != 403 {
			t.Errorf("an outsider %s %s: %d %s", r.method, r.path, w.Code, w.Body.String())
		}
	}
	// Reading evals is standards:read-evals, which the auditor role holds:
	// under the transparent default everyone signed in reads everything,
	// evals included, and changes nothing.
	if w := authzCall(h, "outsider", "GET", "/api/v1/evals", ""); w.Code != 200 {
		t.Errorf("a reader under the transparent default lists evals: %d %s", w.Code, w.Body.String())
	}
	w := authzCall(h, "boss", "GET", "/api/v1/evals/start", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "throwaway copies may land and deploy") {
		t.Fatalf("the start form: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/evals", `{"changes":["00000000-0001-7000-8000-000000000000"],"profile":"standard","budgetDollars":1,"shown":"I agree"}`); w.Code != 400 {
		t.Fatalf("a start not shown the meaning: %d %s", w.Code, w.Body.String())
	}
	for _, r := range []struct{ method, path string }{{"GET", "/api/v1/evals/0000000f-423f-7000-8000-000000000000"}, {"GET", "/api/v1/evals/0000000f-423f-7000-8000-000000000000/arms/00000000-0001-7000-8000-000000000000/diff"}, {"POST", "/api/v1/evals/0000000f-423f-7000-8000-000000000000/continue"}, {"DELETE", "/api/v1/evals/0000000f-423f-7000-8000-000000000000"}} {
		if w := authzCall(h, "boss", r.method, r.path, "{}"); w.Code != 404 {
			t.Errorf("%s %s of no eval: %d %s", r.method, r.path, w.Code, w.Body.String())
		}
	}
	if w := authzCall(h, "boss", "GET", "/api/v1/evals", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"evals":[]`) {
		t.Fatalf("no evals yet: %d %s", w.Code, w.Body.String())
	}
}
