package changeactions_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

// Published values explain the current library; adopted values govern execution.
// Neither a workflow name nor posted, retired cron fields choose the cadence.
func TestStandingCadenceFollowsAdoptedVersionsAndIgnoresPostedCrons(t *testing.T) {
	w := newScheduleWorld(t)
	body := `kind: Workflow
metadata: {name: nightly-deps, title: Night deps, description: Apply declared dependency updates and check the result.}
spec:
  kind: pipeline
  on:
    schedule: {cron: "0 6 * * 1", aggressiveCron: "0 6 * * *", queue: deps, standing: true}
  stages:
    - name: update
      steps:
        - name: repair
          uses: agent/pi
          with: {repairRounds: 2}
        - name: accept
          uses: approval
          with: {means: I accept the checked update.}
`
	publish := func(text string) {
		t.Helper()
		if _, _, err := w.ways.PublishWorkflow(t.Context(), text, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	publish(body)
	w.packWorkflow(t, w.project.ID, "nightly-deps")
	if err := recordCatalogFixture(w.adoption, t.Context(), w.project.ID, "bls"); err != nil {
		t.Fatal(err)
	}
	newer := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(body, "0 6 * * 1", "0 7 * * 2"), "0 6 * * *", "0 4 * * *"), "repairRounds: 2", "repairRounds: 3")
	publish(newer)
	directory := identity.New(w.schedules.DB, identity.Config{})
	person, err := directory.EnsurePerson(t.Context(), "bls")
	if err != nil {
		t.Fatal(err)
	}
	human := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: person.PrincipalID, Kind: "user", Name: "bls", Via: "session"}, Verified: true, Receipt: "bls"})
	in := workflow.StandingDefinition{Scope: "instance", Delivery: "full", Categories: workflow.StandingCategories{Dependencies: "aggressive"}}
	// The retired field is ignored. It cannot override the adopted declaration.
	if err := json.Unmarshal([]byte(`{"schedules":[{"workflow":"nightly-deps","crons":{"aggressive":"* * * * *"}}]}`), &in); err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{"routine", "aggressive"} {
		in.Categories.Dependencies = level
		q, err := w.schedules.Standing.Preview(human, in)
		if err != nil {
			t.Fatal(err)
		}
		want := "every Tuesday at 07:00 UTC"
		if level == "aggressive" {
			want = "every day at 04:00 UTC"
		}
		for _, text := range []string{want, "up to 3 repair sessions", "Night deps", "its adopted packs declare", "The current packs declare"} {
			if !strings.Contains(q.Shown, text) {
				t.Fatalf("missing %q: %s", text, q.Shown)
			}
		}
	}
	q, err := w.schedules.Standing.Preview(human, in)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := w.schedules.Standing.Enable(human, workflow.StandingCommand{Definition: in, ShownDigest: q.Digest})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	check := func(cron string, rounds int) {
		t.Helper()
		got := w.schedule(t, base)
		if got.Cron != cron || got.Refused != "" || got.Queue != "deps" || got.Next.IsZero() || got.Problem != "" {
			t.Fatalf("%+v", got)
		}
		queues, err := w.work.Queues(human, w.project.ID)
		if err != nil || len(queues) != 1 || queues[0].Queue != "deps" || queues[0].Title != "Night deps" {
			t.Fatalf("%+v %v", queues, err)
		}
		projects, err := w.work.JanitorProjects(human)
		if err != nil || len(projects) != 1 || len(projects[0].Procedures) != 1 || projects[0].Procedures[0].Title != "Night deps" || projects[0].Procedures[0].RepairRounds != rounds {
			t.Fatalf("%+v %v", projects, err)
		}
		page, err := w.schedules.Of(human, w.project, base)
		if err != nil {
			t.Fatal(err)
		}
		if !page.Janitor().NextUpdate.Equal(got.Next) {
			t.Fatal(page.Janitor())
		}
		ad, err := adoption.CurrentReceiptIn(t.Context(), w.schedules.DB, w.project.ID)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := w.adoption.Workflow(t.Context(), adoption.WorkflowRequest{Source: adoption.Source{ProjectID: w.project.ID, Revision: ad}, Name: "nightly-deps"})
		if err != nil {
			t.Fatal(err)
		}
		if resolved.Doc.RepairRounds() != rounds {
			t.Fatal(resolved.Doc.RepairRounds())
		}
		eligible, level, _, err := w.schedules.Standing.StandingForWorkflow(t.Context(), w.project.ID, "nightly-deps")
		if err != nil || eligible.RuleID != rule.RuleID || level != "aggressive" {
			t.Fatalf("%+v %s %v", eligible, level, err)
		}
		w.schedules.FireDueSchedules(t.Context(), base)
		w.schedules.FireDueSchedules(t.Context(), got.Next)

		fired := w.schedule(t, got.Next).Last
		if fired == nil || fired.Outcome != workflow.ScheduleOpened {
			t.Fatalf("%+v", fired)
		}
		runs, err := w.work.ChangeRuns(w.engine, fired.ChangeID, workflow.ChangeRuns{})
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) != 1 || runs[0].WorkflowName != "nightly-deps" || runs[0].WorkflowVersion != resolved.Version {
			t.Fatalf("%+v", runs)
		}
		if _, err := w.work.Abandon(human, workflow.StopWork{ChangeID: fired.ChangeID, Reason: "fixture completed"}); err != nil {
			t.Fatal(err)
		}
	}
	check("0 6 * * *", 2)
	if _, err := w.ways.PublishPack(human, "schedule-nightly-deps", "Scheduled work", `{"workflows":["nightly-deps"],"schedules":["nightly-deps"],"description":"new cadence"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	picks, err := w.adoption.PicksWith(human, w.project.ID, "schedule-nightly-deps", 2)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := w.adoption.Prepare(human, w.project.ID, adoption.Request{Packs: picks, AdvanceWorkflows: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.schedules.DB.InTx(human, func(tx *database.Tx) error {
		_, err := w.adoption.AdoptIn(human, tx, w.project.ID, *candidate, candidate.ExpectedRevision, "bls", "adopt new cadence", adoption.OperationSelect)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check("0 4 * * *", 3)
}
