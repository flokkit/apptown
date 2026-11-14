package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/database"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/attention"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/stock"
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
	if err := recordCatalogFixture(a.Adoption, t.Context(), projectID, "boss"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Adoption.RecordCurrent(t.Context(), projectID, adoption.AdoptionEdit, "explicit standing declaration fixture", "boss"); err != nil {
		t.Fatal(err)
	}
	definition := fmt.Sprintf(`{"projects":[{"projectId":%q,"workflow":"janitor-work","environments":[]}],"categories":{"dependencies":"routine","securityFixes":true},"delivery":"land-only"}`, projectID.String())
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

	// Assessment accepts identities only. A caller's claimed verdict cannot turn
	// unavailable committed source into routine evidence or automatic assent.
	change, e := a.Work.Create(asPerson("boss"), workflow.ChangeDraft{ProjectID: projectID, Title: "inspect missing committed source"})
	if e != nil {
		t.Fatal(e)
	}
	run, e := a.Work.StartRun(asPerson("boss"), workflow.StartChangeRun{ChangeID: change.ID, Workflow: "janitor-work"})
	if e != nil {
		t.Fatal(e)
	}
	a.Work.Wait()
	if e = a.Projects.SetDefaultBranch(t.Context(), projectID, "main"); e != nil {
		t.Fatal(e)
	}
	assessed := authzCall(h, "boss", "POST", "/api/v1/standing-approvals/"+consent.RuleID.String()+"/assess", fmt.Sprintf(`{"runId":%q,"classification":"routine-dependency","processingAvailable":true,"candidate":"untrusted-candidate"}`, run.ID.String()))
	if assessed.Code != http.StatusOK {
		t.Fatalf("exact assessment route: %d %s", assessed.Code, assessed.Body)
	}
	var inspection struct {
		ProcessingAvailable bool
		Inspection          struct {
			Classification string
			Candidate      string
		}
	}
	if e = json.Unmarshal(assessed.Body.Bytes(), &inspection); e != nil {
		t.Fatal(e)
	}
	if !inspection.ProcessingAvailable || inspection.Inspection.Classification != "cannot-assess" || inspection.Inspection.Candidate == "untrusted-candidate" {
		t.Fatalf("untrusted assessment accepted: %+v", inspection)
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

	deniedAssessment := authzCall(h, "outsider", "POST", "/api/v1/standing-approvals/"+consent.RuleID.String()+"/assess", fmt.Sprintf(`{"runId":%q}`, run.ID.String()))
	if deniedAssessment.Code != http.StatusForbidden {
		t.Fatalf("unscoped inspection: %d %s", deniedAssessment.Code, deniedAssessment.Body)
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
	if err := recordCatalogFixture(a.Adoption, t.Context(), projectID, "boss"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Adoption.RecordCurrent(t.Context(), projectID, adoption.AdoptionEdit, "record workflow is not working acceptance", "boss"); err != nil {
		t.Fatal(err)
	}
	wrongRole := authzCall(h, "boss", "POST", "/api/v1/standing-approvals/preview", strings.Replace(definition, "janitor-work", "janitor-record", 1))
	if wrongRole.Code != http.StatusConflict || !strings.Contains(wrongRole.Body.String(), "working role") {
		t.Fatalf("record purpose admitted as working acceptance: %d %s", wrongRole.Code, wrongRole.Body)
	}

	if err := a.Adoption.InstallPolicy(t.Context(), projectID, "standing-needs-check", "Check before acceptance", "requireStepBefore", map[string]any{"uses": "approval", "requires": "checks"}, "fixture", "boss"); err != nil {
		t.Fatal(err)
	}
	lawRefusal := authzCall(h, "boss", "POST", "/api/v1/standing-approvals/preview", definition)
	if lawRefusal.Code != http.StatusConflict || !strings.Contains(lawRefusal.Body.String(), "standing-needs-check") {
		t.Fatalf("current law ignored: %d %s", lawRefusal.Code, lawRefusal.Body)
	}

}

func TestJanitorInstancePreviewReadsMainAndReleaseSettings(t *testing.T) {
	a := authzActors(t)
	person, err := a.Access.EnsurePerson(t.Context(), "boss")
	if err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: person.PrincipalID, Kind: "user", Name: "boss", Via: "session"}, Verified: true})
	a.SyncStock(ctx)
	pid, _, _ := fleetRepo(t, a, "janitor-covered", map[string]string{".apptown/app.yaml": "app:\n  web:\n    static: site\n    public: true\n", "site/index.html": "hello"})
	java, _, _ := fleetRepo(t, a, "janitor-java", map[string]string{".apptown/app.yaml": "app:\n  web:\n    build: .\n    public: true\n    tools: [java]\n", "Dockerfile": "FROM scratch\n"})
	pack, err := a.Ways.Pack(ctx, "java")
	if err != nil {
		t.Fatal(err)
	}
	platform, err := a.Ways.Pack(ctx, adoption.PlatformPack)
	if err != nil {
		t.Fatal(err)
	}
	// On the current platform publication, as every project with a
	// repository is, so only its language pack moves.
	if err := a.DB.InTx(ctx, func(tx *database.Tx) error {
		return adoption.SetMembershipIn(ctx, tx, java, []adoption.Membership{{Pack: adoption.PlatformPack, Version: platform.Version}, {Pack: "java", Version: pack.Version}}, "language selection", "boss", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	if err := recordCatalogFixture(a.Adoption, ctx, pid, "boss"); err != nil {
		t.Fatal(err)
	}
	if err := recordCatalogFixture(a.Adoption, ctx, java, "boss"); err != nil {
		t.Fatal(err)
	}
	target, err := a.Deployment.Targets.Put(ctx, &deployment.Target{Name: "janitor-preview", Kind: deployment.KindDockerHost, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"automatic", "window", "person", "protected", "manual"} {
		attrs := deployment.Attributes{Feed: "main", Audience: "team", DataClass: "synthetic"}
		if name == "protected" {
			attrs.Audience = "public"
		}
		if name == "manual" {
			attrs.Feed = "manual"
		}
		dep, err := a.Deployment.Deployments.EnsureStanding(ctx, pid, name, target.ID, attrs)
		if err != nil {
			t.Fatal(err)
		}
		if name == "person" || name == "window" {
			setting := deployment.RoutineUpdates{Mode: name}
			if name == "window" {
				setting.Days = "sun"
				setting.From = "02:00"
				setting.To = "04:00"
				setting.Zone = "UTC"
			}
			if _, err := a.Deployment.SetRoutineUpdates(ctx, pid, name, deployment.RoutineUpdateSettings{Revision: dep.RoutineUpdatesRevision, RoutineUpdates: setting}); err != nil {
				t.Fatal(err)
			}
		}
	}
	in := workflow.StandingDefinition{Scope: "instance", Categories: workflow.StandingCategories{Dependencies: "routine", SecurityFixes: true}, Delivery: "full"}
	if err := json.Unmarshal([]byte(`{"schedules":[{"workflow":"janitor-update","crons":{"routine":"0 6 * * 1","aggressive":"0 6 * * *"}},{"workflow":"janitor-scan","crons":{"routine":"0 5 * * *","aggressive":"0 5 * * *"}}]}`), &in); err != nil {
		t.Fatal(err)
	}
	q, err := a.standingRules.Preview(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.Shown, "releases it to the running app") || !strings.Contains(q.Shown, "including ones created later") {
		t.Fatalf("missing consent: %s", q.Shown)
	}
	if len(q.Portfolio) != 2 {
		t.Fatalf("preview omitted active repository projects: %+v", q.Portfolio)
	}
	for _, p := range q.Portfolio {
		if p.ProjectID == pid {
			if !p.OnlyChecksStarts || len(p.Deployments) != 2 {
				t.Fatalf("covered preview: %+v", p)
			}
			for _, dep := range p.Deployments {
				if dep.Deployment != "automatic" && dep.Deployment != "window" {
					t.Fatalf("deployment should not get automatic releases: %+v", dep)
				}
			}
		} else if p.ProjectID == java {
			if !p.OnlyChecksStarts {
				t.Fatalf("Java preview: %+v", p)
			}
		}
	}
	// This project really has a Java part and its pack declares no dependency
	// fixes. Both dependency and pack consent apply there.
	if _, err := a.standingRules.Enable(ctx, workflow.StandingCommand{Definition: q.Definition, ShownDigest: q.Digest}); err != nil {
		t.Fatal(err)
	}
	if dependency, _, _, err := a.standingRules.StandingForWorkflow(ctx, java, "janitor-update"); err != nil || dependency.RuleID.IsZero() {
		t.Fatalf("Java without declarations did not get dependency work: %+v %v", dependency, err)
	}
	packRule := enablePackRule(t, a, ctx)
	declaration, err := a.Ways.PackDeclaration(ctx, "java")
	if err != nil {
		t.Fatal(err)
	}
	spec := declaration.Spec()
	spec.Suggested = !spec.Suggested
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	version, err := a.Ways.PublishPack(ctx, "java", declaration.Description, string(raw), declaration.Origin)
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := a.conformance.LatestWhatIf(ctx, "java", int64(version))
	if err != nil || assessment == nil {
		t.Fatalf("Java assessment: %+v %v", assessment, err)
	}
	if _, err := fleetReviewsFor(a).RunWhatIfJob(ctx, assessment.ID.String(), nil); err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	attempts, err := a.Work.Attempts(ctx, java, "packs", 10)
	if err != nil || len(attempts) != 1 || attempts[0].RuleID != packRule.RuleID || !attempts[0].Attempted {
		t.Fatalf("uncovered Java got no pack attempt: %+v %v", attempts, err)
	}

}

// Settings changes require current permission on the selected project, and a
// refused retry must leave the last authorized choice intact.
func TestRoutineUpdateSettingsHTTPChecksCurrentProjectAuthority(t *testing.T) {
	a := authzActors(t)
	pid := idstest.N(1)
	target, err := a.Deployment.Targets.Put(t.Context(), &deployment.Target{Name: "routine-settings", Kind: deployment.KindDockerHost, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := a.Deployment.Deployments.EnsureStanding(t.Context(), pid, "main", target.ID, deployment.Attributes{Feed: "main", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	path := "/api/v1/projects/game/deployments/main/routine-updates"
	body := fmt.Sprintf(`{"revision":%d,"routineUpdates":{"mode":"person"}}`, dep.RoutineUpdatesRevision)
	if w := authzCall(h, "outsider", "PUT", path, body); w.Code != 403 {
		t.Fatalf("unscoped write: %d %s", w.Code, w.Body)
	}
	grant, err := a.Access.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "outsider", Capability: string(access.CapDeploymentBind), Scope: access.ScopeProject, ScopeID: pid.String(), By: "boss"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "outsider", "PUT", path, body); w.Code != 200 {
		t.Fatalf("authorized write: %d %s", w.Code, w.Body)
	}
	current, err := a.Deployment.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.RoutineUpdates.Mode != "person" || current.RoutineUpdatesRevision != dep.RoutineUpdatesRevision+1 {
		t.Fatalf("settings not saved: %+v", current)
	}
	if _, err := a.Access.Revoke(t.Context(), grant, "boss"); err != nil {
		t.Fatal(err)
	}
	body = fmt.Sprintf(`{"revision":%d,"routineUpdates":{"mode":"automatic"}}`, current.RoutineUpdatesRevision)
	if w := authzCall(h, "outsider", "PUT", path, body); w.Code != 403 {
		t.Fatalf("revoked write: %d %s", w.Code, w.Body)
	}
	after, err := a.Deployment.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.RoutineUpdates != current.RoutineUpdates || after.RoutineUpdatesRevision != current.RoutineUpdatesRevision {
		t.Fatal("refused write changed settings")
	}
}

func TestJanitorSettingsHTTPChecksCurrentProjectAuthority(t *testing.T) {
	a := authzActors(t)
	pid := idstest.N(1)
	h := a.Handler()
	path := "/api/v1/projects/game/janitor/settings"
	body := `{"level":"off","reason":"inspect updates"}`
	if w := authzCall(h, "outsider", "POST", path, body); w.Code != 403 {
		t.Fatalf("unscoped write: %d %s", w.Code, w.Body)
	}
	grant, err := a.Access.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "outsider", Capability: string(access.CapProjectStandingApproval), Scope: access.ScopeProject, ScopeID: pid.String(), By: "boss"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "outsider", "POST", path, body); w.Code != 200 {
		t.Fatalf("authorized write: %d %s", w.Code, w.Body)
	}
	state, err := a.standingRules.ProjectState(asPerson("owner"), pid)
	if err != nil {
		t.Fatal(err)
	}
	if state.Level != "off" || state.Choice == nil || state.Choice.Person.Name != "outsider" || state.Choice.Reason != "inspect updates" {
		t.Fatalf("choice lost attribution: %+v", state)
	}
	if _, err := a.Access.Revoke(t.Context(), grant, "boss"); err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "outsider", "POST", path, `{"level":"aggressive","reason":"resume"}`); w.Code != 403 {
		t.Fatalf("revoked write: %d %s", w.Code, w.Body)
	}
	after, err := a.standingRules.ProjectState(asPerson("owner"), pid)
	if err != nil {
		t.Fatal(err)
	}
	if after.Level != state.Level || after.Choice == nil || after.Choice.Reason != state.Choice.Reason || after.Choice.At != state.Choice.At {
		t.Fatal("refused write changed choice")
	}
}

func packRuleDefinition(t *testing.T) workflow.StandingDefinition {
	t.Helper()
	var in workflow.StandingDefinition
	if err := json.Unmarshal([]byte(`{"scope":"instance","delivery":"full","categories":{"waysOfWorking":true},"schedules":[{"workflow":"janitor-adopt","crons":{}}]}`), &in); err != nil {
		t.Fatal(err)
	}
	return in
}
func enablePackRule(t *testing.T, a *App, ctx context.Context) workflow.StandingRevision {
	t.Helper()
	q, err := a.standingRules.Preview(ctx, packRuleDefinition(t))
	if err != nil {
		t.Fatal(err)
	}
	rule, err := a.standingRules.Enable(ctx, workflow.StandingCommand{Definition: q.Definition, ShownDigest: q.Digest})
	if err != nil {
		t.Fatal(err)
	}
	return rule
}

// Both entry points start fresh assessments, even when a person already finished
// one. A static project on an old pack without fixes is eligible for packs alone.
func TestJanitorPackPublicationAndEnableCatchUp(t *testing.T) { testPackAdoption(t, "") }
func TestAManualAdoptionRepairsWithoutAStandingRule(t *testing.T) {
	testPackAdoption(t, "manual-repair")
}
func TestAYesInAdvanceIsTheCandidatesQuestion(t *testing.T) { testPackAdoption(t, "manual-advance") }

func testPackAdoption(t *testing.T, only string) {
	for _, scenario := range []struct {
		before bool
		kind   string
	}{{false, "manual-repair"}, {false, "manual-advance"}, {true, "empty"}, {false, "empty"}, {true, "guidance"}, {true, "passing-check"}, {true, "repair"}, {true, "repair-exhausted"}, {false, "two-packs"}, {false, "two-packs-reverse"}} {
		if only != "" && scenario.kind != only || only == "" && strings.HasPrefix(scenario.kind, "manual-") {
			continue
		}
		t.Run(fmt.Sprintf("%t/%s", scenario.before, scenario.kind), func(t *testing.T) {
			enableBefore := scenario.before
			a := authzActors(t)
			ctx := verifiedContext(t.Context(), testAdmittedPrincipalForPacks(t, a))
			a.SyncStock(ctx)
			verification.SyncStockScanners(ctx, a.Integrations, stock.FS)
			tgLocalSh(t, a)
			pid, _, _ := fleetRepo(t, a, "pack-static", map[string]string{".apptown/app.yaml": "app:\n  web:\n    static: site\n    public: true\n", "site/index.html": "hello"})
			block, err := a.Ways.CreateBlock(ctx, "team/rules", "Original guidance.", "", "boss")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Ways.PublishPack(ctx, "old-language", "Old language", `{"guidance":["team/rules"]}`, ""); err != nil {
				t.Fatal(err)
			}
			if err := recordCatalogFixture(a.Adoption, ctx, pid, "boss"); err != nil {
				t.Fatal(err)
			}

			manual := strings.HasPrefix(scenario.kind, "manual-")
			if scenario.kind == "manual-advance" {
				body, _ := stock.FS.ReadFile("janitor-adopt.yaml")
				lines := strings.Split(string(body), "\n")
				var old []string
				for _, line := range lines {
					if !strings.Contains(line, "inAdvance:") {
						old = append(old, line)
					}
				}
				if _, err := wow.NewWorkflows(a.DB).StockUpdate(ctx, "janitor-adopt", "Pack updates", strings.Join(old, "\n")); err != nil {
					t.Fatal(err)
				}
				landing, err := stock.FS.ReadFile("land.yaml")
				if err != nil {
					t.Fatal(err)
				}
				oldLanding := strings.Replace(string(landing), `inAdvance: "`, `inAdvance: "OLDER: `, 1)
				if _, err := wow.NewWorkflows(a.DB).StockUpdate(ctx, "land", "Land", oldLanding); err != nil {
					t.Fatal(err)
				}
				a.Ways.SyncStockPacks(ctx)
			}
			// Every project with a repository is on the platform pack; this one is
			// on its current publication, so only its language pack is outdated.
			picks := []adoption.PackPick{platformPick(t, a, ctx), {Pack: "old-language", Version: 1}}
			twoPacks := strings.HasPrefix(scenario.kind, "two-packs")
			if twoPacks {
				if _, err := a.Ways.PublishPack(ctx, "second-language", "Second language", `{}`, ""); err != nil {
					t.Fatal(err)
				}
				picks = append(picks, adoption.PackPick{Pack: "second-language", Version: 1})
			}
			cand, err := a.Adoption.Prepare(ctx, pid, adoption.Request{Packs: picks})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.lifecycle.Adopt(adoption.WithSystemAuthority(ctx, pid), a.projectByID(ctx, pid), cand, "boss", "start on v1"); err != nil {
				t.Fatal(err)
			}

			if scenario.kind == "manual-advance" {
				a.SyncStock(ctx)
			}
			repairs := 0
			// The session runner is an external seam; repair scheduling, committed code,
			// real guidance checks, standing votes and landing stay in the product.
			// Retain the real fix and routine runners; substitute external tests/boot/scans.
			rebuildWork(t, a, func(d *workflow.Dependencies) {
				d.Runners["agent/pi"] = func(e *workflow.Execution) {
					if _, err := e.Claim(); err != nil {
						return
					}
					repairs++
					source, err := workflow.ReadSourceIn(e.Context(), a.DB, workflow.RunRef{ID: e.Run().ID, ProjectID: e.Run().ProjectID})
					if err != nil {
						t.Fatal(err)
					}
					folder, err := source.Workspace(a.cfg.dataDir)
					if err != nil {
						t.Fatal(err)
					}
					file := "fixed.html"
					if scenario.kind == "repair-exhausted" {
						file = "still-broken.html"
					}
					if err := os.WriteFile(filepath.Join(folder, "site", file), []byte("repaired by the session"), 0644); err != nil {
						t.Fatal(err)
					}
					if err := repositoryedit.CommitStep(e.Context(), a.DB, a.cfg.dataDir, a.branches, repositoryPublisher{attachments: a.repositoryAttachments, db: a.DB, now: a.cfg.clock.Now}, e.Run(), e.Step(), a.cfg.clock.Now()); err != nil {
						t.Fatal(err)
					}
					e.Settle(workflow.EncodeOutcome(workflow.StatusPass, map[string]any{"reason": "repaired application code"}, nil))
				}
				for _, kind := range []string{"tests", "preview", "scan", "build"} {
					d.Runners[kind] = func(e *workflow.Execution) {
						if _, err := e.Claim(); err != nil {
							return
						}
						source, err := workflow.ReadSourceIn(e.Context(), a.DB, workflow.RunRef{ID: e.Run().ID, ProjectID: e.Run().ProjectID})
						if err != nil {
							t.Fatal(err)
						}
						sel, err := verification.SelectCommit(e.Context(), a.DB, a.cfg.dataDir, e.Run(), source)
						if err != nil {
							t.Fatal(err)
						}
						if e.Step().Type == "build" {
							outcome, err := workflow.EncodeOutcome(workflow.StatusPass, map[string]any{"commit": sel.Commit, "runtime": false, "cleanTreeBuild": "pass"}, nil)
							if err != nil {
								t.Fatal(err)
							}
							outcome.Artifacts = []workflow.StepArtifact{{ArtifactDraft: workflow.ArtifactDraft{Kind: "build", Name: "no-runtime", Ref: sel.Commit, Body: "External build substituted in the adoption test."}}}
							e.Settle(outcome, nil)
							return
						}
						e.Settle((workflow.CheckEvidence{Checks: []workflow.CheckReceipt{{Check: e.Step().Name, Verdict: workflow.VerdictNotApplicable, Detail: "static site has no container"}}}).Outcome(workflow.StatusNotApplicable, map[string]any{"commit": sel.Commit, "candidate": sel.Candidate}))
					}
				}
			})
			var rule workflow.StandingRevision
			if enableBefore {
				rule = enablePackRule(t, a, ctx)
			}
			// Use the shipped container declarations from the demo case, without
			// the unrelated front-page runtime probe, which needs a running preview.
			var containerChecks []json.RawMessage
			for _, name := range []string{"deploy/health-declared", "deploy/memory-declared"} {
				b, err := a.Ways.BlockByName(ctx, name)
				if err != nil {
					t.Fatal(err)
				}
				observed, err := a.Ways.Block(ctx, wow.BlockRef{Name: name, Version: b.LatestVersion})
				if err != nil {
					t.Fatal(err)
				}
				var checks []json.RawMessage
				if err := json.Unmarshal([]byte(observed.ChecksJSON), &checks); err != nil {
					t.Fatal(err)
				}
				for _, check := range checks {
					var c struct{ Name string }
					if err := json.Unmarshal(check, &c); err != nil {
						t.Fatal(err)
					}
					if c.Name == "readiness-declared" || c.Name == "memory-declared" {
						containerChecks = append(containerChecks, check)
					}
				}
			}
			staticBlock, err := a.Ways.CreateBlock(ctx, "team/static-container", "Container declarations.", "", "boss")
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(containerChecks)
			if err := a.Ways.BumpBlock(ctx, staticBlock.ID, "Container declarations.", string(raw), "boss", staticBlock.Name); err != nil {
				t.Fatal(err)
			}
			checks := "[]"
			if scenario.kind == "passing-check" {
				checks = checksJSON(shCheck("site-exists", "test -f site/index.html", "high"))
			}
			if strings.Contains(scenario.kind, "repair") {
				checks = checksJSON(shCheck("repair-exists", "test -f site/fixed.html", "high"))
			}
			if err := a.Ways.BumpBlock(ctx, block.ID, "Published guidance.", checks, "boss", block.Name); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Ways.PublishPack(ctx, "old-language", "Old language", `{"guidance":["team/rules","build/container-image","team/static-container"]}`, ""); err != nil {
				t.Fatal(err)
			}
			if twoPacks {
				if _, err := a.Ways.PublishPack(ctx, "second-language", "Second language", `{"description":"second publication"}`, ""); err != nil {
					t.Fatal(err)
				}
			}

			if manual {
				setTestFeatureAs(t, a, "what-ifs", true, "boss")
				id, err := fleetReviewsFor(a).StartWhatIf(ctx, conformance.WhatIfRequest{Pack: "old-language", Version: 2, Projects: []ids.ID{pid}, Also: []conformance.PackVersion{{Pack: "apptown", Version: platformPick(t, a, ctx).Version}}})
				if err != nil {
					t.Fatal(err)
				}
				runWhatIf(t, a, ctx, id)
				shown := []string(nil)
				if scenario.kind == "manual-advance" {
					response := authzCall(a.Handler(), "boss", "GET", "/api/v1/projects/pack-static/adoption/in-advance?fromWhatIf="+id.String(), "")
					var question workflow.InAdvanceQuestion
					if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &question) != nil || question.Workflow != "janitor-adopt" || question.NotOffered != "" {
						t.Fatalf("candidate question: %d %s", response.Code, response.Body.String())
					}
					if len(question.Landing) == 0 || strings.Contains(strings.Join(question.Landing, " "), "OLDER:") {
						t.Fatalf("landing question used current receipt: %+v", question)
					}
					shown = question.Shown()
				}
				input := map[string]any{"fromWhatIf": id, "viaChange": true}
				if len(shown) > 0 {
					input["inAdvance"] = map[string]any{"shown": shown}
				}
				raw, _ := json.Marshal(input)
				response := authzCall(a.Handler(), "boss", "POST", "/api/v1/projects/pack-static/adoption/adopt", string(raw))
				var opened struct {
					ChangeID ids.ID
					RunID    ids.ID
					Workflow string
				}
				if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &opened) != nil || opened.Workflow != "janitor-adopt" || opened.RunID.IsZero() {
					t.Fatalf("manual adoption: %d %s", response.Code, response.Body.String())
				}
				a.Work.Wait()
				snapshot, err := a.Work.Snapshot(ctx, opened.RunID, workflow.DetailView)
				if err != nil {
					t.Fatal(err)
				}
				wantRepairs := 0
				if scenario.kind == "manual-repair" {
					wantRepairs = 1
				}
				if repairs != wantRepairs {
					t.Fatalf("manual repair sessions=%d want=%d: %+v", repairs, wantRepairs, snapshot)
				}
				accept := false
				for _, step := range snapshot.Steps {
					if step.Name == "routine" && step.Status != workflow.StatusPass {
						t.Fatalf("ordinary routine: %+v", step)
					}
					if step.Name == "accept" {
						accept = true
						want := workflow.StatusPending
						if len(shown) > 0 {
							want = workflow.StatusPass
						}
						if step.Status != want {
							t.Fatalf("person's acceptance: %+v", step)
						}
					}
				}
				if !accept {
					t.Fatal("no acceptance step")
				}
				attempts, err := a.Work.Attempts(ctx, pid, "packs", 10)
				if err != nil || len(attempts) != 0 {
					t.Fatalf("manual work entered queue: %+v %v", attempts, err)
				}
				return
			}
			// PackSpec drops unknown fields in resolution, but the authored declaration
			// itself is versioned; publishing unchanged data never starts a second review.
			if !enableBefore {
				id, err := fleetReviewsFor(a).StartWhatIf(ctx, conformance.WhatIfRequest{Pack: "old-language", Version: 2})
				if err != nil {
					t.Fatal(err)
				}
				runWhatIf(t, a, ctx, id)
				rule = enablePackRule(t, a, ctx)
			}
			// The named pack is the first outdated publication; the other
			// updates live in Also. Find automatic work whichever pack leads.
			names := []string{"old-language"}
			if twoPacks {
				names = append(names, "second-language")
			}
			var latest *conformance.WhatIf
			var assessments []ids.ID
			for _, name := range names {
				w, err := a.conformance.LatestWhatIf(ctx, name, 2)
				if err != nil {
					t.Fatal(err)
				}
				if w == nil || w.RuleID != rule.RuleID {
					continue
				}
				if w.RuleRevision != rule.Revision || w.RequestedBy != "apptown-system" {
					t.Fatalf("automatic assessment: %+v", w)
				}
				latest = w
				assessments = append(assessments, w.ID)
			}
			if latest == nil {
				t.Fatal("no automatic assessment for the project's outdated packs")
			}
			// Deliver all work in either order, including the separate per-pack
			// assessments produced if the whole-project guard regresses.
			if scenario.kind == "two-packs-reverse" {
				slices.Reverse(assessments)
			}
			for _, id := range assessments {
				if _, err := fleetReviewsFor(a).RunWhatIfJob(ctx, id.String(), nil); err != nil {
					t.Fatal(err)
				}
				a.Work.Wait()
			}
			finished, err := a.conformance.WhatIf(ctx, latest.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, project := range finished.Projects {
				if project.ProjectID == pid {
					want := conformance.WhatIfNoNewFailures
					if strings.Contains(scenario.kind, "repair") {
						want = conformance.WhatIfNewFailures
					}
					if project.Outcome != want || project.Score.NotApplicable < 4 {
						t.Fatalf("static what-if disagrees with adoption: %+v", project)
					}
				}
			}
			attempts, err := a.Work.Attempts(ctx, pid, "packs", 10)
			if err != nil || len(attempts) != 1 || attempts[0].RuleID != rule.RuleID || attempts[0].RuleRevision != rule.Revision || !attempts[0].Attempted || attempts[0].Fingerprint == "" {
				fresh, _ := a.conformance.WhatIf(ctx, latest.ID)
				eligible, _, _, _ := a.standingRules.StandingForWorkflow(ctx, pid, "janitor-adopt")
				t.Fatalf("packs attempt: %+v %v; assessment=%+v rule=%+v", attempts, err, fresh, eligible)
			}
			if scenario.kind == "repair-exhausted" {
				if repairs != 2 || !attempts[0].Failed {
					t.Fatalf("repair limit: sessions=%d attempt=%+v", repairs, attempts[0])
				}
				return
			}
			wantRepairs := 0
			if scenario.kind == "repair" {
				wantRepairs = 1
			}
			if repairs != wantRepairs {
				ch, _ := a.Work.Change(ctx, attempts[0].ChangeID)
				snap, _ := a.Work.Snapshot(ctx, ch.RunID, workflow.DetailView)
				for _, step := range snap.Steps {
					if step.Status == workflow.StatusFail || step.Status == workflow.StatusError {
						t.Logf("failed %s: %s", step.Name, step.Evidence)
					}
				}
				t.Fatalf("repair sessions=%d want %d", repairs, wantRepairs)
			}
			if dep, _, _, err := a.standingRules.StandingForWorkflow(ctx, pid, "janitor-update"); err != nil || !dep.RuleID.IsZero() {
				t.Fatalf("pack consent granted dependencies: %+v %v", dep, err)
			}
			// A person's earlier review cannot suppress catch-up, and job redelivery
			// cannot open a second Change for this assessed candidate.
			if _, err := fleetReviewsFor(a).RunWhatIfJob(ctx, latest.ID.String(), nil); err != nil {
				t.Fatal(err)
			}
			again, err := a.Work.Attempts(ctx, pid, "packs", 10)
			if err != nil || len(again) != 1 {
				t.Fatalf("repeated delivery: %+v %v", again, err)
			}
			for _, job := range operationJobs(t, a, standingAcceptanceJob) {
				result, err := a.Work.RunStandingAcceptance(ctx, job.Key, nil)
				if err != nil || result["cast"] != true {
					ch, _ := a.Work.Change(ctx, attempts[0].ChangeID)
					snap, _ := a.Work.Snapshot(ctx, ch.RunID, workflow.DetailView)
					t.Fatalf("standing acceptance: %+v %v snapshot=%+v", result, err, snap)
				}
			}
			// The standing vote advances the run itself, as a person's vote
			// does: nothing else would.
			a.Work.Wait()
			ch, _ := a.Work.Change(ctx, attempts[0].ChangeID)
			if snap, err := a.Work.Snapshot(ctx, ch.RunID, workflow.DetailView); err != nil || snap.Run.Status == workflow.RunRunning {
				t.Fatalf("the run did not advance after the standing vote: %+v %v", snap.Run, err)
			}

			// Run the durable landing follow-up through the ordinary merge and adoption settlement.
			for _, job := range operationJobs(t, a, workflowCompletionJob) {
				if strings.HasSuffix(job.Key, "/land") {
					if _, err := workflowFollowUps(a).Complete(ctx, job.Key, nil); err != nil {
						t.Fatal(err)
					}
				}
			}
			a.Work.Wait()
			// The daemon advances a newly entered approval stage on its next turn.
			for i := 0; i < 3; i++ {
				current, err := a.Work.Change(ctx, attempts[0].ChangeID)
				if err != nil {
					t.Fatal(err)
				}
				if current.State == workflow.ChangeShipped {
					break
				}
				if _, err := a.Work.Advance(ctx, current.RunID); err != nil {
					t.Fatal(err)
				}
				a.Work.Wait()
			}
			for _, job := range operationJobs(t, a, workflowCompletionJob) {
				if strings.HasSuffix(job.Key, "/adopt") {
					if _, err := workflowFollowUps(a).Complete(ctx, job.Key, nil); err != nil {
						t.Fatal(err)
					}
				}
			}
			landed, err := a.Work.Change(ctx, attempts[0].ChangeID)
			if err != nil || landed.State != workflow.ChangeShipped {
				snap, _ := a.Work.Snapshot(ctx, landed.RunID, workflow.DetailView)
				t.Fatalf("adoption did not land: state=%s %v run=%+v events=%+v jobs=%+v", landed.State, err, snap.Run, snap.Events, operationJobs(t, a, workflowCompletionJob))
			}
			history, err := a.Work.LandedJanitorChanges(ctx, a.cfg.clock.Now().Add(-7*24*time.Hour), a.cfg.clock.Now().Add(time.Nanosecond), 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			recorded := false
			for _, entry := range history.Changes {
				if entry.ChangeID == landed.ID {
					recorded = entry.RuleID == rule.RuleID && entry.RuleRevision == rule.Revision && entry.Issuer.Principal == rule.Issuer.Principal && slices.ContainsFunc(entry.PackVersions, func(p adoption.PackPick) bool { return p.Pack == "old-language" && p.Version == 2 })
				}
			}
			if !recorded {
				t.Fatalf("landed pack versions or original grant missing: %+v", history)
			}
			members, err := adoption.ProjectPacksIn(ctx, a.DB, pid)
			if err != nil || !slices.ContainsFunc(members, func(p adoption.ProjectPackRecord) bool { return p.Pack == "old-language" && p.Version == 2 }) {
				t.Fatalf("target not adopted: %+v %v", members, err)
			}
			if twoPacks && !slices.ContainsFunc(members, func(p adoption.ProjectPackRecord) bool { return p.Pack == "second-language" && p.Version == 2 }) {
				t.Fatalf("second target not adopted: %+v", members)
			}
			// The job uses current authority, not the publication's saved issuer label.
			if err := a.standingRules.SetProjectChoice(ctx, pid, "off", "pause pack updates"); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Ways.PublishPack(ctx, "old-language", "Old language", `{"description":"third publication"}`, ""); err != nil {
				t.Fatal(err)
			}
			third, err := a.conformance.LatestWhatIf(ctx, "old-language", 3)
			if err != nil || third == nil {
				t.Fatalf("third review: %+v %v", third, err)
			}
			if _, err := fleetReviewsFor(a).RunWhatIfJob(ctx, third.ID.String(), nil); err != nil {
				t.Fatal(err)
			}
			now, err := a.Work.Attempts(ctx, pid, "packs", 10)
			if err != nil || len(now) != 1 {
				t.Fatalf("off project got work: %+v %v", now, err)
			}
		})
	}
}

// Switching Keep packs current on reaches a repository-backed project that
// holds no pack at all: every such project belongs on the platform pack, so
// the rule assesses it for the platform publication with no per-project
// step. A project with no repository stays outside.
func TestKeepPacksCurrentReachesAProjectWithoutThePlatformPack(t *testing.T) {
	a := authzActors(t)
	ctx := verifiedContext(t.Context(), testAdmittedPrincipalForPacks(t, a))
	a.SyncStock(ctx)
	pid, _, _ := fleetRepo(t, a, "packless", map[string]string{".apptown/app.yaml": "app:\n  web:\n    static: site\n    public: true\n", "site/index.html": "hello"})
	var noRepo ids.ID
	if err := a.DB.InTx(t.Context(), func(tx *database.Tx) error {
		var err error
		noRepo, err = project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "no-repo", Name: "no-repo"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Both have an adoption record that names no pack, as projects created
	// without one do.
	for _, id := range []ids.ID{pid, noRepo} {
		if err := recordCatalogFixture(a.Adoption, ctx, id, "boss"); err != nil {
			t.Fatal(err)
		}
	}
	platform := platformPick(t, a, ctx)
	rule := enablePackRule(t, a, ctx)
	w, err := a.conformance.LatestWhatIf(ctx, adoption.PlatformPack, platform.Version)
	if err != nil || w == nil || w.RuleID != rule.RuleID || w.RequestedBy != "apptown-system" {
		t.Fatalf("no automatic platform assessment: %+v %v", w, err)
	}
	found := map[ids.ID]bool{}
	for _, p := range w.Projects {
		found[p.ProjectID] = true
	}
	if !found[pid] || found[noRepo] {
		t.Fatalf("assessed projects: %+v", w.Projects)
	}
}

// platformPick publishes the platform pack again without its guidance and
// returns that publication: its workflows (janitor-adopt among them) stay,
// while its runtime probes, which need a running preview this harness
// substitutes, would leave checks unknown and hold every standing vote.
func platformPick(t *testing.T, a *App, ctx context.Context) adoption.PackPick {
	t.Helper()
	pack, err := a.Ways.Pack(ctx, adoption.PlatformPack)
	if err != nil {
		t.Fatal(err)
	}
	spec := pack.Spec()
	spec.Guidance = []string{}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	version, err := a.Ways.PublishPack(ctx, adoption.PlatformPack, pack.Description, string(raw), "stock")
	if err != nil {
		t.Fatal(err)
	}
	return adoption.PackPick{Pack: adoption.PlatformPack, Version: int64(version)}
}

func testAdmittedPrincipalForPacks(t *testing.T, a *App) access.Principal {
	return access.Principal{ID: testPrincipalOf(t, a.DB, "boss"), Kind: "user", Name: "boss", Via: "session"}
}

// A candidate supplies the procedure that its run will execute. The current
// adoption may still pin the actual triggerless declaration from the old release.
func TestPackPublicationBootstrapsOldTriggerlessPinAndUsesDeclaredQueue(t *testing.T) {
	for _, name := range []string{"janitor-adopt", "keep-packs"} {
		t.Run(name, func(t *testing.T) {
			a := authzActors(t)
			ctx := verifiedContext(t.Context(), testAdmittedPrincipalForPacks(t, a))
			a.SyncStock(ctx)
			q, err := a.standingRules.Preview(ctx, workflow.StandingDefinition{Scope: "instance", Delivery: "full", Categories: workflow.StandingCategories{WaysOfWorking: true}})
			if err != nil {
				t.Fatal(err)
			}
			for i := range q.Projects {
				q.Projects[i].Procedure.Acceptance.Workflow = name
			}
			body, err := stock.FS.ReadFile("janitor-adopt.yaml")
			if err != nil {
				t.Fatal(err)
			}
			current := strings.ReplaceAll(string(body), "janitor-adopt", name)
			current = strings.Replace(current, "queue: packs", "queue: pack-moves", 1)
			old := strings.Replace(current, "  on:\n    publication: {queue: pack-moves, standing: true}\n", "", 1)
			old = strings.Replace(old, "  title: Pack updates\n", "", 1)
			oldStock := strings.Replace(string(body), "  on:\n    publication: {queue: packs, standing: true}\n", "", 1)
			oldStock = strings.Replace(oldStock, "  title: Pack updates\n", "", 1)
			initial := fstest.MapFS{"janitor-adopt.yaml": &fstest.MapFile{Data: []byte(oldStock)}}
			initial[name+".yaml"] = &fstest.MapFile{Data: []byte(old)}
			release := wow.New(a.DB, wow.Dependencies{Stock: initial})
			release.SyncStockWorkflows(ctx)
			platform, err := a.Ways.Pack(ctx, adoption.PlatformPack)
			if err != nil {
				t.Fatal(err)
			}
			spec := platform.Spec()
			spec.Guidance = nil
			for i, n := range spec.Workflows {
				if n == "janitor-adopt" {
					spec.Workflows[i] = name
				}
			}
			raw, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			version, err := a.Ways.PublishPack(ctx, adoption.PlatformPack, platform.Description, string(raw), "stock")
			if err != nil {
				t.Fatal(err)
			}
			pid, _, _ := fleetRepo(t, a, "bootstrap", map[string]string{".apptown/app.yaml": "app:\n  web:\n    static: site\n    public: true\n", "site/index.html": "hello"})
			if err := recordCatalogFixture(a.Adoption, ctx, pid, "boss"); err != nil {
				t.Fatal(err)
			}
			candidate, err := a.Adoption.Prepare(ctx, pid, adoption.Request{Packs: []adoption.PackPick{{Pack: adoption.PlatformPack, Version: int64(version)}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.lifecycle.Adopt(adoption.WithSystemAuthority(ctx, pid), a.projectByID(ctx, pid), candidate, "boss", "old publication"); err != nil {
				t.Fatal(err)
			}
			q.Shown = "Keep packs current. AppTown moves projects to newly published versions of the packs they use, with up to two repair sessions."
			q.Digest = "old-recorded-pack-consent"
			rule := workflow.StandingRevision{RuleID: ids.New(time.Now()), Revision: 1, Enabled: true, Question: q, Issuer: workflow.StandingRevision{}.Issuer, EnabledAt: time.Now()}
			rule.Issuer.Name = "boss"
			rule.Issuer.Principal = testPrincipalOf(t, a.DB, "boss")
			rule.Issuer.Via = "session"
			record, err := json.Marshal(rule)
			if err != nil {
				t.Fatal(err)
			}
			var legacy map[string]any
			if err := json.Unmarshal(record, &legacy); err != nil {
				t.Fatal(err)
			}
			legacy["question"].(map[string]any)["definition"].(map[string]any)["schedules"] = []any{map[string]any{"workflow": name, "crons": map[string]string{}}}
			record, err = json.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			// sql-guarantee: no current operation can create retired consent JSON.
			if _, err := a.DB.SQL().ExecContext(ctx, `INSERT INTO standing_rules(id,current_revision) VALUES($1,1)`, rule.RuleID); err != nil {
				t.Fatal(err)
			}
			// sql-guarantee: preserve the old sentence and definition as recorded evidence.
			if _, err := a.DB.SQL().ExecContext(ctx, `INSERT INTO standing_rule_revisions(rule_id,revision,enabled,record_json) VALUES($1,1,true,$2)`, rule.RuleID, record); err != nil {
				t.Fatal(err)
			}
			for publication := 1; publication <= 2; publication++ {
				release = wow.New(a.DB, wow.Dependencies{Stock: fstest.MapFS{fmt.Sprintf("%s.yaml", name): &fstest.MapFile{Data: []byte(current)}}})
				release.SyncStockWorkflows(ctx)
				// A changed publication captures the candidate's current workflow pins.

				raw, err = json.Marshal(spec)
				if err != nil {
					t.Fatal(err)
				}
				var published map[string]any
				if err := json.Unmarshal(raw, &published); err != nil {
					t.Fatal(err)
				}
				published["description"] = fmt.Sprintf("Publication %d", publication)
				raw, err = json.Marshal(published)
				if err != nil {
					t.Fatal(err)
				}
				version, err = a.Ways.PublishPack(ctx, adoption.PlatformPack, platform.Description, string(raw), "stock")
				if err != nil {
					t.Fatal(err)
				}
				assessment, err := a.conformance.LatestWhatIf(ctx, adoption.PlatformPack, int64(version))
				if err != nil || assessment == nil {
					t.Fatalf("%+v %v", assessment, err)
				}
				finished := runWhatIf(t, a, ctx, assessment.ID)
				if finished.State != conformance.WhatIfDone {
					t.Fatal(finished.State)
				}
				attempts, err := a.Work.Attempts(ctx, pid, "pack-moves", 10)
				if err != nil || len(attempts) != publication {
					t.Fatalf("attempts %+v %v", attempts, err)
				}
				change, err := a.Work.Change(ctx, attempts[0].ChangeID)
				if err != nil {
					t.Fatal(err)
				}
				runs, err := a.Work.ChangeRuns(ctx, change.ID, workflow.ChangeRuns{})
				if err != nil || len(runs) == 0 || runs[0].WorkflowName != name {
					t.Fatalf("runs %+v %v", runs, err)
				}
				if _, err := a.Work.Abandon(ctx, workflow.StopWork{ChangeID: change.ID, Reason: "test adopts the prepared publication"}); err != nil {
					t.Fatal(err)
				}
				// The opened run was dispatched; let it settle before the
				// next publication and before the App shuts down.
				a.Work.Wait()
				picks := []adoption.PackPick{{Pack: adoption.PlatformPack, Version: int64(version)}}
				candidate, err = a.Adoption.Prepare(ctx, pid, adoption.Request{Packs: picks})
				if err != nil {
					t.Fatal(err)
				}
				if err := a.DB.InTx(ctx, func(tx *database.Tx) error {
					_, err := a.Adoption.AdoptIn(ctx, tx, pid, *candidate, candidate.ExpectedRevision, "boss", "prepared publication", adoption.OperationSelect)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestJanitorNightlyCatchUpUsesCurrentRule(t *testing.T) {
	a := authzActors(t)
	ctx := verifiedContext(t.Context(), testAdmittedPrincipalForPacks(t, a))
	a.SyncStock(ctx)
	enablePackRule(t, a, ctx)
	// These projects appear after enablement and publication, so neither event
	// can be responsible for the nightly handler's work.
	behind, _, _ := fleetRepo(t, a, "behind", map[string]string{"x": "x"})
	current, _, _ := fleetRepo(t, a, "current", map[string]string{"x": "x"})
	for _, id := range []ids.ID{behind, current} {
		if err := recordCatalogFixture(a.Adoption, ctx, id, "boss"); err != nil {
			t.Fatal(err)
		}
	}
	candidate, err := a.Adoption.Prepare(ctx, current, adoption.Request{Packs: []adoption.PackPick{platformPick(t, a, ctx)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.lifecycle.Adopt(adoption.WithSystemAuthority(ctx, current), a.projectByID(ctx, current), candidate, "boss", "fixture current"); err != nil {
		t.Fatal(err)
	}
	midnight := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scheduled := false
	for _, schedule := range apptownJobSchedules() {
		if schedule.Operation == "packs-catch-up" {
			scheduled = true
			if got := schedule.When.Next(midnight); !got.Equal(midnight.Add(4*time.Hour + 30*time.Minute)) {
				t.Fatalf("catch-up schedule: %s", got)
			}
		}
	}
	if !scheduled {
		t.Fatal("no nightly pack catch-up schedule")
	}
	job, ok := apptownJobKinds(a)["packs-catch-up"]
	if !ok {
		t.Fatal("no catch-up handler")
	}
	before, err := a.conformance.RecentWhatIfs(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := job.Run(ctx, "daily", nil); err != nil {
		t.Fatal(err)
	}
	after, err := a.conformance.RecentWhatIfs(ctx, 100)
	if err != nil || len(after) != len(before)+1 {
		t.Fatalf("nightly assessments: before=%d after=%d err=%v", len(before), len(after), err)
	}
	w, err := a.conformance.WhatIf(ctx, after[0].ID)
	if err != nil || len(w.Projects) != 1 || w.Projects[0].ProjectID != behind || w.RuleID.IsZero() {
		t.Fatalf("behind project under consent: %+v %v", w, err)
	}
	// A fresh instance has no standing rule; the same registered handler starts nothing.
	off := authzActors(t)
	offCtx := verifiedContext(t.Context(), testAdmittedPrincipalForPacks(t, off))
	off.SyncStock(offCtx)
	fleetRepo(t, off, "off", map[string]string{"x": "x"})
	if _, err := apptownJobKinds(off)["packs-catch-up"].Run(offCtx, "daily", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := off.conformance.RecentWhatIfs(offCtx, 100); err != nil || len(got) != 0 {
		t.Fatalf("rule off started work: %+v %v", got, err)
	}
}

func TestJanitorAttentionUsesTheJanitorProjectSet(t *testing.T) {
	a := authzActors(t)
	ctx := verifiedContext(t.Context(), testAdmittedPrincipalForPacks(t, a))
	a.SyncStock(ctx)
	active, _, _ := fleetRepo(t, a, "active", map[string]string{"x": "x"})
	deleted, _, _ := fleetRepo(t, a, "deleted", map[string]string{"x": "x"})
	copy, _, _ := fleetRepo(t, a, "copy", map[string]string{"x": "x"})
	evalCopyForTest(t, a, copy)
	if _, err := a.deletion.Request(ctx, deleted); err != nil {
		t.Fatal(err)
	}
	if state := passDeletion(t, a, deleted); state.State != project.Deleted || !state.KeptUntil.Valid {
		t.Fatalf("deleted project retained for inspection: %+v", state)
	}
	source := attention.Scans{Work: a.Work, Projects: a.Projects}
	t.Run("unscheduled active project", func(t *testing.T) {
		got, err := source.ProjectSnapshot(ctx, active)
		if err != nil || !got.Known || len(got.Items) != 1 || got.Items[0].Detail != "Its packs schedule no scan. Adopting the platform pack would scan it." {
			t.Fatalf("active project's scans: %+v %v", got, err)
		}
	})
	for _, id := range []ids.ID{deleted, copy} {
		t.Run(id.String(), func(t *testing.T) {
			got, err := source.ProjectSnapshot(ctx, id)
			if err != nil || !got.Known || len(got.Items) != 0 {
				t.Fatalf("excluded project's scans coverage: %+v %v", got, err)
			}
		})
	}

	dbtest.FailNextStatement(t, a.DB, "-- name: JanitorProjectLabels :many")
	if _, err := source.ProjectSnapshot(ctx, active); err == nil {
		t.Fatal("eligibility read failure became exclusion")
	}
}
