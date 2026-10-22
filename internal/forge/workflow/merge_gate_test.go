package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

func mergeGateFixture(t *testing.T) (queries, *Run) {
	t.Helper()
	db, change := changeEditFixture(t, ChangeDraft{Title: "A change"})
	run := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()),
		ProjectID: change.ProjectID, ChangeID: change.ID, WorkflowName: "test", WorkflowVersion: 1,
		Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish",
		DefinitionJson: []byte(`{"kind":"pipeline","role":"landing","stages":[{"name":"finish"}]}`),
	})
	landingAdmittedForTest(t, db, run.ID)
	return queries{db: db}, run
}
func mergeGateRace(n int, fn func(int) int) []int {
	results := make([]int, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() { defer wg.Done(); results[i] = fn(i) }()
	}
	wg.Wait()
	return results
}
func mergeGateWinners(values []int) int {
	n := 0
	for _, v := range values {
		n += v
	}
	return n
}
func prepareMergeGate(t *testing.T, db *database.DB, run *Run) *preparedMergeGate {
	t.Helper()
	p, err := (mergeGateStore{db, clocked{}}).prepare(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMergeGateConflictAndDriftHaveCommittedEvidence(t *testing.T) {
	t.Parallel()
	store, run := mergeGateFixture(t)
	conflicts := []string{"main.go"}
	evidence := MergeBlockEvidence(conflicts, nil)
	conflicts[0] = "caller mutation"
	first, err := prepareMergeGate(t, store.db, run).block(context.Background(), evidence)
	if err != nil || first == nil || first.Status != StatusNeedsHuman || !first.FinishedAt.Valid {
		t.Fatalf("conflict: %+v %v", first, err)
	}
	var saved MergeEvidence
	if err := json.Unmarshal(first.Evidence, &saved); err != nil || len(saved.Conflicts) != 1 || saved.Conflicts[0] != "main.go" {
		t.Fatalf("evidence: %+v %v", saved, err)
	}
	second, err := prepareMergeGate(t, store.db, run).block(context.Background(), MergeBlockEvidence(nil, []string{"untracked"}))
	if err != nil || second.ID != first.ID || second.Status != StatusNeedsHuman {
		t.Fatalf("drift: %+v %v", second, err)
	}
	wantStepCount(t, store.db, 1, nil)
	wantEventCount(t, store.db, 1, func(e Event) bool {
		return e.Kind == "merge.conflict" && jsonPathEqual(e.Payload, "files", `["main.go"]`)
	})
	wantEventCount(t, store.db, 1, func(e Event) bool {
		return e.Kind == "integrity.out-of-band-edits" && jsonPathEqual(e.Payload, "files", `["untracked"]`)
	})
	if err := prepareMergeGate(t, store.db, run).clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantStepCount(t, store.db, 1, func(s *Step) bool {
		return s.Status == StatusPass && jsonEqual(s.Evidence, `{"merged":true}`) && s.FinishedAt.Valid
	})
	wantEventCount(t, store.db, 1, kindWith("check.evaluated", "status", "pass"))
	if err := prepareMergeGate(t, store.db, run).clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantEventCount(t, store.db, 3, anyEvent)
	if first.Status != StatusNeedsHuman || second.Status != StatusNeedsHuman {
		t.Fatal("saved input snapshots changed")
	}
}

func TestMergeGateDecisionAndReceiptsAreAtomic(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"create", "block", "clear"} {
		for _, failure := range []string{"read", "write", "receipt", "suppressed write", "suppressed receipt", "commit", "readback"} {
			if phase == "clear" && failure == "readback" {
				continue
			}
			t.Run(phase+"/"+failure, func(t *testing.T) {
				t.Parallel()
				store, run := mergeGateFixture(t)
				before := 0
				if phase != "create" {
					// Two checks make clear's partial-progress rollback observable.
					insertStoredStep(t, store.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "earlier", Name: "old-merge", Type: "scm/merged", ConfigJson: []byte(`{}`), Status: StatusNeedsHuman.String()})
					insertStoredStep(t, store.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "merge", Type: "scm/merged", ConfigJson: []byte(`{}`), Status: StatusNeedsHuman.String()})
					before = 2
				}
				p := prepareMergeGate(t, store.db, run)
				switch failure {
				case "read":
					dbtest.FailNextStatement(t, store.db, "name: StepsOfTypeForUpdate")
				case "write":
					query := "name: RecordStepEvaluation"
					if phase == "create" {
						query = "name: CreateWorkflowGate"
					}
					dbtest.FailNextStatement(t, store.db, query)
				case "receipt":
					dbtest.FailNextStatement(t, store.db, "INSERT INTO run_events")
				case "readback":
					dbtest.FailNextStatement(t, store.db, "name: StepByID")
				default:
					table, event, body := "steps", "UPDATE", "RETURN NULL;"
					if phase == "create" {
						event = "INSERT"
					}
					if failure == "suppressed receipt" {
						table, event = "run_events", "INSERT"
					}
					trigger := "CREATE TRIGGER veto_merge BEFORE " + event + " ON " + table
					if failure == "commit" {
						body = "RAISE EXCEPTION 'commit refused';"
						trigger = "CREATE CONSTRAINT TRIGGER veto_merge AFTER " + event + " ON " + table + " DEFERRABLE INITIALLY DEFERRED"
					}
					completionExec(t, store.db, `CREATE FUNCTION veto_merge() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
					completionExec(t, store.db, trigger+" FOR EACH ROW EXECUTE FUNCTION veto_merge()")
				}
				var got *Step
				var err error
				if phase == "clear" {
					err = p.clear(context.Background())
				} else {
					got, err = p.block(context.Background(), MergeBlockEvidence([]string{"conflict"}, nil))
				}
				if got != nil || err == nil {
					t.Fatalf("%s %s returned success: %+v %v", phase, failure, got, err)
				}
				wantStepCount(t, store.db, before, untouchedIn(StatusNeedsHuman))
				wantStepCount(t, store.db, before, nil)
				wantEventCount(t, store.db, 0, anyEvent)
			})
		}
	}
}

func TestMergeGateCannotReplaceNewerScope(t *testing.T) {
	t.Parallel()
	for name, mutation := range map[string]func(*database.DB, *Run, ids.ID){
		"run ended": func(db *database.DB, _ *Run, _ ids.ID) {
			// sql-guarantee: TransitionRun also changes updated_at, which lockRunSnapshotIn compares; keep lifecycle as the only changed snapshot field.
			completionExec(t, db, `UPDATE runs SET status='done'`)
		},
		"stage": func(db *database.DB, _ *Run, _ ids.ID) {
			// sql-guarantee: TransitionRun also changes updated_at, which lockRunSnapshotIn compares; keep stage as the only changed snapshot field.
			completionExec(t, db, `UPDATE runs SET current_stage='later'`)
		},
		"revision": func(db *database.DB, _ *Run, _ ids.ID) {
			// sql-guarantee: InvalidateRunDecision also changes updated_at, which lockRunSnapshotIn compares; isolate the retry-revision mismatch.
			completionExec(t, db, `UPDATE runs SET retry_revision=1`)
		},
		"definition": func(db *database.DB, _ *Run, _ ids.ID) {
			// sql-guarantee: No owner mutation rewrites a pinned run declaration; retain the same run identity to test its prepared-definition fence.
			completionExec(t, db, `UPDATE runs SET definition_json='{}'`)
		},
		"status": func(db *database.DB, _ *Run, _ ids.ID) {
			// sql-guarantee: RecordApproval also stamps finished_at, which SameStep compares; keep status as the only changed gate-snapshot field.
			completionExec(t, db, `UPDATE steps SET status='pass'`)
		},
		"attempt": func(db *database.DB, run *Run, id ids.ID) {
			{
				current, err := workflowsql.New(db.SQL()).StepByID(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				if err := requireAttemptWrite(workflowsql.New(db.SQL()).StartParentCall(context.Background(), workflowsql.StartParentCallParams{ID: id, Attempt: current.Attempt, OldStatus: current.Status, Status: current.Status, StartedAt: current.StartedAt, EvidenceJson: current.EvidenceJson})); err != nil {
					t.Fatal(err)
				}
			}
		},
		"configuration": func(db *database.DB, run *Run, id ids.ID) {
			{
				current, err := workflowsql.New(db.SQL()).StepByID(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				if err := requireAttemptWrite(workflowsql.New(db.SQL()).RetryStep(context.Background(), workflowsql.RetryStepParams{ID: id, Attempt: current.Attempt, ConfigJson: []byte(`{"different":true}`)})); err != nil {
					t.Fatal(err)
				}
				// The gate starts with null timestamps and empty evidence: restoring its
				// needs_human verdict leaves configuration as the only SameStep difference.
				if err := requireAttemptWrite(workflowsql.New(db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: id, Attempt: current.Attempt, ExpectedStatus: StatusPending.String(), Status: current.Status, EvidenceJson: current.EvidenceJson})); err != nil {
					t.Fatal(err)
				}
			}
		},
		"evidence": func(db *database.DB, run *Run, id ids.ID) {
			if err := requireAttemptWrite(workflowsql.New(db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: id, ExpectedStatus: StatusNeedsHuman.String(), Status: StatusNeedsHuman.String(), EvidenceJson: []byte(`{"newer":true}`)})); err != nil {
				t.Fatal(err)
			}
		},
		"finished": func(db *database.DB, run *Run, id ids.ID) {
			if err := requireAttemptWrite(workflowsql.New(db.SQL()).RecordStepEvaluation(context.Background(), workflowsql.RecordStepEvaluationParams{At: time.Now(), ID: id, ExpectedStatus: StatusNeedsHuman.String(), Status: StatusNeedsHuman.String(), EvidenceJson: []byte(`{}`)})); err != nil {
				t.Fatal(err)
			}
		},
		"override": func(db *database.DB, run *Run, id ids.ID) {
			if err := requireAttemptWrite(workflowsql.New(db.SQL()).OverrideStep(context.Background(), workflowsql.OverrideStepParams{At: time.Now(), ID: id, ExpectedStatus: StatusNeedsHuman.String(), OverrideBy: "alice", OverrideReason: "reviewed"})); err != nil {
				t.Fatal(err)
			}
		},
		"gate added": func(db *database.DB, run *Run, id ids.ID) {
			insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "earlier", Name: "new-check", Type: "scm/merged", Status: StatusPending.String()})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store, run := mergeGateFixture(t)
			id := insertStoredStep(t, store.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "merge", Type: "scm/merged", ConfigJson: []byte(`{}`), Status: StatusNeedsHuman.String()})
			p := prepareMergeGate(t, store.db, run)
			mutation(store.db, run, id)
			got, err := p.block(context.Background(), MergeBlockEvidence([]string{"old conflict"}, nil))
			if got != nil || !errors.Is(err, ErrRunMoved) {
				t.Fatalf("stale block %s: %+v %v", name, got, err)
			}
			if err := p.clear(context.Background()); !errors.Is(err, ErrRunMoved) {
				t.Fatalf("stale clear %s: %v", name, err)
			}
			wantEventCount(t, store.db, 0, anyEvent)
		})
	}
}

func TestMergeGatePreservesStandingOverrideWithoutInventingMerge(t *testing.T) {
	t.Parallel()
	store, run := mergeGateFixture(t)
	id := insertStoredStep(t, store.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "merge", Type: "scm/merged", Status: StatusPass.String()})
	{
		current, err := workflowsql.New(store.db.SQL()).StepByID(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if err := requireAttemptWrite(workflowsql.New(store.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: id, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: StatusPass.String(), EvidenceJson: []byte(`{"original":true}`)})); err != nil {
			t.Fatal(err)
		}
	}
	{
		current, err := workflowsql.New(store.db.SQL()).StepByID(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if err := requireAttemptWrite(workflowsql.New(store.db.SQL()).OverrideStep(context.Background(), workflowsql.OverrideStepParams{At: time.Now(), ID: id, Attempt: current.Attempt, ExpectedStatus: current.Status, OverrideBy: "alice", OverrideReason: "exception"})); err != nil {
			t.Fatal(err)
		}
	}
	p := prepareMergeGate(t, store.db, run)
	got, err := p.block(context.Background(), MergeBlockEvidence([]string{"still conflicted"}, nil))
	if got != nil || !errors.Is(err, errMergeOverride) {
		t.Fatalf("override became a physical merge: %+v %v", got, err)
	}
	if err := p.clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	step := wantStepState(t, store.db, id, StatusPass)
	if step.OverrideBy != "alice" || step.OverrideReason != "exception" || !jsonEqual(step.Evidence, `{"original":true}`) {
		t.Fatalf("override: %+v", step)
	}
	wantEventCount(t, store.db, 0, anyEvent)
}

func TestMergeGateConcurrentPreparedResultsHaveOneWinner(t *testing.T) {
	t.Parallel()
	store, run := mergeGateFixture(t)
	prepared := make([]*preparedMergeGate, 8)
	for i := range prepared {
		prepared[i] = prepareMergeGate(t, store.db, run)
	}
	outcomes := mergeGateRace(8, func(i int) int {
		step, err := prepared[i].block(context.Background(), MergeBlockEvidence([]string{"conflict"}, nil))
		if err == nil && step != nil && step.Status == StatusNeedsHuman {
			return 1
		}
		if !errors.Is(err, ErrRunMoved) || step != nil {
			t.Errorf("competing result: %+v %v", step, err)
		}
		return 0
	})
	if mergeGateWinners(outcomes) != 1 {
		t.Fatal(outcomes)
	}
	wantStepCount(t, store.db, 1, nil)
	wantEventCount(t, store.db, 1, ofKind("merge.conflict"))
}

func TestMergeGateAdmissionAndCancellation(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"running", "duplicate", "read", "cancelled lock"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			store, run := mergeGateFixture(t)
			if failure == "running" || failure == "duplicate" {
				status := StatusRunning
				if failure == "duplicate" {
					status = StatusPass
				}
				insertStoredStep(t, store.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "merge", Type: "scm/merged", ConfigJson: []byte(`{}`), Status: status.String()})
				if failure == "duplicate" {
					insertStoredStep(t, store.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "other", Type: "scm/merged", ConfigJson: []byte(`{}`), Status: StatusPending.String()})
				}
			}
			if failure == "read" {
				dbtest.FailNextStatement(t, store.db, "name: StepsOfTypeForUpdate")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "cancelled lock" {
				lock, err := store.db.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Rollback()
				holdProjectLock(t, lock, run.ProjectID)
				done := make(chan error, 1)
				go func() { _, err := (mergeGateStore{store.db, clocked{}}).prepare(ctx, run); done <- err }()
				dbtest.WaitForLock(t, store.db, "-- name: ProjectForUpdate")
				cancel()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("cancelled admission kept waiting")
				}
			} else if p, err := (mergeGateStore{store.db, clocked{}}).prepare(ctx, run); err == nil || p != nil {
				t.Fatalf("%s admitted: %+v %v", failure, p, err)
			}
		})
	}
}

func TestPreparedMergeGateHoldsNoConnection(t *testing.T) {
	t.Parallel()
	store, run := mergeGateFixture(t)
	store.db.Pool().SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p, err := (mergeGateStore{store.db, clocked{}}).prepare(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	// Represents an external adapter using the same one-connection pool.
	if _, err := readRun(ctx, store.db, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.block(ctx, MergeBlockEvidence([]string{"conflict"}, nil)); err != nil {
		t.Fatal(err)
	}
}

func TestMergeGateClearRollsBackEarlierGateAndReceipt(t *testing.T) {
	t.Parallel()
	store, run := mergeGateFixture(t)
	insertStoredStep(t, store.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "earlier", Name: "first", Type: "scm/merged", ConfigJson: []byte(`{}`), Status: StatusNeedsHuman.String()})
	insertStoredStep(t, store.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "second", Type: "scm/merged", ConfigJson: []byte(`{}`), Status: StatusNeedsHuman.String()})
	p := prepareMergeGate(t, store.db, run)
	completionExec(t, store.db, `CREATE FUNCTION veto_second_gate() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.name='second' THEN RETURN NULL; END IF; RETURN NEW; END $$`)
	completionExec(t, store.db, `CREATE TRIGGER veto_second_gate BEFORE UPDATE ON steps FOR EACH ROW EXECUTE FUNCTION veto_second_gate()`)
	if err := p.clear(context.Background()); err == nil {
		t.Fatal("partial clear returned success")
	}
	wantStepCount(t, store.db, 2, untouchedIn(StatusNeedsHuman))
	wantEventCount(t, store.db, 0, anyEvent)
}

func TestMergeGateRefusesChangedSourceMetadata(t *testing.T) {
	t.Parallel()
	for name, mutation := range map[string]func(*database.DB, *Run){
		"default branch": func(db *database.DB, run *Run) { setMergeSourceDefaultBranch(t, db, run.ProjectID, "trunk") },
		"adoption": func(db *database.DB, run *Run) {
			if err := db.InTx(context.Background(), func(tx *database.Tx) error {
				return adoption.RecordIn(t.Context(), tx, run.ProjectID, 1, adoption.AdoptionEdit, "competing adoption", adoption.EmptyReceipt(), "alice", time.Now())
			}); err != nil {
				t.Fatal(err)
			}
		},
		"branch": func(db *database.DB, run *Run) {
			if err := requireRunWrite(workflowsql.New(db.SQL()).SetChangeBranch(context.Background(), workflowsql.SetChangeBranchParams{ID: run.ChangeID, ProjectID: run.ProjectID, Branch: "changed"})); err != nil {
				t.Fatal(err)
			}
		},
		"expected revision": func(db *database.DB, _ *Run) {
			// sql-guarantee: No owner mutation edits a pinned expected adoption revision; mutate that same Change after merge preparation.
			completionExec(t, db, `UPDATE changes SET expected_revision=1`)
		},
		"type": func(db *database.DB, _ *Run) {
			// sql-guarantee: No owner mutation changes an admitted Change's immutable type; retain its identity to invalidate the prepared source.
			completionExec(t, db, `UPDATE changes SET type='uplift'`)
		},
		"MR": func(db *database.DB, run *Run) {
			if _, err := AddArtifactIn(context.Background(), db, run.ID, "mr", "!3", "", "group/project", time.Now()); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store, run := mergeGateFixture(t)
			setMergeSourceDefaultBranch(t, store.db, run.ProjectID, "main")
			p := prepareMergeGate(t, store.db, run)
			mutation(store.db, run)
			if step, err := p.block(context.Background(), MergeBlockEvidence([]string{"old conflict"}, nil)); step != nil || !errors.Is(err, ErrRunMoved) {
				t.Fatalf("stale source block: %+v %v", step, err)
			}
			if err := p.clear(context.Background()); !errors.Is(err, ErrRunMoved) {
				t.Fatalf("stale source clear: %v", err)
			}
			wantStepCount(t, store.db, 0, nil)
			wantEventCount(t, store.db, 0, anyEvent)
		})
	}
}

// landingAdmittedForTest records that a person who may land admitted the
// stored run: these fixtures exercise the merge mechanics, which rest on
// that fact (assent.go).
func landingAdmittedForTest(t *testing.T, db *database.DB, runID ids.ID) {
	t.Helper()
	if err := db.InTx(context.Background(), func(tx *database.Tx) error { return markLandingAuthorityIn(context.Background(), tx, runID, "lander") }); err != nil {
		t.Fatal(err)
	}
}
