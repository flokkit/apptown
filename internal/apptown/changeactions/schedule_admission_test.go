package changeactions_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/attention"
	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// The maintenance page reads the same refusal that the act returns, including
// queue exhaustion and the target-discovery exception. State is made by owners.
func TestMaintenanceReadAndActShareScheduleAdmission(t *testing.T) {
	for _, state := range []string{"eligible", "no rule", "off", "busy", "stopped", "discovery stopped"} {
		t.Run(state, func(t *testing.T) {
			w := newScheduleWorld(t, map[string]workflow.Runner{"script": func(e *workflow.Execution) {
				if _, err := e.Claim(); err != nil {
					t.Error(err)
					return
				}
				if err := e.StartFix(nil); err != nil {
					t.Error(err)
					return
				}
				e.Settle(workflow.EncodeOutcome(workflow.StatusFail, map[string]any{"reason": "fixture command failed"}, nil))
			}})
			t.Cleanup(w.work.Wait)
			step := "script\n          with: {run: 'false'}"
			if state == "busy" {
				step = "agent/pi\n          with: {repairRounds: 2}"
			}
			discovers := ""
			if state == "discovery stopped" {
				discovers = "\n        - name: discover\n          uses: workflow/call\n          with: {workflow: repair, findings: fixable}"
			}
			body := fmt.Sprintf(`kind: Workflow
metadata: {name: team-care, title: Team care}
spec:
  kind: pipeline
  on:
    schedule: {cron: "0 6 * * *", queue: team-queue, standing: true}
  stages:
    - name: care
      steps:
        - name: work
          uses: %s%s
`, step, discovers)
			if _, _, err := w.ways.PublishWorkflow(t.Context(), body, "fixture"); err != nil {
				t.Fatal(err)
			}
			if state == "discovery stopped" {
				if _, _, err := w.ways.PublishWorkflow(t.Context(), "kind: Workflow\nmetadata: {name: repair}\nspec:\n  kind: pipeline\n  stages:\n    - name: accept\n      steps:\n        - name: accept\n          uses: approval\n          with: {means: I accept the repairs.}\n", "fixture"); err != nil {
					t.Fatal(err)
				}
			}
			w.packWorkflow(t, w.project.ID, "team-care")
			if err := recordCatalogFixture(w.adoption, t.Context(), w.project.ID, "bls"); err != nil {
				t.Fatal(err)
			}
			person, err := identity.New(w.schedules.DB, identity.Config{}).EnsurePerson(t.Context(), "bls")
			if err != nil {
				t.Fatal(err)
			}
			human := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: person.PrincipalID, Kind: "user", Name: "bls", Via: "session"}, Verified: true, Receipt: "bls"})
			now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
			if state != "no rule" {
				in := workflow.JanitorChoices{Dependencies: "routine"}
				if _, err := w.schedules.Standing.Save(human, workflow.JanitorChoicesCommand{Choices: in}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "off" {
				if err := w.schedules.Standing.SetProjectChoice(human, w.project.ID, "off"); err != nil {
					t.Fatal(err)
				}
			}
			tries := 0
			if state == "busy" {
				tries = 1
			}
			if strings.Contains(state, "stopped") {
				tries = 3
			}
			for i := 0; i < tries; i++ {
				if _, err := w.schedules.RunNow(human, w.project, "team-care", now.Add(time.Duration(i)*time.Hour)); err != nil {
					t.Fatal(err)
				}
				w.work.Wait()
			}
			queues, err := w.work.StoredQueues(human, w.project.ID)
			if err != nil || len(queues) != 1 || queues[0].Queue != "team-queue" || queues[0].Title != "Team care" {
				t.Fatalf("queues %+v %v", queues, err)
			}
			if state == "stopped" {
				snapshot, err := readScanSnapshot(attention.Scans{Work: w.work}, human, w.project.ID)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, item := range snapshot.Items {
					found = found || strings.Contains(item.Title, "Team care AppTown could not finish this update")
				}
				if !found {
					t.Fatal(snapshot.Items)
				}
			}
			got, err := w.schedules.Of(human, w.project, now)
			if err != nil || len(got.Schedules) != 1 {
				t.Fatalf("%+v %v", got, err)
			}
			schedule := got.Schedules[0]
			if schedule.Problem != "" {
				t.Fatal(schedule.Problem)
			}
			if state == "stopped" || state == "off" || state == "no rule" {
				if !schedule.Next.IsZero() {
					t.Fatal(schedule.Next)
				}
			}
			_, err = w.schedules.RunNow(human, w.project, "team-care", now.Add(4*time.Hour))
			if schedule.Refused == "" && err != nil || schedule.Refused != "" && (err == nil || err.Error() != schedule.Refused) {
				t.Fatalf("read %q, act %v", schedule.Refused, err)
			}
			// Finish the admitted discovery run before testing another firing.
			w.work.Wait()
			starts, runErr := w.schedules.RunJanitor(human, w.project, changeactions.JanitorRun{}, now.Add(5*time.Hour))
			if runErr != nil || len(starts) != 1 {
				t.Fatalf("janitor: %+v %v", starts, runErr)
			}
			if state == "stopped" && !strings.Contains(starts[0].Refused, "stopped") {
				t.Fatal(starts)
			}
			if (state == "off" || state == "no rule") && starts[0].Refused != schedule.Waiting {
				t.Fatal(starts)
			}
			if state == "discovery stopped" && starts[0].ChangeID.IsZero() {
				t.Fatal(starts)
			}

		})
	}
}
