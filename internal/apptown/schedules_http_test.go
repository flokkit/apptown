package apptown

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"

	"encoding/json"
	"github.com/flokkit/apptown/internal/database"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A project's schedules are a project read: its members see them, someone
// outside the project is refused, and a failed read of the firing records
// fails rather than answering that the project has no schedules.
func TestProjectSchedulesAreAProjectReadThatNeverReadsFailureAsNone(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	revokeReadingForAll(t, a) // only the project's own people read it
	h := a.Handler()
	yaml := `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: monthly}
spec:
  kind: pipeline
  on:
    schedule: {cron: "0 6 1 * *"}
  stages:
    - name: review
      steps:
        - name: approve
          uses: approval
          with: {approvers: [owner], means: "the monthly review is complete"}
`
	if _, _, err := a.Ways.PublishWorkflow(t.Context(), yaml, "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ways.PublishPack(t.Context(), "monthly-work", "Monthly work", `{"workflows":["monthly"],"schedules":["monthly"]}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	pack, err := a.Ways.Pack(t.Context(), "monthly-work")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DB.InTx(t.Context(), func(tx *database.Tx) error {
		return adoption.SetMembershipIn(t.Context(), tx, idstest.N(1), []adoption.Membership{{Pack: pack.Name, Version: pack.Version}}, "fixture", "fixture", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	if err := recordCatalogFixture(a.Adoption, t.Context(), idstest.N(1), "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Adoption.RecordCurrent(t.Context(), idstest.N(1), adoption.AdoptionEdit, "fixture selects monthly", "fixture"); err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "outsider", "GET", "/api/v1/projects/game/schedules", ""); w.Code != 403 {
		t.Fatalf("outsider: %d %s", w.Code, w.Body.String())
	}
	w := authzCall(h, "owner", "GET", "/api/v1/projects/game/schedules", "")
	var got httpapi.ScheduleList
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("owner: %d %s", w.Code, w.Body.String())
	}
	if len(got.Schedules) != 1 || got.EvalCopy {
		t.Fatalf("schedules: %s", w.Body.String())
	}
	if s := got.Schedules[0]; s.Source.Workflow != "monthly" || s.Cron != "0 6 1 * *" || s.TimeZone != "UTC" || s.Next == "" || s.Last != nil {
		t.Fatalf("schedule: %+v", s)
	}
	dbtest.FailNextStatement(t, a.DB, "name: WorkflowSchedulesOfProject :many")
	if w := authzCall(h, "owner", "GET", "/api/v1/projects/game/schedules", ""); w.Code < 500 {
		t.Fatalf("a failed read answered %d: %s", w.Code, w.Body.String())
	}
}

func TestJanitorPageReadsAreAvailableToInstanceReadersWithoutManagement(t *testing.T) {
	a := authzActors(t)
	h := a.Handler()
	a.addGrant("user", "owner", "", string(access.CapProjectStandingApproval), access.ScopeProject, idstest.N(1).String(), "boss", "")
	// Rule creation uses declaration-only test data with the real consent owner.
	// The page reads the same immutable grant without assessing eligibility.
	rules := workflow.NewStandingRules(a.DB, workflow.StandingDependencies{Authority: access.Checked{Authority: a.Access, Projects: a.Projects}, Identity: a.Access, Procedures: janitorPageProcedure{}})
	definition := workflow.StandingDefinition{Projects: []workflow.StandingProject{{ProjectID: idstest.N(1), Workflow: "restore-fixture"}}, Categories: workflow.StandingCategories{Dependencies: "routine", SecurityFixes: true}, Delivery: "land-only"}
	ctx := verifiedContext(t.Context(), access.Principal{ID: testPrincipalOf(t, a.DB, "boss"), Kind: "user", Name: "boss", Via: "header", Credential: identity.HeaderCredential("boss")})
	q, err := rules.Preview(ctx, definition)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := rules.Enable(ctx, workflow.StandingCommand{Definition: definition, ShownDigest: q.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/janitor/settings", `{"level":"off","reason":"Waiting for a release"}`); w.Code != 200 {
		t.Fatalf("choice: %d %s", w.Code, w.Body)
	}
	w := authzCall(h, "outsider", "GET", "/api/v1/janitor", "")
	var got httpapi.JanitorView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}
	if len(got.Projects) != 2 || len(got.Rules) != 1 || got.Rules[0].RuleID != rule.RuleID {
		t.Fatalf("view=%s", w.Body)
	}
	found := false
	for _, p := range got.Projects {
		if p.ProjectID == idstest.N(1) {
			found = p.Level == "off" && p.Choice != nil && p.Choice.Person.Name == "owner" && p.Choice.At != "" && p.Choice.Reason == "Waiting for a release"
		}
	}
	if !found {
		t.Fatalf("project choice missing: %s", w.Body)
	}
	denied := authzCall(h, "outsider", "POST", "/api/v1/standing-approvals/"+rule.RuleID.String()+"/disable", fmt.Sprintf(`{"expectedRevision":%d}`, rule.Revision))
	if denied.Code != 403 {
		t.Fatalf("reader revoked consent: %d %s", denied.Code, denied.Body)
	}
	for _, path := range []string{"/api/v1/janitor/history", "/api/v1/janitor/history?period=older&before=2026-11-01T00:00:00Z&offset=20"} {
		if w := authzCall(h, "outsider", "GET", path, ""); w.Code != 200 {
			t.Fatalf("history: %d %s", w.Code, w.Body)
		}
	}
	for _, path := range []string{"/api/v1/janitor/history?before=no", "/api/v1/janitor/history?offset=-1"} {
		if w := authzCall(h, "outsider", "GET", path, ""); w.Code != 400 {
			t.Fatalf("bad query: %d %s", w.Code, w.Body)
		}
	}
	dbtest.FailNextStatement(t, a.DB, "-- name: JanitorProjectChoices")
	if w := authzCall(h, "outsider", "GET", "/api/v1/janitor", ""); w.Code < 500 {
		t.Fatalf("failed read: %d %s", w.Code, w.Body)
	}
	revokeReadingForAll(t, a)
	if w := authzCall(h, "outsider", "GET", "/api/v1/janitor", ""); w.Code != 403 {
		t.Fatalf("instance read bypass: %d %s", w.Code, w.Body)
	}
}

type janitorPageProcedure struct{}

func (janitorPageProcedure) StandingProcedure(context.Context, database.Queryer, ids.ID, string) (workflow.StandingProcedure, error) {
	return workflow.StandingProcedure{Acceptance: workflow.StandingPurpose{Workflow: "restore-fixture", Version: 1, Step: "accept", Means: "accept bounded updates after required checks"}, Landing: workflow.StandingPurpose{Workflow: "land", Version: 1, Step: "accept", Means: "land the checked update"}}, nil
}

// A project with no repository yet (no main branch) is outside the janitor's
// coverage; enabling the instance rule must not turn the page into an error.
func TestJanitorPageReadsWithAnInstanceRuleAndAProjectWithoutARepository(t *testing.T) {
	a := authzActors(t)
	person, err := a.Access.EnsurePerson(t.Context(), "boss")
	if err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: person.PrincipalID, Kind: "user", Name: "boss", Via: "session"}, Verified: true})
	a.SyncStock(ctx)
	covered, _, _ := fleetRepo(t, a, "janitor-covered", map[string]string{".apptown/app.yaml": "app:\n  web:\n    static: site\n    public: true\n", "site/index.html": "hello"})
	empty, err := a.Projects.BySlug(t.Context(), "game")
	if err != nil {
		t.Fatal(err)
	}
	if empty.DefaultBranch != "" {
		t.Fatalf("fixture project unexpectedly has a repository: %q", empty.DefaultBranch)
	}
	for _, pid := range []ids.ID{covered, empty.ID} {
		if err := recordCatalogFixture(a.Adoption, ctx, pid, "boss"); err != nil {
			t.Fatal(err)
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
	if _, err := a.standingRules.Enable(ctx, workflow.StandingCommand{Definition: q.Definition, ShownDigest: q.Digest}); err != nil {
		t.Fatal(err)
	}
	if rule, _, err := a.standingRules.StandingForWorkflow(ctx, empty.ID, "janitor-update"); err != nil || !rule.RuleID.IsZero() {
		t.Fatalf("a project without a repository: rule=%+v err=%v", rule, err)
	}
	w := authzCall(a.Handler(), "boss", "GET", "/api/v1/janitor", "")
	var got httpapi.JanitorView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("janitor page: %d %s", w.Code, w.Body)
	}
	rows := map[ids.ID]httpapi.JanitorProject{}
	for _, p := range got.Projects {
		rows[p.ProjectID] = p
	}
	// It reads as no repository, outside the janitor's reach: never an error
	// from asking Git for an empty branch name. Its own scans page answers too.
	if row, ok := rows[empty.ID]; !ok || row.Problem != "" || !row.Scans.NoRepository || row.Scans.Known {
		t.Fatalf("project without a repository: %s", w.Body)
	}
	if w := authzCall(a.Handler(), "boss", "GET", "/api/v1/projects/game/scans", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"noRepository":true`) {
		t.Fatalf("scans page of a project without a repository: %d %s", w.Code, w.Body)
	}
	if p, ok := rows[covered]; !ok || !p.Enabled || strings.HasPrefix(p.Problem, "Eligibility not known") {
		t.Fatalf("covered project: %+v", p)
	}
}

// Scan now fires the project's janitor-scan schedule through the HTTP route:
// a caller who may start work but not open it is refused with nothing fired,
// a schedule without its standing approval is refused, and an eligible one
// opens the schedule's change and records the firing. The Janitor read names
// why a scan cannot start.
func TestScanNowFiresTheJanitorScanScheduleForAPersonWhoMayOpenWork(t *testing.T) {
	a := authzActors(t)
	person, err := a.Access.EnsurePerson(t.Context(), "boss")
	if err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: person.PrincipalID, Kind: "user", Name: "boss", Via: "session"}, Verified: true})
	a.SyncStock(ctx)
	covered, _, _ := fleetRepo(t, a, "janitor-scan-now", map[string]string{".apptown/app.yaml": "app:\n  web:\n    static: site\n    public: true\n", "site/index.html": "hello"})
	platform, err := a.Ways.Pack(t.Context(), "apptown")
	if err != nil {
		t.Fatal(err)
	}
	// The adoption owner's membership participant puts the project on the
	// platform pack, which schedules janitor-scan, without a repository change.
	if err := a.DB.InTx(t.Context(), func(tx *database.Tx) error {
		return adoption.SetMembershipIn(t.Context(), tx, covered, []adoption.Membership{{Pack: platform.Name, Version: platform.Version}}, "fixture", "fixture", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	if err := recordCatalogFixture(a.Adoption, ctx, covered, "boss"); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	scanNow := func() string {
		w := authzCall(h, "boss", "GET", "/api/v1/janitor", "")
		var got httpapi.JanitorView
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
			t.Fatalf("janitor page: %d %s", w.Code, w.Body)
		}
		for _, p := range got.Projects {
			if p.ProjectID == covered {
				return p.ScanNow
			}
		}
		t.Fatalf("project missing: %s", w.Body)
		return ""
	}
	const run = "/api/v1/projects/janitor-scan-now/schedules/janitor-scan/run"
	if got := scanNow(); got != "Turned off" {
		t.Fatalf("scan without a janitor approval: %q", got)
	}
	if w := authzCall(h, "boss", "POST", run, ""); w.Code != 422 {
		t.Fatalf("fired without a standing approval: %d %s", w.Code, w.Body)
	}
	in := workflow.StandingDefinition{Scope: "instance", Categories: workflow.StandingCategories{Dependencies: "routine", SecurityFixes: true}, Delivery: "full"}
	if err := json.Unmarshal([]byte(`{"schedules":[{"workflow":"janitor-update","crons":{"routine":"0 6 * * 1","aggressive":"0 6 * * *"}},{"workflow":"janitor-scan","crons":{"routine":"0 5 * * *","aggressive":"0 5 * * *"}}]}`), &in); err != nil {
		t.Fatal(err)
	}
	q, err := a.standingRules.Preview(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.standingRules.Enable(ctx, workflow.StandingCommand{Definition: q.Definition, ShownDigest: q.Digest}); err != nil {
		t.Fatal(err)
	}
	if got := scanNow(); got != "" {
		t.Fatalf("an eligible scan: %q", got)
	}
	a.addGrant("user", "outsider", "", string(access.CapChangeInvokeLane), access.ScopeProject, covered.String(), "boss", "")
	if w := authzCall(h, "outsider", "POST", run, ""); w.Code != 403 {
		t.Fatalf("starting work without opening it: %d %s", w.Code, w.Body)
	}
	last := func() *httpapi.ScheduleFiringView {
		w := authzCall(h, "boss", "GET", "/api/v1/projects/janitor-scan-now/schedules", "")
		var got httpapi.ScheduleList
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
			t.Fatalf("schedules: %d %s", w.Code, w.Body)
		}
		for _, s := range got.Schedules {
			if s.Source.Workflow == "janitor-scan" {
				return s.Last
			}
		}
		t.Fatalf("janitor-scan not scheduled: %s", w.Body)
		return nil
	}
	if l := last(); l != nil {
		t.Fatalf("a refused request fired: %+v", l)
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/projects/janitor-scan-now/schedules/no-such/run", ""); w.Code != 404 {
		t.Fatalf("an unscheduled workflow: %d %s", w.Code, w.Body)
	}
	w := authzCall(h, "boss", "POST", run, "")
	var started httpapi.ScheduleRunView
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &started) != nil || started.ChangeID.IsZero() || started.ChangeNumber == 0 {
		t.Fatalf("scan now: %d %s", w.Code, w.Body)
	}
	if l := last(); l == nil || l.Outcome != workflow.ScheduleOpened || l.ChangeID != started.ChangeID {
		t.Fatalf("the firing was not recorded: %+v", l)
	}
}
