package remediation

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/remediation/internal/remediationsql"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func TestRemediationSchedulingContinuesAfterUnstartedCandidates(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"skip", "resolve failure", "admission rejected", "busy console"} {
		t.Run(outcome, func(t *testing.T) {
			a, _, item, def := attemptFixture(t, false)
			otherProject := a.project(t, "other")
			other := a.job(t, 0, otherProject)[0].ID
			var dispatched []ids.ID
			starter := NewStarter(NewAttempts(a.db, a.work, a.ctx, nil), func(_ context.Context, candidate Item) (StartDefinition, error) {
				if candidate.ID == item.ID {
					switch outcome {
					case "skip":
						return StartDefinition{Skip: "check removed"}, nil
					case "resolve failure":
						return StartDefinition{}, database.ErrInjectedFailure
					case "admission rejected":
						// Work acquired the project after the queue was read.
						otherDef := workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "other", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "hold", Uses: "approval"}}}}}}
						otherWork := workflow.New(context.Background(), a.db, workflow.Dependencies{Authority: reconcileActor{}, Git: attemptGit{t.TempDir()}, Repositories: &scm.RepositoryLocks{}, Resolve: func(context.Context, workflow.StartRequest, workflow.StartSource) (workflow.PreparedRun, error) {
							return otherDef, nil
						}})
						otherWork.Stop()
						change, err := otherWork.Create(a.ctx, workflow.ChangeDraft{ProjectID: item.ProjectID, Title: "Other work"})
						if err != nil {
							t.Fatal(err)
						}
						_, err = database.Transact(context.Background(), a.db, func(tx *database.Tx) (*workflow.Run, error) {
							return otherWork.AdmitRunIn(a.ctx, tx, workflow.StartRequest{ProjectID: item.ProjectID, ChangeID: change.ID, WorkflowName: "other", InputsJSON: "{}"}, otherDef)
						})
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				return def, nil
			}, func(runID ids.ID) error {
				run, err := workflow.ReadRunIn(context.Background(), a.db, runID)
				if err != nil {
					return err
				}
				dispatched = append(dispatched, run.ProjectID)
				return nil
			})
			busy := func(_ context.Context, _ ids.ID) bool { return false }
			if outcome == "busy console" {
				busy = func(_ context.Context, project ids.ID) bool { return project == item.ProjectID }
			}
			scheduler := NewScheduler(NewSchedule(a.db, nil).Queue, func() bool { return false }, busy,
				func(ctx context.Context, candidate Item) (*workflow.Run, error) {
					return starter.Start(ctx, candidate, "worker")
				})
			err := scheduler.Fill(context.Background())
			if (err != nil) != (outcome == "resolve failure") {
				t.Fatalf("unexpected scheduling error: %v", err)
			}
			if !reflect.DeepEqual(dispatched, []ids.ID{otherProject}) {
				t.Fatalf("ready project did not start in the same pass: %v", dispatched)
			}
			if current := a.item(t, other); current.State != ItemWorking {
				t.Fatalf("ready project: %+v", current)
			}
		})
	}
}

func TestRemediationQueueReadFailuresStartNothing(t *testing.T) {
	t.Parallel()
	for _, fragment := range []string{"SET TRANSACTION", "-- name: FreeWorkerSlots :one", "-- name: RunningJobCapacity :many", "-- name: QueuedItemsForJob :many", "-- name: ProjectHasActiveItem :one"} {
		t.Run(fragment, func(t *testing.T) {
			a, _, _, _ := attemptFixture(t, false)
			s := NewScheduler(NewSchedule(a.db, nil).Queue, func() bool { return false }, func(context.Context, ids.ID) bool { return false },
				func(context.Context, Item) (*workflow.Run, error) {
					t.Error("started on a failed read")
					return nil, nil
				})
			dbtest.FailNextStatement(t, a.db, fragment)
			if err := s.Fill(context.Background()); err == nil {
				t.Fatal("read failure hidden")
			}
			if changes := a.changes(t); len(changes) != 0 {
				t.Fatalf("started on a failed read: %+v", changes)
			}
		})
	}
}

func TestRemediationQueueCapacityAndEligibility(t *testing.T) {
	t.Parallel()
	a, _, item, _ := attemptFixture(t, false)
	store := NewSchedule(a.db, nil)
	assertQueue := func(free, candidates int) {
		t.Helper()
		q, err := store.Queue(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var n int
		for _, job := range q.Jobs {
			n += len(job.Items)
		}
		if q.Free != free || n != candidates {
			t.Fatalf("queue %+v, want %d free and %d candidates", q, free, candidates)
		}
	}
	assertQueue(DefaultWorkers, 1)
	if err := NewPools(a.db, nil).Set(context.Background(), WorkerKindRemediation, 0, "alice"); err != nil {
		t.Fatal(err)
	}
	assertQueue(0, 0)
	if err := NewPools(a.db, nil).Set(context.Background(), WorkerKindRemediation, 3, "alice"); err != nil {
		t.Fatal(err)
	}
	requireStoredWrite(t)(queries(a.db).SetItemOutcome(context.Background(), remediationsql.SetItemOutcomeParams{UpdatedAt: time.Now(), ID: item.ID, State: item.State, Reason: item.Reason, NotBefore: sql.NullTime{Time: time.Now().Add(+time.Hour), Valid: true}}))
	assertQueue(3, 0)
	requireStoredWrite(t)(queries(a.db).SetItemOutcome(context.Background(), remediationsql.SetItemOutcomeParams{UpdatedAt: time.Now(), ID: item.ID, State: item.State, Reason: item.Reason, NotBefore: sql.NullTime{Time: time.Now().Add(-time.Hour), Valid: true}}))
	if err := NewAttempts(a.db, a.work, a.ctx, nil).Status(context.Background(), item.RemediationID, []string{"running"}, "paused", "alice"); err != nil {
		t.Fatal(err)
	}
	assertQueue(3, 0)
	if err := NewAttempts(a.db, a.work, a.ctx, nil).Status(context.Background(), item.RemediationID, []string{"paused"}, "running", "alice"); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{1, 2} {
		t.Run(fmt.Sprintf("job limit %d with one active", limit), func(t *testing.T) {
			f, _, initial, def := attemptFixture(t, false)
			if err := NewPools(f.db, nil).Set(context.Background(), WorkerKindRemediation, 3, "alice"); err != nil {
				t.Fatal(err)
			}
			if err := NewAttempts(f.db, f.work, f.ctx, nil).Status(context.Background(), initial.RemediationID, []string{"running"}, "paused", "alice"); err != nil {
				t.Fatal(err)
			}
			items := f.job(t, limit, f.project(t, fmt.Sprintf("limit-%d-a", limit)), f.project(t, fmt.Sprintf("limit-%d-b", limit)))
			starts := NewAttempts(f.db, f.work, f.ctx, nil)
			if run, err := starts.Start(context.Background(), items[0], def, "worker"); err != nil || run == nil {
				t.Fatalf("active admission: %+v %v", run, err)
			}
			queue, err := NewSchedule(f.db, nil).Queue(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			candidates := 0
			for _, job := range queue.Jobs {
				candidates += len(job.Items)
			}
			want := limit - 1
			if queue.Free != 2 || candidates != want {
				t.Fatalf("queue %+v, want 2 free and %d candidates", queue, want)
			}
		})
	}
}

func TestRemediationCompletionCandidateReadFailureClosesNothing(t *testing.T) {
	t.Parallel()
	a, starts, item, def := attemptFixture(t, false)
	if run, err := starts.Start(context.Background(), item, def, "worker"); err != nil || run == nil {
		t.Fatalf("start: %+v %v", run, err)
	}
	item = a.item(t, item.ID)
	a.moveChange(t, item, workflow.ShipChange)
	item = a.outcome(t, item, Outcome{Kind: "landed"})
	a.outcome(t, item, Outcome{Kind: "verified", ReviewID: idstest.N(99), Verdict: conformance.Conforms})
	dbtest.FailNextStatement(t, a.db, "-- name: RunningRemediationIDs :many")
	store := NewSchedule(a.db, nil)
	if err := store.CloseFinished(context.Background()); err == nil {
		t.Fatal("completion read failure hidden")
	}
	if n := jobsWithStatus(t, a.db, RemRunning); n != 1 {
		t.Fatalf("running jobs after failed close: %d", n)
	}
	if err := store.CloseFinished(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := jobsWithStatus(t, a.db, RemCompleted); n != 1 {
		t.Fatalf("completed jobs: %d", n)
	}
}

// An item waits until its not-before by AppTown's clock, which wrote it
// and judges it again at admission, so the queue judges it by that clock
// too: moved an hour either way, the database's clock neither holds back an
// item whose wait is over nor offers one still waiting.
func TestAQueuedItemWaitsByTheForgesClock(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		wait, moved string
		until       time.Duration
		candidates  int
	}{
		{"over", "-1 hour", -time.Minute, 1},
		{"not over", "1 hour", time.Minute, 0},
	} {
		t.Run(c.wait, func(t *testing.T) {
			t.Parallel()
			a, _, item, _ := attemptFixture(t, false)
			if err := NewPools(a.db, nil).Set(context.Background(), WorkerKindRemediation, 3, "alice"); err != nil {
				t.Fatal(err)
			}
			requireStoredWrite(t)(queries(a.db).SetItemOutcome(context.Background(), remediationsql.SetItemOutcomeParams{UpdatedAt: time.Now(), ID: item.ID, State: item.State, Reason: item.Reason, NotBefore: sql.NullTime{Time: time.Now().Add(c.until), Valid: true}}))
			moveDatabaseClock(t, a.db, c.moved)
			q, err := NewSchedule(a.db, nil).Queue(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, job := range q.Jobs {
				n += len(job.Items)
			}
			if n != c.candidates {
				t.Fatalf("the database's clock moved %s: %d candidates, want %d", c.moved, n, c.candidates)
			}
		})
	}
}

// moveDatabaseClock makes now() in db read the real clock moved by (an
// interval such as "-1 hour") on every connection made from here on, so a
// test can show which clock a rule reads: only the database's is moved.
func moveDatabaseClock(t *testing.T, db *database.DB, by string) {
	t.Helper()
	for _, statement := range []string{
		`CREATE OR REPLACE FUNCTION public.now() RETURNS timestamptz LANGUAGE plpgsql STABLE AS $$ BEGIN RETURN pg_catalog.now() + interval '` + by + `'; END $$`,
		`DO $$ BEGIN EXECUTE format('ALTER DATABASE %I SET search_path = public, pg_catalog', current_database()); END $$`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	db.Pool().SetMaxIdleConns(0)
	if _, err := db.Exec(`DO $$ BEGIN IF now() = pg_catalog.now() THEN RAISE EXCEPTION 'the database''s clock was not moved'; END IF; END $$`); err != nil {
		t.Fatal(err)
	}
}
