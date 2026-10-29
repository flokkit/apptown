package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
)

func recheckFixture(t *testing.T) (reworkStore, *Run) {
	t.Helper()
	w, run, _ := completionFixture(t, "working")
	storedReadyStart(t, w, run.ID)
	// sql-guarantee: Seed checks on both sides of the producer's historical finish time, including an override; no step mutation accepts finished_at.
	completionExec(t, w, `INSERT INTO steps(id,run_id,stage,name,type,status,evidence_json,finished_at,override_by) VALUES
		($1,$2,'finish','work','agent/pi','pass','{}',now()-interval '1 minute',''),
		($3,$4,'finish','scan','scan','pass','{"findings":["old"]}',now()-interval '2 minutes',''),
		($5,$6,'finish','review','agent/review','fail','{"reason":"old"}',now()-interval '2 minutes',''),
		($7,$8,'finish','accept','approval','fail','{"decisions":[{"approver":"alice","decision":"reject"}],"comment":"fix this","identifier":9007199254740993}',now()-interval '2 minutes',''),
		($9,$10,'finish','override','preview','pass','{}',now()-interval '2 minutes','alice'),
		($11,$12,'finish','fresh','tests','pass','{}',now(),'')`, ids.New(time.Now()), run.ID, ids.New(time.Now()), run.ID, ids.New(time.Now()), run.ID, ids.New(time.Now()), run.ID, ids.New(time.Now()), run.ID, ids.New(time.Now()), run.ID)
	return reworkStore{w, clocked{}}, run
}
func TestStepRecheckConcurrentBatchPreservesHistoryAndFencesAdvancement(t *testing.T) {
	t.Parallel()
	s, run := recheckFixture(t)
	results := race(8, func(int) int {
		changed, err := s.refresh(context.Background(), run.ID)
		if err != nil {
			t.Error(err)
		}
		if changed {
			return 1
		}
		return 0
	})
	if count(results, 1) != 1 {
		t.Fatalf("reset batches: %v", results)
	}
	// Whole-store reads: a reset or receipt leaked onto another run still counts.
	steps := storedSteps(t, s.db)
	expect(t, stepCount(steps, func(s *Step) bool { return s.Status == StatusPending && !s.StartedAt.Valid && !s.FinishedAt.Valid }) == 3, "reset batch size")
	wantEventCount(t, s.db, 3, func(e Event) bool { return e.Kind == "step.reverified" && hasKey(e.Payload, "previousEvidence") })
	expect(t, stepCount(steps, func(s *Step) bool {
		return s.Name == "accept" && jsonMatches(t, s.Evidence, "previousDecisions.0.approver=alice", "lastRejection.comment=fix this")
	}) == 1, "approval history was not preserved")
	expect(t, stepCount(steps, func(s *Step) bool { return (s.Name == "override" || s.Name == "fresh") && s.Status == StatusPass }) == 2, "override or fresh check was reset")
	wantRunCount(t, s.db, 1, func(r *Run) bool { return r.RetryRevision == 1 })
	expect(t, stepCount(steps, func(s *Step) bool {
		return s.Type == "approval" && jsonMatches(t, s.Evidence, "identifier=9007199254740993")
	}) == 1, "approval lost its exact identifier")
	// A pre-reset advancement decision no longer owns this run revision.
	if err := (advanceStore{db: s.db, completion: completionRecorderFunc(noCompletionEffects)}).complete(context.Background(), run); err != ErrRunMoved {
		t.Fatalf("stale advancement: %v", err)
	}
}
func TestStepRecheckWaitsForAllWork(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"pending", "running"} {
		t.Run(state, func(t *testing.T) {
			s, run := recheckFixture(t)
			insertStoredStep(t, s.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "more-work", Type: "agent/pi", ConfigJson: []byte(`{}`), Status: state})
			if changed, err := s.refresh(context.Background(), run.ID); err != nil || changed {
				t.Fatalf("refreshed during work: %v %v", changed, err)
			}
			wantEventCount(t, s.db, 0, anyEvent)
		})
	}
}

func TestStepRecheckRequiresWholeBatchAndRevision(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"last receipt error", "step suppressed", "revision suppressed", "receipt suppressed"} {
		t.Run(failure, func(t *testing.T) {
			s, run := recheckFixture(t)
			before := completionSnapshot(t, s.db)
			if failure == "last receipt error" {
				completionExec(t, s.db, `ALTER TABLE run_events ADD CONSTRAINT reject_last_recheck CHECK(kind!='step.reverified' OR payload_json->>'step'!='accept')`)
			} else {
				table, write, condition := "steps", "UPDATE", "NEW.status='pending'"
				if failure == "revision suppressed" {
					table, condition = "runs", "NEW.retry_revision != OLD.retry_revision"
				}
				if failure == "receipt suppressed" {
					table, write, condition = "run_events", "INSERT", "NEW.kind='step.reverified'"
				}
				completionExec(t, s.db, `CREATE FUNCTION veto_recheck() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN RETURN NULL; END IF; RETURN NEW; END $$`)
				completionExec(t, s.db, `CREATE TRIGGER veto_recheck BEFORE `+write+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_recheck()`)
			}
			if changed, err := s.refresh(context.Background(), run.ID); err == nil || changed {
				t.Fatalf("uncommitted reset: %v %v", changed, err)
			}
			if completionSnapshot(t, s.db) != before {
				t.Fatal("partial resets, history, receipts or revision committed")
			}
			completionExec(t, s.db, `ALTER TABLE run_events DROP CONSTRAINT IF EXISTS reject_last_recheck`)
			completionExec(t, s.db, `DROP FUNCTION IF EXISTS veto_recheck() CASCADE`)
			if changed, err := s.refresh(context.Background(), run.ID); err != nil || !changed {
				t.Fatalf("explicit rerun: %v %v", changed, err)
			}
		})
	}
}
