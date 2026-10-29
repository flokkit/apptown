package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
)

func retryWorkflowSpec() schema.Spec {
	return schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish",
		OnEnter: []schema.Step{{Name: "build", Uses: "agent/pi", With: map[string]any{"n": json.Number("9007199254740993"), "extra": map[string]any{"keep": true}}}},
		Steps:   []schema.Step{{Name: "review", Uses: "agent/review"}},
	}}}
}

func retryFixture(t *testing.T) (stepRetries, *Run, ids.ID, ids.ID) {
	t.Helper()
	return retryFixtureWithSpec(t, retryWorkflowSpec())
}

func retryFixtureWithSpec(t *testing.T, spec schema.Spec) (stepRetries, *Run, ids.ID, ids.ID) {
	t.Helper()
	q, run := admitWorkflowSpec(t, spec)
	agent, review := admittedStepID(t, q, run, "build"), admittedStepID(t, q, run, "review")
	attempts := newStepAttempts(q.db, func(kind string) bool { return kind == "agent/pi" || kind == "agent/review" })
	for _, outcome := range []struct {
		id       ids.ID
		state    StepState
		evidence json.RawMessage
	}{
		{agent, StatusPass, json.RawMessage(`{}`)},
		{review, StatusFail, json.RawMessage(`{"n":9007199254740993}`)},
	} {
		snapshot, err := readStep(context.Background(), q.db, outcome.id)
		if err != nil {
			t.Fatal(err)
		}
		claim, err := attempts.Start(context.Background(), snapshot, snapshot.Type)
		if err != nil {
			t.Fatal(err)
		}
		if err := attempts.Finish(context.Background(), claim, StepOutcome{Status: outcome.state, Evidence: outcome.evidence}); err != nil {
			t.Fatal(err)
		}
	}
	return newStepRetries(q.db, []string{"agent/pi", "script", "agent/review"}), run, agent, review
}
func TestRetryKeepsExactConfigurationAndSessionAndInvalidatesOldDecisions(t *testing.T) {
	t.Parallel()
	owner, run, id, check := retryFixture(t)
	// sql-guarantee: Keep the failed verdict and its evidence while adding historical override attribution; OverrideStep would pass it and change retry feedback.
	completionExec(t, owner.db, `UPDATE steps SET override_by='person',override_reason='old exception' WHERE id=$1`, check)
	if rows, err := workflowsql.New(owner.db.SQL()).InsertRunEvent(context.Background(), workflowsql.InsertRunEventParams{ID: ids.New(time.Now()), RunID: run.ID, At: time.Now(), Kind: "step.started", PayloadJson: []byte(`{"step":"build","sessionId":"long-lived-session"}`)}); err != nil || rows != 1 {
		t.Fatalf("stored historical receipt: rows=%d err=%v", rows, err)
	}
	result, err := owner.retryExpected(context.Background(), retryIntent{RetryCommand: RetryCommand{StepID: id, Mode: RetryContinue}, Actor: commandActor{Receipt: "verified"}}, nil)
	if err != nil || result.RunID != run.ID || result.StepID != id {
		t.Fatalf("retry: %+v %v", result, err)
	}
	config := wantStepState(t, owner.db, id, StatusPending).Config
	wantJSONField(t, "config", config, "n", "9007199254740993", "extra.keep", "true", "continueSession", "long-lived-session")
	if sendback, _ := jsonField(config, "sendback"); !strings.Contains(sendback, "9007199254740993") {
		t.Fatalf("sendback lost the exact identifier: %s", config)
	}
	if s := wantStepState(t, owner.db, check, StatusPending); !jsonEqual(s.Evidence, `{}`) || s.OverrideBy != "" {
		t.Fatalf("check not reset: %+v", s)
	}
	wantEventCount(t, owner.db, 1, kindWith("step.invalidated", "previousEvidence.n", "9007199254740993", "previousOverrideBy", "person"))
	wantEventCount(t, owner.db, 1, kindWith("step.retried", "by", "verified", "mode", "continue"))
	wantRetryRevision(t, owner.db, run.ID, 1)
	if err := owner.db.InTx(context.Background(), func(tx *database.Tx) error {
		return TransitionRunIn(context.Background(), tx, run, RunDone, run.CurrentStage, time.Now())
	}); !errors.Is(err, ErrRunMoved) {
		t.Fatalf("stale advancement: %v", err)
	}
}
func TestRetryRequiresEveryWriteAndReceipt(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"dependent", "retry", "revision", "receipt", "suppressed dependent", "suppressed retry", "suppressed revision", "suppressed retry receipt", "suppressed feedback receipt"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			owner, run, id, check := retryFixture(t)
			before := completionSnapshot(t, owner.db)
			switch failure {
			case "dependent":
				dbtest.FailNextStatement(t, owner.db, "-- name: InvalidateStep")
			case "retry":
				dbtest.FailNextStatement(t, owner.db, "-- name: RetryStep")
			case "revision":
				dbtest.FailNextStatement(t, owner.db, "-- name: InvalidateRunDecision")
			case "receipt":
				dbtest.FailNextStatement(t, owner.db, "INSERT INTO run_events")
			case "suppressed dependent":
				completionExec(t, owner.db, `CREATE FUNCTION veto_retry_step() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.type='agent/review' THEN RETURN NULL; END IF; RETURN NEW; END $$`)
				completionExec(t, owner.db, `CREATE TRIGGER veto_retry_step BEFORE UPDATE ON steps FOR EACH ROW EXECUTE FUNCTION veto_retry_step()`)
			case "suppressed retry":
				completionExec(t, owner.db, `CREATE FUNCTION veto_retry_source() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.type='agent/pi' THEN RETURN NULL; END IF; RETURN NEW; END $$`)
				completionExec(t, owner.db, `CREATE TRIGGER veto_retry_source BEFORE UPDATE ON steps FOR EACH ROW EXECUTE FUNCTION veto_retry_source()`)
			case "suppressed retry receipt", "suppressed feedback receipt":
				kind := "step.retried"
				if failure == "suppressed feedback receipt" {
					kind = "step.sendback"
				}
				completionExec(t, owner.db, `CREATE FUNCTION veto_retry_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='`+kind+`' THEN RETURN NULL; END IF; RETURN NEW; END $$`)
				completionExec(t, owner.db, `CREATE TRIGGER veto_retry_receipt BEFORE INSERT ON run_events FOR EACH ROW EXECUTE FUNCTION veto_retry_receipt()`)
			case "suppressed revision":
				completionExec(t, owner.db, `CREATE FUNCTION veto_retry_revision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
				completionExec(t, owner.db, `CREATE TRIGGER veto_retry_revision BEFORE UPDATE ON runs FOR EACH ROW EXECUTE FUNCTION veto_retry_revision()`)
			}
			result, err := owner.retryExpected(context.Background(), retryIntent{RetryCommand: RetryCommand{StepID: id, Mode: RetryContinue, Feedback: "exact words"}}, nil)
			if err == nil || result != (Retried{}) {
				t.Fatalf("partial retry: %+v %v", result, err)
			}
			wantStepState(t, owner.db, id, StatusPass)
			wantStepState(t, owner.db, check, StatusFail)
			wantRetryRevision(t, owner.db, run.ID, 0)
			wantEventCount(t, owner.db, 7, anyEvent) // Admission and two completed attempts only.
			if after := completionSnapshot(t, owner.db); after != before {
				t.Fatal("retry left partial facts or receipts")
			}
			completionExec(t, owner.db, `DROP FUNCTION IF EXISTS veto_retry_step(), veto_retry_source(), veto_retry_receipt(), veto_retry_revision() CASCADE`)
			if _, err := owner.retryExpected(context.Background(), retryIntent{RetryCommand: RetryCommand{StepID: id, Mode: RetryContinue, Feedback: "exact words"}}, nil); err != nil {
				t.Fatalf("explicit retry after %s: %v", failure, err)
			}
			wantRetryRevision(t, owner.db, run.ID, 1)

		})
	}
}
func TestRetryConcurrentRequestsAndBusyPrerequisites(t *testing.T) {
	t.Parallel()
	owner, run, id, _ := retryFixture(t)
	var winners atomic.Int64
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := owner.retryExpected(context.Background(), retryIntent{RetryCommand: RetryCommand{StepID: id}}, nil)
			if err == nil {
				winners.Add(1)
			} else {
				var conflict *RetryError
				if !errors.As(err, &conflict) || conflict.Kind != RetryConflict {
					t.Errorf("concurrent: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("winners=%d", winners.Load())
	}
	wantRetryRevision(t, owner.db, run.ID, 1)
	for _, state := range []string{"busy", "prerequisite"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish", OnEnter: []schema.Step{{Name: "earlier", Uses: "script"}, {Name: "later", Uses: "agent/pi"}}}}}
			q, run := admitWorkflowSpec(t, spec)
			earlier, later := admittedStepID(t, q, run, "earlier"), admittedStepID(t, q, run, "later")
			attempts := newStepAttempts(q.db, func(kind string) bool { return kind == "script" || kind == "agent/pi" })
			for _, id := range []ids.ID{earlier, later} {
				snapshot, err := readStep(context.Background(), q.db, id)
				if err != nil {
					t.Fatal(err)
				}
				claim, err := attempts.Start(context.Background(), snapshot, snapshot.Type)
				if err != nil {
					t.Fatal(err)
				}
				if err := attempts.Finish(context.Background(), claim, StepOutcome{Status: StatusPass, Evidence: []byte(`{}`)}); err != nil {
					t.Fatal(err)
				}
			}
			owner := newStepRetries(q.db, []string{"script", "agent/pi"})
			id := earlier
			if state == "busy" {
				// A gate retry has no dependent reset to independently reject a busy sibling.
				{
					current, err := workflowsql.New(q.db.SQL()).StepByID(context.Background(), later)
					if err != nil {
						t.Fatal(err)
					}
					if err := requireAttemptWrite(workflowsql.New(q.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: later, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: StatusRunning.String(), EvidenceJson: current.EvidenceJson})); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				// A newer failed prerequisite invalidates the already completed later entry.
				{
					current, err := workflowsql.New(q.db.SQL()).StepByID(context.Background(), earlier)
					if err != nil {
						t.Fatal(err)
					}
					if err := requireAttemptWrite(workflowsql.New(q.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: earlier, Attempt: current.Attempt, ExpectedStatus: current.Status, Status: StatusFail.String(), EvidenceJson: current.EvidenceJson})); err != nil {
						t.Fatal(err)
					}
				}
				id = later
			}
			if _, err := owner.retryExpected(context.Background(), retryIntent{RetryCommand: RetryCommand{StepID: id}}, nil); err == nil {
				t.Fatal("busy/prerequisite bypassed")
			}
			wantEventCount(t, owner.db, 7, anyEvent) // Admission and two completed attempts only.
		})
	}
}
func TestRetryServiceRequiresAuthorityAndRechecksItsSnapshot(t *testing.T) {
	t.Parallel()
	if _, err := newService(nil, nil, Runtime{}).RetryStep(context.Background(), RetryCommand{StepID: idstest.N(1)}); !errors.Is(err, ErrCallerRequired) {
		t.Fatalf("missing authority: %v", err)
	}
	for _, change := range []string{"none", "denied", "project", "run", "change", "attempt", "revision", "stage", "type", "configuration"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			owner, run, id, _ := retryFixture(t)
			auth := authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
				if p.Action == actionIdentify {
					return "verified", nil
				}
				if p.Action != actionRetryAttempt || p.ProjectID != run.ProjectID || p.RunID != run.ID || p.ChangeID != run.ChangeID {
					t.Fatalf("wrong permission %+v", p)
				}
				switch change {
				case "denied":
					return "", &access.Denied{Reason: "not allowed"}
				case "project":
					foreign := workflowTestProject(t, owner.db, "other", "Other", "")
					ch := admissionTestChange(t, owner.db, foreign, "Other")
					// sql-guarantee: No owner mutation rehomes a run's project/Change binding; change that same identity inside retry authorization to isolate its scope fence.
					completionExec(t, owner.db, `UPDATE runs SET project_id=$1,number=number+100000,change_id=$2 WHERE id=$3`, foreign, ch, run.ID)
				case "run":
					other := insertStoredRun(t, owner.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, WorkflowName: "other", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: []byte(`{"stages":[{"name":"finish"}]}`)}).ID
					// sql-guarantee: No owner mutation reparents an existing step; move the authorized row to another run to invalidate its captured identity.
					completionExec(t, owner.db, `UPDATE steps SET run_id=$1 WHERE id=$2`, other, id)
				case "change":
					// sql-guarantee: No owner mutation detaches an existing run's Change; erase the authorized binding to isolate the retry scope fence.
					completionExec(t, owner.db, `UPDATE runs SET change_id=NULL WHERE id=$1`, run.ID)
				case "attempt":
					{
						current, err := workflowsql.New(owner.db.SQL()).StepByID(context.Background(), id)
						if err != nil {
							t.Fatal(err)
						}
						if err := requireAttemptWrite(workflowsql.New(owner.db.SQL()).StartParentCall(context.Background(), workflowsql.StartParentCallParams{ID: id, Attempt: current.Attempt, OldStatus: current.Status, Status: current.Status, StartedAt: current.StartedAt, EvidenceJson: current.EvidenceJson})); err != nil {
							t.Fatal(err)
						}
					}
				case "revision":
					{
						current, err := workflowsql.New(owner.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: run.ID})
						if err != nil {
							t.Fatal(err)
						}
						if err := requireRunWrite(workflowsql.New(owner.db.SQL()).InvalidateRunDecision(context.Background(), workflowsql.InvalidateRunDecisionParams{At: time.Now(), ID: run.ID, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision})); err != nil {
							t.Fatal(err)
						}
					}
				case "stage":
					{
						current, err := workflowsql.New(owner.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: run.ID})
						if err != nil {
							t.Fatal(err)
						}
						if err := requireRunWrite(workflowsql.New(owner.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, ExpectedStatus: current.Status, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision, NextStage: "later", NextStatus: current.Status})); err != nil {
							t.Fatal(err)
						}
					}
				case "type":
					// sql-guarantee: Replace the step's immutable type inside retry authorization to invalidate its admitted runner identity.
					completionExec(t, owner.db, `UPDATE steps SET type='script' WHERE id=$1`, id)
				case "configuration":
					{
						current, err := workflowsql.New(owner.db.SQL()).StepByID(context.Background(), id)
						if err != nil {
							t.Fatal(err)
						}
						if err := requireAttemptWrite(workflowsql.New(owner.db.SQL()).RetryStep(context.Background(), workflowsql.RetryStepParams{ID: id, Attempt: current.Attempt, ConfigJson: []byte(`{"changed":true}`)})); err != nil {
							t.Fatal(err)
						}
						// Restore the current verdict/evidence; only configuration changes among
						// the authority snapshot's fields. Its fence does not include timestamps.
						if err := requireAttemptWrite(workflowsql.New(owner.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: id, Attempt: current.Attempt, ExpectedStatus: StatusPending.String(), Status: current.Status, EvidenceJson: current.EvidenceJson})); err != nil {
							t.Fatal(err)
						}
					}
				}
				return "verified", nil
			})
			kinds := []string{"agent/pi", "script"}
			svc := newService(owner.db, auth, Runtime{Automatic: kinds})
			kinds[0] = "approval"
			result, err := svc.RetryStep(context.Background(), RetryCommand{StepID: id})
			if change == "none" {
				if err != nil || result.StepID != id {
					t.Fatalf("admitted: %+v %v", result, err)
				}
				wantEventCount(t, owner.db, 1, kindWith("step.retried", "by", "verified"))
			} else {
				if err == nil || result != (Retried{}) {
					t.Fatalf("changed %s accepted: %+v %v", change, result, err)
				}
				wantEventCount(t, owner.db, 7, anyEvent) // Admission and two completed attempts only.
			}
		})
	}
}
func TestRetryCancelsLockWaitAndRejectsInvalidModes(t *testing.T) {
	t.Parallel()
	for _, mode := range []RetryMode{RetryDefault, RetryContinue, RetryRestart} {
		got, err := ParseRetryMode(mode.String())
		if err != nil || got != mode {
			t.Fatalf("mode: %v %v", got, err)
		}
	}
	if _, err := ParseRetryMode("replay"); err == nil {
		t.Fatal("unknown retry mode accepted")
	}
	owner, run, id, _ := retryFixture(t)
	if _, err := owner.retryExpected(context.Background(), retryIntent{RetryCommand: RetryCommand{StepID: id, Mode: RetryMode(255)}}, nil); err == nil {
		t.Fatal("invalid typed retry mode accepted")
	}
	gate, err := owner.db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Rollback() }()
	if _, err := workflowsql.New(gate.SQL()).ProjectForUpdate(context.Background(), run.ProjectID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := owner.retryExpected(ctx, retryIntent{RetryCommand: RetryCommand{StepID: id}}, nil)
		done <- err
	}()
	dbtest.WaitForLock(t, owner.db, "-- name: ProjectForUpdate")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retry wait did not cancel")
	}
	wantEventCount(t, owner.db, 7, anyEvent) // Admission and two completed attempts only.
}
func TestRetryConfigurationRetainsUnknownFields(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"n":9007199254740993,"sendback":{"unknown":"shape"}}`)
	cfg, err := retryConfiguration(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw[5] = '1'
	setRetryField(cfg, "retryMode", "continue")
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg["n"]) != "9007199254740993" || string(cfg["sendback"]) != `{"unknown":"shape"}` {
		t.Fatal(fmt.Sprint(string(body)))
	}
}
