package changeactions_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/fault"
)

const nightlyScan = `apiVersion: apptown.dev/v1
kind: Workflow
metadata: {name: nightly-scan}
spec:
  kind: pipeline
  role: working
  on:
    schedule: {cron: "0 5 * * *", queue: security, standing: true}
  stages:
    - name: scan
      steps:
        - name: command
          uses: script
          with: {run: "true"}
        - name: accept
          uses: approval
          with:
            means: I reviewed the scan
            standing: {class: routine-update, means: the declared fix is within the bound and checks are met}
`

// A person can fire a standing schedule early: the change is the schedule's
// own automated queue change under the rule, as at its cron time, whether
// the schedule is armed and not yet due or never armed; the firing is
// recorded. While that change works, a second request is refused and
// records nothing.
func TestAPersonFiresAStandingScheduleNowAsTheScheduleWould(t *testing.T) {
	release := make(chan struct{})
	claimed := make(chan struct{}, 4)
	w := newScheduleWorld(t, map[string]workflow.Runner{"script": func(task *workflow.Execution) {
		if _, err := task.Claim(); err != nil {
			t.Error(err)
			return
		}
		claimed <- struct{}{}
		<-release
		task.Settle(workflow.EncodeOutcome(workflow.StatusPass, map[string]any{}, nil))
	}})
	// Registered after the world, so the held run ends before Work stops.
	t.Cleanup(func() { close(release) })
	if _, _, err := w.ways.PublishWorkflow(t.Context(), nightlyScan, "fixture"); err != nil {
		t.Fatal(err)
	}
	w.packWorkflow(t, w.project.ID, "nightly-scan")
	if err := recordCatalogFixture(w.adoption, t.Context(), w.project.ID, "fixture"); err != nil {
		t.Fatal(err)
	}
	directory := identity.New(w.schedules.DB, identity.Config{Projects: project.NewCatalogue(w.schedules.DB)})
	person, err := directory.EnsurePerson(t.Context(), "bls")
	if err != nil {
		t.Fatal(err)
	}
	human := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: person.PrincipalID, Kind: "user", Name: "bls", Via: "session"}, Verified: true})
	noon := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

	// Without a standing approval the schedule does not fire, early or not.
	if _, err := w.schedules.RunNow(human, w.project, "nightly-scan", noon); fault.KindOf(err) != fault.Unprocessable {
		t.Fatalf("fired without a standing approval: %v", err)
	}
	in := workflow.StandingDefinition{Scope: "instance", Categories: workflow.StandingCategories{Dependencies: "routine", SecurityFixes: true}, Delivery: "full"}
	if err := json.Unmarshal([]byte(`{"schedules":[{"workflow":"nightly-scan","crons":{"routine":"0 5 * * *","aggressive":"0 5 * * *"}}]}`), &in); err != nil {
		t.Fatal(err)
	}
	q, err := w.schedules.Standing.Preview(human, in)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := w.schedules.Standing.Enable(human, workflow.StandingCommand{Definition: in, ShownDigest: q.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.schedules.RunNow(human, w.project, "no-such-schedule", noon); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("an unscheduled workflow: %v", err)
	}

	// A person who may not open work in the project is refused before any effect.
	stranger, err := directory.EnsurePerson(t.Context(), "sam")
	if err != nil {
		t.Fatal(err)
	}
	sam := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: stranger.PrincipalID, Kind: "user", Name: "sam", Via: "session"}, Verified: true})
	if _, err := w.schedules.RunNow(sam, w.project, "nightly-scan", noon); fault.KindOf(err) != fault.Forbidden {
		t.Fatalf("a person without authority: %v", err)
	}
	if runs := w.runs(t); len(runs) != 0 {
		t.Fatalf("a refused request started work: %+v", runs)
	}

	// A committed firing whose preparation fails still identifies its requester.
	dbtest.FailNextStatement(t, w.schedules.DB, "-- name: MarkRunReady")
	_, prepErr := w.schedules.RunNow(human, w.project, "nightly-scan", noon)
	if prepErr == nil || !strings.Contains(prepErr.Error(), "abandoned after preparation failed") {
		t.Fatalf("expected committed preparation failure, got %v", prepErr)
	}
	requests, readErr := audit.Recent(t.Context(), w.schedules.DB, audit.Filter{ProjectID: w.project.ID, KindPrefix: "schedule.run-requested"})
	if readErr != nil || len(requests) != 1 || requests[0].ActorPrincipal != person.PrincipalID {
		t.Fatalf("committed firing lost its requester: requests=%+v read=%v preparation=%v", requests, readErr, prepErr)
	}

	// A failed admission is an error, not a started scan.
	dbtest.FailNextStatement(t, w.schedules.DB, "-- name: InsertChange :one")
	if ch, err := w.schedules.RunNow(human, w.project, "nightly-scan", noon); err == nil || ch != nil {
		t.Fatalf("a failed admission answered %+v %v", ch, err)
	}

	for i, at := range []time.Time{noon, noon.Add(time.Hour)} {
		if i == 1 {
			// Armed by the previous firing; the next cron time is tomorrow 05:00.
			release <- struct{}{}
			if !waitFor(func() bool {
				s, err := w.work.QueueState(w.engine, w.project.ID, "security")
				return err == nil && s.State != "updating"
			}) {
				t.Fatal("the first scan never settled")
			}
		}
		ch, err := w.schedules.RunNow(human, w.project, "nightly-scan", at)
		if err != nil {
			t.Fatalf("firing %d: %v", i, err)
		}
		select {
		case <-claimed:
		case <-time.After(10 * time.Second):
			t.Fatalf("firing %d started no run", i)
		}
		attempts, err := w.work.Attempts(w.engine, w.project.ID, "security", 20)
		if err != nil || len(attempts) == 0 || attempts[0].ChangeID != ch.ID || attempts[0].RuleID != rule.RuleID || attempts[0].RuleRevision != rule.Revision {
			t.Fatalf("firing %d is not the schedule's queue change under the rule: %+v %v", i, attempts, err)
		}
		records, err := workflow.ScheduleRecordsIn(t.Context(), w.schedules.DB, w.project.ID)
		if r := records["nightly-scan"]; err != nil || r.Outcome != workflow.ScheduleOpened || r.ChangeID != ch.ID || !r.At.Equal(at) {
			t.Fatalf("firing %d record: %+v %v", i, r, err)
		}
	}

	// The second scan is still working: another request is refused and the
	// firing record stays the second scan's.
	if !waitFor(func() bool {
		s, err := w.work.QueueState(w.engine, w.project.ID, "security")
		return err == nil && s.State == "updating"
	}) {
		t.Fatal("the running scan never showed as updating")
	}
	_, err = w.schedules.RunNow(human, w.project, "nightly-scan", noon.Add(2*time.Hour))
	if fault.KindOf(err) != fault.Conflict {
		t.Fatalf("a busy queue: %v", err)
	}
	records, err := workflow.ScheduleRecordsIn(t.Context(), w.schedules.DB, w.project.ID)
	if err != nil || !records["nightly-scan"].At.Equal(noon.Add(time.Hour)) {
		t.Fatalf("a refused request recorded a firing: %+v %v", records, err)
	}
}

// waitFor polls cond until it holds or ten seconds pass.
func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		<-time.After(20 * time.Millisecond)
	}
	return false
}
