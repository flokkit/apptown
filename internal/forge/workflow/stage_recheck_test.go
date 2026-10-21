package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/ids"
)

// Run 422: a checks stage opened before stages recorded their expected
// scanners. Its steps all passed, yet its gate can never pass as recorded.
// It must read as blocked with the reason, not "ready to advance"; running
// the stage's checks again must record the expected scanners, run every
// check and approval again, and let the run pass once they do.
func TestAStageOpenedBeforeItsRulesSaysSoAndCanRunItsChecksAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{
		{Name: "gates", Steps: []schema.Step{{Name: "scan-source", Uses: "scan", With: map[string]any{"on": "source"}}, {Name: "approve-landing", Uses: "approval"}}},
		{Name: "ship"},
	}}
	store, run := settlementFixture(t, spec)
	insertStoredStep(t, store, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "gates", Name: "scan-source:osv", Type: "scan", Status: StatusPass.String()})
	insertStoredStep(t, store, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "gates", Name: "approve-landing", Type: "approval", Status: StatusPass.String()})
	// The receipt a stage wrote before stages recorded their checks: none.
	tx, err := store.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := recordReceiptIn(ctx, tx, run.ID, "stage.entered", map[string]string{"stage": "gates"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	blocked := func(where string, a Activity, err error) {
		t.Helper()
		if err != nil || a.State != ActivityBlocked || !strings.Contains(a.Detail, "opened before AppTown recorded which scanners it expects") || !strings.Contains(a.Detail, "Run its checks again.") {
			t.Fatalf("%s: %+v %v", where, a, err)
		}
	}
	// The list read judges from its own columns; the detail view from the
	// steps it read.
	graph, err := readWorkflowActivity(ctx, store, []ids.ID{run.ID}, Placement{})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := graph.classify(run.ID, nil, map[ids.ID]bool{})
	blocked("list", graph.judgedByGate(run.ID, listed), err)
	snapshot, err := readWorkflowSnapshot(ctx, store, run.ID, DetailView, nil, Placement{}, run.ProjectID, nil)
	if err != nil {
		t.Fatal(err)
	}
	blocked("detail", Activity{State: snapshot.State, Detail: snapshot.StateDetail}, nil)

	allow := actorAuthorityFunc(func(context.Context, operationPermission) (commandActor, error) {
		return commandActor{Receipt: "bls"}, nil
	})
	s := newServiceWithAuthority(store, Dependencies{Scanners: recordedScanners{{Name: "osv", Target: "source"}, {Name: "gitleaks", Target: "source"}}}, allow)
	s.dispatcher.Stop()
	out, err := s.RecheckStage(ctx, run.ID)
	if err != nil || out.Stage != "gates" || len(out.Missing) != 1 {
		t.Fatalf("recheck: %+v %v", out, err)
	}
	steps, err := readStageSteps(ctx, store, run.ID, "gates")
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]StepState{}
	for _, st := range steps {
		statuses[st.Name] = st.Status
	}
	for _, name := range []string{"scan-source:osv", "scan-source:gitleaks", "approve-landing"} {
		if statuses[name] != StatusPending {
			t.Fatalf("%s did not start again: %v", name, statuses)
		}
	}
	checks, err := readStageChecks(ctx, store, run.ID, "gates")
	if err != nil || len(checks) != 3 {
		t.Fatalf("expected checks were not recorded: %+v %v", checks, err)
	}
	// Once there is nothing missing, a stage's checks are retried one by one.
	if _, err := s.RecheckStage(ctx, run.ID); !errors.Is(err, errNothingToRecheck) {
		t.Fatalf("second recheck: %v", err)
	}
	// The earlier results stay in the record.
	events, err := workflowsql.New(store.SQL()).ReadRunEvents(ctx, workflowsql.ReadRunEventsParams{RunID: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, e := range events {
		kinds[e.Kind]++
		if e.Kind == "stage.rechecked" {
			var r stageRecheckReceipt
			if json.Unmarshal(e.PayloadJson, &r) != nil || r.By != "bls" {
				t.Fatalf("recheck receipt: %s", e.PayloadJson)
			}
		}
	}
	if kinds["stage.rechecked"] != 1 || kinds["step.invalidated"] != 2 {
		t.Fatalf("receipts: %v", kinds)
	}
	recordScopeRunSteps(t, store, run.ID, "pass")
	current, err := ReadRunIn(ctx, store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	steps, err = readStageSteps(ctx, store, run.ID, "gates")
	if err != nil {
		t.Fatal(err)
	}
	if verdict, err := current.Advance(steps, checks, Placement{}); err != nil || !verdict.Passed() {
		t.Fatalf("after its checks ran again: %+v %v", verdict, err)
	}
}

// A restart re-asks the gate; the same refusal by the system is recorded once,
// while every person's attempt is recorded.
func TestTheSystemRecordsARepeatedRefusalOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	spec := schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "gates", Steps: []schema.Step{{Name: "approve", Uses: "approval"}}}}}
	store, run := settlementFixture(t, spec)
	calls := 0
	a := policyEngine(store, &calls)
	refusal := advanceBlockedReceipt{Stage: "gates", Missing: []string{"scan-source (x)"}, By: "system"}
	count := func() int {
		t.Helper()
		events, err := workflowsql.New(store.SQL()).ReadRunEvents(ctx, workflowsql.ReadRunEventsParams{RunID: run.ID})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range events {
			if e.Kind == "advance.blocked" {
				n++
			}
		}
		return n
	}
	for range 3 {
		a.engine.note(ctx, run.ID, "advance.blocked", refusal)
	}
	if count() != 1 {
		t.Fatalf("repeated system refusals: %d", count())
	}
	person := refusal
	person.By = "admin"
	a.engine.note(ctx, run.ID, "advance.blocked", person)
	a.engine.note(ctx, run.ID, "advance.blocked", person)
	if count() != 3 {
		t.Fatalf("a person's attempts: %d", count())
	}
	if refused, err := lastRefusedBySystem(ctx, store, run.ID); err != nil || refused {
		t.Fatalf("after a person's attempt: %v %v", refused, err)
	}
}

// An approval short of its quorum keeps the votes cast so far; running the
// stage's checks again must ask it afresh, with those votes kept only in the
// record.
func TestRecheckStageAsksAnApprovalAgainEvenWithPartialVotes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	spec := schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "gates", Steps: []schema.Step{{Name: "scan-source", Uses: "scan", With: map[string]any{"on": "source"}}, {Name: "approve", Uses: "approval", With: map[string]any{"quorum": 2}}}}, {Name: "ship"}}}
	db, run := settlementFixture(t, spec)
	insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "gates", Name: "scan-source:osv", Type: "scan", Status: StatusPass.String()})
	insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "gates", Name: "approve", Type: "approval", ConfigJson: json.RawMessage(`{"quorum":2}`), Status: StatusPending.String()})
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := recordReceiptIn(ctx, tx, run.ID, "stage.entered", map[string]string{"stage": "gates"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	auth := actorAuthorityFunc(func(context.Context, operationPermission) (commandActor, error) {
		return commandActor{Receipt: "alice"}, nil
	})
	s := newServiceWithAuthority(db, Dependencies{Scanners: recordedScanners{{Name: "osv", Target: "source"}}}, auth)
	s.dispatcher.Stop()
	steps, err := readStageSteps(ctx, db, run.ID, "gates")
	if err != nil {
		t.Fatal(err)
	}
	var approval *Step
	for _, step := range steps {
		if step.Name == "approve" {
			approval = step
		}
	}
	if _, err := s.Approve(ctx, Approval{StepID: approval.ID, Decision: VoteApprove, Comment: "first look"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecheckStage(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := ReadStepIn(ctx, db, approval.ID)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := ReadApprovalEvidence(fresh.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.Decisions) > 0 {
		t.Fatalf("recheck kept old votes while rerunning checks: status=%s evidence=%s", fresh.Status, fresh.Evidence)
	}
}
