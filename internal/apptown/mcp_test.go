package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The user MCP endpoint (mcp_server.go, mcp_tools.go): the door is
// token-only, the protocol plumbing answers as the spec says, and the
// read-only catalog reads what the web page reads.

// mcp_token mints a personal token through the Identity command and returns
// its plaintext for the MCP request.
func mcp_token(t *testing.T, a *App, name, user, role string) string {
	t.Helper()
	testAdministrator(t, a, "boss")
	if _, err := a.Access.EnsureUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	issued, err := a.Access.IssueToken(context.Background(), identity.NewToken{Purpose: access.TokenAgent, Name: name, User: user, Role: role}, access.Principal{Kind: "user", Name: "boss", Via: "session"})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Plaintext
}

// mcp_raw sends one JSON-RPC body to /mcp/user exactly as given, with an
// optional token and session id and the Accept header every client sends.
func mcp_raw(a *App, token, sid, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/mcp/user", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	return w
}

// mcpTestSessions holds one initialized session per App and token, the way a
// client keeps its session for the life of its connection.
var mcpTestSessions sync.Map

// mcp_session initializes a session for the token and returns its id, or ""
// when the door refused (the caller's next request then shows why).
func mcp_session(a *App, token string) string {
	key := fmt.Sprintf("%p|%s", a, token)
	if sid, ok := mcpTestSessions.Load(key); ok {
		return sid.(string)
	}
	w := mcp_raw(a, token, "", mcp_rpc(0, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "forge-test", "version": "1"}}))
	sid := w.Header().Get("Mcp-Session-Id")
	if sid == "" {
		return ""
	}
	mcp_raw(a, token, sid, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	mcpTestSessions.Store(key, sid)
	return sid
}

// mcp_post sends one JSON-RPC body in the token's session, opening it first
// when no session id is given, as a client does.
func mcp_post(a *App, token, sid, body string) *httptest.ResponseRecorder {
	if sid == "" && token != "" && !strings.Contains(body, `"method":"initialize"`) {
		sid = mcp_session(a, token)
	}
	return mcp_raw(a, token, sid, body)
}

// rpcError is a JSON-RPC error object as a client decodes it.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func mcp_rpc(id int, method string, params any) string {
	m := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		m["params"] = params
	}
	return mustJSON(m)
}

// mcp_call runs tools/call and returns the decoded result object.
func mcp_call(t *testing.T, a *App, token, tool string, args map[string]any) map[string]any {
	t.Helper()
	w := mcp_post(a, token, "", mcp_rpc(7, "tools/call", map[string]any{"name": tool, "arguments": args}))
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", tool, w.Code, w.Body.String())
	}
	var resp struct {
		Result map[string]any `json:"result"`
		Error  *rpcError      `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("%s: rpc error %d %s", tool, resp.Error.Code, resp.Error.Message)
	}
	return resp.Result
}

func TestMCPRequiresPersonalToken(t *testing.T) {
	t.Parallel()
	a := testApp(t)

	// No token: 401 with the realm, even in open mode (the actor header is
	// not a credential here).
	req := httptest.NewRequest("POST", "/mcp/user", strings.NewReader(mcp_rpc(1, "ping", nil)))
	req.Header.Set("X-AppTown-Actor", "bls")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), `realm="apptown-mcp"`) {
		t.Fatalf("no token must 401 with a realm: %d %q %s", w.Code, w.Header().Get("WWW-Authenticate"), w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("401 must carry a JSON error body: %s", w.Body.String())
	}
	// A bad token is a 401 too, never a fall-through.
	if w := mcp_post(a, "forge_nope", "", mcp_rpc(1, "ping", nil)); w.Code != 401 {
		t.Fatalf("bad token must 401: %d", w.Code)
	}
	// A good token opens the door.
	tok := mcp_token(t, a, "laptop", "bls", "member")
	if w := mcp_post(a, tok, "", mcp_rpc(1, "ping", nil)); w.Code != 200 || !strings.Contains(w.Body.String(), `"result":{}`) {
		t.Fatalf("ping with a token: %d %s", w.Code, w.Body.String())
	}
}

func TestMCPInitialize(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{AuthMode: "internal", AuthHeader: "", AuthProxySecret: ""})

	tok := mcp_token(t, a, "laptop", "bls", "member")
	w := mcp_raw(a, tok, "", mcp_rpc(1, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"clientInfo":      map[string]any{"name": "claude-code", "version": "1.2.3"},
		"capabilities":    map[string]any{},
	}))
	if w.Code != 200 {
		t.Fatalf("initialize: %d %s", w.Code, w.Body.String())
	}
	sid := w.Header().Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("initialize must issue a session id")
	}
	var resp struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
			Capabilities map[string]any `json:"capabilities"`
			Instructions string         `json:"instructions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// The client's version is echoed when supported.
	if resp.Result.ProtocolVersion != "2025-03-26" {
		t.Fatalf("protocol negotiation: %q", resp.Result.ProtocolVersion)
	}
	if resp.Result.ServerInfo.Name != "apptown" || resp.Result.ServerInfo.Version == "" {
		t.Fatalf("serverInfo: %+v", resp.Result.ServerInfo)
	}
	if _, ok := resp.Result.Capabilities["tools"]; !ok {
		t.Fatalf("capabilities must advertise tools: %v", resp.Result.Capabilities)
	}
	if !strings.Contains(resp.Result.Instructions, "cannot approve, land, administer, or write main") {
		t.Fatalf("instructions must say what this surface can never do: %q", resp.Result.Instructions)
	}
	// One audit row per session, naming the client.
	var kind, subject, actor string
	if err := a.DB.QueryRow(`SELECT kind, subject, actor FROM audit_log WHERE kind='mcp.session'`).Scan(&kind, &subject, &actor); err != nil {
		t.Fatalf("initialize must audit the session: %v", err)
	}
	if subject != "claude-code" || actor != "bls" {
		t.Fatalf("audit row: %s %s %s", kind, subject, actor)
	}
	// The session remembers the client: receipts from its calls name it.
	createReadTestProject(t, a, "shop")
	if w := mcp_raw(a, tok, sid, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); w.Code != 202 || w.Body.Len() != 0 {
		t.Fatalf("notification must be 202 with an empty body: %d %q", w.Code, w.Body.String())
	}
	if w := mcp_raw(a, tok, sid, mcp_rpc(2, "tools/call", map[string]any{"name": "create_change", "arguments": map[string]any{"slug": "shop", "title": "from the session"}})); w.Code != 200 || strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("create_change in the session: %d %s", w.Code, w.Body.String())
	}
	entries, err := audit.Recent(context.Background(), a.DB, audit.Filter{Limit: audit.MaxLimit})
	attributed := false
	for _, e := range entries {
		attributed = attributed || e.Actor == "bls via mcp/claude-code"
	}
	if err != nil || !attributed {
		t.Fatalf("a call in the session must be attributed to its client: %v %+v", err, entries)
	}
	// An unknown protocol version gets the newest one this AppTown speaks.
	w = mcp_raw(a, tok, "", mcp_rpc(3, "initialize", map[string]any{"protocolVersion": "1999-01-01", "capabilities": map[string]any{}}))
	if w.Code != 200 || strings.Contains(w.Body.String(), `"protocolVersion":"1999-01-01"`) || !strings.Contains(w.Body.String(), `"protocolVersion":"20`) {
		t.Fatalf("unsupported version must fall back to a supported one: %s", w.Body.String())
	}
	// Someone else's session id is refused, and nothing runs in it.
	other := mcp_token(t, a, "other", "kid", "member")
	if w := mcp_raw(a, other, sid, mcp_rpc(4, "ping", nil)); w.Code != 403 {
		t.Fatalf("another user's session id must be refused: %d %s", w.Code, w.Body.String())
	}
}

func TestMCPToolsList(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	tok := mcp_token(t, a, "laptop", "bls", "member")
	w := mcp_post(a, tok, "", mcp_rpc(1, "tools/list", nil))
	if w.Code != 200 {
		t.Fatalf("tools/list: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	want := []string{"list_projects", "get_project", "get_project_guidance", "get_application_contract", "list_changes", "get_change",
		"get_change_diff", "get_change_evidence", "get_run", "get_waiting_on_you",
		"create_change", "start_change_lane", "run_change_checks", "preview_change", "request_change_landing",
		"send_change_back", "abandon_change", "checkout_change", "renew_change_grant", "sync_change", "file_complaint", "create_project", "get_what_if", "get_eval", "get_operation"}
	// The SDK lists tools by name; the catalog is the set.
	sort.Strings(want)
	if len(resp.Result.Tools) != len(want) {
		t.Fatalf("want %d tools, got %d", len(want), len(resp.Result.Tools))
	}
	for i, tl := range resp.Result.Tools {
		if tl.Name != want[i] {
			t.Fatalf("tool %d: want %s got %s", i, want[i], tl.Name)
		}
		if tl.Description == "" {
			t.Fatalf("%s has no description", tl.Name)
		}
		if tl.InputSchema["type"] != "object" || tl.InputSchema["properties"] == nil || tl.InputSchema["additionalProperties"] != false {
			t.Fatalf("%s schema must be a closed object: %v", tl.Name, tl.InputSchema)
		}
		if _, ok := tl.InputSchema["required"]; !ok {
			t.Fatalf("%s schema must list required (even when empty): %v", tl.Name, tl.InputSchema)
		}
		var required []string
		for _, r := range tl.InputSchema["required"].([]any) {
			required = append(required, r.(string))
		}
		sort.Strings(required)
		if got := strings.Join(required, ","); got != wantRequired[tl.Name] {
			t.Errorf("%s requires %q, want %q: a required argument is renamed or added only in a new tool (compatibility.md)", tl.Name, got, wantRequired[tl.Name])
		}
	}
}

// wantRequired is each MCP tool's required arguments, which 1.x keeps: a
// tool may gain optional arguments, never a renamed or a new required one.
var wantRequired = map[string]string{
	"abandon_change": "id,reason", "checkout_change": "id", "create_change": "slug,title", "create_project": "name,slug",
	"file_complaint": "text", "get_application_contract": "slug", "get_change": "id", "get_change_diff": "id",
	"get_change_evidence": "id", "get_eval": "", "get_operation": "id", "get_project": "slug",
	"get_project_guidance": "slug", "get_run": "id", "get_waiting_on_you": "", "get_what_if": "",
	"list_changes": "slug", "list_projects": "", "preview_change": "id", "renew_change_grant": "id",
	"request_change_landing": "id", "run_change_checks": "id", "send_change_back": "id,note",
	"start_change_lane": "id,lane", "sync_change": "id,pushed_sha",
}

func TestMCPListProjectsAndGetProject(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	pid := createReadTestProject(t, a, "shop")
	createReadTestChange(t, a, pid, "add the cart")
	tok := mcp_token(t, a, "laptop", "bls", "member")

	res := mcp_call(t, a, tok, "list_projects", map[string]any{})
	// Both renderings are present and agree (MCP 2025-06-18).
	sc, _ := res["structuredContent"].(map[string]any)
	projects, _ := sc["projects"].([]any)
	if len(projects) != 1 {
		t.Fatalf("list_projects: %v", res)
	}
	p := projects[0].(map[string]any)
	if p["slug"] != "shop" || p["openChanges"] != float64(1) || !strings.HasSuffix(p["webUrl"].(string), "/projects/shop") {
		t.Fatalf("project row: %v", p)
	}
	content, _ := res["content"].([]any)
	if len(content) != 1 || !strings.Contains(content[0].(map[string]any)["text"].(string), `"slug": "shop"`) {
		t.Fatalf("content must carry the pretty JSON: %v", content)
	}

	target, err := a.Deployment.Targets.Put(t.Context(), &deployment.Target{Name: "mcp-local", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Deployments.EnsureStanding(t.Context(), pid, "main", target.ID, (deployment.Attributes{}).WithDefaults()); err != nil {
		t.Fatal(err)
	}
	res = mcp_call(t, a, tok, "get_project", map[string]any{"slug": "shop"})
	sc = res["structuredContent"].(map[string]any)
	if sc["slug"] != "shop" || sc["hasRepository"] != false {
		t.Fatalf("get_project: %v", sc)
	}
	for _, k := range []string{"lanes", "systemWorkflows", "guidance", "deployments"} {
		if _, ok := sc[k].([]any); !ok {
			t.Fatalf("get_project must carry %s as a list: %v", k, sc[k])
		}
	}
	placed := sc["deployments"].([]any)
	if len(placed) != 1 || placed[0].(map[string]any)["deploymentPolicy"] != "compatible-update" {
		t.Fatalf("MCP deployment procedure: %+v", placed)
	}
	// A project that does not exist is the caller's problem, not the protocol's.
	res = mcp_call(t, a, tok, "get_project", map[string]any{"slug": "ghost"})
	if res["isError"] != true || res["structuredContent"] != nil {
		t.Fatalf("unknown project must be isError: %v", res)
	}
	// A repo-less project cannot have a contract.
	res = mcp_call(t, a, tok, "get_application_contract", map[string]any{"slug": "shop"})
	if res["isError"] != true || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "is not repo-backed") {
		t.Fatalf("contract without a repo must refuse plainly: %v", res)
	}
}

func TestMCPChangesAndRuns(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	pid := createReadTestProject(t, a, "shop")
	chID := createReadTestChange(t, a, pid, "add the cart")
	if _, err := a.Access.PutGrant(context.Background(), identity.Spec{SubjectType: "user", Subject: "bls", Role: access.RoleProjectApprover, Scope: access.ScopeProject, ScopeID: fmt.Sprint(pid), Origin: "test", By: "bls"}); err != nil {
		t.Fatal(err)
	}

	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Automatic = []string{"agent/pi"}
		deps.Runners = map[string]workflow.Runner{"agent/pi": func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass, Evidence: json.RawMessage(`{"model":"m"}`)}, nil); err != nil {
				t.Error(err)
			}
		}}
	})
	snapshot := startReadTestRun(t, a, chID, schema.Stage{Name: "work", Steps: []schema.Step{
		{Name: "do", Uses: "agent/pi"},
		{Name: "accept", Uses: "approval", With: map[string]any{"means": "I trust the agent did what it said", "quorum": 2}},
	}})
	if len(snapshot.Steps) != 2 || snapshot.Steps[0].Status != workflow.StatusPass || snapshot.Steps[1].Status != workflow.StatusPending {
		t.Fatalf("expected completed work awaiting approval: %+v", snapshot.Steps)
	}
	runID, stepID := snapshot.Run.ID, snapshot.Steps[1].ID
	tok := mcp_token(t, a, "laptop", "bls", "member")

	res := mcp_call(t, a, tok, "list_changes", map[string]any{"slug": "shop", "state": "in_progress"})
	changes := res["structuredContent"].(map[string]any)["changes"].([]any)
	if len(changes) != 1 || changes[0].(map[string]any)["id"] != chID.String() || changes[0].(map[string]any)["runStatus"] != "running" {
		t.Fatalf("list_changes: %v", changes)
	}
	if res := mcp_call(t, a, tok, "list_changes", map[string]any{"slug": "shop", "state": "shipped"}); len(res["structuredContent"].(map[string]any)["changes"].([]any)) != 0 {
		t.Fatalf("state filter must apply: %v", res)
	}

	// get_change on a real change: the record, its run's steps, and what it
	// waits on — a person, with what their OK means.
	res = mcp_call(t, a, tok, "get_change", map[string]any{"id": chID})
	sc := res["structuredContent"].(map[string]any)
	if sc["title"] != "add the cart" || sc["project"] != "shop" || !strings.HasSuffix(sc["webUrl"].(string), fmt.Sprintf("/changes/%d", chID)) {
		t.Fatalf("get_change: %v", sc)
	}
	run := sc["run"].(map[string]any)
	if run["id"] != runID.String() || len(run["steps"].([]any)) != 2 {
		t.Fatalf("run summary: %v", run)
	}
	waiting := sc["waiting"].(map[string]any)
	if waiting["on"] != "a person" || waiting["means"] != "I trust the agent did what it said" || waiting["stepId"] != stepID.String() {
		t.Fatalf("waiting: %v", waiting)
	}
	// Ids may be quoted — models do that.
	if res := mcp_call(t, a, tok, "get_change", map[string]any{"id": fmt.Sprint(chID)}); res["isError"] == true {
		t.Fatalf("a numeric string id must work: %v", res)
	}
	// A bogus id is isError with a sentence, not a JSON-RPC error.
	res = mcp_call(t, a, tok, "get_change", map[string]any{"id": "0000000f-423f-7000-8000-000000000000"})
	if res["isError"] != true {
		t.Fatalf("bogus change must be isError: %v", res)
	}
	if txt := res["content"].([]any)[0].(map[string]any)["text"].(string); !strings.Contains(txt, "no Change 0000000f-423f-7000-8000-000000000000") {
		t.Fatalf("refusal wording: %q", txt)
	}
	// No branch: the diff refuses plainly.
	if res := mcp_call(t, a, tok, "get_change_diff", map[string]any{"id": chID}); res["isError"] != true {
		t.Fatalf("branchless diff must refuse: %v", res)
	}

	// Evidence: steps with bounded evidence, artifacts without bodies.
	if _, err := a.Work.AttachArtifact(asPerson("bls"), runID, workflow.ArtifactDraft{Kind: "change-report", Name: "report", Body: strings.Repeat("x", 50000)}); err != nil {
		t.Fatal(err)
	}
	res = mcp_call(t, a, tok, "get_change_evidence", map[string]any{"id": chID})
	sc = res["structuredContent"].(map[string]any)
	steps := sc["steps"].([]any)
	if len(steps) != 2 || steps[0].(map[string]any)["evidence"].(map[string]any)["model"] != "m" {
		t.Fatalf("evidence steps: %v", steps)
	}
	arts := sc["artifacts"].([]any)
	if len(arts) != 1 || arts[0].(map[string]any)["kind"] != "change-report" || arts[0].(map[string]any)["bytes"] != float64(50000) {
		t.Fatalf("artifacts: %v", arts)
	}
	if _, hasBody := arts[0].(map[string]any)["body"]; hasBody {
		t.Fatal("artifact bodies must not ride the evidence result")
	}

	// get_run reads the same run.
	res = mcp_call(t, a, tok, "get_run", map[string]any{"id": runID})
	sc = res["structuredContent"].(map[string]any)
	if sc["state"] != "waiting on you" || sc["change"].(map[string]any)["id"] != chID.String() {
		t.Fatalf("get_run: %v", sc)
	}

	// The approval is waiting on the caller, with its means.
	res = mcp_call(t, a, tok, "get_waiting_on_you", map[string]any{})
	items := res["structuredContent"].(map[string]any)["waiting"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["means"] != "I trust the agent did what it said" || items[0].(map[string]any)["changeId"] != chID.String() {
		t.Fatalf("waiting on you: %v", items)
	}
	// Once this person has voted it is no longer waiting on them.
	if step, err := a.Work.Approve(asPerson("bls"), workflow.Approval{StepID: stepID, Decision: workflow.VoteApprove}); err != nil || step.Status != workflow.StatusPending {
		t.Fatalf("first vote should leave quorum pending: %+v %v", step, err)
	}
	res = mcp_call(t, a, tok, "get_waiting_on_you", map[string]any{"slug": "shop"})
	if len(res["structuredContent"].(map[string]any)["waiting"].([]any)) != 0 {
		t.Fatalf("a voted approval must not be listed: %v", res)
	}
	// Another approver who has not voted still sees it: a vote counts for
	// the person who cast it, by principal.
	if _, err := a.Access.PutGrant(context.Background(), identity.Spec{SubjectType: "user", Subject: "ann", Role: access.RoleProjectApprover, Scope: access.ScopeProject, ScopeID: fmt.Sprint(pid), Origin: "test", By: "bls"}); err != nil {
		t.Fatal(err)
	}
	annTok := mcp_token(t, a, "ann-laptop", "ann", "member")
	res = mcp_call(t, a, annTok, "get_waiting_on_you", map[string]any{"slug": "shop"})
	if items := res["structuredContent"].(map[string]any)["waiting"].([]any); len(items) != 1 || items[0].(map[string]any)["stepId"] != stepID.String() {
		t.Fatalf("an approval waiting on another person must be listed for them: %v", res)
	}
}

func TestMCPProtocolErrors(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	tok := mcp_token(t, a, "laptop", "bls", "member")
	// Unknown method → -32601.
	// An unknown method is refused by the transport before it reaches a session.
	w := mcp_post(a, tok, "", mcp_rpc(1, "forge/nonsense", nil))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "forge/nonsense") {
		t.Fatalf("unknown method: %d %s", w.Code, w.Body.String())
	}
	// Unknown tool and a stray argument are protocol errors (-32602).
	w = mcp_post(a, tok, "", mcp_rpc(2, "tools/call", map[string]any{"name": "delete_everything"}))
	if !strings.Contains(w.Body.String(), `"code":-32602`) {
		t.Fatalf("unknown tool: %s", w.Body.String())
	}
	w = mcp_post(a, tok, "", mcp_rpc(3, "tools/call", map[string]any{"name": "get_project", "arguments": map[string]any{"slug": "x", "nuke": true}}))
	if !strings.Contains(w.Body.String(), `"code":-32602`) || !strings.Contains(w.Body.String(), "nuke") {
		t.Fatalf("stray argument: %s", w.Body.String())
	}
	// Missing required argument.
	w = mcp_post(a, tok, "", mcp_rpc(4, "tools/call", map[string]any{"name": "get_project"}))
	if !strings.Contains(w.Body.String(), `"code":-32602`) || !strings.Contains(w.Body.String(), "required") {
		t.Fatalf("missing argument: %s", w.Body.String())
	}
	// Resources: AppTown serves none, and says so with an empty list.
	w = mcp_post(a, tok, "", mcp_rpc(5, "resources/list", nil))
	if !strings.Contains(w.Body.String(), `"resources":[]`) {
		t.Fatalf("resources/list: %s", w.Body.String())
	}
	// Bad JSON is refused before anything runs.
	w = mcp_post(a, tok, "", `{not json`)
	if w.Code != 400 {
		t.Fatalf("bad json: %d %s", w.Code, w.Body.String())
	}
	// An unsupported protocol version header is refused up front.
	req := httptest.NewRequest("POST", "/mcp/user", strings.NewReader(mcp_rpc(7, "ping", nil)))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "1999-01-01")
	pw := httptest.NewRecorder()
	a.Handler().ServeHTTP(pw, req)
	if pw.Code != 400 {
		t.Fatalf("unsupported protocol header must 400: %d %s", pw.Code, pw.Body.String())
	}
}

func TestMCPBatch(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	createReadTestProject(t, a, "shop")
	tok := mcp_token(t, a, "laptop", "bls", "member")
	// Batching is part of the 2025-03-26 protocol; later versions dropped it.
	w := mcp_raw(a, tok, "", mcp_rpc(0, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}}))
	sid := w.Header().Get("Mcp-Session-Id")
	body := "[" + mcp_rpc(1, "ping", nil) + "," +
		mcp_rpc(2, "tools/call", map[string]any{"name": "list_projects", "arguments": map[string]any{}}) + "," +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "]"
	w = mcp_raw(a, tok, sid, body)
	if w.Code != 200 {
		t.Fatalf("batch: %d %s", w.Code, w.Body.String())
	}
	var out []struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("batch must answer with an array: %v %s", err, w.Body.String())
	}
	// Two requests answered, the notification silently taken.
	if len(out) != 2 || string(out[0].ID) != "1" || string(out[1].ID) != "2" {
		t.Fatalf("batch responses: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `\"slug\": \"shop\"`) && !strings.Contains(w.Body.String(), `"slug":"shop"`) {
		t.Fatalf("batch must carry the tool result: %s", w.Body.String())
	}
}

func TestMCPDeleteEndsSession(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	tok := mcp_token(t, a, "laptop", "bls", "member")
	sid := mcp_session(a, tok)
	if sid == "" {
		t.Fatal("no session id")
	}
	del := func(token, sid string) int {
		req := httptest.NewRequest("DELETE", "/mcp/user", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		if sid != "" {
			req.Header.Set("Mcp-Session-Id", sid)
		}
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, req)
		return w.Code
	}
	// Someone else cannot end it, and it still works for its owner.
	other := mcp_token(t, a, "other", "kid", "member")
	if code := del(other, sid); code != 403 {
		t.Fatalf("another user's DELETE must be refused: %d", code)
	}
	if w := mcp_raw(a, tok, sid, mcp_rpc(1, "ping", nil)); w.Code != 200 {
		t.Fatalf("a refused DELETE must keep the session: %d %s", w.Code, w.Body.String())
	}
	// Without a session id there is nothing to end.
	if code := del(tok, ""); code != 400 {
		t.Fatalf("DELETE without a session id must 400: %d", code)
	}
	if code := del(tok, sid); code != 204 {
		t.Fatalf("DELETE must end the session with 204: %d", code)
	}
	// An ended session is gone; the client initializes a new one.
	if w := mcp_raw(a, tok, sid, mcp_rpc(2, "ping", nil)); w.Code != 404 {
		t.Fatalf("an ended session must be unknown: %d %s", w.Code, w.Body.String())
	}
}

// A spec-following client completes the whole handshake over real HTTP: it
// initializes, lists the tools, calls one as the token's person, and a tool
// refusal comes back as a result the model can read rather than a failure.
func TestMCPOfficialClientConnectsAndCallsATool(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	createReadTestProject(t, a, "shop")
	tok := mcp_token(t, a, "laptop", "bls", "member")
	server := httptest.NewServer(a.Handler())
	t.Cleanup(server.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "sdk-client", Version: "1"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp/user", HTTPClient: &http.Client{Transport: bearer{tok}}}
	ctx := context.Background()
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) == 0 {
		t.Fatalf("list tools: %v %+v", err, tools)
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_projects", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("list_projects: %v %+v", err, res)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, `"slug": "shop"`) {
		t.Fatalf("list_projects content: %s", text)
	}
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "get_project", Arguments: map[string]any{"slug": "ghost"}})
	if err != nil || !res.IsError {
		t.Fatalf("a missing project is a readable refusal: %v %+v", err, res)
	}

	// Behind a reverse proxy apptownd listens on loopback and sees the public
	// host name; the door must still answer.
	req, _ := http.NewRequest("POST", server.URL+"/mcp/user", strings.NewReader(mcp_rpc(1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}})))
	req.Host = "forge.example.org"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+tok)
	proxied, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	proxied.Body.Close()
	if proxied.StatusCode != 200 {
		t.Fatalf("a proxied request under the public host name: %d", proxied.StatusCode)
	}
}

// bearer adds the person's token to every request, as a configured client does.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// The SDK server is built by the door's first admitted request, never with
// the App: an App that is asked only over its API, or refused at the MCP
// door, has not built it; concurrent first sessions build it exactly once,
// and every one of them is served.
func TestTheMCPServerIsBuiltOnceByItsFirstRequest(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	door := a.mcpDoor.(*mcpDoor)
	tok := mcp_token(t, a, "laptop", "bls", "member")
	req := httptest.NewRequest("GET", "/api/v1/projects", nil)
	req.Header.Set("X-AppTown-Actor", "bls")
	a.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if w := mcp_post(a, "forge_nope", "", mcp_rpc(1, "ping", nil)); w.Code != 401 {
		t.Fatalf("a bad token: %d", w.Code)
	}
	if n := door.builds.Load(); n != 0 {
		t.Fatalf("built %d times before any admitted MCP request", n)
	}
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = mcp_raw(a, tok, "", mcp_rpc(i, "initialize", map[string]any{
				"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
				"clientInfo": map[string]any{"name": "forge-test", "version": "1"}})).Code
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Fatalf("first session %d: %d", i, c)
		}
	}
	if n := door.builds.Load(); n != 1 {
		t.Fatalf("built %d times by concurrent first sessions", n)
	}
}

// A server that could not be built is that request's answer, and every
// later one's: the failure is never served as an empty door.
func TestAnMCPServerThatCannotBeBuiltAnswersWithItsFailure(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	tok := mcp_token(t, a, "laptop", "bls", "member")
	door := &mcpDoor{a: a}
	door.once.Do(func() { door.err = errors.New("a tool's schema was refused") })
	for range 2 {
		req := httptest.NewRequest("POST", "/mcp/user", strings.NewReader(mcp_rpc(1, "ping", nil)))
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		door.ServeHTTP(w, req)
		if w.Code != 500 || !strings.Contains(w.Body.String(), "a tool's schema was refused") {
			t.Fatalf("a failed build: %d %s", w.Code, w.Body.String())
		}
	}
}
