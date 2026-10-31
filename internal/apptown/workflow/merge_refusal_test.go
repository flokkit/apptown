package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

// landingAtMerge is a repository-backed landing stopped at its merge: a
// person who may land admitted it, its candidate is pinned, its one gate
// (a script) passed on that candidate, and its merge step is pending. The
// merge is driven through Work's merge owner and SCM's service as AppTown
// composes them; only the Git driver is the test's.
type landingAtMerge struct {
	db          *database.DB
	dir         string
	locks       *scm.RepositoryLocks
	run         *Run
	gate, merge ids.ID
}

const (
	landingCandidateSHA = "1111111111111111111111111111111111111111"
	landingTargetSHA    = "2222222222222222222222222222222222222222"
)

func landingAtMergeFixture(t *testing.T) landingAtMerge {
	t.Helper()
	db := testDB(t)
	projectID := workflowTestProject(t, db, "landing", "Landing", "main")
	change := insertChangeForTest(t, db, workflowsql.InsertChangeParams{ID: ids.New(time.Now()), CreatedAt: time.Now(), ProjectID: projectID, Type: "feature", Title: "Work to land", State: ChangeLanding.String(), TitleOwnership: "human"})
	spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{
		{Name: "check", Uses: "script"}, {Name: "merge", Uses: "scm/merged"},
	}}}}
	run := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: projectID, ChangeID: change, WorkflowName: "land", WorkflowVersion: 1,
		Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(mustJSON(spec))})
	landingAdmittedForTest(t, db, run.ID)
	// The start events Work's admission records for a pinned landing; this
	// fixture stores its run directly, below admission.
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		if err := recordReceiptIn(context.Background(), tx, run.ID, landingPinRequiredEvent, struct{}{}, time.Now()); err != nil {
			return err
		}
		return recordReceiptIn(context.Background(), tx, run.ID, landingCandidateEvent, LandingCandidate{Candidate: landingCandidateSHA, Target: landingTargetSHA, By: "catch-up"}, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	gate := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "check", Type: "script", Status: StatusPending.String()})
	recordScopeStep(t, db, workflowsql.RecordApprovalParams{At: time.Now(), ID: gate, Status: StatusPass.String(), EvidenceJson: json.RawMessage(`{"candidate":"` + landingCandidateSHA + `"}`)})
	merge := insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "merge", Type: "scm/merged", Status: StatusPending.String()})
	return landingAtMerge{db: db, dir: t.TempDir(), locks: &scm.RepositoryLocks{}, run: run, gate: gate, merge: merge}
}

// owner is Work's merge owner over SCM's service, with the test's driver.
func (l landingAtMerge) owner(driver scm.Driver) *Merges {
	service := scm.New(l.db, NewSCMWork(l.db, l.dir, policyLawRules{}, nil), l.locks, nil)
	return NewMerges(l.db, l.dir, policyLawRules{}, service, mergeEffectsTest{driver: driver, observed: func(scm.Result) {}})
}

// service is Work as New assembles it, with the test's Git driver: its
// merge owner and its abandonment owner get their repository locks from
// that assembly, not from the test.
func (l landingAtMerge) service(driver scm.Driver) *Service {
	s := newServiceWithAuthority(l.db, Dependencies{DataDir: l.dir, Laws: policyLawRules{}, Abandonment: abandonRecorderFunc(noAbandonEffects),
		Merge: mergeEffectsTest{driver: driver, observed: func(scm.Result) {}}}, allowProject(l.run.ProjectID))
	s.dispatcher.Stop()
	return s
}

// call is the merge assertion of the landing as it is stored now.
func (l landingAtMerge) call(t *testing.T) MergeCall {
	t.Helper()
	ctx := context.Background()
	run, err := readRun(ctx, l.db, l.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	step, err := readStep(ctx, l.db, l.merge)
	if err != nil {
		t.Fatal(err)
	}
	source, err := readMergeSourceForRun(ctx, l.db, run)
	if err != nil {
		t.Fatal(err)
	}
	return MergeCall{Purpose: MergeForAssertion, Run: *run, Step: *step, Source: source}
}

// record is SCM's merge record for the landing, read as startup inspection
// reads it.
func (l landingAtMerge) record(t *testing.T) scm.Record {
	t.Helper()
	service := scm.New(l.db, NewSCMWork(l.db, l.dir, policyLawRules{}, nil), &scm.RepositoryLocks{}, nil)
	record, err := service.Read(SCMInspectionContext(context.Background(), l.db), l.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

// gitDriver stands in for Git: a local merge of the candidate that, unless
// told otherwise, succeeds. Every call it was not given a way to answer
// fails the test: the merge was not to get that far.
type gitDriver struct {
	t        *testing.T
	prepared func()
	execute  func() (scm.Execution, error)
	inspect  func() (scm.Inspection, error)
}

func (d gitDriver) Prepare(context.Context, scm.Source, string) (scm.Prepared, error) {
	if d.prepared == nil {
		d.t.Error("the merge was prepared")
		return scm.Prepared{}, errors.New("unexpected preparation")
	}
	d.prepared()
	return scm.Prepared{Kind: scm.LocalMerge, Expected: landingTargetSHA}, nil
}
func (d gitDriver) Execute(context.Context, scm.Source, scm.Prepared, string) (scm.Execution, error) {
	if d.execute == nil {
		d.t.Error("the merge was executed")
		return scm.Execution{}, errors.New("unexpected merge")
	}
	return d.execute()
}
func (d gitDriver) Inspect(context.Context, scm.Source, scm.Record) (scm.Inspection, error) {
	if d.inspect == nil {
		d.t.Error("the merge was inspected")
		return scm.Inspection{}, errors.New("unexpected inspection")
	}
	return d.inspect()
}

// A merge assertion is refused, with nothing asked of Git and nothing
// recorded, when the evidence it rests on is missing or the current law
// forbids it. The same landing with nothing changed merges, so each refusal
// is the change's doing.
func TestAMergeIsRefusedWithoutItsEvidenceOrUnderCurrentLaw(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"", "missing gate", "wrong check type", "missing scanners", "law violation", "malformed law", "unreadable law", "stale uplift"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			l := landingAtMergeFixture(t)
			switch failure {
			case "missing gate":
				// sql-guarantee: remove the passed gate while keeping the run's declaration and merge step; no owner command deletes one admitted step.
				completionExec(t, l.db, `DELETE FROM steps WHERE id=$1`, l.gate)
			case "wrong check type":
				// sql-guarantee: keep the passed verdict but replace its immutable runner kind; no owner command changes a step's type.
				completionExec(t, l.db, `UPDATE steps SET type='tests' WHERE id=$1`, l.gate)
			case "missing scanners":
				spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish", Steps: []schema.Step{{Name: "scan", Uses: "scan", With: map[string]any{"on": "source"}}, {Name: "merge", Uses: "scm/merged"}}}}}
				// sql-guarantee: replace the pinned declaration without its steps; admission creates a declaration and its steps together.
				completionExec(t, l.db, `UPDATE runs SET definition_json=$1 WHERE id=$2`, mustJSON(spec), l.run.ID)
			case "law violation":
				installPolicy(t, l.db, l.run.ProjectID, "scan-first", "requireStepBefore", map[string]any{"uses": "scm/merged", "requires": "scan"})
			case "malformed law":
				if err := l.db.InTx(context.Background(), func(tx *database.Tx) error {
					return adoption.InstallPolicyIn(t.Context(), tx, l.run.ProjectID, "test", "", "requireStepBefore", map[string]any{"uses": []any{}, "requires": "scan"}, "", time.Now())
				}); err != nil {
					t.Fatal(err)
				}
			case "unreadable law":
				dbtest.FailNextStatement(t, l.db, "FROM policies")
			case "stale uplift":
				// sql-guarantee: make the admitted Change an uplift prepared against another revision; creation would allocate another Change.
				completionExec(t, l.db, `UPDATE changes SET type='uplift',expected_revision=99 WHERE id=$1`, l.run.ChangeID)
			}
			driver := gitDriver{t: t}
			if failure == "" {
				driver = gitDriver{t: t, prepared: func() {}, execute: func() (scm.Execution, error) { return scm.Execution{Result: "merged"}, nil }}
			}
			observed, err := l.owner(driver).Execute(context.Background(), l.call(t))
			if failure == "" {
				if err != nil || !observed.Merged {
					t.Fatalf("the unchanged landing did not merge: %+v %v", observed, err)
				}
				wantEventCount(t, l.db, 1, ofKind("merge.completed"))
				return
			}
			if (err == nil && observed.Refusal == "") || observed.Merged {
				t.Fatalf("%s granted the merge: %+v %v", failure, observed, err)
			}
			wantEventCount(t, l.db, 0, ofKind("merge.completed"))
			if record := l.record(t); record.State != scm.Idle {
				t.Fatalf("%s left a merge record: %+v", failure, record)
			}
			if failure == "law violation" {
				wantStepCount(t, l.db, 1, func(s *Step) bool { return s.Type == "policy/current" && s.Status == StatusNeedsHuman })
				wantReceiptCount(t, l.db, 1, receiptKind("landing.paused-by-law"))
			}
		})
	}
}

// A merge is admitted only for the call Work prepared: an empty call, an
// unknown purpose, an advance that carries an assertion's step, or an
// advance from a stage that is not the last is refused before Git is asked
// anything.
func TestAMergeIsAdmittedOnlyForItsPreparedCaller(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"empty", "unknown purpose", "mixed purposes", "wrong advancement stage"} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			l := landingAtMergeFixture(t)
			call := l.call(t)
			switch mutation {
			case "empty":
				call = MergeCall{}
			case "unknown purpose":
				call.Purpose = 99
			case "mixed purposes":
				call.Purpose = MergeForAdvance
			case "wrong advancement stage":
				spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "finish"}, {Name: "middle"}, {Name: "last"}}}
				// sql-guarantee: replace the pinned declaration without its steps, to present a stale prepared caller; admission creates a declaration and its steps together.
				completionExec(t, l.db, `UPDATE runs SET definition_json=$1 WHERE id=$2`, mustJSON(spec), l.run.ID)
				call = l.call(t)
				call.Purpose, call.Step = MergeForAdvance, Step{}
			}
			observed, err := l.owner(gitDriver{t: t}).Execute(context.Background(), call)
			if err == nil || observed.Merged {
				t.Fatalf("%s granted the merge: %+v %v", mutation, observed, err)
			}
			wantEventCount(t, l.db, 0, ofKind("merge.completed"))
		})
	}
}

// Admission is checked again when SCM records its durable intent, after
// Git prepared the merge: a gate reset or a new law in between withdraws
// it, and Git is not asked to merge.
func TestALocalMergeRechecksItsGatesWhenRecordingItsIntent(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"gate reset", "new law"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			l := landingAtMergeFixture(t)
			driver := gitDriver{t: t, prepared: func() {
				if change == "gate reset" {
					// sql-guarantee: reset only the observed verdict and evidence after preparation; a retry also clears attempt timestamps and would mask this late-verdict fence.
					completionExec(t, l.db, `UPDATE steps SET status='pending',evidence_json='{}' WHERE id=$1`, l.gate)
				} else {
					installPolicy(t, l.db, l.run.ProjectID, "new", "requireStepBefore", map[string]any{"uses": "scm/merged", "requires": "scan"})
				}
			}}
			observed, err := l.owner(driver).Execute(context.Background(), l.call(t))
			if err != nil || observed.Refusal == "" || observed.Merged {
				t.Fatalf("stale admission: %+v %v", observed, err)
			}
			if record := l.record(t); record.State != scm.Idle {
				t.Fatalf("withdrawn admission left a merge record: %+v", record)
			}
			wantEventCount(t, l.db, 0, ofKind("merge.completed"))
			if change == "new law" {
				wantReceiptCount(t, l.db, 1, receiptKind("landing.paused-by-law"))
			}
		})
	}
}

// A merge admitted and then interrupted is settled from what Git shows only
// while everything it was admitted for still holds: its run's revision,
// stage and status, its project, its change's branch and its definition.
// Changed during the inspection, the merge is not settled and the change
// takes no landed commit. Unchanged, it is.
func TestSettlingAPendingMergeRequiresItsAdmittedScope(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{
		"",
		// sql-guarantee: invalidate only retry_revision during inspection; lockRunSnapshotIn compares the whole Run and InvalidateRunDecision also changes UpdatedAt, masking this field-specific check.
		`UPDATE runs SET retry_revision=retry_revision+1`,
		// sql-guarantee: move only current_stage during inspection; TransitionRun would also change UpdatedAt and independently invalidate the snapshot.
		`UPDATE runs SET current_stage='moved'`,
		// sql-guarantee: close only the run status during inspection; Abandon/TransitionRun also change UpdatedAt and would mask the status check.
		`UPDATE runs SET status='abandoned'`,
		// sql-guarantee: move only the Change branch during inspection; Work's workspace commands also change selected bases.
		`UPDATE changes SET branch='another'`,
		// sql-guarantee: replace only the pinned declaration during inspection; admission creates a new run and no mutation rewrites definition_json.
		`UPDATE runs SET definition_json='{"changed":true}'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			l := landingAtMergeFixture(t)
			interrupted := gitDriver{t: t, prepared: func() {}, execute: func() (scm.Execution, error) {
				return scm.Execution{}, errors.New("interrupted after durable admission")
			}}
			if observed, err := l.owner(interrupted).Execute(context.Background(), l.call(t)); err == nil || observed.Merged {
				t.Fatalf("the interrupted merge: %+v %v", observed, err)
			}
			if record := l.record(t); record.State != scm.Pending {
				t.Fatalf("the interrupted merge is not pending: %+v", record)
			}
			inspecting := gitDriver{t: t, inspect: func() (scm.Inspection, error) {
				switch mutation {
				case "":
				default:
					completionExec(t, l.db, mutation)
				}
				return scm.Inspection{Kind: scm.Found, Result: "old-effect"}, nil
			}}
			service := scm.New(l.db, NewSCMWork(l.db, l.dir, policyLawRules{}, nil), &scm.RepositoryLocks{}, nil)
			_, err := service.Inspect(SCMInspectionContext(context.Background(), l.db), l.run.ID, inspecting)
			if mutation == "" {
				if err != nil {
					t.Fatalf("an unchanged pending merge was not settled: %v", err)
				}
				wantEventCount(t, l.db, 1, ofKind("merge.completed"))
				wantEqual(t, "landed", storedChange(t, l.db, l.run.ChangeID).LandedSHA, "old-effect")
				return
			}
			if err == nil {
				t.Fatal("a stale scope settled the merge")
			}
			wantEventCount(t, l.db, 0, ofKind("merge.completed"))
			wantEqual(t, "landed", storedChange(t, l.db, l.run.ChangeID).LandedSHA, "")
		})
	}
}

// A merge and an abandonment of one change serialize, in either order, as
// Work is assembled. While Git merges, a stop waits (one that gives up
// waiting changes nothing) and then finds the merge recorded and is
// refused. Once a prepared stop holds the change's closure and commits, the
// merge waiting behind it is refused before Git is asked anything.
func TestAMergeAndAnAbandonmentSerializeInBothOrders(t *testing.T) {
	t.Parallel()
	t.Run("merge first", func(t *testing.T) {
		t.Parallel()
		l := landingAtMergeFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		entered, release := make(chan struct{}), make(chan struct{})
		work := l.service(gitDriver{t: t, prepared: func() {}, execute: func() (scm.Execution, error) {
			close(entered)
			select {
			case <-release:
				return scm.Execution{Result: "merged"}, nil
			case <-ctx.Done():
				return scm.Execution{}, ctx.Err()
			}
		}})
		call := l.call(t)
		done := make(chan error, 1)
		go func() { _, err := work.ExecuteMerge(ctx, call); done <- err }()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		closed := make(chan error, 1)
		go func() {
			_, err := work.Abandon(ctx, StopWork{ChangeID: l.run.ChangeID})
			closed <- err
		}()
		// A waiter that gives up cannot enter SQL or close the change while
		// Git is held.
		waiting, stop := context.WithTimeout(ctx, 20*time.Millisecond)
		defer stop()
		if _, err := work.Abandon(waiting, StopWork{RunID: l.run.ID}); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("waiting cancellation: %v", err)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if err := <-closed; err == nil || err.Error() != scm.ErrClosureRecorded.Error() {
			t.Fatalf("closure after merge: %v", err)
		}
		wantEqual(t, "state", storedChange(t, l.db, l.run.ChangeID).State, ChangeLanding)
	})
	t.Run("abandon first", func(t *testing.T) {
		t.Parallel()
		l := landingAtMergeFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		work := l.service(gitDriver{t: t})
		stop, err := work.PrepareStop(ctx, l.run.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		defer stop.Close()
		call := l.call(t)
		done := make(chan error, 1)
		go func() { _, err := work.ExecuteMerge(ctx, call); done <- err }()
		if err := l.db.InTx(ctx, func(tx *database.Tx) error {
			_, err := stop.AbandonIn(ctx, tx, StopWork{ChangeID: l.run.ChangeID})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		stop.Close()
		if err := <-done; err == nil {
			t.Fatal("merge admitted after abandonment")
		}
		wantEventCount(t, l.db, 0, ofKind("merge.completed"))
	})
}

// Once a merge is pending or recorded, the change cannot be closed under
// it: stopping the run, closing the change, sending it back and a prepared
// stop are each refused with why, the change stays landing, and no
// abandonment is recorded.
func TestAClosureIsRefusedOnceAMergeIsPendingOrRecorded(t *testing.T) {
	t.Parallel()
	for _, recorded := range []bool{false, true} {
		for _, command := range []string{"run", "change", "sendback", "participant"} {
			t.Run(command+map[bool]string{false: "/pending", true: "/recorded"}[recorded], func(t *testing.T) {
				t.Parallel()
				l := landingAtMergeFixture(t)
				ctx := context.Background()
				driver := gitDriver{t: t, prepared: func() {}, execute: func() (scm.Execution, error) {
					return scm.Execution{}, errors.New("interrupted after durable admission")
				}}
				if recorded {
					driver.execute = func() (scm.Execution, error) { return scm.Execution{Result: "merged"}, nil }
				}
				work := l.service(driver)
				if observed, err := work.ExecuteMerge(ctx, l.call(t)); observed.Merged != recorded || (recorded && err != nil) {
					t.Fatalf("the merge: %+v %v", observed, err)
				}
				var err error
				switch command {
				case "run":
					_, err = work.Abandon(ctx, StopWork{RunID: l.run.ID})
				case "change":
					_, err = work.Abandon(ctx, StopWork{ChangeID: l.run.ChangeID})
				case "sendback":
					_, err = work.SendBack(ctx, SendBack{ChangeID: l.run.ChangeID, Note: "try again"})
				case "participant":
					stop, e := work.PrepareStop(ctx, l.run.ProjectID)
					if e != nil {
						t.Fatal(e)
					}
					defer stop.Close()
					err = l.db.InTx(ctx, func(tx *database.Tx) error {
						_, e := stop.AbandonIn(ctx, tx, StopWork{ChangeID: l.run.ChangeID})
						return e
					})
				}
				want := scm.ErrClosurePending.Error()
				if recorded {
					want = scm.ErrClosureRecorded.Error()
				}
				if err == nil || err.Error() != want {
					t.Fatalf("closure: %v, want %s", err, want)
				}
				wantEqual(t, "state", storedChange(t, l.db, l.run.ChangeID).State, ChangeLanding)
				wantEventCount(t, l.db, 0, ofKind("run.abandoned"))
			})
		}
	}
}
