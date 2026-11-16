package apptown

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing/fstest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workcommands"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/ids"

	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database"

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
	var got changeactions.ProjectSchedules
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("owner: %d %s", w.Code, w.Body.String())
	}
	if len(got.Schedules) != 1 || got.EvalCopy {
		t.Fatalf("schedules: %s", w.Body.String())
	}
	if s := got.Schedules[0]; s.Source.Workflow != "monthly" || s.Cron != "0 6 1 * *" || s.TimeZone != "UTC" || s.Next.IsZero() || s.Last != nil {
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
	definition := workflow.JanitorChoices{Dependencies: "routine", SecurityFixes: true}
	ctx := verifiedContext(t.Context(), access.Principal{ID: testPrincipalOf(t, a.DB, "boss"), Kind: "user", Name: "boss", Via: "header", Credential: identity.HeaderCredential("boss")})
	rule, err := rules.Save(ctx, workflow.JanitorChoicesCommand{Choices: definition})
	if err != nil {
		t.Fatal(err)
	}
	if w := authzCall(h, "owner", "POST", "/api/v1/projects/game/janitor/settings", `{"level":"off"}`); w.Code != 200 {
		t.Fatalf("choice: %d %s", w.Code, w.Body)
	}
	w := authzCall(h, "outsider", "GET", "/api/v1/janitor", "")
	var got httpapi.JanitorView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}
	if len(got.Projects) != 2 || got.Choices == nil || got.Choices.RuleID != rule.RuleID {
		t.Fatalf("view=%s", w.Body)
	}
	found := false
	for _, p := range got.Projects {
		if p.ProjectID == idstest.N(1) {
			found = p.Level == "off" && p.Choice != nil && p.Choice.Person.Name == "owner" && p.Choice.At != ""
		}
	}
	if !found {
		t.Fatalf("project choice missing: %s", w.Body)
	}
	denied := authzCall(h, "outsider", "PUT", "/api/v1/janitor/choices", fmt.Sprintf(`{"choices":{"dependencies":"off"},"expectedRevision":%d}`, rule.Revision))
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

func (janitorPageProcedure) standingProcedure(context.Context, database.Queryer, ids.ID, string) (workflow.StandingProcedure, error) {
	return workflow.StandingProcedure{Trigger: "schedule", Acceptance: workflow.StandingPurpose{Workflow: "restore-fixture", Version: 1, Step: "accept", Means: "accept bounded updates after required checks"}, Landing: workflow.StandingPurpose{Workflow: "land", Version: 1, Step: "accept", Means: "land the checked update"}}, nil
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
	covered, _, _ := fleetRepo(t, a, "janitor-covered", map[string]string{".apptown/app.yaml": "staticServer: caddy:2.11.9-alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\napp:\n  web:\n    static: site\n    public: true\n", "site/index.html": "hello"})
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
	in := workflow.JanitorChoices{Dependencies: "routine", SecurityFixes: true}
	if err := json.Unmarshal([]byte(`{"schedules":[{"workflow":"janitor-update","crons":{"routine":"0 6 * * 1","aggressive":"0 6 * * *"}},{"workflow":"janitor-scan","crons":{"routine":"0 5 * * *","aggressive":"0 5 * * *"}}]}`), &in); err != nil {
		t.Fatal(err)
	}
	if _, err := a.standingRules.Save(ctx, workflow.JanitorChoicesCommand{Choices: in}); err != nil {
		t.Fatal(err)
	}
	if rule, _, _, err := standingEligibilityOne(a.standingRules, ctx, empty.ID, "janitor-update"); err != nil || !rule.RuleID.IsZero() {
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
	if row, ok := rows[empty.ID]; !ok || row.State.Word != "no repository" || row.State.Unknown {
		t.Fatalf("project without a repository: %s", w.Body)
	}
	if w := authzCall(a.Handler(), "boss", "GET", "/api/v1/projects/game/scans", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"noRepository":true`) {
		t.Fatalf("scans page of a project without a repository: %d %s", w.Code, w.Body)
	}
	if p, ok := rows[covered]; !ok || p.State.Word == "off" || strings.HasPrefix(p.State.Help, "Eligibility not known") {
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
	covered, _, _ := fleetRepo(t, a, "janitor-scan-now", map[string]string{".apptown/app.yaml": "staticServer: caddy:2.11.9-alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\napp:\n  web:\n    static: site\n    public: true\n", "site/index.html": "hello"})
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
	const run = "/api/v1/projects/janitor-scan-now/schedules/janitor-scan/run"
	if w := authzCall(h, "boss", "POST", run, ""); w.Code != 422 {
		t.Fatalf("fired without a standing approval: %d %s", w.Code, w.Body)
	}
	in := workflow.JanitorChoices{Dependencies: "routine", SecurityFixes: true}
	if err := json.Unmarshal([]byte(`{"schedules":[{"workflow":"janitor-update","crons":{"routine":"0 6 * * 1","aggressive":"0 6 * * *"}},{"workflow":"janitor-scan","crons":{"routine":"0 5 * * *","aggressive":"0 5 * * *"}}]}`), &in); err != nil {
		t.Fatal(err)
	}
	if _, err := a.standingRules.Save(ctx, workflow.JanitorChoicesCommand{Choices: in}); err != nil {
		t.Fatal(err)
	}
	a.addGrant("user", "outsider", "", string(access.CapChangeInvokeLane), access.ScopeProject, covered.String(), "boss", "")
	if w := authzCall(h, "outsider", "POST", run, ""); w.Code != 403 {
		t.Fatalf("starting work without opening it: %d %s", w.Code, w.Body)
	}
	last := func() *workflow.ScheduleRecord {
		w := authzCall(h, "boss", "GET", "/api/v1/projects/janitor-scan-now/schedules", "")
		var got changeactions.ProjectSchedules
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
	janitorRun := "/api/v1/projects/janitor-scan-now/janitor/run"
	if w := authzCall(h, "outsider", "POST", janitorRun, `{}`); w.Code != 403 {
		t.Fatalf("run without create: %d %s", w.Code, w.Body)
	}
	a.addGrant("user", "outsider", "", string(access.CapChangeCreate), access.ScopeProject, covered.String(), "boss", "")
	engine := access.WithCaller(ctx, access.Caller{Verified: true, Receipt: "system", Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule", ProjectID: covered}})
	blocking, err := a.Work.Create(engine, workflow.ChangeDraft{ProjectID: covered, Type: "scheduled", Title: "Blocked security update", Queue: "security", Schedule: "janitor-scan"})
	if err != nil {
		t.Fatal(err)
	}
	started.ChangeID = blocking.ID
	body := fmt.Sprintf(`{"queue":"security","again":true,"changeId":%q}`, started.ChangeID)
	if w := authzCall(h, "outsider", "POST", janitorRun, body); w.Code != 403 {
		t.Fatalf("again without close: %d %s", w.Code, w.Body)
	}
	ch, err := a.Work.Change(ctx, started.ChangeID)
	if err != nil || ch.State == workflow.ChangeAbandoned {
		t.Fatalf("refused close changed work: %+v %v", ch, err)
	}
	stale := fmt.Sprintf(`{"queue":"security","again":true,"changeId":%q}`, ids.New(time.Now()))
	if w := authzCall(h, "boss", "POST", janitorRun, stale); w.Code != 409 {
		t.Fatalf("stale retry: %d %s", w.Code, w.Body)
	}

}

func (f janitorPageProcedure) StandingProcedures(ctx context.Context, q database.Queryer, id ids.ID, names []string) (map[string]workflow.StandingProcedureResult, error) {
	out := map[string]workflow.StandingProcedureResult{}
	for _, name := range names {
		proc, err := f.standingProcedure(ctx, q, id, name)
		out[name] = workflow.StandingProcedureResult{Procedure: proc, Err: err}
	}
	return out, nil
}

// Caller and standing issuer facts stay constant as the visible fleet grows.
func TestFleetReadsAuthorityFactsOnceForTheSet(t *testing.T) {
	t.Parallel()
	a := authzActors(t)
	person, err := a.Access.EnsurePerson(t.Context(), "boss")
	if err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{ID: person.PrincipalID, Kind: "user", Name: "boss", Via: "session"}})
	a.SyncStock(ctx)
	pack, err := a.Ways.Pack(ctx, "apptown")
	if err != nil {
		t.Fatal(err)
	}
	projects := []ids.ID{}
	add := func(i int) {
		id, _, _ := fleetRepo(t, a, fmt.Sprintf("shape-%d", i), map[string]string{".apptown/app.yaml": "staticServer: caddy:2.11.9-alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\napp:\n  web:\n    static: site\n    public: true\n", "site/index.html": "hello"})
		err := a.DB.InTx(ctx, func(tx *database.Tx) error {
			return adoption.SetMembershipIn(ctx, tx, id, []adoption.Membership{{Pack: pack.Name, Version: pack.Version}}, "fixture", "fixture", time.Now())
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := recordCatalogFixture(a.Adoption, ctx, id, "boss"); err != nil {
			t.Fatal(err)
		}
		projects = append(projects, id)
	}
	add(0)
	add(1)
	in := workflow.JanitorChoices{Dependencies: "routine", SecurityFixes: true}
	if _, err := a.standingRules.Save(ctx, workflow.JanitorChoicesCommand{Choices: in}); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	counts := func() map[string]int {
		out := map[string]int{}
		for _, path := range []string{"/api/v1/janitor", "/api/v1/attention"} {
			for _, name := range []string{"UserByPrincipal", "ActiveGrantsOfPrincipal"} {
				count, stop := a.DB.CountStatements("-- name: " + name + " :")
				w := authzCall(h, "boss", "GET", path, "")
				stop()
				if w.Code != 200 {
					t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
				}
				out[path+name] = count()
			}
		}
		return out
	}
	two := counts()
	add(2)
	add(3)
	four := counts()
	for key, n := range two {
		if n == 0 || four[key] != n {
			t.Errorf("%s: 2 projects=%d, 4=%d", key, n, four[key])
		}
	}
	count, stop := a.DB.CountStatements("-- name: CurrentStandingRules :")
	set := []workflow.ProjectWorkflows{}
	for _, id := range projects[:2] {
		set = append(set, workflow.ProjectWorkflows{ProjectID: id, Workflows: []string{"janitor-scan", "janitor-update"}})
	}
	results, err := a.standingRules.StandingEligibility(ctx, set)
	stop()
	if err != nil || count() != 1 {
		t.Fatalf("rules read %d times: %v", count(), err)
	}
	for _, id := range projects[:2] {
		for _, name := range set[0].Workflows {
			r := results[id][name]
			if r.Err != nil || r.Refused != "" || r.Rule.RuleID.IsZero() {
				t.Fatalf("%s %s: %+v", id, name, r)
			}
		}
	}
	facts, _, _, err := a.Work.JanitorProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stopFailure := a.DB.FailNextStatement("-- name: CurrentStandingRules :")
	defer stopFailure()
	schedules := changeactions.Schedules{Standing: &a.standingRules, Actions: a.changeActions, Adoption: a.Adoption, Projects: a.Projects, DB: a.DB}
	views, err := schedules.ForJanitor(ctx, facts, time.Now())
	if err != nil {
		t.Fatalf("shared eligibility failure lost the page: %v", err)
	}
	for _, id := range projects {
		if views[id].Janitor().ScheduleProblem != "" {
			t.Fatalf("schedule read repeated eligibility: %+v", views[id])
		}
	}

}

type workflowChangeInspection struct{ a *App }

func (f workflowChangeInspection) InspectRoutine(ctx context.Context, source workflow.Source, _ workflow.JanitorChoices) (workflow.StandingInspection, error) {
	run, err := workflow.ReadRunIn(ctx, f.a.DB, source.RunID)
	if err != nil {
		return workflow.StandingInspection{}, err
	}
	selected, err := verification.SelectCommit(ctx, f.a.DB, f.a.cfg.dataDir, run, source)
	return workflow.StandingInspection{Classification: "routine-dependency", Candidate: selected.Commit}, err
}

func TestJanitorChoicesSurviveAnAdoptedWorkflowMeaningAndVersionChange(t *testing.T) {
	a := authzActors(t)
	ctx := verifiedContext(t.Context(), testAdmittedPrincipalForPacks(t, a))
	a.SyncStock(ctx)
	pid, _, _ := fleetRepo(t, a, "workflow-change", map[string]string{".apptown/app.yaml": "staticServer: caddy:2.11.9-alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\napp:\n  web:\n    static: site\n    public: true\n", "site/index.html": "hello"})
	declare := func(means string) string {
		return fmt.Sprintf(`apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: janitor-update, title: Dependency updates}
spec:
  kind: pipeline
  role: working
  on:
    schedule: {cron: '* * * * *', queue: updates, standing: true}
  stages:
    - name: update
      steps:
        - {name: update, uses: agent/pi}
        - name: accept
          uses: approval
          with:
            means: I accept this checked update.
            standing: {class: routine-update, means: %q}
`, means)
	}
	adopt := func(means string) int {
		t.Helper()
		shipped := wow.New(a.DB, wow.Dependencies{Stock: fstest.MapFS{"janitor-update.yaml": &fstest.MapFile{Data: []byte(declare(means))}}})
		if counts := shipped.SyncStockWorkflows(ctx); counts.Errors != 0 || counts.Skipped != 0 {
			t.Fatalf("workflow release: %+v", counts)
		}
		version, _, err := a.Ways.WorkflowVersion(ctx, "janitor-update", 0)
		if err != nil {
			t.Fatal(err)
		}
		packVersion, err := a.Ways.PublishPack(ctx, "choice-test", "Choice test", fmt.Sprintf(`{"description":%q,"workflows":["janitor-update","land"],"schedules":["janitor-update"]}`, means), "fixture")
		if err != nil {
			t.Fatal(err)
		}
		if err := a.DB.InTx(ctx, func(tx *database.Tx) error {
			return adoption.SetMembershipIn(ctx, tx, pid, []adoption.Membership{{Pack: "choice-test", Version: int64(packVersion)}}, "boss", "test adoption", time.Now())
		}); err != nil {
			t.Fatal(err)
		}
		if err := recordCatalogFixture(a.Adoption, ctx, pid, "boss"); err != nil {
			t.Fatal(err)
		}
		candidate, err := a.Adoption.Prepare(ctx, pid, adoption.Request{Packs: []adoption.PackPick{{Pack: "choice-test", Version: int64(packVersion)}}, AdvanceWorkflows: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.DB.InTx(ctx, func(tx *database.Tx) error {
			_, err := a.Adoption.AdoptIn(ctx, tx, pid, *candidate, candidate.ExpectedRevision, "boss", "adopt workflow version", adoption.OperationSelect)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return version
	}
	const original = "original scheduled update acceptance"
	const changed = "changed scheduled update acceptance"
	first := adopt(original)
	a.standingRules = workflow.NewStandingRules(a.DB, workflow.StandingDependencies{Authority: access.Checked{Authority: identity.Scoped(a.Access, "standing-approval"), Projects: a.Projects}, Identity: a.Access, Procedures: workcommands.StandingProcedures{Adoptions: a.Adoption, Laws: workflowLawRules{}}, Portfolio: verification.Portfolio{Releases: a.Deployment}, Presence: holderPresenceFor(a), Inspector: workflowChangeInspection{a}})
	saved, err := a.standingRules.Save(ctx, workflow.JanitorChoicesCommand{Choices: workflow.JanitorChoices{Dependencies: "routine", BaseImages: true}})
	if err != nil {
		t.Fatal(err)
	}
	second := adopt(changed)
	if second <= first {
		t.Fatal("workflow version did not advance")
	}
	rebuildWork(t, a, func(d *workflow.Dependencies) {
		d.Standing = &a.standingRules
		d.Runners["agent/pi"] = func(e *workflow.Execution) {
			if _, err := e.Claim(); err != nil {
				return
			}
			source, err := workflow.ReadSourceIn(e.Context(), a.DB, workflow.RunRef{ID: e.Run().ID, ProjectID: pid})
			if err != nil {
				t.Error(err)
				return
			}
			folder, err := source.Workspace(a.cfg.dataDir)
			if err != nil {
				t.Error(err)
				return
			}
			if err = os.WriteFile(filepath.Join(folder, "site", "index.html"), []byte("updated"), 0644); err != nil {
				t.Error(err)
				return
			}
			if err = repositoryedit.CommitStep(e.Context(), a.DB, a.cfg.dataDir, a.branches, repositoryPublisher{attachments: a.repositoryAttachments, db: a.DB, now: a.cfg.clock.Now}, e.Run(), e.Step(), a.cfg.clock.Now()); err != nil {
				t.Error(err)
				return
			}
			e.Settle((workflow.CheckEvidence{Checks: []workflow.CheckReceipt{{Check: "check", Verdict: workflow.VerdictPass}}}).Outcome(workflow.StatusPass, nil))
		}
	})
	schedules := changeactions.Schedules{Standing: &a.standingRules, Actions: a.changeActions, Adoption: a.Adoption, Projects: a.Projects, DB: a.DB}
	now := time.Now().UTC().Truncate(time.Minute)
	schedules.FireDueSchedules(ctx, now)
	schedules.FireDueSchedules(ctx, now.Add(time.Minute))
	a.Work.Wait()
	attempts, err := a.Work.Attempts(ctx, pid, "updates", 10)
	if err != nil || len(attempts) != 1 || attempts[0].RuleID != saved.RuleID {
		t.Fatalf("changed workflow did not fire: %+v %v", attempts, err)
	}
	runs, err := a.Work.ChangeRuns(ctx, attempts[0].ChangeID, workflow.ChangeRuns{})
	if err != nil || len(runs) != 1 || runs[0].WorkflowVersion != second {
		t.Fatalf("adopted workflow: %+v want version %d: %v", *runs[0], second, err)
	}
	jobs := operationJobs(t, a, standingAcceptanceJob)
	if len(jobs) != 1 {
		t.Fatalf("acceptance jobs: %+v", jobs)
	}
	assessment, assessErr := a.standingRules.Assess(ctx, runs[0].ID)
	if assessErr != nil {
		t.Fatalf("assessment: %+v %v", assessment, assessErr)
	}
	result, err := a.Work.RunStandingAcceptance(ctx, jobs[0].Key, nil)
	if err != nil || result["cast"] != true {
		snap, _ := a.Work.Snapshot(ctx, runs[0].ID, workflow.DetailView)
		t.Fatalf("changed meaning paused acceptance: %+v %v; assessment=%+v events=%+v steps=%+v", result, err, assessment, snap.Events, snap.Steps)
	}
	snapshot, err := a.Work.Snapshot(ctx, runs[0].ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, step := range snapshot.Steps {
		if step.Type != "approval" {
			continue
		}
		ev, err := workflow.ReadApprovalEvidence(step.Evidence)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range ev.Decisions {
			if v.Standing != nil && v.Standing.RuleID == saved.RuleID && v.Standing.Means == changed {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("current procedure missing from vote: %+v", snapshot.Steps)
	}
	current, err := a.standingRules.Current(ctx)
	if err != nil || current == nil || current.Revision != saved.Revision || current.Choices != saved.Choices {
		t.Fatalf("choices changed: %+v %v", current, err)
	}
}
