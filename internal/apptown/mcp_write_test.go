package apptown

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// The governed-work tools (mcp_write_tools.go): they act as the person via
// the client, the kernel decides, and a consent that did not include the
// act refuses it in words.

func mcpWriteFixture(t *testing.T, configs ...Config) (*App, string) {
	t.Helper()
	a := testApp(t, configs...)

	testAdministrator(t, a, "boss")
	if _, err := a.Access.EnsureUser(context.Background(), "viewer"); err != nil {
		t.Fatal(err)
	}
	fleetRepo(t, a, "app", map[string]string{"README.md": "# app\n"})
	if err := a.EnsureAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, _ := a.Projects.BySlug(context.Background(), "app")
	// The migration made the roster-less project open; close it so only
	// named roles act, then give viewer a viewer's role.
	for _, g := range mustRecords(a.Access.GrantsAt(context.Background(), access.ScopeProject, p.ID.String())) {
		revokeTestGrant(t, a, g.ID, "test")
	}
	a.addGrant("user", "viewer", access.RoleProjectViewer, "", access.ScopeProject, p.ID.String(), "", "boss", "")
	return a, mcp_token(t, a, "boss", "boss", "admin")
}

func TestMCPCreateAndAbandonChange(t *testing.T) {
	t.Parallel()
	a, boss := mcpWriteFixture(t)
	// initialize so the acts are attributed via the client's name
	w := mcp_post(a, boss, "", mcp_rpc(1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "clientInfo": map[string]any{"name": "Claude Code", "version": "1"}}))
	sid := w.Header().Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatalf("initialize: %d %s", w.Code, w.Body.String())
	}
	res := mcp_call(t, a, boss, "create_change", map[string]any{"slug": "app", "title": "Add a title", "intent": "the README needs a real title"})
	sc, _ := res["structuredContent"].(map[string]any)
	if sc == nil || sc["state"] != "in_progress" || sc["webUrl"] == "" {
		t.Fatalf("create_change: %v", res)
	}
	id := idstest.Parse(t, sc["id"].(string))
	ch, err := a.changeByID(context.Background(), id)
	if err != nil || ch.Title != "Add a title" || ch.Body != "the README needs a real title" {
		t.Fatalf("the change was not opened as asked: %+v %v", ch, err)
	}
	var who string
	for _, entry := range ledgerEntries(t, a, ch.ProjectID, "change.created") {
		if exactText(entry.Payload, "changeId") == id.String() {
			who = entry.Actor
		}
	}
	if !strings.HasPrefix(who, "boss via mcp/") {
		t.Errorf("the ledger should say the person via the client, got %q", who)
	}

	res = mcp_call(t, a, boss, "abandon_change", map[string]any{"id": id, "reason": "not needed"})
	sc, _ = res["structuredContent"].(map[string]any)
	if sc == nil || sc["state"] != "abandoned" {
		t.Fatalf("abandon_change: %v", res)
	}
	// A complaint from the MCP lands in the box as the person via the client.
	res = mcp_call(t, a, boss, "file_complaint", map[string]any{"text": "the preview took a minute to say it was starting", "slug": "app", "why": "I waited", "wish": "say so at once"})
	if res["isError"] == true {
		t.Fatalf("file_complaint: %v", res)
	}
	complaints, err := a.Complaints.All(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var filed bool
	for _, item := range complaints {
		if item.Text == "the preview took a minute to say it was starting" {
			filed = item.Source == "mcp" && strings.HasPrefix(item.Actor, "boss via mcp/")
		}
	}
	if !filed {
		t.Errorf("complaint provenance: %+v", complaints)
	}
	// Nothing more can be done to it, in a sentence.
	res = mcp_call(t, a, boss, "request_change_landing", map[string]any{"id": id})
	if res["isError"] != true || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "abandoned") {
		t.Fatalf("acting on an abandoned change should refuse: %v", res)
	}
}

func TestMCPWritesAreBoundedByRoleAndConsent(t *testing.T) {
	t.Parallel()
	a, _ := mcpWriteFixture(t)
	viewer := mcp_token(t, a, "viewer", "viewer", "member")
	// A viewer's personal token carries the viewer's whole (small) authority.
	res := mcp_call(t, a, viewer, "create_change", map[string]any{"slug": "app", "title": "x"})
	if res["isError"] != true {
		t.Fatalf("a viewer may not open changes: %v", res)
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "viewer may not open a change") || !strings.Contains(text, "Project contributor") {
		t.Errorf("the refusal should say who, what and which role carries it: %s", text)
	}
	// A denied consequential act is receipted and audited.
	var n int
	// sql-guarantee: authorization_decisions is write-only receipt storage; no owner read lists decisions
	_ = a.DB.QueryRow(`SELECT COUNT(*) FROM authorization_decisions WHERE outcome='deny' AND principal='viewer' AND capability='change:create'`).Scan(&n)
	if n == 0 {
		t.Error("a refused MCP write should leave a decision receipt")
	}
	// An OAuth delegation narrows even the sovereign: boss consented to reads only.
	client, err := a.Access.RegisterOAuthClient(context.Background(), identity.Registration{Name: "Codex", RedirectURIs: []string{"http://127.0.0.1:9999/callback"}})
	if err != nil {
		t.Fatal(err)
	}
	verifier, challenge := pkce()
	browser := identity.BrowserAuthorization{User: "boss", Binding: "session", Request: identity.AuthorizationRequest{
		ClientID: client.ID, RedirectURI: "http://127.0.0.1:9999/callback", Scope: "apptown:user:read", Challenge: challenge, Method: "S256", Resource: identity.AudienceUser,
	}}
	prepared, err := a.Access.PrepareConsent(context.Background(), browser)
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Access.DecideConsent(context.Background(), identity.ConsentDecision{Browser: browser, Token: prepared.Token, Choice: identity.ConsentAllow})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := a.Access.ExchangeCode(context.Background(), identity.CodeExchange{Code: result.Code, Verifier: verifier, ClientID: client.ID, RedirectURI: browser.Request.RedirectURI, AuthMethod: "internal"})
	if err != nil {
		t.Fatal(err)
	}
	res = mcp_call(t, a, pair.Access, "create_change", map[string]any{"slug": "app", "title": "x"})
	if res["isError"] != true {
		t.Fatalf("a read-only delegation may not open changes: %v", res)
	}
	text = res["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "connected agent was not allowed to open a change") {
		t.Errorf("the refusal should name the consent, not the person's role: %s", text)
	}
	if mcp_call(t, a, pair.Access, "list_projects", map[string]any{})["isError"] == true {
		t.Error("the read-only delegation still reads")
	}
}

// create_project: the person's personal group owns it by default, the repo
// is initialised, the pack applied; a viewer is refused; a young project
// moves between groups with one call.
func TestMCPCreateProject(t *testing.T) {
	t.Parallel()
	a, boss := mcpWriteFixture(t)
	a.SyncStock(t.Context()) // the packs and their guidance, as apptownd has them at boot
	res := mcp_call(t, a, boss, "create_project", map[string]any{"slug": "snake", "name": "Snake", "blurb": "a snake game", "pack": "html-game"})
	sc, _ := res["structuredContent"].(map[string]any)
	if sc == nil || res["isError"] == true {
		t.Fatalf("create_project: %v", res)
	}
	p, err := a.Projects.BySlug(context.Background(), "snake")
	if err != nil || p.DefaultBranch == "" {
		t.Fatalf("the project should exist with a repo: %+v %v", p, err)
	}
	if sc["ownerGroup"] != "boss" || p.OwnerGroup != "boss" {
		t.Errorf("the creator's personal group owns it: %v", sc["ownerGroup"])
	}
	if pk, _ := sc["pack"].(map[string]any); pk == nil || pk["name"] != "html-game" || pk["error"] != nil {
		t.Errorf("the pack should be applied: %v", sc["pack"])
	}
	if !slices.ContainsFunc(mustRecords(a.Access.ActiveGrants(context.Background(), access.Principal{Kind: "user", Name: "boss"})), func(g access.Grant) bool {
		return g.RoleName == access.RoleProjectOwner && g.ScopeType == access.ScopeProject && g.ScopeID == p.ID.String()
	}) {
		t.Error("the creator holds Project owner")
	}
	// The next call in the story works: a Change on the new project.
	res = mcp_call(t, a, boss, "create_change", map[string]any{"slug": "snake", "title": "first bite"})
	if res["isError"] == true {
		t.Fatalf("create_change on the new project: %v", res)
	}
	// A viewer cannot create projects.
	viewer := mcp_token(t, a, "viewer", "viewer", "member")
	res = mcp_call(t, a, viewer, "create_project", map[string]any{"slug": "nope", "name": "Nope"})
	if res["isError"] != true || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "viewer may not create a project") {
		t.Fatalf("a viewer must be refused in words: %v", res)
	}
	if p, err := a.Projects.BySlug(context.Background(), "nope"); err == nil {
		t.Fatalf("denied project persisted: %+v", p)
	}
	// Transfer a young project (no changes) between groups in one call.
	res2 := mcp_call(t, a, boss, "create_project", map[string]any{"slug": "young", "name": "Young"})
	if res2["isError"] == true {
		t.Fatal(res2)
	}
	if err := a.Access.SaveGroup(context.Background(), identity.GroupChange{Name: "platform"}, access.Principal{Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	w := authzCall(a.Handler(), "boss", "POST", "/api/v1/projects/young/owner", `{"group":"platform"}`)
	if w.Code != 200 {
		t.Fatalf("transfer: %d %s", w.Code, w.Body.String())
	}
	py, _ := a.Projects.BySlug(context.Background(), "young")
	if py.OwnerGroup != "platform" {
		t.Error("the young project should now belong to platform")
	}
	w = authzCall(a.Handler(), "boss", "POST", "/api/v1/projects/snake/owner", `{"group":"platform"}`)
	if w.Code != 409 {
		t.Errorf("a project with history is not moved by this button: %d", w.Code)
	}
}

// A change acted on whose re-read then fails is an error saying the act
// happened, never a crash: abandon_change abandons it, and the tool reports
// that reading it again failed.
func TestMCPAbandonWhoseReReadFailsSaysSo(t *testing.T) {
	t.Parallel()
	a, boss := mcpWriteFixture(t)
	res := mcp_call(t, a, boss, "create_change", map[string]any{"slug": "app", "title": "Add a title", "intent": "the README needs a real title"})
	sc, _ := res["structuredContent"].(map[string]any)
	if sc == nil {
		t.Fatalf("create_change: %v", res)
	}
	id := idstest.Parse(t, sc["id"].(string))
	// The tool reads the change once to authorize the act and once after
	// it; the second read fails.
	t.Cleanup(a.DB.FailStatementAfter("-- name: ChangeByID ", 1))
	w := mcp_post(a, boss, "", mcp_rpc(7, "tools/call", map[string]any{"name": "abandon_change", "arguments": map[string]any{"id": id, "reason": "not needed"}}))
	var resp struct{ Error *rpcError }
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Error == nil || !strings.Contains(resp.Error.Message, "Change "+id.String()+" is abandoned, but it could not be read again") {
		t.Fatalf("abandon_change with a failed re-read: %d %s", w.Code, w.Body.String())
	}
	if ch, err := a.changeByID(context.Background(), id); err != nil || ch.State != workflow.ChangeAbandoned {
		t.Fatalf("the change should be abandoned: %+v %v", ch, err)
	}
}
