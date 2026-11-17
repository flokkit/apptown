package workflow

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
)

// Compare committed owner reads with the derivation after each owner write.
func TestJanitorRecordTracksApprovalOnlyStageAndKeepsStateSince(t *testing.T) {
	s, ch, _, _, _ := automatedFixture(t)
	var previous QueueStatus
	check := func() QueueStatus {
		t.Helper()
		got, err := s.StoredQueue(t.Context(), ch.ProjectID, "security")
		if err != nil {
			t.Fatal(err)
		}
		want, err := queueStateIn(t.Context(), s.db, ch.ProjectID, "security")
		if err != nil {
			t.Fatal(err)
		}
		since := got.StateSince
		got.StateSince = time.Time{}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("stored %+v; derived %+v", got, want)
		}
		got.StateSince = since
		if since.IsZero() {
			t.Fatal("missing state time")
		}
		if previous.State == got.State && !previous.StateSince.Equal(since) {
			t.Fatal("unchanged state changed its time")
		}
		if previous.State != "" && previous.State != got.State && !since.After(previous.StateSince) {
			t.Fatal("new state retained old time")
		}
		previous = got
		return got
	}
	check()
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "work", Uses: "script"}}}, {Name: "accept", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"means": "I accept this change."}}}}}}
	adopted, err := adoption.CurrentReceiptIDIn(t.Context(), s.db, ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.runAdmissions.records.create(t.Context(), StartRequest{ProjectID: ch.ProjectID, ChangeID: ch.ID, WorkflowName: "care", InputsJSON: "{}", requestedBy: "apptown-system"}, PreparedRun{AdoptionID: adopted, Workflow: DefinitionRef{Name: "care", Version: 1}, Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.runAdmissions.records.prepared(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	check()
	steps, err := s.RunSteps(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	owner := newStepAttempts(s.db, func(string) bool { return true })
	claim, err := owner.start(t.Context(), steps[0], "script", ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if check().State != "updating" {
		t.Fatal("attempt not recorded")
	}
	if err := owner.finish(t.Context(), claim, StepOutcome{Status: StatusPass}); err != nil {
		t.Fatal(err)
	}
	check()
	if err := s.db.InTx(t.Context(), func(tx *database.Tx) error {
		if err := TransitionRunIn(t.Context(), tx, run, RunRunning, "accept", time.Now()); err != nil {
			return err
		}
		_, err := s.stages.EnterIn(t.Context(), tx, run.ID, &spec, "accept", time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if check().State != "decide" {
		t.Fatal("approval-only stage was not recorded")
	}
	check()
	steps, err = s.RunSteps(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var approval ids.ID
	for _, step := range steps {
		if step.Type == "approval" {
			approval = step.ID
		}
	}
	ctx := access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{Kind: "user", Name: "verified-person", Via: "session"}})
	if _, err := s.Approve(ctx, Approval{StepID: approval, Decision: VoteApprove}); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	check()
	if err := s.db.InTx(ctx, func(tx *database.Tx) error {
		_, err := TransitionChangeIn(ctx, tx, ch.ProjectID, ch.ID, ChangeMove{Action: ShipChange}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if check().State != "up_to_date" {
		t.Fatal("landed queue not recorded")
	}
}

func TestJanitorScanRecordReadsUncommittedObservationsAndRetainsMainAnswer(t *testing.T) {
	s, ch, repo, _, _ := automatedFixture(t)
	tree := gittest.Run(t, repo, "rev-parse", "HEAD^{tree}")
	record := func(tree, verdict string, expected []string) MainScans {
		t.Helper()
		run := admitAutomatedRun(t, s, ch, "scan")
		steps, err := s.RunSteps(t.Context(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := newStepAttempts(s.db, func(string) bool { return true }).start(t.Context(), steps[0], "scan", ch.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		e := &Execution{service: s, ctx: t.Context(), run: run, step: steps[0], claim: claim}
		if err := e.InAttempt("scan", func(tx *database.Tx, attempt int64) error {
			at := time.Now()
			if err := conformance.RecordScanObservationIn(t.Context(), tx, conformance.ScanObservation{ID: ids.New(at), ProjectID: ch.ProjectID, ChangeID: ch.ID, RunID: run.ID, StepID: steps[0].ID, Attempt: attempt, Subject: conformance.ScanSubject{On: "source", Tree: tree}, Scanner: "osv", ExpectedScanners: expected, Verdict: verdict, Reason: "scan result", ObservedAt: at}); err != nil {
				return err
			}
			return e.RecordJanitorScansIn(t.Context(), tx)
		}); err != nil {
			t.Fatal(err)
		}
		if err := newStepAttempts(s.db, func(string) bool { return true }).finish(t.Context(), claim, StepOutcome{Status: StatusPass}); err != nil {
			t.Fatal(err)
		}
		transitionStoredRunForTest(t, s.db, run.ID, "", RunDone)
		rows, err := s.ScanAttention(t.Context(), []ids.ID{ch.ProjectID})
		if err != nil || len(rows) != 1 {
			t.Fatalf("%+v %v", rows, err)
		}
		return rows[0].Scans
	}
	if got := record(tree, "pass", []string{"osv", "trivy"}); got.Known {
		t.Fatal("unanswered peer became known")
	}
	if got := record(tree, "error", []string{"osv"}); !got.Failed {
		t.Fatalf("uncommitted failure not stored: %+v", got)
	}
	if got := record("unlanded-tree", "pass", []string{"osv"}); !got.Failed {
		t.Fatal("unlanded scan cleared main failure")
	}
	if got := record(tree, "pass", []string{"osv"}); !got.Known || got.Failed {
		t.Fatalf("newer main scan did not replace failure: %+v", got)
	}
}

func TestJanitorFleetReadsHaveConstantQueryCounts(t *testing.T) {
	s, _, ctx, _ := janitorReadFixture(t)
	visible, _ := s.authority.VisibleProjects(ctx, actionReadProject)
	for i := 1; i < 8; i++ {
		visible = append(visible, workflowTestProject(t, s.db, ids.New(time.Now()).String(), "Extra", ""))
	}
	var selected []ids.ID
	s.authority = aggregateAuthority{operationAuthority: s.authority, read: func(context.Context, commandAction) ([]ids.ID, error) { return selected, nil }}
	for _, read := range []struct {
		name string
		run  func() error
	}{
		{"Scans", func() error { _, err := s.ScanAttention(ctx, selected); return err }},
		{"Janitor", func() error { _, _, _, err := s.JanitorProjects(ctx); return err }},
	} {
		t.Run(read.name, func(t *testing.T) {
			counts := []int{}
			for _, n := range []int{2, 8} {
				selected = visible[:n]
				counted := &countedQueries{}
				stop := s.db.ObserveStatements("", func([]any) { counted.reads++ })
				err := read.run()
				stop()
				if err != nil {
					t.Fatal(err)
				}
				counts = append(counts, counted.reads)
			}
			if counts[0] == 0 || counts[0] != counts[1] {
				t.Fatalf("2 and 8 projects: %v queries", counts)
			}
		})
	}
}

func TestJanitorRepositoryPresenceComesFromCurrentProjectRecord(t *testing.T) {
	s, _, ctx, _ := janitorReadFixture(t)
	visible, err := s.authority.VisibleProjects(ctx, actionReadProject)
	if err != nil {
		t.Fatal(err)
	}
	for _, branch := range []string{"", "main"} {
		if err := project.New(s.db, nil).SetDefaultBranch(ctx, visible[0], branch); err != nil {
			t.Fatal(err)
		}
		rows, _, _, err := s.JanitorProjects(ctx)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%+v %v", rows, err)
		}
		scans, err := s.ScanAttention(ctx, visible)
		if err != nil || len(scans) != 1 {
			t.Fatalf("%+v %v", scans, err)
		}
		if rows[0].Scans.NoRepository != (branch == "") || scans[0].Scans.NoRepository != (branch == "") {
			t.Fatal("repository fact required another observation")
		}
	}
}

// These fixtures admit synthetic definitions without an adoption source. Seed
// their adopted membership explicitly; attempt writes must never create queues.
func seedSyntheticJanitorQueues(t *testing.T, db *database.DB, project ids.ID, names ...string) {
	t.Helper()
	queues := make([]QueueStatus, len(names))
	for i, name := range names {
		queues[i] = QueueStatus{Queue: name}
	}
	raw, err := json.Marshal(queues)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTx(t.Context(), func(tx *database.Tx) error {
		if _, err := workflowsql.New(tx.SQL()).ProjectForUpdate(t.Context(), project); err != nil {
			return err
		}
		return workflowsql.New(tx.SQL()).RecordJanitorQueues(t.Context(), workflowsql.RecordJanitorQueuesParams{ProjectID: project, Queues: raw, UpdatedAt: time.Now()})
	}); err != nil {
		t.Fatal(err)
	}
}

func TestJanitorBootRecordsProjectWithoutAdoption(t *testing.T) {
	s, _, _, _, _ := automatedFixture(t)
	id, err := database.Transact(t.Context(), s.db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "not-adopted", Name: "Not adopted"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordJanitorProjects(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.authority = allowProject(id)
	queue, err := s.StoredQueue(t.Context(), id, "updates")
	if err != nil || queue.State != "unknown" {
		t.Fatalf("queue %+v: %v", queue, err)
	}
}

// The stored queues are written only by recordJanitorIn, from each change and
// run write path. After a mix of those operations every stored queue must equal
// a fresh derivation from the rows, so a write path that skips the writer fails
// here instead of leaving the record stale. Scans have their own writers (scan
// attempts and boot) and are not compared.
//
// Kills: dropping the writer call from change creation (step 1) or from the
// change transition (the ship).
func TestJanitorStoredQueuesEqualAFreshDerivationAcrossChangeAndRunEvents(t *testing.T) {
	s, ch, _, _, _ := automatedFixture(t)
	matches := func(step string) {
		t.Helper()
		for _, queue := range []string{"security", "updates"} {
			stored, err := s.StoredQueue(t.Context(), ch.ProjectID, queue)
			if err != nil {
				t.Fatal(err)
			}
			derived, err := queueStateIn(t.Context(), s.db, ch.ProjectID, queue)
			if err != nil {
				t.Fatal(err)
			}
			stored.StateSince = time.Time{}
			if !reflect.DeepEqual(stored, derived) {
				t.Fatalf("after %s, %s queue stored %+v; derived %+v", step, queue, stored, derived)
			}
		}
	}
	system := access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule"}})
	updates, err := database.Transact(system, s.db, func(tx *database.Tx) (CreatedChange, error) {
		return CreateChangeIn(system, tx, ChangeDraft{ProjectID: ch.ProjectID, Title: "Update project", Queue: "updates"}, "apptown-system", time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	matches("creation")

	run := admitAutomatedRun(t, s, ch)
	matches("run admission")
	steps, err := s.RunSteps(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempts := newStepAttempts(s.db, func(string) bool { return true })
	claim, err := attempts.start(t.Context(), steps[0], "scan", ch.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	matches("step start")
	if err := attempts.finish(t.Context(), claim, StepOutcome{Status: StatusPass}); err != nil {
		t.Fatal(err)
	}
	matches("step finish")
	if err := s.db.InTx(t.Context(), func(tx *database.Tx) error {
		return TransitionRunIn(t.Context(), tx, run, RunDone, "", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	matches("run done")

	if _, err := s.Abandon(t.Context(), StopWork{ChangeID: updates.Change.ID, Reason: "nothing to update"}); err != nil {
		t.Fatal(err)
	}
	matches("abandonment")
	if err := s.db.InTx(t.Context(), func(tx *database.Tx) error {
		_, err := TransitionChangeIn(t.Context(), tx, ch.ProjectID, ch.ID, ChangeMove{Action: ShipChange}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	matches("ship")
}
