package remediation

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/remediation/internal/remediationsql"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

// The public reads explain every queued item using the admission rule, name
// blockers for links, and count those reasons on both the list and detail.
func TestQueuedItemReadsExplainTheirCurrentHold(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"worker", "project", "remediation", "cooldown", "retry", "paused", "concurrency", "replacement"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f, _, queued, def := attemptFixture(t, kind == "replacement")
			s := readOnly(t, f.db)
			var changes []workflow.ProjectBlocker
			var runID ids.ID
			switch kind {
			case "project":
				for i, title := range []string{"Update dependencies", "Resolve Dockerfile warnings"} {
					ch, err := database.Transact(f.ctx, f.db, func(tx *database.Tx) (*workflow.Change, error) {
						created, err := workflow.CreateChangeIn(f.ctx, tx, workflow.ChangeDraft{ProjectID: queued.ProjectID, Type: "scheduled", Title: title}, "alice", time.Now())
						if err != nil {
							return nil, err
						}
						run, err := f.work.AdmitRunIn(f.ctx, tx, workflow.StartRequest{ProjectID: queued.ProjectID, ChangeID: created.Change.ID, WorkflowName: def.Run.Workflow.Name, InputsJSON: "{}"}, def.Run)
						if err == nil {
							runID = run.ID
						}
						return created.Change, err
					})
					if err != nil {
						t.Fatal(err)
					}
					kind := "step"
					if i == 0 {
						f.execute(t, Item{RunID: runID}, workflow.StatusRunning)
					} else {
						kind = "landing"
						f.moveChange(t, Item{ProjectID: queued.ProjectID, ChangeID: ch.ID, RunID: runID}, workflow.LandChange)
					}
					changes = append(changes, workflow.ProjectBlocker{ID: ch.ID, Number: ch.Number, Kind: kind})
				}
			case "remediation":
				other := f.jobOn(t, 0, "tidy", queued.ProjectID)[0]
				requireStoredWrite(t)(queries(f.db).SetItemState(f.ctx, remediationsql.SetItemStateParams{ID: other.ID, State: ItemWorking, UpdatedAt: time.Now()}))
			case "cooldown":
				s.cool.hold("provider rate limit")
			case "paused":
				requireStoredWrite(t)(queries(f.db).SetRemediationStatus(f.ctx, remediationsql.SetRemediationStatusParams{ID: queued.RemediationID, Status: RemPaused, UpdatedAt: time.Now()}))
			case "retry":
				requireStoredWrite(t)(queries(f.db).SetItemOutcome(f.ctx, remediationsql.SetItemOutcomeParams{ID: queued.ID, State: ItemQueued, Reason: "provider rate limit", NotBefore: sql.NullTime{Time: time.Now().Add(time.Hour), Valid: true}, UpdatedAt: time.Now()}))
			case "concurrency":
				// The fixture's job was created without a limit, and the owner has no
				// operation to change concurrency after creation.
				// sql-guarantee: set a concurrency limit the owner cannot set after creation.
				completionExec(t, f.db, `UPDATE remediations SET concurrency=1 WHERE id=$1`, queued.RemediationID)
				p := f.project(t, "other")
				insertStoredItem(t, f.db, remediationsql.InsertItemParams{ID: ids.New(time.Now()), RemediationID: queued.RemediationID, ProjectID: p, ProjectSlug: "other", CheckKey: "other", CheckName: "other", State: ItemWorking}, 0, ids.Nil)
			}
			items, err := s.Items(f.ctx, &queued.RemediationID)
			if err != nil {
				t.Fatal(err)
			}
			var got Item
			for _, it := range items {
				if it.ID == queued.ID {
					got = it
				}
			}
			want := kind
			if kind == "replacement" {
				want = "worker"
			}
			if got.Waiting == nil || got.Waiting.Kind != want || !strings.HasPrefix(got.Waiting.Reason, "Waiting: ") {
				t.Fatalf("waiting %s: %+v", kind, got.Waiting)
			}
			if want == "worker" && got.Waiting.Reason != "Waiting: starts on the next pass when a worker is free." {
				t.Fatal(got.Waiting.Reason)
			}
			if kind == "project" {
				if len(got.Waiting.Changes) != 2 || got.Waiting.Changes[0] != changes[0] || got.Waiting.Changes[1] != changes[1] || got.Waiting.Reason != "Waiting: a step is running in change 1 and change 2 is landing." {
					t.Fatalf("named blockers: %+v", got.Waiting)
				}
			}
			if kind == "remediation" && (got.Waiting.RemediationID.IsZero() || got.Waiting.RemediationNumber == 0) {
				t.Fatalf("missing remediation identity: %+v", got.Waiting)
			}
			if kind == "cooldown" && !strings.Contains(got.Waiting.Reason, "provider rate limit") {
				t.Fatal(got.Waiting.Reason)
			}
			job, err := s.Remediation(f.ctx, queued.RemediationID)
			if err != nil || !strings.Contains(job.Sentence, " · 1 waiting") {
				t.Fatalf("detail counts: %+v %v", job, err)
			}
			if want == "worker" && !strings.Contains(job.Sentence, "1 waiting for the next pass or a free worker") {
				t.Fatal(job.Sentence)
			}
			jobs, err := s.Remediations(f.ctx, 50)
			if err != nil {
				t.Fatal(err)
			}
			for _, j := range jobs {
				if j.ID == queued.RemediationID && j.Sentence != job.Sentence {
					t.Fatalf("list counts: %+v", j)
				}
			}
			if kind == "project" || kind == "remediation" {
				queue, err := newSchedule(f.db, nil).Queue(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				for _, job := range queue.Jobs {
					for _, it := range job.Items {
						if it.ID == queued.ID {
							t.Fatal("explained blocker did not hold scheduling")
						}
					}
				}
			}
		})
	}
}

func TestQueuedReasonReadFailuresAreVisible(t *testing.T) {
	t.Parallel()
	for _, fragment := range []string{"-- name: ProjectActiveItems", "-- name: ProjectWorkBlockers"} {
		t.Run(fragment, func(t *testing.T) {
			f, _, queued, _ := attemptFixture(t, false)
			s := readOnly(t, f.db)
			for _, read := range []func() error{
				func() error { _, err := s.Items(f.ctx, &queued.RemediationID); return err },
				func() error { _, err := s.Remediation(f.ctx, queued.RemediationID); return err },
				func() error { _, err := s.Remediations(f.ctx, 50); return err },
			} {
				dbtest.FailNextStatement(t, f.db, fragment)
				if err := read(); err == nil {
					t.Fatal("occupancy failure hidden as a worker wait")
				}
			}
		})
	}
}

// A running Run waiting on approval leaves the project free in page reads,
// scheduling and the locked attempt start, all through Work's occupancy rule.
func TestApprovalWaitLeavesProjectFreeForRemediation(t *testing.T) {
	t.Parallel()
	f, store, queued, def := attemptFixture(t, false)
	_, err := database.Transact(f.ctx, f.db, func(tx *database.Tx) (*workflow.Run, error) {
		created, err := workflow.CreateChangeIn(f.ctx, tx, workflow.ChangeDraft{ProjectID: queued.ProjectID, Type: "scheduled", Title: "Update dependencies"}, "alice", time.Now())
		if err != nil {
			return nil, err
		}
		approval := def.Run
		approval.Spec.Stages = []schema.Stage{{Name: "accept", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}
		return f.work.AdmitRunIn(f.ctx, tx, workflow.StartRequest{ProjectID: queued.ProjectID, ChangeID: created.Change.ID, WorkflowName: approval.Workflow.Name, InputsJSON: "{}"}, approval)
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := readOnly(t, f.db).Items(f.ctx, &queued.RemediationID)
	if err != nil || len(items) != 1 || items[0].Waiting == nil || items[0].Waiting.Kind != "worker" {
		t.Errorf("approval holds page: %+v %v", items, err)
	}
	queue, err := newSchedule(f.db, nil).Queue(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	offered := false
	for _, job := range queue.Jobs {
		for _, item := range job.Items {
			if item.ID == queued.ID {
				offered = true
			}
		}
	}
	if !offered {
		t.Error("approval holds scheduling")
	}
	run, err := store.Start(f.ctx, queued, def, "worker")
	if err != nil || run == nil {
		t.Fatalf("approval holds admission: %+v %v", run, err)
	}
}
