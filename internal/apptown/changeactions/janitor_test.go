package changeactions_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

type projectCatchUp struct {
	projects []ids.ID
}

func (p *projectCatchUp) CatchUpProject(_ context.Context, id ids.ID) (ids.ID, error) {
	p.projects = append(p.projects, id)

	return ids.New(time.Now()), nil
}

// The immediate operation enumerates adopted declarations, preserves automated
// authority and names the requester. It neither substitutes workflow names nor
// fires a nonstanding schedule.
func TestRunJanitorUsesEveryAdoptedStandingStart(t *testing.T) {
	w := newScheduleWorld(t, map[string]workflow.Runner{"script": func(e *workflow.Execution) {
		if _, err := e.Claim(); err != nil {
			t.Error(err)
			return
		}
		if err := e.StartFix(nil); err != nil {
			t.Error(err)
			return
		}
		e.Settle(workflow.EncodeOutcome(workflow.StatusFail, map[string]any{"reason": "checks failed"}, nil))
	}})
	security := strings.Replace(nightlyScan, "        - name: accept", "        - name: discover\n          uses: workflow/call\n          with: {workflow: dependency-care, findings: fixable}\n        - name: accept", 1)
	for _, body := range []string{security, strings.ReplaceAll(strings.ReplaceAll(nightlyScan, "nightly-scan", "dependency-care"), "queue: security", "queue: updates"), `kind: Workflow
metadata: {name: pack-care}
spec:
  kind: pipeline
  on:
    publication: {queue: packs, standing: true}
  stages:
    - name: review
      steps:
        - name: accept
          uses: approval
          with: {means: I accept the pack changes.}
`} {
		if _, _, err := w.ways.PublishWorkflow(t.Context(), body, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.ways.PublishPack(t.Context(), "care", "Care", `{"workflows":["nightly-scan","dependency-care","pack-care"],"schedules":["nightly-scan","dependency-care"]}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := w.schedules.DB.InTx(t.Context(), func(tx *database.Tx) error {
		return adoption.SetMembershipIn(t.Context(), tx, w.project.ID, []adoption.Membership{{Pack: "care", Version: 1}}, "fixture", "bls", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	if err := recordCatalogFixture(w.adoption, t.Context(), w.project.ID, "bls"); err != nil {
		t.Fatal(err)
	}
	// The Janitor read supplies the eligibility answer; schedule shaping must
	// neither reread rules nor replace it with its own judgment.
	receipt, err := adoption.CurrentReceiptIn(t.Context(), w.schedules.DB, w.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := w.adoption.AdoptedWorkflows(t.Context(), adoption.Source{ProjectID: w.project.ID, Revision: receipt})
	if err != nil {
		t.Fatal(err)
	}
	scheduled, err := w.adoption.ScheduledWorkflowNames(t.Context(), w.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	supplied := map[string]workflow.StandingEligibilityResult{}
	for _, name := range []string{"nightly-scan", "dependency-care", "pack-care"} {
		supplied[name] = workflow.StandingEligibilityResult{Refused: "supplied eligibility refusal"}
	}
	rows, err := w.schedules.ForJanitor(t.Context(), []workflow.JanitorProject{{ProjectID: w.project.ID, Slug: w.project.Slug, Procedures: janitorProcedures(t, items, scheduled), Eligibility: supplied, Queues: []workflow.QueueStatus{}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, schedule := range rows[w.project.ID].Schedules {
		if schedule.Waiting != "supplied eligibility refusal" {
			t.Fatalf("supplied eligibility lost: %+v", schedule)
		}
	}
	person, err := identity.New(w.schedules.DB, identity.Config{}).EnsurePerson(t.Context(), "bls")
	if err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: person.PrincipalID, Kind: "user", Name: "bls", Via: "session"}, Verified: true, Receipt: "bls"})
	packs := &projectCatchUp{}
	w.schedules.Packs = packs
	now := time.Now()
	got, err := w.schedules.RunJanitor(ctx, w.project, changeactions.JanitorRun{}, now)
	if err != nil || len(got) != 3 {
		t.Fatalf("unapproved starts: %+v %v", got, err)
	}
	for _, start := range got {
		if start.Refused != "waits for Janitor choices" {
			t.Fatal(start)
		}
	}
	if len(packs.projects) != 0 {
		t.Fatal("unapproved pack assessment")
	}
	in := workflow.JanitorChoices{Dependencies: "routine", SecurityFixes: true}
	rule, err := w.schedules.Standing.Save(ctx, workflow.JanitorChoicesCommand{Choices: in})
	if err != nil {
		t.Fatal(err)
	}
	in.Packs = true
	if _, err := w.schedules.Standing.Save(ctx, workflow.JanitorChoicesCommand{Choices: in, ExpectedRevision: rule.Revision}); err != nil {
		t.Fatal(err)
	}

	got, err = w.schedules.RunJanitor(ctx, w.project, changeactions.JanitorRun{}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, start := range got {
		if start.Refused != "" {
			t.Fatal(start)
		}
		if start.Queue == "packs" {
			if start.WhatIfID.IsZero() {
				t.Fatal(start)
			}
			continue
		}
		ch, err := w.work.Change(ctx, start.ChangeID)
		if err != nil || ch.Type != "scheduled" {
			t.Fatalf("change: %+v %v", ch, err)
		}
		records, err := audit.Recent(ctx, w.schedules.DB, audit.Filter{ProjectID: w.project.ID, KindPrefix: "schedule.run-requested"})
		if err != nil || len(records) != 3 {
			t.Fatalf("request receipts: %+v %v", records, err)
		}
		for _, record := range records {
			var payload struct {
				RuleID ids.ID `json:"ruleId"`
			}
			if err := json.Unmarshal([]byte(record.Payload), &payload); err != nil {
				t.Fatal(err)
			}
			if record.ActorPrincipal != person.PrincipalID || (record.Subject != "pack-care" && payload.RuleID != rule.RuleID) {
				t.Fatal(record)
			}
		}
	}
	if len(packs.projects) != 1 || packs.projects[0] != w.project.ID {
		t.Fatal(packs)
	}
	w.work.Wait()
	for i := 0; i < 2; i++ {
		if _, err := w.schedules.RunNow(ctx, w.project, "dependency-care", now.Add(time.Duration(i+1)*time.Hour)); err != nil {
			t.Fatal(err)
		}
		w.work.Wait()
	}
	got, err = w.schedules.RunJanitor(ctx, w.project, changeactions.JanitorRun{}, now.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, start := range got {
		if start.Queue == "updates" && !strings.Contains(start.Refused, "stopped") {
			t.Fatalf("stopped updates started: %+v", start)
		}
		if start.Queue == "security" && (start.ChangeID.IsZero() || start.Refused != "") {
			t.Fatalf("stopped updates blocked discovery: %+v", start)
		}
	}
	copy := w.project
	copy.Purpose = project.EvalCopy // an eval must start nothing
	got, err = w.schedules.RunJanitor(ctx, copy, changeactions.JanitorRun{}, now)
	if err != nil || len(got) != 1 || !strings.Contains(got[0].Refused, "eval") {
		t.Fatalf("eval: %+v %v", got, err)
	}
}

// One person's retry ends one queue episode, even when several adopted
// schedules feed it. Without eligible starts it must leave the episode alone.
func TestRunJanitorResetsOneQueueBeforeItsStandingStarts(t *testing.T) {
	t.Parallel()
	// A pending repair reserves the queue even before its runner claims it.
	// Let the seed fail, then hold its replacement until both starts are read.
	release := make(chan struct{}, 1)
	release <- struct{}{}
	w := newScheduleWorld(t, map[string]workflow.Runner{"agent/pi": func(e *workflow.Execution) {
		<-release
		if _, err := e.Claim(); err != nil {
			t.Error(err)
			return
		}
		if err := e.StartFix(nil); err != nil {
			t.Error(err)
			return
		}
		// A terminal error records failure without asking for another repair.
		e.Settle(workflow.EncodeOutcome(workflow.StatusError, map[string]any{"reason": "checks failed"}, nil))
	}})
	t.Cleanup(func() {
		close(release)
		w.work.Wait()
	})
	repair := strings.Replace(nightlyScan, "uses: script\n          with: {run: \"true\"}", "uses: agent/pi\n          with: {}", 1)
	for _, name := range []string{"first-care", "second-care"} {
		if _, _, err := w.ways.PublishWorkflow(t.Context(), strings.ReplaceAll(repair, "nightly-scan", name), "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.ways.PublishPack(t.Context(), "care", "Care", `{"workflows":["first-care","second-care"],"schedules":["first-care","second-care"]}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := w.schedules.DB.InTx(t.Context(), func(tx *database.Tx) error {
		return adoption.SetMembershipIn(t.Context(), tx, w.project.ID, []adoption.Membership{{Pack: "care", Version: 1}}, "fixture", "bls", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	if err := recordCatalogFixture(w.adoption, t.Context(), w.project.ID, "bls"); err != nil {
		t.Fatal(err)
	}
	person, err := identity.New(w.schedules.DB, identity.Config{}).EnsurePerson(t.Context(), "bls")
	if err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: person.PrincipalID, Kind: "user", Name: "bls", Via: "session"}, Verified: true, Receipt: "bls"})
	req := changeactions.JanitorRun{Queue: "security", Again: true}
	if _, err := w.schedules.RunJanitor(ctx, w.project, req, time.Now()); err != nil {
		t.Fatal(err)
	}
	receipts, err := audit.Recent(ctx, w.schedules.DB, audit.Filter{ProjectID: w.project.ID, KindPrefix: "queue.try-again"})
	if err != nil || len(receipts) != 0 {
		t.Fatalf("ineligible reset: %+v %v", receipts, err)
	}
	in := workflow.JanitorChoices{Dependencies: "routine", SecurityFixes: true}
	if _, err := w.schedules.Standing.Save(ctx, workflow.JanitorChoicesCommand{Choices: in}); err != nil {
		t.Fatal(err)
	}
	ch, err := w.schedules.RunNow(ctx, w.project, "first-care", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w.work.Wait()
	req.ChangeID = ch.ID
	got, err := w.schedules.RunJanitor(ctx, w.project, req, time.Now())
	if err != nil || len(got) != 2 {
		t.Fatalf("starts after one reset: %+v %v", got, err)
	}
	if got[0].ChangeID.IsZero() {
		t.Fatalf("first replacement not started: %+v", got)
	}
	if got[1].BusyChangeID != got[0].ChangeID || got[1].Refused == "" || !got[1].ChangeID.IsZero() {
		t.Fatalf("second start did not preserve the pending replacement: %+v", got)
	}
	receipts, err = audit.Recent(ctx, w.schedules.DB, audit.Filter{ProjectID: w.project.ID, KindPrefix: "queue.try-again"})
	if err != nil || len(receipts) != 1 || receipts[0].ActorPrincipal != person.PrincipalID {
		t.Fatalf("retry receipts: %+v %v", receipts, err)
	}
	closed, err := w.work.Change(ctx, ch.ID)
	if err != nil || closed.State != workflow.ChangeAbandoned {
		t.Fatalf("old change: %+v %v", closed, err)
	}
	receipts, err = audit.Recent(ctx, w.schedules.DB, audit.Filter{ProjectID: w.project.ID, KindPrefix: "change.abandoned"})
	if err != nil || len(receipts) != 1 || receipts[0].ActorPrincipal != person.PrincipalID {
		t.Fatalf("closure attribution: %+v %v", receipts, err)
	}
}

func TestScheduleOnlyReadsDoNotResolveUnscheduledDefinitions(t *testing.T) {
	t.Parallel()
	w := newScheduleWorld(t)
	body := strings.Replace(nightlyScan, `schedule: {cron: "0 5 * * *", queue: security, standing: true}`, `publication: {queue: security, standing: true}`, 1)
	if _, _, err := w.ways.PublishWorkflow(t.Context(), body, "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ways.PublishPack(t.Context(), "care", "Care", `{"workflows":["nightly-scan"]}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := w.schedules.DB.InTx(t.Context(), func(tx *database.Tx) error {
		return adoption.SetMembershipIn(t.Context(), tx, w.project.ID, []adoption.Membership{{Pack: "care", Version: 1}}, "fixture", "bls", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	if err := recordCatalogFixture(w.adoption, t.Context(), w.project.ID, "bls"); err != nil {
		t.Fatal(err)
	}
	// Leave a definition-read fault armed across the schedule-only callers.
	// Only the publication read should consume it.
	dbtest.FailNextStatement(t, w.schedules.DB, "lib_workflow_versions")
	if got, err := w.schedules.ScansSource(t.Context(), w.project.ID); err != nil || got {
		t.Fatalf("scan-only read: %t %v", got, err)
	}
	if got, err := w.schedules.Of(t.Context(), w.project, time.Now()); err != nil || len(got.Schedules) != 0 {
		t.Fatalf("schedule-only read: %+v %v", got, err)
	}
	w.schedules.FireDueSchedules(t.Context(), time.Now())
	call := access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, ProjectID: w.project.ID}})
	ad, err := adoption.CurrentReceiptIn(call, w.schedules.DB, w.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := w.adoption.AdoptedWorkflows(call, adoption.Source{ProjectID: w.project.ID, Revision: ad})
	if err != nil {
		t.Fatal(err)
	}
	scheduled, err := w.adoption.ScheduledWorkflowNames(call, w.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	procedures := janitorProcedures(t, items, scheduled)
	queues := []workflow.QueueStatus{}
	rows, err := w.schedules.ForJanitor(call, []workflow.JanitorProject{{ProjectID: w.project.ID, Slug: w.project.Slug, Procedures: procedures, Queues: queues}}, time.Now())
	got := rows[w.project.ID]

	if err != nil || !strings.Contains(got.Janitor().ScheduleProblem, "could not be read") {
		t.Fatalf("publication read: %+v %v", got, err)
	}
}

func janitorProcedures(t *testing.T, items []adoption.AdoptedWorkflow, scheduled map[string]bool) []workflow.JanitorProcedure {
	t.Helper()
	var out []workflow.JanitorProcedure
	for _, item := range items {
		p, _ := workflow.DescribeJanitorProcedure(item, scheduled[item.Name])
		out = append(out, p)
	}
	return out
}

func TestAdoptionAdvanceReplacesStoredJanitorQueues(t *testing.T) {
	w := newScheduleWorld(t)
	var retainedChange ids.ID
	record := func(names ...string) {
		t.Helper()
		current, err := adoption.CurrentReceiptIn(t.Context(), w.schedules.DB, w.project.ID)
		if err != nil {
			t.Fatal(err)
		}
		receipt := adoption.EmptyReceipt()
		for _, name := range names {
			receipt.Workflows = append(receipt.Workflows, adoption.ReceiptWorkflow{Name: name, Version: 1})
		}
		if err := w.schedules.DB.InTx(t.Context(), func(tx *database.Tx) error {
			return w.adoption.RecordIn(t.Context(), tx, w.project.ID, current.Revision+1, adoption.AdoptionEdit, "fixture adoption", receipt, "bls", time.Now())
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"first", "second"} {
		body := fmt.Sprintf("kind: Workflow\nmetadata: {name: %s, title: %s}\nspec:\n  kind: pipeline\n  on:\n    schedule: {cron: '0 6 * * *', queue: %s, standing: true}\n  stages:\n    - name: accept\n      steps:\n        - name: accept\n          uses: approval\n          with: {means: I accept this update.}\n", name, name, name)
		if _, _, err := w.ways.PublishWorkflow(t.Context(), body, "fixture"); err != nil {
			t.Fatal(err)
		}
		w.packWorkflow(t, w.project.ID, name)
		record(name)
		if retainedChange.IsZero() {
			if err := w.schedules.DB.InTx(w.engine, func(tx *database.Tx) error {
				scheduled := access.WithCaller(w.engine, access.Caller{Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule"}, Verified: true, Receipt: "system"})
				created, err := workflow.CreateChangeIn(scheduled, tx, workflow.ChangeDraft{ProjectID: w.project.ID, Title: "Retained queue work", Queue: name}, "apptown-system", time.Now())
				if err != nil {
					return err
				}
				retainedChange = created.Change.ID
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		queues, err := w.work.StoredQueues(w.engine, w.project.ID)
		if err != nil || len(queues) != 1 || queues[0].Queue != name || queues[0].Title != name {
			t.Fatalf("stored queues after adoption: %+v %v", queues, err)
		}
	}
	if err := w.schedules.DB.InTx(t.Context(), func(tx *database.Tx) error {
		return adoption.SetMembershipIn(t.Context(), tx, w.project.ID, nil, "remove schedule", "bls", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	record()
	queues, err := w.work.StoredQueues(w.engine, w.project.ID)
	if err != nil || len(queues) != 0 {
		t.Fatalf("removed queues: %+v %v", queues, err)
	}
	person, err := identity.New(w.schedules.DB, identity.Config{}).EnsurePerson(t.Context(), "bls")
	if err != nil {
		t.Fatal(err)
	}
	human := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: person.PrincipalID, Kind: "user", Name: "bls", Via: "session"}, Verified: true, Receipt: "bls"})
	if _, err := w.work.Abandon(human, workflow.StopWork{ChangeID: retainedChange, Reason: "Close work from the removed procedure"}); err != nil {
		t.Fatal(err)
	}
	queues, err = w.work.StoredQueues(w.engine, w.project.ID)
	if err != nil || len(queues) != 0 {
		t.Fatalf("a write to retained work resurrected removed queues: %+v %v", queues, err)
	}
}
