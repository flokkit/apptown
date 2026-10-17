package forge

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids/idstest"
)

// tokenFor mints a personal token through Identity's command, issued by an
// administrator's session, and returns its plaintext.
func tokenFor(t *testing.T, a *App, name, user, role, purpose string) string {
	t.Helper()
	testAdministrator(t, a, "boss")
	issued, err := a.Access.IssueToken(context.Background(), identity.NewToken{Name: name, User: user, Role: role, Purpose: purpose},
		access.Principal{Kind: "user", Name: "boss", Via: "session"})
	if err != nil {
		t.Fatal(err)
	}
	return issued.Plaintext
}

func bearerCall(a *App, token, method, path, body string) *httptest.ResponseRecorder {
	if body == "" {
		body = `{}`
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	return w
}

// refusedAsAgent: the refusal is the agent bound in the authorization
// decision, not a missing route or a lifecycle conflict.
func refusedAsAgent(t *testing.T, what string, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != 403 || !strings.Contains(w.Body.String(), "agent token may not") {
		t.Fatalf("%s: an agent token must be refused by authorization: %d %s", what, w.Code, w.Body.String())
	}
}

// A person whose authority covers approving and landing keeps it on their
// command-line token, and an agent token of theirs does not inherit it: the
// same decision that bounds an OAuth connection bounds the agent token on
// the plain API. Requesting a landing is an agent's act and stays allowed.
func TestAnAgentTokenMayNotApproveOrLandWhatItsPersonMay(t *testing.T) {
	t.Parallel()
	spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "land", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}
	a, ctx, ch := changeScenario(t, spec)
	seedLibraryWorkflow(t, a, ch.ProjectID, "test", `apiVersion: forge.flokkit.io/v1
kind: Workflow
metadata: {name: test}
spec:
  kind: pipeline
  role: landing
  stages:
    - name: land
      steps:
        - name: accept
          uses: approval
          with: {means: "Approve the landing"}
`)
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "test"})
	if err != nil {
		t.Fatal(err)
	}
	agent := tokenFor(t, a, "alice-agent", "alice", "member", access.TokenAgent)
	cli := tokenFor(t, a, "alice-cli", "alice", "member", access.TokenCLI)

	restart := fmt.Sprintf("/api/v1/runs/%d/restart-landing", run.ID)
	refusedAsAgent(t, "restart-landing (change:land)", bearerCall(a, agent, "POST", restart, ""))
	if snap, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView); err != nil || snap.Run.Status != workflow.RunRunning {
		t.Fatalf("a refused restart changed the run: %+v %v", snap.Run, err)
	}
	if w := bearerCall(a, agent, "POST", fmt.Sprintf("/api/v1/changes/%d/land", ch.ID), ""); w.Code == 403 {
		t.Fatalf("requesting a landing is an agent's act: %d %s", w.Code, w.Body.String())
	}
	w := bearerCall(a, cli, "POST", restart, "")
	if w.Code != 200 {
		t.Fatalf("the person's cli token restarts the landing: %d %s", w.Code, w.Body.String())
	}
	runs, err := a.Work.ChangeRuns(ctx, ch.ID, workflow.ChangeRuns{})
	if err != nil || len(runs) < 2 {
		t.Fatalf("fresh landing: %+v %v", runs, err)
	}
	fresh := runs[0]
	for _, r := range runs {
		if r.Status == workflow.RunRunning {
			fresh = r
		}
	}
	snap, err := a.Work.Snapshot(ctx, fresh.ID, workflow.DetailView)
	if err != nil || len(snap.Steps) != 1 {
		t.Fatalf("fresh landing steps: %+v %v", snap, err)
	}
	approve := fmt.Sprintf("/api/v1/steps/%d/approve", snap.Steps[0].ID)
	refusedAsAgent(t, "approve (change:approve)", bearerCall(a, agent, "POST", approve, `{"decision":"approve"}`))
	if snap, err := a.Work.Snapshot(ctx, fresh.ID, workflow.DetailView); err != nil || snap.Steps[0].Status == workflow.StatusPass {
		t.Fatalf("a refused approval was recorded: %+v %v", snap.Steps, err)
	}
	if w := bearerCall(a, cli, "POST", approve, `{"decision":"approve"}`); w.Code != 200 {
		t.Fatalf("the person's cli token approves: %d %s", w.Code, w.Body.String())
	}
}

// Token issuance has its own administration check. An administrator's agent
// token cannot use it to mint a cli token and take back its person's
// authority: refused, no credential, no token, no receipt. The same
// administrator's cli token can.
func TestAnAgentTokenCannotMintItselfACommandLineToken(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	agent := tokenFor(t, a, "boss-agent", "boss", "admin", access.TokenAgent)
	w := bearerCall(a, agent, "POST", "/api/v1/tokens", `{"name":"escape","role":"admin","purpose":"cli"}`)
	if w.Code != 403 || strings.Contains(w.Body.String(), identity.TokenPrefix) {
		t.Fatalf("an agent token minted a token: %d %s", w.Code, w.Body.String())
	}
	boss := access.Principal{Kind: "user", Name: "boss", Via: "session"}
	tokens, err := a.Access.Tokens(context.Background(), boss)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range tokens {
		if tok.Name == "escape" {
			t.Fatal("a refused issuance stored a token")
		}
	}
	receipts, err := audit.Recent(context.Background(), a.DB, audit.Filter{KindPrefix: "token.created", Subject: "escape"})
	if err != nil || len(receipts) != 0 {
		t.Fatalf("a refused issuance wrote a receipt: %+v %v", receipts, err)
	}
	cli := tokenFor(t, a, "boss-cli", "boss", "admin", access.TokenCLI)
	if w := bearerCall(a, cli, "POST", "/api/v1/tokens", `{"name":"made-by-cli","role":"member","purpose":"agent"}`); w.Code != 201 {
		t.Fatalf("an administrator's cli token issues: %d %s", w.Code, w.Body.String())
	}
}

// A request on the MCP door is an agent's. A cli token, which acts with its
// person's full authority, is refused there with the agent-token
// alternative named; an agent token opens it.
func TestMCPRefusesACommandLineToken(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	if _, err := a.Access.EnsureUser(context.Background(), "bls"); err != nil {
		t.Fatal(err)
	}
	cli := tokenFor(t, a, "bls-cli", "bls", "member", access.TokenCLI)
	w := mcp_post(a, cli, "", mcp_rpc(1, "ping", nil))
	if w.Code != 401 || !strings.Contains(w.Body.String(), "--purpose agent") || !strings.Contains(w.Header().Get("WWW-Authenticate"), `realm="forge-mcp"`) {
		t.Fatalf("a cli token at MCP: %d %s", w.Code, w.Body.String())
	}
	agent := tokenFor(t, a, "bls-agent", "bls", "member", access.TokenAgent)
	if w := mcp_post(a, agent, "", mcp_rpc(1, "ping", nil)); w.Code != 200 {
		t.Fatalf("an agent token at MCP: %d %s", w.Code, w.Body.String())
	}
}

// Disconnecting a coding-agent connection is the person's act, even on
// their own connections: an agent holding one of their credentials must not
// end the others. The person's own command line still can.
func TestAnAgentTokenCannotDisconnectItsPersonsConnections(t *testing.T) {
	t.Parallel()
	for _, purpose := range []string{access.TokenAgent, access.TokenCLI} {
		t.Run(purpose, func(t *testing.T) {
			t.Parallel()
			a := testApp(t)
			token := tokenFor(t, a, "caller", "boss", "admin", purpose)
			id := policyConnection(t, a, "boss")
			w := bearerCall(a, token, "DELETE", fmt.Sprintf("/api/v1/auth/delegations/%d", id), "")
			d, err := a.Access.Connection(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if purpose == access.TokenAgent {
				refusedAsAgent(t, "disconnect", w)
				if !d.LiveAt(time.Now()) {
					t.Fatal("a refused disconnect ended the connection")
				}
				return
			}
			if w.Code != 200 || d.LiveAt(time.Now()) {
				t.Fatalf("the person's cli token disconnects their own connection: %d %s live=%v", w.Code, w.Body.String(), d.LiveAt(time.Now()))
			}
		})
	}
}

// An agent that may create projects gets the offered pack it asks for, the
// new project's first way of working, without gaining the power to change
// an existing project's way of working. A pack nobody may be given (the
// platform tier) is refused before anything is made.
func TestAnAgentCreatesAProjectWithTheOfferedPackItAskedFor(t *testing.T) {
	t.Parallel()
	a, agent := mcpWriteFixture(t)
	a.SyncStock(t.Context())
	res := mcp_call(t, a, agent, "create_project", map[string]any{"slug": "with-pack", "name": "With pack", "pack": "html-game"})
	sc, _ := res["structuredContent"].(map[string]any)
	pk, _ := sc["pack"].(map[string]any)
	if res["isError"] == true || pk == nil || pk["error"] != nil {
		t.Fatalf("the requested pack was not applied: %v", res)
	}
	p, err := a.Projects.BySlug(context.Background(), "with-pack")
	if err != nil {
		t.Fatal(err)
	}
	current, err := a.Adoption.Current(t.Context(), p.ID)
	if err != nil || current.Pack != "html-game" {
		t.Fatalf("adoption after creation: %+v %v", current, err)
	}
	refusedAsAgent(t, "applying a pack to an existing project", bearerCall(a, agent, "POST", "/api/v1/projects/with-pack/pack", `{"name":"conventions-only"}`))
	res = mcp_call(t, a, agent, "create_project", map[string]any{"slug": "platform", "name": "Platform", "pack": "forge"})
	if res["isError"] != true {
		t.Fatalf("the platform tier is not offered: %v", res)
	}
	if _, err := a.Projects.BySlug(context.Background(), "platform"); err == nil {
		t.Fatal("a refused pack still made the project")
	}
}

// Where a project's code is pushed is decided by a person with the project's
// integration authority, not by roster membership: an administrator's agent
// token cannot replace origin, and nothing changes before the refusal. The
// same administrator's cli token gets past authorization (this fixture has
// no GitLab, so its push then fails, after origin was replaced).
func TestAnAgentTokenCannotReplaceAProjectsRemote(t *testing.T) {
	t.Parallel()
	a, agent := mcpWriteFixture(t)
	dir := a.projectRepoPath("app")
	if out, err := gitexec.Run(t.Context(), dir, "remote", "add", "origin", "https://original.invalid/repo.git"); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	origin := func() string {
		t.Helper()
		out, err := gitexec.Run(t.Context(), dir, "remote", "get-url", "origin")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	refusedAsAgent(t, "attach remote", bearerCall(a, agent, "POST", "/api/v1/projects/app/repo/remote", `{"url":"https://replacement.invalid/repo.git"}`))
	if !strings.Contains(origin(), "original.invalid") {
		t.Fatalf("a refused attach replaced origin: %s", origin())
	}
	cli := tokenFor(t, a, "boss-cli", "boss", "admin", access.TokenCLI)
	if w := bearerCall(a, cli, "POST", "/api/v1/projects/app/repo/remote", `{"url":"https://replacement.invalid/repo.git"}`); w.Code == 403 {
		t.Fatalf("the person's cli token is refused: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(origin(), "replacement.invalid") {
		t.Fatalf("the person's attach did not reach origin: %s", origin())
	}
}

// The routes that change where a project's code goes, or what lands on its
// default branch outside a Change, are decided by capability, not roster
// membership, so an agent token is refused on every one before any effect.
func TestAgentTokensCannotChangeWhereAProjectsCodeGoes(t *testing.T) {
	t.Parallel()
	a, agent := mcpWriteFixture(t)
	for _, call := range []struct{ path, body string }{
		{"/api/v1/projects/app/repo", `{}`},
		{"/api/v1/projects/app/repo/remote", `{"url":"https://replacement.invalid/repo.git"}`},
		{"/api/v1/projects/app/toolchain", `{"name":"go"}`},
	} {
		refusedAsAgent(t, call.path, bearerCall(a, agent, "POST", call.path, call.body))
	}
}

// A restore rehearsal runs production data somewhere new, so it is a
// person's act: an agent token is refused at the door, before any operation
// is recorded, while the person's own command-line token gets past
// authorization.
func TestAnAgentTokenCannotStartARestoreRehearsal(t *testing.T) {
	t.Parallel()
	a, agent := mcpWriteFixture(t)
	const path = "/api/v1/projects/app/deployments/prod/rehearsals"
	refusedAsAgent(t, "rehearse", bearerCall(a, agent, "POST", path, `{}`))
	cli := tokenFor(t, a, "boss-cli", "boss", "admin", access.TokenCLI)
	if w := bearerCall(a, cli, "POST", path, `{}`); w.Code == 403 {
		t.Fatalf("the person's cli token is refused: %d %s", w.Code, w.Body.String())
	}
}

// Opening a chat gives a model write access to a Change branch, so it needs
// the authority to put an agent to work, not only a place on the roster: a
// project viewer's own command line is refused before any session is held.
func TestAViewerCannotOpenAChat(t *testing.T) {
	t.Parallel()
	a, _ := mcpWriteFixture(t)
	viewer := tokenFor(t, a, "viewer-cli", "viewer", "member", access.TokenCLI)
	w := bearerCall(a, viewer, "POST", "/api/v1/projects/app/console", `{}`)
	if w.Code != 403 {
		t.Fatalf("a viewer opened a chat: %d %s", w.Code, w.Body.String())
	}
	sessions := bearerCall(a, viewer, "GET", "/api/v1/projects/app/console", "")
	if sessions.Code != 200 || strings.Contains(sessions.Body.String(), `"id"`) {
		t.Fatalf("a refused chat left a session: %d %s", sessions.Code, sessions.Body.String())
	}
}

// shipFixture: a project on the stock workflows with a Change whose branch
// carries one commit, and the default branch's tip before anything ran.
func shipFixture(t *testing.T) (*App, string, *workflow.Change, string) {
	t.Helper()
	a, agent := mcpWriteFixture(t)
	a.Ways.SyncStockWorkflows(t.Context())
	p, err := a.Projects.BySlug(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.recordAdoption(&p, "edit", "stock fixture", "fixture"); err != nil {
		t.Fatal(err)
	}
	ch := engineChange(t, a, &p)
	ws := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	if err := os.WriteFile(filepath.Join(ws, "agent-work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, ws, "add", "agent-work.txt")
	gittest.Run(t, ws, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "agent work")
	return a, agent, ch, gittest.Run(t, a.projectRepoPath(p.Slug), "rev-parse", p.DefaultBranch)
}

func defaultTip(t *testing.T, a *App) string {
	t.Helper()
	p, err := a.Projects.BySlug(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	return gittest.Run(t, a.projectRepoPath(p.Slug), "rev-parse", p.DefaultBranch)
}

// A run that would merge or deploy before any person's approval needs
// landing authority, judged from what the workflow declares (stock ship
// merges and deploys with no approval), whichever door starts it: an agent
// token on the HTTP run route or the MCP lane tool, or a full-scope OAuth
// connection, is refused and main does not move. Requesting a landing is
// the agent's door and waits for a person. A person who may land starts
// ship and it merges.
func TestAnAgentCannotLandThroughALaneWithNoApproval(t *testing.T) {
	t.Parallel()
	t.Run("agent token over HTTP", func(t *testing.T) {
		t.Parallel()
		a, agent, ch, before := shipFixture(t)
		w := bearerCall(a, agent, "POST", fmt.Sprintf("/api/v1/changes/%d/runs", ch.ID), `{"workflow":"ship"}`)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "request a landing") {
			t.Fatalf("an agent started ship: %d %s", w.Code, w.Body.String())
		}
		a.Work.Wait()
		if after := defaultTip(t, a); after != before {
			t.Fatalf("main moved: %s -> %s", before, after)
		}
	})
	t.Run("agent token over the MCP lane tool", func(t *testing.T) {
		t.Parallel()
		a, agent, ch, before := shipFixture(t)
		if err := a.lifecycle.SelectLane(t.Context(), ch.ProjectID, "ship", true, "fixture"); err != nil {
			t.Fatal(err)
		}
		res := mcp_call(t, a, agent, "start_change_lane", map[string]any{"id": ch.ID, "lane": "ship"})
		if res["isError"] != true {
			t.Fatalf("an agent started ship over MCP: %v", res)
		}
		a.Work.Wait()
		if after := defaultTip(t, a); after != before {
			t.Fatalf("main moved: %s -> %s", before, after)
		}
	})
	t.Run("full-scope OAuth connection", func(t *testing.T) {
		t.Parallel()
		a, _, ch, before := shipFixture(t)
		ctx := callerMCPContext(context.Background(), &authIdentity{User: "boss", Via: "oauth", DelegationID: idstest.N(1), Scopes: access.ScopeNames()}, "boss via client")
		if _, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "ship"}); err == nil {
			t.Fatal("an OAuth agent started ship")
		}
		a.Work.Wait()
		if after := defaultTip(t, a); after != before {
			t.Fatalf("main moved: %s -> %s", before, after)
		}
	})
	t.Run("an agent's landing request waits for a person", func(t *testing.T) {
		t.Parallel()
		a, agent, ch, before := shipFixture(t)
		if w := bearerCall(a, agent, "POST", fmt.Sprintf("/api/v1/changes/%d/land", ch.ID), ""); w.Code >= 300 {
			t.Fatalf("landing request: %d %s", w.Code, w.Body.String())
		}
		a.Work.Wait()
		if after := defaultTip(t, a); after != before {
			t.Fatalf("a landing request merged without approval: %s -> %s", before, after)
		}
	})
	t.Run("a person who may land starts ship", func(t *testing.T) {
		t.Parallel()
		a, _, ch, before := shipFixture(t)
		cli := tokenFor(t, a, "boss-cli", "boss", "admin", access.TokenCLI)
		if w := bearerCall(a, cli, "POST", fmt.Sprintf("/api/v1/changes/%d/runs", ch.ID), `{"workflow":"ship"}`); w.Code != 201 {
			t.Fatalf("the person's ship: %d %s", w.Code, w.Body.String())
		}
		a.Work.Wait()
		if after := defaultTip(t, a); after == before {
			t.Fatal("the person's ship did not merge")
		}
	})
}
