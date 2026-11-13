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
