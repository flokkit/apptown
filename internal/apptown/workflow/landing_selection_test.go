package workflow

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
)

// Recheck fixtures need a genuinely admitted and prepared landing, while their
// individual checks can retain deliberate historical evidence shapes.
func admitLandingSpec(t *testing.T, spec schema.Spec) (queries, *Run) {
	t.Helper()
	db, change := changeEditFixture(t, ChangeDraft{Title: "Landing recheck"})
	spec.Kind, spec.Role = "pipeline", "landing"
	admissions := newRunAdmissions(db, NewStages(nil, func(string) bool { return true }), nil)
	run, err := admissions.records.create(context.Background(), StartRequest{ProjectID: change.ProjectID, ChangeID: change.ID, WorkflowName: "land", InputsJSON: "{}", requestedBy: "operator"}, PreparedRun{Workflow: DefinitionRef{Name: "land", Version: 1}, Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	if err := admissions.records.prepared(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	return queries{db: db}, run
}

func landingRecheckFixture(t *testing.T) (landingRechecks, *Run, map[string]ids.ID) {
	t.Helper()
	spec := schema.Spec{Role: "landing", Stages: []schema.Stage{{Name: "finish", OnEnter: []schema.Step{
		{Name: "image", Uses: "scan"}, {Name: "deploy", Uses: "deploy"}, {Name: "optional", Uses: "scan"},
	}}}}
	q, run := admitLandingSpec(t, spec)
	w := q.db
	owner := newLandingRechecks(w, []string{"scan", "deploy", "tests"}, nil)
	stepIDs := map[string]ids.ID{}
	for _, step := range []struct{ name, typ, status, evidence, override, stage string }{
		{"image", "scan", "fail", `{"reason":"provider overloaded"}`, "", "finish"},
		{"deploy", "deploy", "skipped", `{"reason":"not run: “image” did not pass"}`, "", "finish"},
		{"optional", "scan", "skipped", `{"reason":"no image to scan"}`, "", "finish"},
		{"tests", "tests", "error", `{"reason":"provider overloaded"}`, "", "finish"},
		{"passed", "scan", "pass", `{"reason":"clean"}`, "", "finish"},
		{"override", "scan", "fail", `{"reason":"human decision"}`, "alice", "finish"},
		{"approve", "approval", "fail", `{"comment":"keep my decision"}`, "", "finish"},
		{"require", "artifact/require", "fail", `{}`, "", "finish"},
		{"old", "scan", "fail", `{"reason":"old stage"}`, "", "earlier"},
	} {
		var id ids.ID
		if step.name == "image" || step.name == "deploy" || step.name == "optional" {
			id = admittedStepID(t, q, run, step.name)
		} else {
			// Selection needs extra historical rows beyond the declared entry checks.
			id = insertStoredStep(t, w, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: step.stage, Name: step.name, Type: step.typ, Status: StatusPending.String(), ConfigJson: json.RawMessage(`{}`)})
		}
		wq := workflowsql.New(w.SQL())
		if step.override != "" {
			// The historical row retains a failing result beside an override. Use the
			// existing call-state writer to retain that result and attempt after override.
			overrideScopeStep(t, w, workflowsql.OverrideStepParams{At: time.Now(), ID: id, OverrideBy: step.override})
			if n, err := wq.StartParentCall(context.Background(), workflowsql.StartParentCallParams{ID: id, OldStatus: "pass", Status: step.status, EvidenceJson: json.RawMessage(step.evidence)}); err != nil || n != 1 {
				t.Fatalf("store overridden historical result: %d %v", n, err)
			}
		} else {
			if n, err := wq.StartAttempt(context.Background(), workflowsql.StartAttemptParams{At: time.Now(), ID: id}); err != nil || n != 1 {
				t.Fatalf("start historical %s: %d %v", step.name, n, err)
			}
			if n, err := wq.FinishAttempt(context.Background(), workflowsql.FinishAttemptParams{At: time.Now(), Status: step.status, EvidenceJson: json.RawMessage(step.evidence), ID: id, Attempt: 1}); err != nil || n != 1 {
				t.Fatalf("finish historical %s: %d %v", step.name, n, err)
			}
		}
		// sql-guarantee: Recheck must distinguish old execution timestamps from cleared timestamps while retaining ignored historical decisions; StartAttempt/FinishAttempt use database now() and cannot backdate either field.
		completionExec(t, w, `UPDATE steps SET started_at=now()-interval '2 minutes',finished_at=now()-interval '1 minute' WHERE id=$1`, id)
		stepIDs[step.name] = id
	}
	return owner, run, stepIDs
}
func TestLandingRecheckAtomicBatchPreservesDecisions(t *testing.T) {
	t.Parallel()
	owner, run, _ := landingRecheckFixture(t)
	result, err := owner.recheck(context.Background(), LandingRecheck{who: "alice", ChangeID: run.ChangeID})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Names, []string{"image", "deploy", "tests"}) {
		t.Fatalf("rechecked: %v", result.Names)
	}
	wantStepCount(t, owner.db, 3, func(s *Step) bool { return untouchedIn(StatusPending)(s) && !s.StartedAt.Valid && s.Attempt == 1 })
	wantEventCount(t, owner.db, 3, func(e Event) bool {
		return kindWith("step.recheck", "by", "alice", "attempt", "1")(e) && hasKey(e.Payload, "previousEvidence")
	})
	wantStepCount(t, owner.db, 1, func(s *Step) bool {
		return s.Name == "approve" && s.Status == StatusFail && hasFields(s.Evidence, "comment", "keep my decision")
	})
	wantStepCount(t, owner.db, 1, func(s *Step) bool { return s.Name == "override" && s.Status == StatusFail && s.OverrideBy == "alice" })
	wantStepCount(t, owner.db, 4, func(s *Step) bool {
		return s.Status != StatusPending && (s.Name == "old" || s.Name == "require" || s.Name == "passed" || s.Name == "optional")
	})
	if err := (advanceStore{db: owner.db, completion: completionRecorderFunc(noCompletionEffects)}).complete(context.Background(), run); err != ErrRunMoved {
		t.Fatalf("old advancement decision accepted: %v", err)
	}
}
func TestLandingRecheckAbandonmentRace(t *testing.T) {
	t.Parallel()
	owner, run, stepIDs := landingRecheckFixture(t)
	race(2, func(i int) int {
		if i == 0 {
			_, _ = owner.recheck(context.Background(), LandingRecheck{who: "alice", ChangeID: run.ChangeID})
		} else {
			if _, err := NewAbandonments(owner.db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{}).Abandon(context.Background(), Abandonment{ChangeID: run.ChangeID, Who: "alice", Reason: "stop"}); err != nil {
				t.Error(err)
			}
		}
		return 0
	})
	wantRunCount(t, owner.db, 1, func(r *Run) bool { return r.Status == RunAbandoned })
	if _, err := owner.recheck(context.Background(), LandingRecheck{who: "alice", ChangeID: run.ChangeID}); err == nil {
		t.Fatal("abandoned work was rechecked")
	}
	// A reset that won the race still cannot start after abandonment.
	step, err := readStep(context.Background(), owner.db, stepIDs["image"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newStepAttempts(owner.db, func(string) bool { return true }).start(context.Background(), step, step.Type, ids.Nil); err == nil {
		t.Fatal("abandoned work was admitted")
	}
}

func TestLandingRecheckRejectsChangedOrBusyWork(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"run abandoned", "change abandoned", "change shipped", "step running", "stage"} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			owner, run, stepIDs := landingRecheckFixture(t)
			switch mutation {
			case "run abandoned":
				transitionScopeRun(t, owner.db, workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, NextStatus: "abandoned"})
			case "change abandoned":
				transitionScopeChange(t, owner.db, workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, State: "abandoned"})
			case "change shipped":
				transitionScopeChange(t, owner.db, workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, State: "shipped"})
			case "step running":
				recordScopeStep(t, owner.db, workflowsql.RecordApprovalParams{At: time.Now(), ID: stepIDs["passed"], Status: "running"})
			case "stage":
				transitionScopeRun(t, owner.db, workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, NextStage: "missing"})
			}
			if _, err := owner.recheck(context.Background(), LandingRecheck{ChangeID: run.ChangeID, who: "alice"}); err == nil {
				t.Fatal("changed work accepted")
			}
			wantEventCount(t, owner.db, 4, anyEvent)
			wantStepCount(t, owner.db, 0, func(s *Step) bool { return s.Status == StatusPending })
		})
	}
}
