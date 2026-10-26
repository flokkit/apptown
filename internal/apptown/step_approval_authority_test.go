package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func TestStepApprovalHTTPKeepsActorAndDistinguishesConflictsFromWriteFailures(t *testing.T) {
	t.Parallel()
	a := authzFixtureWithApproval(t, workflow.ApprovalConfig{Quorum: 2})
	fx := fixtureWork(t, a)
	a.Work.Stop()
	h := a.Handler()
	dbtest.FailNextStatement(t, a.DB, "INSERT INTO run_events")
	if out := authzCall(h, "owner", "POST", "/api/v1/steps/"+fx.Accept.String()+"/approve", `{"decision":"approve"}`); out.Code != http.StatusInternalServerError {
		t.Fatalf("receipt failure: %d %s", out.Code, out.Body.String())
	}
	requireEvents(t, a, 0, eventKind("approval"))
	if out := authzCall(h, "owner", "POST", "/api/v1/steps/"+fx.Accept.String()+"/approve", `{"user":"boss","decision":"approve","comment":"reviewed"}`); out.Code != http.StatusOK {
		t.Fatalf("vote: %d %s", out.Code, out.Body.String())
	}
	requireEvents(t, a, 1, func(e workflow.Event) bool { return e.Kind == "approval" && payloadField(e, "by") == "owner" })
	if out := authzCall(h, "owner", "POST", "/api/v1/steps/"+fx.Accept.String()+"/approve", `{"decision":"approve"}`); out.Code != http.StatusConflict {
		t.Fatalf("duplicate: %d %s", out.Code, out.Body.String())
	}
}

type workPermissionFunc func(context.Context, access.Permission) (access.Caller, error)

func (f workPermissionFunc) Require(ctx context.Context, p access.Permission) (access.Caller, error) {
	return f(ctx, p)
}

// Approver enforcement over HTTP, in internal mode: the list binds to real
// identity, groups resolve, admins bypass, and the refusal names the list.
func TestApproverEnforcementHTTP(t *testing.T) {
	t.Parallel()
	a, h := internalApp(t)
	admin := sessionFrom(t, postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "boss", "boss-password")))
	for _, u := range []string{"partner", "kid"} {
		if w := postJSON(h, "/api/v1/users", map[string]string{"username": u, "password": u + "-password"}, admin); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	if w := postJSON(h, "/api/v1/groups", map[string]any{"name": "parents", "members": []string{"partner"}}, admin); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	partner := sessionFrom(t, postJSON(h, "/api/v1/auth/login", map[string]string{"username": "partner", "password": "partner-password"}))
	kid := sessionFrom(t, postJSON(h, "/api/v1/auth/login", map[string]string{"username": "kid", "password": "kid-password"}))

	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "p", Name: "p", Actor: "boss"})
	if err != nil {
		t.Fatal(err)
	}
	installFakeGit(t, a, nil)
	approvalSpec := schema.Spec{Kind: "lifecycle", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{
		{Name: "accept", Uses: "approval", With: map[string]any{"approvers": []string{"group:parents"}, "means": "we read it"}},
		{Name: "accept2", Uses: "approval", With: map[string]any{"approvers": []string{"nobody-else"}}},
	}}}}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "approvals", Version: 1}, Spec: approvalSpec})
	change, err := a.Work.Create(asPerson("boss"), workflow.ChangeDraft{ProjectID: p.ID, Title: "approval work"})
	if err != nil {
		t.Fatal(err)
	}
	running, err := a.Work.StartRun(asPerson("boss"), workflow.StartChangeRun{ChangeID: change.ID, Workflow: "approvals"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	snapshot, err := a.Work.Snapshot(asPerson("boss"), running.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 2 {
		t.Fatalf("approval steps: %+v %v", snapshot, err)
	}
	runID, stepID, step2 := running.ID, snapshot.Steps[0].ID, snapshot.Steps[1].ID
	// Being named in a step's approver list is one of two requirements
	// (design/subsystems/authorization-capabilities.md §8); the other is holding
	// change:approve on the project, which the parents get as a project role.
	run, _ := workflow.ReadRunIn(context.Background(), a.DB, runID)
	for subj, role := range map[string]string{"group:parents": access.RoleProjectApprover, "kid": access.RoleProjectContrib} {
		if w := postJSON(h, "/api/v1/authz/grants", map[string]any{"subject": subj, "role": role,
			"scope": "project", "scopeId": fmt.Sprint(run.ProjectID)}, admin); w.Code != 201 {
			t.Fatal(w.Body.String())
		}
	}

	// The change page asks the same rules before anyone votes, so it says
	// "waiting on you" only to someone who may decide.
	standing := func(who *http.Cookie) map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/changes/%d", change.ID), nil)
		req.AddCookie(who)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var view struct{ Approval map[string]any }
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || w.Code != 200 || view.Approval == nil {
			t.Fatalf("change page: %d %s %v", w.Code, w.Body.String(), err)
		}
		return view.Approval
	}
	if got := standing(partner); got["viewer"] != "yes" || got["stepId"] != stepID.String() {
		t.Fatalf("an approver's standing: %+v", got)
	}
	if got := standing(kid); got["viewer"] != "no" || !strings.Contains(fmt.Sprint(got["reason"]), "group:parents") {
		t.Fatalf("a non-approver's standing: %+v", got)
	}
	deny := postJSON(h, fmt.Sprintf("/api/v1/steps/%d/approve", stepID),
		map[string]string{"decision": "approve"}, kid)
	// Kid holds no change:approve, which the route states, so the refusal
	// names the role; the standing above names the list before anyone votes.
	if deny.Code != 403 || !strings.Contains(deny.Body.String(), "Project approver") {
		t.Fatalf("non-approver must be refused naming what is missing: %d %s", deny.Code, deny.Body.String())
	}
	ok := postJSON(h, fmt.Sprintf("/api/v1/steps/%d/approve", stepID),
		map[string]string{"decision": "approve", "comment": "yes"}, partner)
	if ok.Code != 200 {
		t.Fatalf("group member refused: %d %s", ok.Code, ok.Body.String())
	}
	st, _ := workflow.ReadStepIn(context.Background(), a.DB, stepID)
	if st.Status != workflow.StatusPass || !strings.Contains(string(st.Evidence), `"partner"`) {
		t.Fatalf("the ledger must record the person: %q %s", st.Status, st.Evidence)
	}

	// Admin bypass on the second declared step.
	if w := postJSON(h, fmt.Sprintf("/api/v1/steps/%d/approve", step2),
		map[string]string{"decision": "approve"}, admin); w.Code != 200 {
		t.Fatalf("admin bypass failed: %d %s", w.Code, w.Body.String())
	}
}

// This command-only double deliberately cannot authorize aggregate reads.
func (workPermissionFunc) VisibleProjects(context.Context, access.Capability) ([]ids.ID, error) {
	return nil, access.ErrAuthorityUnavailable
}

func TestStandingConsentHTTPUsesCurrentDeclaredMeaningAndVerifiedIssuer(t *testing.T) {
	a := authzActors(t)
	a.Ways.SyncStockWorkflows(t.Context())
	var workingYAML string
	for _, kind := range []string{"working", "landing"} {
		name := "janitor-work"
		system := "false"
		if kind == "landing" {
			name = "janitor-land"
			system = "true"
		}
		body := fmt.Sprintf(`apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: %s, system: %s}
spec:
  kind: pipeline
  role: %s
  stages:
    - name: accept
      steps:
        - name: approve
          uses: approval
          with:
            means: I reviewed this change
            standing: {class: routine-update, means: Bounded routine updates after all required checks pass}
`, name, system, kind)
		if kind == "working" {
			workingYAML = body
		}
		if _, _, err := a.Ways.PublishWorkflow(t.Context(), body, "boss"); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Ways.BindSystemWorkflow(t.Context(), "land", "janitor-land", "boss"); err != nil {
		t.Fatal(err)
	}
	p, err := a.Projects.BySlug(t.Context(), "game")
	if err != nil {
		t.Fatal(err)
	}
	projectID := p.ID
	if err := a.Adoption.SelectLane(t.Context(), projectID, "janitor-work", true, "boss"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Adoption.RecordCurrent(t.Context(), projectID, adoption.AdoptionEdit, "explicit standing declaration fixture", "boss"); err != nil {
		t.Fatal(err)
	}
	definition := fmt.Sprintf(`{"projects":[{"projectId":%q,"workflow":"janitor-work","environments":[]}],"categories":{"dependencies":"patch","securityFixes":true},"delivery":"land-only"}`, projectID.String())
	h := a.Handler()
	refused := authzCall(h, "outsider", "POST", "/api/v1/standing-approvals/preview", definition)
	if refused.Code != http.StatusForbidden {
		t.Fatalf("unscoped actor reached consent: %d %s", refused.Code, refused.Body)
	}
	question := authzCall(h, "boss", "POST", "/api/v1/standing-approvals/preview", definition)
	if question.Code != http.StatusOK {
		t.Fatalf("declared meaning preview: %d %s", question.Code, question.Body)
	}
	var preview struct {
		Digest string
		Shown  string
	}
	if err := json.Unmarshal(question.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Digest == "" || !strings.Contains(preview.Shown, "Running software remains unchanged") {
		t.Fatalf("wrong meaning: %+v", preview)
	}
	saved := authzCall(h, "boss", "POST", "/api/v1/standing-approvals", fmt.Sprintf(`{"definition":%s,"shownDigest":%q,"issuer":"outsider"}`, definition, preview.Digest))
	if saved.Code != http.StatusOK {
		t.Fatalf("save: %d %s", saved.Code, saved.Body)
	}
	var consent struct {
		RuleID   ids.ID
		Revision int64
		Issuer   struct{ Name string }
	}
	if err := json.Unmarshal(saved.Body.Bytes(), &consent); err != nil {
		t.Fatal(err)
	}
	if consent.RuleID.IsZero() || consent.Revision != 1 || consent.Issuer.Name != "boss" {
		t.Fatalf("stored issuer followed untrusted input: %+v", consent)
	}
	// This fixture seeds transparent instance reads. Remove that explicit
	// grant before asserting the unscoped-reader case; names alone are not scope.
	grants, err := a.Access.AllGrants(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range grants {
		if grant.Subject == identity.GroupAllUsers && grant.RoleName == access.RoleAuditor && grant.ScopeType == access.ScopeInstance {
			if _, err := a.Access.Revoke(t.Context(), grant.ID, "boss"); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, route := range []string{"/api/v1/standing-approvals", "/api/v1/standing-approvals/" + consent.RuleID.String() + "/history"} {
		read := authzCall(h, "boss", "GET", route, "")
		if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), consent.RuleID.String()) {
			t.Fatalf("owner read: %d %s", read.Code, read.Body)
		}
		other := authzCall(h, "outsider", "GET", route, "")
		if strings.Contains(other.Body.String(), consent.RuleID.String()) || other.Code != http.StatusOK && other.Code != http.StatusForbidden {
			t.Fatalf("unscoped read: %d %s", other.Code, other.Body)
		}
	}
	otherDisable := authzCall(h, "outsider", "POST", "/api/v1/standing-approvals/"+consent.RuleID.String()+"/disable", `{"expectedRevision":1}`)
	if otherDisable.Code != http.StatusForbidden {
		t.Fatalf("unscoped disable: %d %s", otherDisable.Code, otherDisable.Body)
	}
	disabled := authzCall(h, "boss", "POST", "/api/v1/standing-approvals/"+consent.RuleID.String()+"/disable", `{"expectedRevision":1}`)
	if disabled.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", disabled.Code, disabled.Body)
	}
	// A selected declaration alone is insufficient: its real lifecycle role
	// must still supply working acceptance after a governed version update.
	if _, _, err := a.Ways.PublishWorkflow(t.Context(), strings.Replace(strings.Replace(workingYAML, "role: working", "role: record", 1), "name: janitor-work", "name: janitor-record", 1), "boss"); err != nil {
		t.Fatal(err)
	}
	if err := a.Adoption.SelectLane(t.Context(), projectID, "janitor-record", true, "boss"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Adoption.RecordCurrent(t.Context(), projectID, adoption.AdoptionEdit, "record workflow is not working acceptance", "boss"); err != nil {
		t.Fatal(err)
	}
	wrongRole := authzCall(h, "boss", "POST", "/api/v1/standing-approvals/preview", strings.Replace(definition, "janitor-work", "janitor-record", 1))
	if wrongRole.Code != http.StatusConflict || !strings.Contains(wrongRole.Body.String(), "working role") {
		t.Fatalf("record purpose admitted as working acceptance: %d %s", wrongRole.Code, wrongRole.Body)
	}

	if err := a.Adoption.SelectLane(t.Context(), projectID, "janitor-work", false, "boss"); err != nil {
		t.Fatal(err)
	}
	withdrawn := authzCall(h, "boss", "POST", "/api/v1/standing-approvals/preview", definition)
	if withdrawn.Code != http.StatusUnprocessableEntity || !strings.Contains(withdrawn.Body.String(), "ways of working") {
		t.Fatalf("withdrawn selection was not a known refusal: %d %s", withdrawn.Code, withdrawn.Body)
	}
	if err := a.Adoption.SelectLane(t.Context(), projectID, "janitor-work", true, "boss"); err != nil {
		t.Fatal(err)
	}
	if err := a.Adoption.InstallPolicy(t.Context(), projectID, "standing-needs-check", "Check before acceptance", "requireStepBefore", map[string]any{"uses": "approval", "requires": "checks"}, "fixture", "boss"); err != nil {
		t.Fatal(err)
	}
	lawRefusal := authzCall(h, "boss", "POST", "/api/v1/standing-approvals/preview", definition)
	if lawRefusal.Code != http.StatusConflict || !strings.Contains(lawRefusal.Body.String(), "standing-needs-check") {
		t.Fatalf("current law ignored: %d %s", lawRefusal.Code, lawRefusal.Body)
	}

}
