package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/httpapi"
	"flokkit.io/forge/internal/forge/operations"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
)

// Explicit current grants: owner can maintain/approve one project; the other
// project deliberately grants those roles to everyone signed in.
func authzFixture(t *testing.T, configs ...Config) *App {
	return authzFixtureWithApproval(t, workflow.ApprovalConfig{}, configs...)
}

// authzFixtureWithApproval is the authorization world (authzActors) with
// the implement workflow (authzWorkflow) and work under way in it: owner's
// change 1 on game, its run 1 waiting at a pending approval (step 1) after
// its script (step 2) failed. Tests that never read that change, run or its
// steps start from authzActors or authzWorkflow instead.
func authzFixtureWithApproval(t *testing.T, approval workflow.ApprovalConfig, configs ...Config) *App {
	t.Helper()
	a := authzActors(t, configs...)
	authzWorkflow(t, a, approval)
	change, err := a.Work.Create(asPerson("owner"), workflow.ChangeDraft{ProjectID: idstest.N(1), Type: "change", Title: "t"})
	if err != nil || change.Number != 1 {
		t.Fatalf("authz fixture change: %+v %v", change, err)
	}
	run, err := a.Work.StartRun(asPerson("owner"), workflow.StartChangeRun{ChangeID: change.ID, Workflow: "implement"})
	if err != nil || run.Number != 1 {
		t.Fatalf("authz fixture run: %+v %v", run, err)
	}
	a.Work.Wait()
	snapshot, err := a.Work.Snapshot(asPerson("owner"), run.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 2 || snapshot.Steps[0].Name != "accept" || snapshot.Steps[1].Name != "gate" || snapshot.Steps[0].Status != workflow.StatusPending || snapshot.Steps[1].Status != workflow.StatusFail {
		t.Fatalf("authz fixture workflow state: %+v %v", snapshot.Steps, err)
	}
	authzWork.Store(a, authzRecords{Change: change.ID, Run: run.ID, Accept: snapshot.Steps[0].ID, Gate: snapshot.Steps[1].ID})
	return a
}

// authzRecords is the work authzFixture made: owner's change on game, its
// run, the pending approval (accept) and the failed script (gate).
type authzRecords struct{ Change, Run, Accept, Gate ids.ID }

var authzWork sync.Map // *App → authzRecords

// fixtureWork is the work authzFixture made for a.
func fixtureWork(t *testing.T, a *App) authzRecords {
	t.Helper()
	w, ok := authzWork.Load(a)
	if !ok {
		t.Fatal("no authz fixture work for this app")
	}
	return w.(authzRecords)
}

// authzActors is the authorization world with no work in it: boss the
// administrator; owner, outsider, amy and kid; project game (1), where
// owner alone maintains and approves, and project open (2), where everyone
// signed in does; amy's instance amender capabilities; and everyone's
// release-manager role on every target.
func authzActors(t *testing.T, configs ...Config) *App {
	t.Helper()
	a := testApp(t, configs...)
	testAdministrator(t, a, "boss")
	for _, u := range []string{"owner", "outsider", "amy", "kid"} {
		if _, err := a.Access.EnsureUser(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
	gameID, err := database.Transact(context.Background(), a.DB, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{ID: idstest.N(1), Slug: "game", Name: "G", DefaultApprovers: "role:project-approver"})
	})
	if err != nil {
		t.Fatal(err)
	}
	openID, err := database.Transact(context.Background(), a.DB, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{ID: idstest.N(2), Slug: "open", Name: "O", DefaultApprovers: "role:project-approver"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if gameID != idstest.N(1) || openID != idstest.N(2) {
		t.Fatalf("authz fixture project IDs changed: game=%d open=%d", gameID, openID)
	}
	for _, projectID := range []ids.ID{gameID, openID} {
		if err := a.DB.InTx(context.Background(), func(tx *database.Tx) error {
			if err := adoption.InitializeIn(t.Context(), tx, projectID, 0, "", ""); err != nil {
				return err
			}
			return adoption.RecordIn(t.Context(), tx, projectID, 1, adoption.AdoptionCreate, "authz fixture", adoption.EmptyReceipt(), "fixture", time.Now())
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.EnsureAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{access.RoleProjectMaintainer, access.RoleProjectApprover} {
		a.addGrant("user", "owner", role, "", access.ScopeProject, idstest.N(1).String(), "fixture", "boss", "")
		a.addGrant("group", identity.GroupAllUsers, role, "", access.ScopeProject, idstest.N(2).String(), "fixture", "boss", "")
	}
	for _, capability := range access.AmenderCaps {
		a.addGrant("user", "amy", "", string(capability), access.ScopeInstance, "", "fixture", "boss", "")
	}
	a.addGrant("group", identity.GroupAllUsers, access.RoleReleaseManager, "", access.ScopeTarget, "*", "fixture", "boss", "")
	return a
}

// authzWorkflow defines the implement workflow the projects run: an
// approval (configured by approval) and a script that always fails.
func authzWorkflow(t *testing.T, a *App, approval workflow.ApprovalConfig) {
	t.Helper()
	defineWorkflows(t, a, workflow.PreparedRun{
		Workflow: workflow.DefinitionRef{Name: "implement", Version: 1},
		Spec: schema.Spec{Kind: "lifecycle", Role: "working", Stages: []schema.Stage{{Name: "apply", Steps: []schema.Step{
			{Name: "accept", Uses: "approval", With: approvalConfigWith(approval)},
			{Name: "gate", Uses: "script"},
		}}}},
	})
	rebuildWork(t, a, func(deps *workflow.Dependencies) {
		deps.Git = fakeGit{}
		deps.Runners = map[string]workflow.Runner{"script": func(task *workflow.Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusFail}, nil); err != nil {
				t.Error(err)
			}
		}}
	})
}

func approvalConfigWith(approval workflow.ApprovalConfig) map[string]any {
	with := map[string]any{}
	if approval.Quorum != 0 {
		with["quorum"] = approval.Quorum
	}
	if approval.Means != "" {
		with["means"] = approval.Means
	}
	if approval.Approvers != nil {
		with["approvers"] = approval.Approvers
	}
	return with
}

func authzCall(h http.Handler, who, method, path, body string) *httptest.ResponseRecorder {
	if body == "" {
		body = `{}`
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-AppTown-Actor", who)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// Upgrade day changes nobody's effective access (§18, §19 item 3). The route
// table is the permission policy: every project door below refuses someone
// who holds the roles only on another project before its handler runs, and
// lets the project's member and an administrator through.
func TestAuthzExplicitGrantsPreserveScopedAccess(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	fx := fixtureWork(t, a)
	h := a.Handler()
	for _, d := range []string{
		"POST /api/v1/changes/" + fx.Change.String() + "/land", "POST /api/v1/steps/" + fx.Accept.String() + "/approve", "POST /api/v1/steps/" + fx.Accept.String() + "/override", "POST /api/v1/steps/" + fx.Gate.String() + "/override",
		"PUT /api/v1/projects/game/guidance", "POST /api/v1/changes/" + fx.Change.String() + "/abandon", "POST /api/v1/changes/" + fx.Change.String() + "/sendback",
		"POST /api/v1/runs/" + fx.Run.String() + "/advance", "POST /api/v1/runs/" + fx.Run.String() + "/abandon", "POST /api/v1/projects/game/pack",
		"POST /api/v1/projects/game/integrations",
	} {
		m, p, _ := strings.Cut(d, " ")
		if c := authzCall(h, "outsider", m, p, "").Code; c != 403 {
			t.Errorf("%s: an outsider got %d, want 403", d, c)
		}
		if c := authzCall(h, "owner", m, p, `{"decision":"approve","reason":"x","comment":"ok"}`).Code; c == 403 {
			t.Errorf("%s: the migrated roster member was refused", d)
		}
		if c := authzCall(h, "boss", m, p, `{"decision":"approve","reason":"x"}`).Code; c == 403 {
			t.Errorf("%s: the administrator was refused", d)
		}
	}
	// A route on something that does not exist falls through to the
	// handler's own 404: the guard never invents one.
	if c := authzCall(h, "outsider", "POST", "/api/v1/changes/0000000f-423f-7000-8000-000000000000/land", "").Code; c != 404 {
		t.Errorf("missing change: got %d, want 404", c)
	}
	// Project authority never grants instance-level content administration.
	for _, who := range []string{"outsider", "owner"} {
		if c := authzCall(h, who, "POST", "/api/v1/packs/nope/enabled", "").Code; c != 403 {
			t.Errorf("amend route: %s got %d, want 403", who, c)
		}
	}
	// An outsider is refused on the closed project, in plain words, with a code.
	w := authzCall(h, "outsider", "POST", "/api/v1/changes/"+fx.Change.String()+"/land", "")
	// outsider holds the migrated-open grant on the OTHER project, so the
	// honest code is outside_scope ("you hold it elsewhere, not here").
	if w.Code != 403 || w.Header().Get("X-AppTown-Deny-Code") != access.DenyOutsideScope {
		t.Fatalf("outsider land: %d %s %s", w.Code, w.Header().Get("X-AppTown-Deny-Code"), w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "outsider may not") || !strings.Contains(w.Body.String(), "Project contributor") {
		t.Errorf("denial should say who, what, and which role carries it: %s", w.Body.String())
	}
	// The open-roster project stays open to every signed-in person, visibly.
	if !a.projectMember(idstest.N(2), "outsider", "member") {
		t.Error("empty roster should have migrated to an explicit everyone grant")
	}
	gs := mustRecords(a.Access.GrantsAt(context.Background(), access.ScopeProject, idstest.N(2).String()))
	found := false
	for _, g := range gs {
		if g.Subject == identity.GroupAllUsers && g.Origin == "fixture" {
			found = true
		}
	}
	if !found {
		t.Error("the migrated-open grant should be on the record")
	}
	// amend: amy holds it, others do not; release was unclaimed → everyone.
	if !a.hasPrivilege(context.Background(), "amy", "member", "amend") || a.hasPrivilege(context.Background(), "outsider", "member", "amend") {
		t.Error("amend migration wrong")
	}
	if !a.hasPrivilege(context.Background(), "outsider", "member", "release") {
		t.Error("unclaimed release should have migrated to everyone")
	}
	// Running the migration again is a no-op.
	before := len(mustRecords(a.Access.GrantsAt(context.Background(), access.ScopeProject, idstest.N(1).String())))
	if err := a.EnsureAuthorization(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := len(mustRecords(a.Access.GrantsAt(context.Background(), access.ScopeProject, idstest.N(1).String()))); after != before {
		t.Errorf("EnsureAuthorization is not idempotent: %d → %d grants", before, after)
	}
}

// Every route declares its authorization policy.
func TestAuthzRouteInventory(t *testing.T) {
	t.Parallel()
	if _, err := newRouteMux(nil, appRoutes(nil)); err != nil {
		t.Fatal(err)
	}
}

// Tokens are bounds: a demoted person's admin token acts as a member, and a
// disabled account's token stops working (§10).
func TestAuthzTokenReadsCurrentAuthority(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	secured := newApp(t, a.DB, Config{AuthMode: "internal"})
	h := secured.Handler()
	// mint a token for boss with the admin snapshot
	req := httptest.NewRequest("POST", "/api/v1/tokens", strings.NewReader(`{"name":"t","user":"boss","role":"admin","purpose":"cli"}`))
	req.Header.Set("X-AppTown-Actor", "boss")
	// Mint through open mode, using the same configuration wiring as startup.
	var tok string
	{
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, req)
		if w.Code != 201 {
			t.Fatalf("mint: %d %s", w.Code, w.Body.String())
		}
		tok = strings.Split(strings.Split(w.Body.String(), `"token":"`)[1], `"`)[0]
	}
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/v1/me", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call(); w.Code != 200 || !strings.Contains(w.Body.String(), `"role":"admin"`) {
		t.Fatalf("admin token: %d %s", w.Code, w.Body.String())
	}
	testAdministrator(t, a, "second")
	if err := a.Access.SaveUser(context.Background(), identity.AccountChange{Username: "boss", Role: "member"}, access.Principal{Kind: "user", Name: "second", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	if w := call(); !strings.Contains(w.Body.String(), `"role":"member"`) {
		t.Errorf("demotion should constrain the token immediately: %s", w.Body.String())
	}
	disabled := true
	if err := a.Access.SaveUser(context.Background(), identity.AccountChange{Username: "boss", Disabled: &disabled}, access.Principal{Kind: "user", Name: "second", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	if w := call(); w.Code != 401 {
		t.Errorf("a disabled account's token should be refused: %d", w.Code)
	}
}

// Approval: the approver expression, the project default, and the per-project
// independent-approval rule.
func TestAuthzApprovalEligibility(t *testing.T) {
	t.Parallel()
	a := authzFixtureWithApproval(t, workflow.ApprovalConfig{Approvers: []string{"group:parents"}})
	fx := fixtureWork(t, a)
	h := a.Handler()
	// Independent approval: 'kid' holds Project approver but is not on the step's
	// list; 'owner' authored the change and may not approve it.
	if err := a.Projects.SetIndependentApproval(verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "boss", Via: "session"}), idstest.N(1), true); err != nil {
		t.Fatal(err)
	}
	a.addGrant("user", "kid", access.RoleProjectApprover, "", access.ScopeProject, idstest.N(1).String(), "", "boss", "")
	if !a.projectMember(idstest.N(1), "kid", "member") {
		t.Fatal("an approver works here for the roster's purposes")
	}
	if err := a.Access.SaveGroup(context.Background(), identity.GroupChange{Name: "parents", Members: []string{"owner"}}, access.Principal{Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	a.audit(context.Background(), "owner", idstest.N(1), "change.created", "t", map[string]any{"changeId": 1})
	w := authzCall(h, "kid", "POST", "/api/v1/steps/"+fx.Accept.String()+"/approve", `{"decision":"approve"}`)
	if w.Code != 403 || w.Header().Get("X-AppTown-Deny-Code") != access.DenyNotEligible {
		t.Errorf("kid is not on the approver list: %d %s", w.Code, w.Body.String())
	}
	w = authzCall(h, "owner", "POST", "/api/v1/steps/"+fx.Accept.String()+"/approve", `{"decision":"approve"}`)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "opened this change") {
		t.Errorf("the author may not approve when independent approval is required: %d %s", w.Code, w.Body.String())
	}
	// Root is eligible independently of the former profile.
	w = authzCall(h, "boss", "POST", "/api/v1/steps/"+fx.Accept.String()+"/approve", `{"decision":"approve"}`)
	if w.Code != 200 {
		t.Errorf("root may approve outside the approver list: %d %s", w.Code, w.Body.String())
	}
	// An empty approver list resolves to the project default (role:...).
	a = authzFixture(t)
	fx = fixtureWork(t, a)
	h = a.Handler()
	a.addGrant("user", "kid", access.RoleProjectApprover, "", access.ScopeProject, idstest.N(1).String(), "", "boss", "")
	if err := a.Projects.SetIndependentApproval(verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "boss", Via: "session"}), idstest.N(1), true); err != nil {
		t.Fatal(err)
	}
	w = authzCall(h, "kid", "POST", "/api/v1/steps/"+fx.Accept.String()+"/approve", `{"decision":"approve"}`)
	if w.Code != 200 {
		t.Errorf("project default approvers should make kid eligible: %d %s", w.Code, w.Body.String())
	}
}

// Grants API: a project maintainer may add people to their project, a
// contributor may not; receipts and audit rows are written on denial.
func TestAuthzGrantsAPI(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	fx := fixtureWork(t, a)
	h := a.Handler()
	w := authzCall(h, "outsider", "POST", "/api/v1/authz/grants", `{"subject":"outsider","role":"project-owner","scope":"project","scopeId":"00000000-0001-7000-8000-000000000000"}`)
	if w.Code != 403 {
		t.Fatalf("self-grant should be refused: %d %s", w.Code, w.Body.String())
	}
	var n int
	// sql-guarantee: authorization_decisions is write-only receipt storage; no owner read lists decisions
	_ = a.DB.QueryRow(`SELECT COUNT(*) FROM authorization_decisions WHERE outcome='deny' AND principal='outsider'`).Scan(&n)
	if n == 0 {
		t.Error("a denied consequential act should leave a receipt")
	}
	if !slices.ContainsFunc(auditEntries(t, a.DB, "authz.denied"), func(e audit.Entry) bool { return e.Actor == "outsider" }) {
		t.Error("a denied consequential act should be audited")
	}
	w = authzCall(h, "owner", "POST", "/api/v1/authz/grants", `{"subject":"kid","role":"project-contributor","scope":"project","scopeId":"00000000-0001-7000-8000-000000000000"}`)
	if w.Code != 201 {
		t.Fatalf("a migrated member (maintainer authority) may add a contributor: %d %s", w.Code, w.Body.String())
	}
	if c := authzCall(h, "kid", "POST", "/api/v1/changes/"+fx.Change.String()+"/land", "").Code; c == 403 {
		t.Error("the new contributor may hand off for landing")
	}
	if c := authzCall(h, "kid", "POST", "/api/v1/runs/"+fx.Run.String()+"/restart-landing", "").Code; c != 403 {
		t.Errorf("a contributor may not operate a landing attempt: %d", c)
	}
	w = authzCall(h, "kid", "GET", "/api/v1/authz/explain?capability=change:land&project=game", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"allow":false`) || !strings.Contains(w.Body.String(), "owner") {
		t.Errorf("explain should say kid may not and who can: %s", w.Body.String())
	}
	// A person's name the forge would read as something else is refused.
	if w := authzCall(h, "boss", "POST", "/api/v1/authz/grants", `{"subject":"kid via console/x","role":"project-contributor","scope":"project","scopeId":"00000000-0001-7000-8000-000000000000"}`); w.Code != 400 {
		t.Errorf("an unusable person's name is refused: %d %s", w.Code, w.Body.String())
	}
	// A migration-only role cannot be granted afresh.
	w = authzCall(h, "boss", "POST", "/api/v1/authz/grants", `{"subject":"kid","role":"migrated-project-member","scope":"project","scopeId":"00000000-0001-7000-8000-000000000000"}`)
	if w.Code != 400 {
		t.Errorf("migrated roles are not for new grants: %d", w.Code)
	}
}

// A half-booted or ownerless process cannot acquire broad mutation authority.
func TestAuthorizationRequiresExplicitGrantsBeforeBootstrap(t *testing.T) {
	t.Parallel()
	a := constructApp(t, testDB(t), Config{DataDir: t.TempDir()})
	_, err := database.Transact(context.Background(), a.DB, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{ID: idstest.N(1), Slug: "game", Name: "G"})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, initialized := range []bool{false, true} {
		if initialized {
			if err := a.EnsureAuthorization(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		if w := authzCall(a.Handler(), "anyone", "PUT", "/api/v1/projects/game/guidance", `{"imports":[]}`); w.Code != 403 {
			t.Fatalf("ownerless authorization allowed mutation (initialized=%t): %d", initialized, w.Code)
		}
	}
}

func TestMembersKeepExplicitGroupGrantsAndRevokeAtomically(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	h := a.Handler()
	// Project 2 deliberately grants access to everyone. Adding a named member
	// must not turn that into a private project as a hidden side effect.
	before := len(mustRecords(a.Access.GrantsAt(context.Background(), access.ScopeProject, idstest.N(2).String())))
	w := authzCall(h, "boss", "POST", "/api/v1/projects/open/members", `{"grantee":"kid","role":"project-viewer"}`)
	if w.Code != 200 || len(mustRecords(a.Access.GrantsAt(context.Background(), access.ScopeProject, idstest.N(2).String()))) != before+1 || !a.projectMember(idstest.N(2), "outsider", "member") {
		t.Fatalf("adding a named member changed group access: %d %s", w.Code, w.Body.String())
	}
	w = authzCall(h, "boss", "POST", "/api/v1/projects/open/members", `{"grantee":"kid","role":"migrated-project-member"}`)
	if w.Code != 400 || len(mustRecords(a.Access.GrantsAt(context.Background(), access.ScopeProject, idstest.N(2).String()))) != before+1 {
		t.Fatal("invalid role changed grants")
	}
	dbtest.FailNextStatement(t, a.DB, "INSERT INTO audit_log")
	w = authzCall(h, "boss", "DELETE", "/api/v1/projects/open/members/kid", "")
	if w.Code != 500 || len(mustRecords(a.Access.GrantsAt(context.Background(), access.ScopeProject, idstest.N(2).String()))) != before+1 {
		t.Fatal("failed removal partially revoked authority")
	}
	w = authzCall(h, "boss", "DELETE", "/api/v1/projects/open/members/kid", "")
	if w.Code != 200 || len(mustRecords(a.Access.GrantsAt(context.Background(), access.ScopeProject, idstest.N(2).String()))) != before {
		t.Fatalf("removal failed: %d %s", w.Code, w.Body.String())
	}
}

func TestRetiredPermissionAPIIsAbsent(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	for _, method := range []string{"GET", "POST", "DELETE"} {
		w := authzCall(a.Handler(), "bls", method, "/api/v1/permissions", "{}")
		if w.Code != 404 || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("retired API returned an application page: %s %d %s", method, w.Code, w.Body.String())
		}
	}
}

func TestProjectApprovalRuleChangesOnlyNonRootSelfApproval(t *testing.T) {
	t.Parallel()
	for _, who := range []string{"owner", "boss"} {
		t.Run(who, func(t *testing.T) {
			t.Parallel()
			a := authzFixture(t)
			fx := fixtureWork(t, a)
			a.addGrant("user", "owner", access.RoleProjectOwner, "", access.ScopeProject, idstest.N(1).String(), "fixture", "boss", "")
			a.Work.Stop()
			h := a.Handler()
			a.audit(context.Background(), who, idstest.N(1), "change.created", "t", map[string]any{"changeId": 1})
			path := "/api/v1/projects/game/approval-policy"
			if w := authzCall(h, "outsider", "PUT", path, `{"requireIndependentApproval":true}`); w.Code != 403 {
				t.Fatalf("unauthorized configuration: %d %s", w.Code, w.Body)
			}
			if w := authzCall(h, "owner", "PUT", path, `{"requireIndependentApproval":true}`); w.Code != 200 {
				t.Fatalf("set rule: %d %s", w.Code, w.Body)
			}
			w := authzCall(h, who, "POST", "/api/v1/steps/"+fx.Accept.String()+"/approve", `{"decision":"approve"}`)
			if who == "boss" {
				if w.Code != 200 {
					t.Fatalf("root self approval refused: %d %s", w.Code, w.Body)
				}
				return
			}
			if w.Code != 403 || w.Header().Get("X-AppTown-Deny-Code") != access.DenyNotEligible {
				t.Fatalf("author admitted: %d %s", w.Code, w.Body)
			}
			requireEvents(t, a, 0, eventKind("approval"))
			if w := authzCall(h, "owner", "PUT", path, `{"requireIndependentApproval":false}`); w.Code != 200 {
				t.Fatalf("clear rule: %d %s", w.Code, w.Body)
			}
			if w := authzCall(h, who, "POST", "/api/v1/steps/"+fx.Accept.String()+"/approve", `{"decision":"approve"}`); w.Code != 200 {
				t.Fatalf("self approval with rule off: %d %s", w.Code, w.Body)
			}
		})
	}
}

// Test fixture for explicit grants; production callers use Identity commands.
func (a *App) addGrant(subjectType, subject, role, capability string, scope access.ScopeKind, scopeID, origin, by, reason string) ids.ID {
	id, err := a.Access.PutGrant(context.Background(), identity.Spec{SubjectType: subjectType, Subject: subject, Role: role, Capability: capability,
		Scope: scope, ScopeID: scopeID, Origin: origin, By: by, Reason: reason})
	if err != nil {
		log.Printf("grant: %v", err)
		return ids.Nil
	}
	return id
}

// Project membership comes from explicit project grants and administrator
// grants. An empty roster grants no authority.
func (a *App) projectMember(projectID ids.ID, user, role string) bool {
	if user == "" || user == "anonymous" {
		return false
	}
	return a.Access.HasProjectRole(context.Background(), access.Principal{Kind: "user", Name: user, Role: role}, projectID)
}

func TestUnauthorizedAdministratorRevokeCannotDemote(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	testAdministrator(t, a, "second")
	id := testGrantID(t, a.DB, "user", "boss", access.RoleInstanceAdmin, "", access.ScopeInstance, "")
	w := authzCall(a.Handler(), "outsider", "DELETE", fmt.Sprintf("/api/v1/authz/grants/%d", id), "")
	if w.Code != 403 {
		t.Fatalf("unauthorized revoke: %d %s", w.Code, w.Body)
	}
	testAccountRole(t, a, "boss", "admin")
	live := 0
	for _, g := range liveGrants(t, a, access.ScopeInstance, "", "user", "boss", access.RoleInstanceAdmin) {
		if g.ID == id {
			live++
		}
	}
	if live != 1 {
		t.Fatalf("an unauthorized revoke ended grant %d", id)
	}
}

func testAccountRole(t *testing.T, a *App, user, want string) {
	t.Helper()
	got, found, err := a.Access.Role(context.Background(), user)
	if err != nil || !found || got != want {
		t.Fatalf("%s role: %s found=%v want %s: %v", user, got, found, want, err)
	}
}

// What a restart would interrupt includes a running operation (a restore,
// a copy, a take-down), not only executing steps, so a deploy's guard
// waits for it; with none, the list is there and empty.
//
// Kills: apptown executing's route reading only the steps (soak finding 56).
func TestExecutingNamesARunningOperation(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	h := a.Handler()
	ctx := context.Background()
	read := func() httpapi.ExecutingStepsView {
		t.Helper()
		var v httpapi.ExecutingStepsView
		w := authzCall(h, "boss", "GET", "/api/v1/runs/executing", "")
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatalf("executing: %d %s", w.Code, w.Body.String())
		}
		return v
	}
	if v := read(); v.Operations == nil || len(v.Operations) != 0 {
		t.Fatalf("with nothing running the operations are an empty list: %+v", v)
	}
	inStep, release := make(chan struct{}), make(chan struct{})
	a.operations.Register(operations.Kind{Name: "test.restore", Capability: access.CapDeploymentDeploy,
		Execute: func(ctx context.Context, _ operations.Operation, log *operations.StepLog) operations.Outcome {
			log.Begin(ctx, "restoring postgres")
			close(inStep)
			<-release
			return operations.Outcome{State: operations.Succeeded}
		}})
	op, err := database.Transact(ctx, a.DB, func(tx *database.Tx) (*operations.Operation, error) {
		return a.operations.AdmitIn(ctx, tx, operations.Spec{Kind: "test.restore", ProjectID: idstest.N(1), Subject: "project:1/deployment:9",
			Input: map[string]any{"deployment": "fresh"}, Authority: access.Principal{Kind: "user", Name: "owner", Via: "open", Credential: identity.HeaderCredential("owner")}})
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = a.operations.Run(ctx, op.ID) }()
	t.Cleanup(func() { close(release); <-done })
	<-inStep
	v := read()
	if len(v.Operations) != 1 || v.Operations[0].Number != op.Number || v.Operations[0].Kind != "test.restore" || v.Operations[0].Deployment != "fresh" || v.Operations[0].Step != "restoring postgres" || v.Operations[0].AskedBy != "owner" {
		t.Fatalf("the running operation: %+v", v.Operations)
	}
}

// The operation routes and get_operation ask the operations owner: a
// reader reads it, cancel needs the kind's capability, and only the asker
// puts away an ended one. (That one the caller may not read is missing is
// the owner's: TestAnOperationIsReadOnlyByThoseWhoMayReadIt.)
func TestOperationRoutesAskTheOperationsOwner(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	h := a.Handler()
	ctx := context.Background()
	a.operations.Register(operations.Kind{Name: "test.root", Capability: access.CapDeploymentTeardown, Read: access.CapDeploymentRead, Cancellable: true,
		Execute: func(context.Context, operations.Operation, *operations.StepLog) operations.Outcome {
			return operations.Outcome{State: operations.Succeeded}
		}})
	op, err := database.Transact(ctx, a.DB, func(tx *database.Tx) (*operations.Operation, error) {
		return a.operations.AdmitIn(ctx, tx, operations.Spec{Kind: "test.root", ProjectID: idstest.N(1), Subject: "project:1/deployment:9",
			Authority: access.Principal{Kind: "user", Name: "owner", Via: "open", Credential: identity.HeaderCredential("owner")}})
	})
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/operations/%d", op.ID)
	if w := authzCall(h, "owner", "GET", path, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"queued"`) {
		t.Fatalf("the project's maintainer read: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "outsider", "POST", path+"/cancel", ""); w.Code != 403 {
		t.Fatalf("an outsider without the teardown grant cancelled: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "owner", "POST", path+"/dismiss", ""); w.Code == 200 {
		t.Fatalf("a queued operation was put away: %s", w.Body.String())
	}
	w := authzCall(h, "boss", "POST", path+"/cancel", "")
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"state":"cancelled"`) || w.Header().Get("Location") != path {
		t.Fatalf("cancel: %d %s %q", w.Code, w.Body.String(), w.Header().Get("Location"))
	}
	if w := authzCall(h, "boss", "POST", path+"/dismiss", ""); w.Code == 200 {
		t.Fatalf("someone else put away the asker's operation: %s", w.Body.String())
	}
	if w := authzCall(h, "owner", "POST", path+"/dismiss", ""); w.Code != 200 {
		t.Fatalf("the asker put it away: %d %s", w.Code, w.Body.String())
	}
	reader := mcp_token(t, a, "boss-agent", "boss", "admin")
	if res := mcp_call(t, a, reader, "get_operation", map[string]any{"id": op.ID}); res["isError"] == true {
		t.Fatalf("an administrator's agent reads an operation: %v", res)
	}
	if res := mcp_call(t, a, reader, "get_operation", map[string]any{"id": idstest.N(999999)}); res["isError"] != true {
		t.Fatalf("an operation that does not exist: %v", res)
	}

	// An operation outlives its project: once the project is deleted, an
	// instance administrator reads it; its project's former maintainer does
	// not, nor does an administrator's agent (an agent carries no instance
	// administration), though the asker can still put it away.
	ended, err := database.Transact(ctx, a.DB, func(tx *database.Tx) (*operations.Operation, error) {
		return a.operations.AdmitIn(ctx, tx, operations.Spec{Kind: "test.root", ProjectID: idstest.N(2), Subject: "project:2/deployment:9",
			Authority: access.Principal{Kind: "user", Name: "owner", Via: "open", Credential: identity.HeaderCredential("owner")}})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.operations.Run(ctx, ended.ID); err != nil {
		t.Fatal(err)
	}
	deleteProject(t, a, "open")
	if op := purgeProject(t, a, "open"); op.State != operations.Succeeded {
		t.Fatalf("purging the project: %s %q", op.State, op.Detail)
	}
	gone := fmt.Sprintf("/api/v1/operations/%d", ended.ID)
	if w := authzCall(h, "boss", "GET", gone, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"succeeded"`) {
		t.Fatalf("an administrator reads a deleted project's operation: %d %s", w.Code, w.Body.String())
	}
	if res := mcp_call(t, a, reader, "get_operation", map[string]any{"id": ended.ID}); res["isError"] != true || !strings.Contains(fmt.Sprint(res), "no such operation") {
		t.Fatalf("an administrator's agent read a deleted project's operation: %v", res)
	}
	if w := authzCall(h, "owner", "GET", gone, ""); w.Code != 404 {
		t.Fatalf("a non-administrator read a deleted project's operation: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "owner", "POST", gone+"/dismiss", ""); w.Code != 200 {
		t.Fatalf("the asker put away a deleted project's operation: %d %s", w.Code, w.Body.String())
	}
}

// A step agent reads its own project and the instance lists its table
// names, never the instance's own records: not the ledger, the people or
// their grants. Its reads cannot be bounded by a person's current grants
// (it has none), so revoking the shared read grant must not leave runs
// reading what people no longer may.
//
// Kills: implying every read for a run token, which reached instance
// resources because only a resource with a project was held to the run's.
func TestAStepTokenDoesNotReadTheInstancesOwnRecords(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	mux, err := newRouteMux(a, appRoutes(a))
	if err != nil {
		t.Fatal(err)
	}
	step := &authIdentity{User: "run/" + fixtureWork(t, a).Run.String(), Role: "member", Via: "run", ProjectID: idstest.N(1)}
	for _, path := range []string{"/api/v1/audit", "/api/v1/users", "/api/v1/groups", "/api/v1/authz/grants"} {
		if w := callAs(t, mux, step, "GET", path, nil); w.Code != 403 {
			t.Errorf("a step read %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/projects/game", "/api/v1/integrations"} {
		if w := callAs(t, mux, step, "GET", path, nil); w.Code != 200 {
			t.Errorf("a step lost an ordinary read %s: %d %s", path, w.Code, w.Body.String())
		}
	}

	// With the shared instance-read grant revoked (checklist 0.4), the
	// person whose run it is holds project authority only: neither they,
	// nor their chat, nor their run reads the instance's own records, and
	// each keeps the project reads its work needs.
	grants, err := a.Access.AllGrants(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	revoked := 0
	for _, g := range grants {
		if g.RoleName == access.RoleAuditor && g.RevokedAt.IsZero() {
			if _, err := a.Access.Revoke(t.Context(), g.ID, "boss"); err != nil {
				t.Fatal(err)
			}
			revoked++
		}
	}
	if revoked == 0 {
		t.Fatal("the fixture has no shared Auditor grant to revoke")
	}
	owner := &authIdentity{User: "owner", Via: "session"}
	chat := &authIdentity{User: "owner via console/c", Via: "console", ProjectID: idstest.N(1), ConsoleID: "c"}
	for _, who := range []*authIdentity{owner, chat, step} {
		for _, path := range []string{"/api/v1/audit", "/api/v1/users", "/api/v1/groups", "/api/v1/authz/grants"} {
			if w := callAs(t, mux, who, "GET", path, nil); w.Code != 403 {
				t.Errorf("%s read %s without an instance grant: %d %s", who.User, path, w.Code, w.Body.String())
			}
		}
		if w := callAs(t, mux, who, "GET", "/api/v1/projects/game", nil); w.Code != 200 {
			t.Errorf("%s lost its project read: %d %s", who.User, w.Code, w.Body.String())
		}
	}
}
