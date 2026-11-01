package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
)

// A completed working run is an admission fixture with its execution finished.
// Continuation transaction tests own these rows and their failure conditions.
func completedSendBackFixture(t *testing.T) (sendBackStore, *Run, ids.ID) {
	return completedSendBackBy(t, "operator")
}
func completedSendBackBy(t *testing.T, actor string) (sendBackStore, *Run, ids.ID) {
	t.Helper()
	admissions, req, def := runAdmissionFixture(t)
	req.requestedBy = actor
	def.Spec.Stages = []schema.Stage{{Name: "first", OnEnter: []schema.Step{{Name: "build", Uses: "agent/pi"}}, Steps: []schema.Step{{Name: "review", Uses: "agent/review"}, {Name: "accept", Uses: "approval"}}}}
	automatic := []string{"agent/pi", "agent/review"}
	store := runStartStore{db: admissions.records.db, stages: NewStages(nil, func(kind string) bool { return kind == "agent/pi" || kind == "agent/review" })}
	run, err := store.create(context.Background(), req, def)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.prepared(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	transitionScopeRun(t, store.db, workflowsql.TransitionRunParams{At: time.Now(), ID: run.ID, NextStatus: "done"})
	recordScopeRunSteps(t, store.db, run.ID, "pass")
	run.Status = RunDone
	var agent ids.ID
	for _, step := range storedRunSteps(t, store.db, run.ID) {
		if step.Type == "agent/pi" {
			agent = step.ID
		}
	}
	if agent.IsZero() {
		t.Fatal("no agent step")
	}
	if n, err := workflowsql.New(store.db.SQL()).InsertRunEvent(context.Background(), workflowsql.InsertRunEventParams{ID: ids.New(time.Now().UTC()), RunID: run.ID, At: time.Now().UTC(), Kind: "step.started", PayloadJson: json.RawMessage(`{"step":"build","sessionId":"original-session"}`)}); err != nil || n != 1 {
		t.Fatalf("store original session start: %d %v", n, err)
	}
	return sendBackStore{db: store.db, automatic: automatic, abandonments: NewAbandonments(store.db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{})}, run, agent
}

func TestWorkflowRetryRecoveryAndOldLandingFence(t *testing.T) {
	t.Parallel()
	for _, dispatched := range []bool{false, true} {
		t.Run(fmt.Sprint(dispatched), func(t *testing.T) {
			s, source, _ := completedSendBackFixture(t)
			plans := newDispatchPlans(s.db, s.automatic)
			old := &dispatcher{records: plans}
			// Commit while the original dispatch owner is stopped, or simulate
			// process loss before calling dispatch at all.
			old.Stop()
			result, err := s.sendBack(context.Background(), source.ChangeID, "recover these words", "alice", "", source.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			if dispatched {
				plan, err := plans.Plan(context.Background(), result.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if old.DispatchAgain(plan) {
					t.Fatal("stopped dispatcher accepted a retry")
				}
			}
			var calls []ids.ID
			runners := map[string]func(ids.ID){}
			for _, typ := range []string{"agent/pi", "agent/review"} {
				runners[typ] = func(id ids.ID) {
					calls = append(calls, id)
					recordScopeStep(t, s.db, workflowsql.RecordApprovalParams{At: time.Now(), ID: id, Status: "pass"})
				}
			}
			d := newDispatcher(s.db, dispatcherConfig{Life: context.Background(), Runners: runners, Report: func(err error) { t.Error(err) }})
			d.prepare = func(id ids.ID) error { return (runStartStore{db: s.db}).prepared(context.Background(), id) }
			d.RestartPending(nil)
			d.Wait()
			if len(calls) != 2 || calls[0] != result.StepID {
				t.Fatalf("recovered work: %v", calls)
			}
			wantRunReady(t, s.db, result.RunID, true)
			if _, err := newLandings(s.db, nil).AdvanceAccepted(context.Background(), source.ID, acceptedLandingRuntime{}); err != nil {
				t.Fatal(err)
			}
			// Even after the replacement finishes, a caller that passed an
			// earlier stale read cannot create a landing for the old work.
			transitionScopeRun(t, s.db, workflowsql.TransitionRunParams{At: time.Now(), ID: result.RunID, NextStatus: "done"})
			def := PreparedRun{Workflow: DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Role: "landing", Kind: "pipeline", Stages: []schema.Stage{{Name: "start"}}}}
			if _, err := (runStartStore{db: s.db}).create(context.Background(), StartRequest{ProjectID: source.ProjectID, ChangeID: source.ChangeID, WorkflowName: "land", acceptedRunID: source.ID, InputsJSON: "{}"}, def); err == nil || !strings.Contains(err.Error(), "sent back") {
				t.Fatalf("old acceptance was not fenced: %v", err)
			}
			wantRunCount(t, s.db, 2, nil)
		})
	}
}
