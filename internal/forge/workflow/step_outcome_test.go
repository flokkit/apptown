package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func outcomeFixture(t *testing.T, kind string) (stepAttempts, *Step) {
	t.Helper()
	q, _, id := admitSingleStep(t, schema.Step{Name: "accept", Uses: kind, With: map[string]any{"sendback": map[string]any{"reason": "feedback"}, "identifier": json.Number("9007199254740993")}})
	snapshot, err := readStep(context.Background(), q.db, id)
	if err != nil {
		t.Fatal(err)
	}
	owner := newStepAttempts(q.db, func(k string) bool { return k == kind })
	claim, err := owner.Start(context.Background(), snapshot, kind)
	if err != nil {
		t.Fatal(err)
	}
	return owner, claim
}
func TestStepOutcomeRequiresCommittedCurrentClaim(t *testing.T) {
	t.Parallel()
	if err := newStepAttempts(nil, nil).Finish(context.Background(), nil, StepOutcome{}); !errors.Is(err, ErrStepAttemptLost) {
		t.Fatalf("nil claim: %v", err)
	}
	for _, change := range []string{"read", "retarget", "moved run", "attempt", "run closed", "stage", "override", "invalid outcome"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			owner, claim := outcomeFixture(t, "script")
			id := claim.ID
			status := StatusPass
			switch change {
			case "read":
				var err error
				claim, err = readStep(context.Background(), owner.db, id)
				if err != nil {
					t.Fatal(err)
				}
			case "retarget":
				claim.ID = idstest.N(999999)
			case "moved run":
				other := insertStoredRun(t, owner.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: claim.ownedProjectID, WorkflowName: "other", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(`{}`)}).ID
				// sql-guarantee: Reparent the claimed step without changing its running status or owned attempt to isolate the run-identity completion fence; admission fixes run_id and no generated step mutation reparents it.
				completionExec(t, owner.db, `UPDATE steps SET run_id=? WHERE id=?`, other, id)
			case "attempt":
				incrementScopeAttempt(t, owner.db, id)
			case "run closed":
				transitionScopeRun(t, owner.db, workflowsql.TransitionRunParams{At: time.Now(), ID: claim.RunID, NextStatus: "abandoned"})
			case "stage":
				transitionScopeRun(t, owner.db, workflowsql.TransitionRunParams{At: time.Now(), ID: claim.RunID, NextStage: "later"})
			case "override":
				// sql-guarantee: Set only override_by while retaining the running claim's status and attempt so rejection must honor the override guard; OverrideStep also changes status to pass and would mask that guard.
				completionExec(t, owner.db, `UPDATE steps SET override_by='other' WHERE id=?`, id)
			case "invalid outcome":
				status = StatusPending
			}
			if err := owner.Finish(context.Background(), claim, StepOutcome{Status: status, Evidence: json.RawMessage(`{"reason":"late"}`)}); err == nil {
				t.Fatal("invalid claim published outcome")
			}
			wantStepState(t, owner.db, id, StatusRunning)
			wantEventCount(t, owner.db, 0, ofKind("step.finished"))
		})
	}
}
func TestStepOutcomeProtectsReceiptIdentityAndExactEvidence(t *testing.T) {
	t.Parallel()
	owner, claim := outcomeFixture(t, "agent/pi")
	outcome := StepOutcome{Status: StatusPass, Evidence: json.RawMessage(`{"large":9007199254740993}`), Details: json.RawMessage(`{"step":"spoof","status":"fail","attempt":99,"large":9007199254740993}`)}
	if err := owner.Finish(context.Background(), claim, outcome); err != nil {
		t.Fatal(err)
	}
	finished := wantStepState(t, owner.db, claim.ID, StatusPass)
	wantJSONField(t, "evidence", finished.Evidence, "large", "9007199254740993")
	wantJSONField(t, "config", finished.Config, "identifier", "9007199254740993")
	if hasKey(finished.Config, "sendback") {
		t.Fatalf("sendback kept after the outcome: %s", finished.Config)
	}
	wantRunEventCount(t, owner.db, claim.RunID, 1, kindWith("step.finished", "step", "accept", "status", "pass", "attempt", "1", "large", "9007199254740993"))
	if err := owner.Finish(context.Background(), claim, outcome); !errors.Is(err, ErrStepAttemptLost) {
		t.Fatalf("second outcome: %v", err)
	}
	current, err := readStep(context.Background(), owner.db, claim.ID)
	if err != nil || current.OwnedAttempt() != 0 {
		t.Fatalf("completion granted another claim: %+v %v", current, err)
	}
}
func TestStepOutcomeWriteReceiptAndContinuationAreAtomic(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"write", "suppressed", "receipt", "continuation", "invalid evidence", "invalid details"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			owner, claim := outcomeFixture(t, "agent/pi")
			outcome := StepOutcome{Status: StatusPass, Evidence: json.RawMessage(`{"result":"finished"}`)}
			switch failure {
			case "write":
				dbtest.FailNextStatement(t, owner.db, "UPDATE steps SET status=")
			case "suppressed":
				completionExec(t, owner.db, `CREATE FUNCTION veto_attempt_finish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status='pass' THEN RETURN NULL; END IF; RETURN NEW; END $$`)
				completionExec(t, owner.db, `CREATE TRIGGER veto_attempt_finish BEFORE UPDATE ON steps FOR EACH ROW EXECUTE FUNCTION veto_attempt_finish()`)
			case "receipt":
				dbtest.FailNextStatement(t, owner.db, "INSERT INTO run_events")
			case "invalid evidence":
				outcome.Evidence = json.RawMessage(`bad`)
			case "invalid details":
				outcome.Details = json.RawMessage(`[]`)
			}
			err := owner.db.InTx(context.Background(), func(tx *database.Tx) error {
				result, err := owner.FinishIn(context.Background(), tx, claim, outcome)
				if err != nil {
					return err
				}
				if result.Run.ID != claim.RunID || result.Step.ID != claim.ID || result.Step.Status != StatusPass || result.Step.OwnedAttempt() != 0 {
					t.Fatalf("uncommitted projection: %+v", result)
				}
				if failure == "continuation" {
					return errors.New("continuation failed")
				}
				return nil
			})
			if err == nil {
				t.Fatalf("partial %s completed", failure)
			}
			wantEqual(t, "sendback kept", hasKey(wantStepState(t, owner.db, claim.ID, StatusRunning).Config, "sendback"), true)
			wantEventCount(t, owner.db, 0, ofKind("step.finished"))
		})
	}
}
func TestStepOutcomeFreezesCallerBuffers(t *testing.T) {
	t.Parallel()
	input := StepOutcome{Evidence: json.RawMessage(`{"n":9007199254740993}`), Details: json.RawMessage(`{"n":9007199254740993}`)}
	evidence, details, err := freezeOutcome(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Evidence[5] = '1'
	input.Details[5] = '1'
	if string(evidence) != `{"n":9007199254740993}` || string(details["n"]) != "9007199254740993" {
		t.Fatalf("aliased buffers: %s %s", evidence, details["n"])
	}
}
func TestStepOutcomeCancelsWaitAndRejectsScopeMovedDuringLock(t *testing.T) {
	t.Parallel()
	for _, cancelWait := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancel", false: "scope"}[cancelWait], func(t *testing.T) {
			t.Parallel()
			owner, claim := outcomeFixture(t, "script")
			project := storedRun(t, owner.db, claim.RunID).ProjectID
			gate, err := owner.db.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = gate.Rollback() }()
			holdProjectLock(t, gate, project)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- owner.Finish(ctx, claim, StepOutcome{Status: StatusPass}) }()
			dbtest.WaitForLock(t, owner.db, "-- name: ProjectForUpdate")
			if cancelWait {
				cancel()
			} else {
				foreign := workflowTestProject(t, owner.db, "foreign", "Foreign", "")
				run := insertStoredRun(t, owner.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: foreign, WorkflowName: "foreign", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "finish", DefinitionJson: json.RawMessage(`{}`)}).ID
				// sql-guarantee: Reparent the running step after Finish waits on the original project lock to test scope revalidation; existing step mutations preserve run_id and admission cannot move an in-flight claim.
				completionExec(t, owner.db, `UPDATE steps SET run_id=? WHERE id=?`, run, claim.ID)
				if err := gate.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				want := ErrStepAttemptLost
				if cancelWait {
					want = context.Canceled
				}
				if !errors.Is(err, want) {
					t.Fatalf("scope/cancel: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("completion did not leave its wait")
			}
			wantStepState(t, owner.db, claim.ID, StatusRunning)
			wantEventCount(t, owner.db, 0, ofKind("step.finished"))
		})
	}
}

func TestStepOutcomeKeepsFeedbackUntilAgentSuccess(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"agent/pi", "scan"} {
		t.Run(kind, func(t *testing.T) {
			owner, claim := outcomeFixture(t, kind)
			status := StatusPass
			if kind == "agent/pi" {
				status = StatusFail
			}
			if err := owner.Finish(context.Background(), claim, StepOutcome{Status: status, Evidence: json.RawMessage(`{"result":"recorded"}`)}); err != nil {
				t.Fatal(err)
			}
			finished := wantStepState(t, owner.db, claim.ID, status)
			wantEqual(t, "sendback kept", hasKey(finished.Config, "sendback"), true)
			wantJSONField(t, "config", finished.Config, "identifier", "9007199254740993")
			wantJSONField(t, "evidence", finished.Evidence, "result", "recorded")
		})
	}
}

// stepArtifactsOf reads a run's artifacts and artifact.added receipts through
// the owner's reads, bodies included, binary ones as bytes.
func stepArtifactsOf(t *testing.T, db *database.DB, runID ids.ID) ([]ArtifactBody, []Event) {
	t.Helper()
	artifacts, err := readWorkflowArtifacts(context.Background(), db, workflowArtifactQuery{RunID: runID, View: artifactBody})
	if err != nil {
		t.Fatal(err)
	}
	events, err := readWorkflowEvents(context.Background(), db, runID, TraceView)
	if err != nil {
		t.Fatal(err)
	}
	var added []Event
	for _, event := range events {
		if event.Kind == "artifact.added" {
			added = append(added, event)
		}
	}
	return artifacts, added
}

func producedArtifacts() []StepArtifact {
	return []StepArtifact{
		{ArtifactDraft: ArtifactDraft{Kind: "spec", Name: "spec-draft", Body: "# the spec"}, By: "agent:accept"},
		{ArtifactDraft: ArtifactDraft{Kind: "file", Name: "docs/spec.md", Body: "# the spec"}},
		{ArtifactDraft: ArtifactDraft{Kind: "file", Name: "bin/tool", Body: "\x00\xff raw"}},
	}
}

// A passing outcome and the step's artifacts commit together: a reader that
// sees the pass finds every artifact, bodies exact, and the typed output's
// receipt names its author.
func TestStepOutcomeRecordsArtifactsWithThePass(t *testing.T) {
	t.Parallel()
	owner, claim := outcomeFixture(t, "agent/pi")
	if err := owner.Finish(context.Background(), claim, StepOutcome{Status: StatusPass, Evidence: json.RawMessage(`{}`), Artifacts: producedArtifacts()}); err != nil {
		t.Fatal(err)
	}
	step, err := readStep(context.Background(), owner.db, claim.ID)
	if err != nil || step.Status != StatusPass {
		t.Fatalf("step not passed: %+v %v", step, err)
	}
	artifacts, added := stepArtifactsOf(t, owner.db, claim.RunID)
	got := map[string]string{}
	for _, a := range artifacts {
		body := a.Body
		if a.Binary {
			body = string(a.Raw)
		}
		got[a.Kind+" "+a.Name] = body
	}
	want := map[string]string{"spec spec-draft": "# the spec", "file docs/spec.md": "# the spec", "file bin/tool": "\x00\xff raw"}
	if len(got) != len(want) || len(artifacts) != len(want) {
		t.Fatalf("artifacts: %q", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("artifact %s: %q want %q", k, got[k], v)
		}
	}
	if len(added) != 1 {
		t.Fatalf("receipts: %+v", added)
	}
	var receipt struct {
		Kind, Name, By string
		ID             ids.ID
	}
	if err := json.Unmarshal(added[0].Payload, &receipt); err != nil || receipt.Kind != "spec" || receipt.Name != "spec-draft" || receipt.By != "agent:accept" || receipt.ID.IsZero() {
		t.Fatalf("receipt: %s %v", added[0].Payload, err)
	}
}

// A downstream reader never observes a passing producer without its
// artifacts: when any artifact cannot be written, the outcome is not
// recorded, the step is still running at its attempt, and nothing the
// transaction wrote before the failure survives.
func TestStepOutcomeArtifactFailureRecordsNoPass(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"database", "invalid artifact", "receipt"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			owner, claim := outcomeFixture(t, "agent/pi")
			artifacts := producedArtifacts()
			switch failure {
			case "database":
				dbtest.FailNextStatement(t, owner.db, "INSERT INTO artifacts")
			case "invalid artifact":
				artifacts = append(artifacts, StepArtifact{ArtifactDraft: ArtifactDraft{Name: "no kind"}})
			case "receipt":
				// The step.finished receipt is written first; fail the
				// artifact.added receipt that follows the typed output.
				completionExec(t, owner.db, `CREATE FUNCTION veto_artifact_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='artifact.added' THEN RAISE EXCEPTION 'receipt refused'; END IF; RETURN NEW; END $$`)
				completionExec(t, owner.db, `CREATE TRIGGER veto_artifact_receipt BEFORE INSERT ON run_events FOR EACH ROW EXECUTE FUNCTION veto_artifact_receipt()`)
			}
			if err := owner.Finish(context.Background(), claim, StepOutcome{Status: StatusPass, Evidence: json.RawMessage(`{}`), Artifacts: artifacts}); err == nil {
				t.Fatal("outcome recorded without its artifacts")
			}
			step, err := readStep(context.Background(), owner.db, claim.ID)
			if err != nil || step.Status != StatusRunning || step.Attempt != claim.OwnedAttempt() {
				t.Fatalf("step settled without its artifacts: %+v %v", step, err)
			}
			if artifacts, added := stepArtifactsOf(t, owner.db, claim.RunID); len(artifacts) != 0 || len(added) != 0 {
				t.Fatalf("partial artifacts survived: %+v %+v", artifacts, added)
			}
		})
	}
}

// Replaying a settlement of the same attempt writes its artifacts once: the
// replay is refused as a lost attempt before anything is written.
func TestStepOutcomeReplayWritesArtifactsOnce(t *testing.T) {
	t.Parallel()
	owner, claim := outcomeFixture(t, "agent/pi")
	outcome := StepOutcome{Status: StatusPass, Evidence: json.RawMessage(`{}`), Artifacts: producedArtifacts()}
	if err := owner.Finish(context.Background(), claim, outcome); err != nil {
		t.Fatal(err)
	}
	if err := owner.Finish(context.Background(), claim, outcome); !errors.Is(err, ErrStepAttemptLost) {
		t.Fatalf("replayed settlement: %v", err)
	}
	artifacts, added := stepArtifactsOf(t, owner.db, claim.RunID)
	if len(artifacts) != len(outcome.Artifacts) || len(added) != 1 {
		t.Fatalf("replay duplicated artifacts: %d artifacts, %d receipts", len(artifacts), len(added))
	}
}

// An artifact is identified by its kind as well as its name: a typed report
// named result.txt and a written file result.txt are two artifacts, each kept
// with its own body, and a replayed settlement is refused before it writes
// either again or overwrites the accepted ones.
func TestStepOutcomeKeepsSameNamedArtifactsOfDifferentKindsOnce(t *testing.T) {
	t.Parallel()
	owner, claim := outcomeFixture(t, "agent/pi")
	outcome := StepOutcome{Status: StatusPass, Evidence: json.RawMessage(`{}`), Artifacts: []StepArtifact{
		{ArtifactDraft: ArtifactDraft{Kind: "report", Name: "result.txt", Body: "the report"}, By: "agent:accept"},
		{ArtifactDraft: ArtifactDraft{Kind: "file", Name: "result.txt", Body: "the file"}},
	}}
	if err := owner.Finish(context.Background(), claim, outcome); err != nil {
		t.Fatal(err)
	}
	replay := outcome
	replay.Artifacts = []StepArtifact{
		{ArtifactDraft: ArtifactDraft{Kind: "report", Name: "result.txt", Body: "a different report"}, By: "agent:accept"},
		{ArtifactDraft: ArtifactDraft{Kind: "file", Name: "result.txt", Body: "a different file"}},
	}
	if err := owner.Finish(context.Background(), claim, replay); !errors.Is(err, ErrStepAttemptLost) {
		t.Fatalf("replayed settlement: %v", err)
	}
	artifacts, added := stepArtifactsOf(t, owner.db, claim.RunID)
	got := map[string][]string{}
	for _, a := range artifacts {
		got[a.Kind+" "+a.Name] = append(got[a.Kind+" "+a.Name], a.Body)
	}
	if len(artifacts) != 2 || len(got["report result.txt"]) != 1 || got["report result.txt"][0] != "the report" ||
		len(got["file result.txt"]) != 1 || got["file result.txt"][0] != "the file" || len(added) != 1 {
		t.Fatalf("both artifacts, once each, with their own bodies: %q, %d receipts", got, len(added))
	}
}
