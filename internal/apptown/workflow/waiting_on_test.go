package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestWaitingOnNamesTheHoldingChild(t *testing.T) {
	t.Parallel()
	parent, child, call, decision := idstest.N(1), idstest.N(2), idstest.N(3), idstest.N(4)
	for _, state := range []StepState{StatusPending, StatusFail, StatusError} {
		kind := "approval"
		if state != StatusPending {
			kind = "agent/review"
		}
		graph := workflowActivityGraph{
			parent: {id: parent, projectID: parent, state: RunRunning, steps: []workflowActivityStep{{id: call, attempt: 1, kind: "workflow/call", state: StatusRunning, evidence: json.RawMessage(`{"childRunId":"00000000-0002-7000-8000-000000000000","workflow":"child"}`)}}},
			child:  {id: child, projectID: parent, parentID: parent, parentStepID: call, parentAttempt: 1, state: RunRunning, steps: []workflowActivityStep{{id: decision, name: "review", kind: kind, state: state}}},
		}
		a, err := graph.classify(parent, nil, map[ids.ID]bool{})
		if err != nil {
			t.Fatal(err)
		}
		run := &Run{ID: parent, Status: RunRunning}
		w := waitingOn(nil, &RunSnapshot{Run: run, activity: a})
		if w.Run != child || w.Step != decision {
			t.Fatalf("lost child identity: %+v", w)
		}
		want := map[StepState]string{StatusPending: "waiting for approval", StatusFail: "review failed", StatusError: "review could not run"}[state]
		if w.Says != want {
			t.Fatalf("%s: %+v", state, w)
		}
		if acts := nextActs(run, []*Step{{ID: call, Type: "workflow/call", Status: StatusRunning}}, a, nil, false); len(acts) > 0 {
			t.Fatalf("parent acts: %+v", acts)
		}
	}
}

func TestWaitingOnRecoveryChecksTheReadersPermission(t *testing.T) {
	t.Parallel()
	db, run, _ := completionFixture(t, "working")
	insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "review", Type: "agent/review", Status: "fail"})
	insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "accept", Type: "approval", Status: "pending"})
	for _, answer := range []string{"yes", "no", "unknown"} {
		auth := authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
			switch p.Action {
			case actionOverrideStep, actionRunStep, actionRetryAttempt, actionSendBackChange:
				if answer == "no" {
					return "", &access.Denied{Reason: "read only"}
				}
				if answer == "unknown" {
					return "", errors.New("authority unavailable")
				}
			}
			return "operator", nil
		})
		svc := newService(db, auth, Runtime{Automatic: []string{"agent/review"}})
		d, err := svc.Snapshot(t.Context(), run.ID, DetailView)
		if err != nil {
			t.Fatal(err)
		}
		if d.WaitingOn.You != answer || d.WaitingOn.Kind != "check" || d.WaitingOn.Says != "review failed" {
			t.Fatalf("%s: %+v", answer, d.WaitingOn)
		}
	}
}

func TestWaitingOnApprovalNamesDefaultsAndTheNextEligibleVoter(t *testing.T) {
	t.Parallel()
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"quorum": 2}}}}}}
	a := stepAuthorityProjectRunnerFixture(t, spec, nil, project.Spec{Slug: "game", Name: "G", DefaultApprovers: "group:reviewers"})
	if err := a.Access.SaveGroup(t.Context(), identity.GroupChange{Name: "reviewers", Members: []string{"owner", "boss"}}, testAdmittedPrincipal(t, a.Access, "boss")); err != nil {
		t.Fatal(err)
	}
	owner := stepVerifiedContext(t.Context(), access.Principal{Kind: "user", Name: "owner", Via: "session"})
	if _, err := a.Work.Approve(owner, Approval{StepID: a.StepID, Decision: VoteApprove}); err != nil {
		t.Fatal(err)
	}
	for _, person := range []string{"owner", "boss"} {
		ctx := stepVerifiedContext(t.Context(), access.Principal{Kind: "user", Name: person, Via: "session"})
		d, err := a.Work.Snapshot(ctx, a.RunID, DetailView)
		if err != nil {
			t.Fatal(err)
		}
		w := d.WaitingOn
		want, says := "no", "waiting for reviewers"
		if person == "boss" {
			want, says = "yes", "waiting for you or reviewers"
		}
		if w.You != want || w.Says != says || len(w.Who) != 1 || w.Who[0] != (Decider{Kind: "group", Name: "reviewers"}) {
			t.Fatalf("%s: %+v", person, w)
		}
	}
}

func TestHomeWorkPartitionsTheReadersCurrentDecisions(t *testing.T) {
	t.Parallel()
	db, run, _ := completionFixture(t, "working")
	insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "review", Type: "agent/review", Status: "fail"})
	insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "accept", Type: "approval", Status: "pending"})
	// Another change's approval waits for somebody else, independently of recovery permission.
	ch := admissionTestChange(t, db, run.ProjectID, "Another person's approval")
	other := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: run.ProjectID, ChangeID: ch, WorkflowName: "work", Kind: "pipeline", Status: "running", CurrentStage: "finish", DefinitionJson: json.RawMessage(`{"kind":"pipeline","stages":[{"name":"finish"}]}`)})
	insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: other.ID, Stage: "finish", Name: "accept", Type: "approval", Status: "pending"})
	for _, mayRecover := range []bool{false, true} {
		auth := projectAuthority{project: run.ProjectID, authorityFunc: func(_ context.Context, p operationPermission) (string, error) {
			if p.Action == actionApproveStep || !mayRecover && (p.Action == actionOverrideStep || p.Action == actionRunStep || p.Action == actionSendBackChange) {
				return "", &access.Denied{Reason: "another person's work"}
			}
			return "reader", nil
		}}
		svc := newService(db, auth, Runtime{Automatic: []string{"agent/review"}})
		home, err := svc.Dashboard(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if mayRecover {
			want = 1
		}
		if len(home.WaitingOnYou) != want || len(home.ActiveRuns) != 2-want {
			t.Fatalf("recovery=%v: %+v", mayRecover, home)
		}
		if mayRecover && (home.WaitingOnYou[0].ChangeID != run.ChangeID || home.WaitingOnYou[0].WaitingOn.Says != "review failed") {
			t.Fatalf("personal row: %+v", home)
		}
	}
}

// A reader who can request landing cannot restart a landing that requires land.
func TestWaitingOnRestartRequiresLandingAuthority(t *testing.T) {
	t.Parallel()
	db, run, _ := completionFixture(t, "landing")
	insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "rules", Type: "policy/current", Status: "fail"})
	auth := authorityFunc(func(_ context.Context, p operationPermission) (string, error) {
		switch p.Action {
		case actionLandUnassented, actionOverrideStep, actionSendBackChange, actionRunStep:
			return "", &access.Denied{Reason: "contributor cannot land"}
		}
		return "contributor", nil
	})
	d, err := newService(db, auth, Runtime{}).Snapshot(t.Context(), run.ID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	if d.WaitingOn.Kind != "person" || d.WaitingOn.You != "no" {
		t.Fatalf("request-land granted restart: %+v", d.WaitingOn)
	}
}

func TestTraceWithoutClassifiedActsDoesNotNeedTheReader(t *testing.T) {
	t.Parallel()
	db, run, _ := completionFixture(t, "working")
	d, err := newService(db, allowProject(run.ProjectID), Runtime{}).Snapshot(t.Context(), run.ID, TraceView)
	if err != nil {
		t.Fatal(err)
	}
	if d.WaitingOn.You != "no" {
		t.Fatalf("trace without a current act needs no reader: %+v", d.WaitingOn)
	}
}

func TestFinishedRunsWithoutOpenChangesAreClosed(t *testing.T) {
	t.Parallel()
	db, run, _ := completionFixture(t, "working")
	// A historical completion without a queued landing keeps its change open.
	if err := db.InTx(t.Context(), func(tx *database.Tx) error {
		return TransitionRunIn(t.Context(), tx, run, RunDone, run.CurrentStage, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	svc := newService(db, allowProject(run.ProjectID), Runtime{})
	transitionScopeChange(t, db, workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, RunID: run.ID})
	changes, err := svc.Changes(t.Context(), run.ProjectID, ChangeList{})
	if err != nil || len(changes) != 1 || changes[0].WaitingOn.Kind != "chat" {
		t.Fatalf("open change list: %+v %v", changes, err)
	}
	rows, err := svc.Runs(t.Context(), RunQuery{})
	if err != nil || len(rows) != 1 || rows[0].WaitingOn.Kind != "chat" {
		t.Fatalf("open change: %+v %v", rows, err)
	}
	transitionScopeChange(t, db, workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, State: "shipped"})
	rows, err = svc.Runs(t.Context(), RunQuery{})
	if err != nil || rows[0].WaitingOn.Kind != "closed" {
		t.Fatalf("closed change: %+v %v", rows, err)
	}
	system := insertStoredRun(t, db, workflowsql.InsertRunParams{ProjectID: run.ProjectID, WorkflowName: "system", Kind: "pipeline", Status: "running", CurrentStage: "finish", DefinitionJson: json.RawMessage(run.DefinitionJSON)})
	if err := (advanceStore{db: db, completion: completionRecorderFunc(noCompletionEffects)}).complete(t.Context(), system); err != nil {
		t.Fatal(err)
	}
	rows, err = svc.Runs(t.Context(), RunQuery{})
	if err != nil || len(rows) != 2 {
		t.Fatalf("system run list: %+v %v", rows, err)
	}
	found := false
	for _, row := range rows {
		if row.ID == system.ID {
			found = true
			if row.WaitingOn.Kind != "closed" || row.WaitingOn.Says == "chat open" {
				t.Fatalf("system run: %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("system run missing from list")
	}
}

func TestBoardKeepsUnreadableProjectionAsUnknown(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"running", "abandoned"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			db, run, _ := completionFixture(t, "working")
			if state == "abandoned" {
				owner := NewAbandonments(db, abandonRecorderFunc(noAbandonEffects), &scm.RepositoryLocks{})
				if _, err := owner.Abandon(t.Context(), Abandonment{RunID: run.ID, Who: "operator"}); err != nil {
					t.Fatal(err)
				}
			}
			transitionScopeChange(t, db, workflowsql.TransitionChangeParams{At: time.Now(), ID: run.ChangeID, RunID: run.ID})
			ch := admissionTestChange(t, db, run.ProjectID, "Readable")
			good := insertStoredRun(t, db, workflowsql.InsertRunParams{ProjectID: run.ProjectID, ChangeID: ch, WorkflowName: "good", Kind: "pipeline", Status: "running", CurrentStage: "finish", DefinitionJson: json.RawMessage(`{"kind":"pipeline","stages":[{"name":"finish"}]}`)})
			transitionScopeChange(t, db, workflowsql.TransitionChangeParams{At: time.Now(), ID: ch, RunID: good.ID})
			// sql-guarantee: corrupt recorded definitions cannot be admitted normally.
			completionExec(t, db, `UPDATE runs SET definition_json='{"kind":"pipeline","stages":"unreadable"}' WHERE id=$1`, run.ID)
			svc := newService(db, allowProject(run.ProjectID), Runtime{})
			rows, err := svc.Changes(t.Context(), run.ProjectID, ChangeList{})
			if err != nil || len(rows) != 2 {
				t.Fatalf("one row broke the board: %+v %v", rows, err)
			}
			for _, row := range rows {
				if row.ID == run.ChangeID && (row.WaitingOn.Says != "unknown" || row.WaitingOn.Tone != "unknown") {
					t.Fatalf("bad row: %+v", row.WaitingOn)
				}
				if row.ID == ch && row.WaitingOn.Tone == "unknown" {
					t.Fatalf("good row: %+v", row.WaitingOn)
				}
			}
			home, err := svc.Dashboard(t.Context())
			wantActive := 2
			if state == "abandoned" {
				wantActive = 1
			}
			if err != nil || len(home.ActiveRuns)+len(home.WaitingOnYou) != wantActive {
				t.Fatalf("one row broke Home: %+v %v", home, err)
			}
			for _, row := range home.ActiveRuns {
				if row.ID == run.ID && row.WaitingOn.Tone != "unknown" {
					t.Fatalf("Home bad row: %+v", row)
				}
			}
			// Ended changes use the same failure boundary on project overview rows.
			if state != "abandoned" {
				return
			}
			open, err := svc.OpenChangesWithoutRuns(t.Context(), run.ProjectID, 10)
			if err != nil || len(open) != 1 || open[0].WaitingOn.Tone != "unknown" {
				t.Fatalf("overview bad row: %+v %v", open, err)
			}
		})
	}
}

func TestHomeLimitsOtherVisibleRows(t *testing.T) {
	t.Parallel()
	db, run, _ := completionFixture(t, "working")
	for range 21 {
		insertStoredRun(t, db, workflowsql.InsertRunParams{ProjectID: run.ProjectID, WorkflowName: "system", Kind: "pipeline", Status: "running", CurrentStage: "finish", DefinitionJson: json.RawMessage(`{"kind":"pipeline","stages":[{"name":"finish"}]}`)})
	}
	other := workflowTestProject(t, db, "hidden", "Hidden", "")
	for range 21 {
		insertStoredRun(t, db, workflowsql.InsertRunParams{ProjectID: other, WorkflowName: "hidden", Kind: "pipeline", Status: "running", CurrentStage: "finish", DefinitionJson: json.RawMessage(`{"kind":"pipeline","stages":[{"name":"finish"}]}`)})
	}
	home, err := newService(db, projectAuthority{project: run.ProjectID, authorityFunc: func(_ context.Context, p operationPermission) (string, error) {
		if p.Action == actionAdvanceRun || p.Action == actionSendBackChange {
			return "", &access.Denied{Reason: "another person's work"}
		}
		return "reader", nil
	}}, Runtime{}).Dashboard(t.Context())
	if err != nil || len(home.ActiveRuns) != 20 || len(home.WaitingOnYou) != 0 {
		t.Fatalf("limit after visibility: %+v %v", home, err)
	}
}

func TestHomeNeverLimitsWorkWaitingOnTheReader(t *testing.T) {
	t.Parallel()
	db, run, _ := completionFixture(t, "working")
	insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "review", Type: "agent/review", Status: "fail"})
	for i := range 42 {
		other := insertStoredRun(t, db, workflowsql.InsertRunParams{ProjectID: run.ProjectID, WorkflowName: "system", Kind: "pipeline", Status: "running", CurrentStage: "finish", CreatedAt: time.Now().Add(time.Duration(i+1) * time.Second), DefinitionJson: json.RawMessage(`{"kind":"pipeline","stages":[{"name":"finish"}]}`)})
		if i < 21 {
			insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: other.ID, Stage: "finish", Name: "work", Type: "agent/work", Status: "running"})
		} else {
			insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: other.ID, Stage: "finish", Name: "review", Type: "agent/review", Status: "fail"})
		}
	}
	home, err := newService(db, allowProject(run.ProjectID), Runtime{Automatic: []string{"agent/review"}}).Dashboard(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(home.WaitingOnYou) != 22 || len(home.ActiveRuns) != 20 {
		t.Fatalf("personal work must be unlimited, other work limited: waiting=%d other=%d", len(home.WaitingOnYou), len(home.ActiveRuns))
	}
	if home.WaitingOnYou[len(home.WaitingOnYou)-1].ID != run.ID {
		t.Fatal("oldest personal change disappeared")
	}
}

func TestWaitingOnWithoutAnActDoesNotNeedTheReader(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		change   *Change
		snapshot *RunSnapshot
		kind     string
	}{
		{name: "work", snapshot: &RunSnapshot{Run: &Run{Status: RunRunning}, activity: Activity{State: ActivityWorking}}, kind: "work"},
		{name: "chat", kind: "chat"},
		{name: "queued", snapshot: &RunSnapshot{Run: &Run{Status: RunRunning}, LandingQueued: true}, kind: "queued"},
		{name: "closed change", change: &Change{State: ChangeShipped}, kind: "closed"},
		{name: "closed run", snapshot: &RunSnapshot{Run: &Run{Status: RunDone}}, kind: "closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := (*Service)(nil).readerWaitingOn(t.Context(), tc.change, tc.snapshot)
			if w.Kind != tc.kind || w.You != "no" {
				t.Fatalf("nothing to act on: %+v", w)
			}
		})
	}
}
