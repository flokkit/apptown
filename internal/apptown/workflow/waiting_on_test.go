package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
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
	"github.com/flokkit/apptown/stock"
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
			child:  {id: child, projectID: parent, parentID: parent, parentStepID: call, parentAttempt: 1, state: RunRunning, steps: []workflowActivityStep{{id: decision, attempt: 2, name: "review", kind: kind, state: state}}},
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

		if state != StatusPending {
			raw, err := json.Marshal(w)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct{ Attempt int64 }
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.Attempt != 2 {
				t.Fatalf("lost held attempt: %s", raw)
			}
		}
		want := map[StepState]string{StatusPending: "waiting for approval", StatusFail: "Review failed", StatusError: "Review could not run"}[state]
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
		if d.WaitingOn.You != answer || d.WaitingOn.Kind != "check" || d.WaitingOn.Says != "Review failed" {
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
		want, says := "no", "waiting for approval"
		if person == "boss" {
			want, says = "yes", "waiting for approval"
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
		if mayRecover && (home.WaitingOnYou[0].ChangeID != run.ChangeID || home.WaitingOnYou[0].WaitingOn.Says != "Review failed") {
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
	// A change open in its chat is in progress: the busy tone, not neutral.
	if err != nil || len(changes) != 1 || changes[0].WaitingOn.Kind != "chat" || changes[0].WaitingOn.Says != "building" || changes[0].WaitingOn.Tone != "busy" {
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
			if row.WaitingOn.Kind != "closed" || row.WaitingOn.Says == "building" {
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
			runs, err := svc.Runs(t.Context(), RunQuery{ProjectID: run.ProjectID})
			if err != nil || len(runs) != 2 {
				t.Fatalf("one row broke the run list: %+v %v", runs, err)
			}
			for _, row := range runs {
				if row.ID == run.ID && (row.WaitingOn.Says != "unknown" || row.WaitingOn.Tone != "unknown") {
					t.Fatalf("run list bad row: %+v", row.WaitingOn)
				}
				if row.ID == good.ID && row.WaitingOn.Tone == "unknown" {
					t.Fatalf("run list good row: %+v", row.WaitingOn)
				}
			}
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

func TestWaitingOnApprovalDisplaysTheRoleTitleAndRetainsItsID(t *testing.T) {
	t.Parallel()
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}
	a := stepAuthorityProjectRunnerFixture(t, spec, nil, project.Spec{Slug: "game", Name: "G", DefaultApprovers: "role:" + access.RoleProjectApprover})
	if _, err := a.Access.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "owner", Role: access.RoleProjectApprover, Scope: access.ScopeProject, ScopeID: a.ProjectID.String(), By: "boss"}, ""); err != nil {
		t.Fatal(err)
	}
	ctx := stepVerifiedContext(t.Context(), access.Principal{Kind: "user", Name: "owner", Via: "session"})
	d, err := a.Work.Snapshot(ctx, a.RunID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	w := d.WaitingOn
	role := access.RoleByName(access.RoleProjectApprover)
	raw, err := json.Marshal(w.Who)
	if err != nil {
		t.Fatal(err)
	}
	var who []struct{ Kind, Name, DisplayName string }
	if err := json.Unmarshal(raw, &who); err != nil {
		t.Fatal(err)
	}
	if len(who) != 1 || who[0].Kind != "role" || who[0].Name != access.RoleProjectApprover || who[0].DisplayName != role.Title {
		t.Fatalf("deciders: %s", raw)
	}
	want := "waiting for approval"
	if w.You != "yes" || w.Says != want {
		t.Fatalf("says=%q want %q", w.Says, want)
	}
}

// Summaries stay short while every effective decider remains available to clients.
func TestWaitingOnApprovalSummarizesCompleteDeciders(t *testing.T) {
	t.Parallel()
	person := Decider{Kind: "person", Name: "Alex"}
	role := Decider{Kind: "role", Name: "project-approver", DisplayName: "Project approver"}
	group := Decider{Kind: "group", Name: "reviewers"}
	for _, tc := range []struct {
		name, you string
		who       []Decider
		want      string
	}{
		{"sole person", "yes", []Decider{person}, "waiting for you"},
		{"several people", "yes", []Decider{person, {Kind: "person", Name: "Sam"}}, "waiting for approval"},
		{"role", "yes", []Decider{role}, "waiting for approval"},
		{"group", "yes", []Decider{group}, "waiting for approval"},
		{"roles", "yes", []Decider{role, {Kind: "role", Name: "project-owner", DisplayName: "Project owner"}}, "waiting for approval"},
		{"other person", "no", []Decider{person}, "waiting for approval"},
		{"other role", "no", []Decider{role}, "waiting for approval"},
		{"other group", "no", []Decider{group}, "waiting for approval"},
		{"other people", "no", []Decider{person, role, group}, "waiting for approval"},
		{"other roles", "no", []Decider{role, person}, "waiting for approval"},
		{"unknown reader", "unknown", []Decider{role, person}, "waiting for approval"},
		{"unspecified", "no", nil, "waiting for approval"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := sayWaitingOn(WaitingOn{Kind: "approval", You: tc.you, Who: tc.who}, Activity{}, nil)
			if w.Says != tc.want || !reflect.DeepEqual(w.Who, tc.who) {
				t.Fatalf("waiting = %+v, want %q and complete deciders", w, tc.want)
			}
		})
	}
}

func TestWaitingOnFailedChecksUsePlainTitles(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, kind, title string }{
		{"review-change", "agent/review", "Review"},
		{"test-change", "tests", "Tests"},
		{"style", "script", "Style"},
		{"check_code-style", "script", "Check code style"},
		{"scan-osv", "scan", "Dependency scan"},
	} {
		for _, state := range []StepState{StatusFail, StatusError} {
			t.Run(tc.name+state.String(), func(t *testing.T) {
				step := workflowActivityStep{name: tc.name, kind: tc.kind, state: state}
				w := waitingOn(nil, &RunSnapshot{Run: &Run{Status: RunRunning}, activity: Activity{State: ActivityBlocked, holdingStep: &step, failedChecks: 1}})
				suffix := " failed"
				if state == StatusError {
					suffix = " could not run"
				}
				if w.Says != tc.title+suffix {
					t.Fatalf("says=%q, want %q", w.Says, tc.title+suffix)
				}
			})
		}
	}
}

// Every change label is short and comes from the same classified facts.
func TestWaitingOnChangeLabels(t *testing.T) {
	reason := "The helper stopped.\n" + strings.Repeat("A detailed blocker report.\n", 30)
	heldAgent := &workflowActivityStep{kind: "agent/pi", name: "build", state: StatusNeedsHuman}
	heldScript := &workflowActivityStep{kind: "script", name: "publish", state: StatusNeedsHuman}
	raw, err := stock.FS.ReadFile("land.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := schema.ParseWorkflowStored(raw)
	if err != nil {
		t.Fatal(err)
	}
	landing := &doc.Spec
	for _, tc := range []struct {
		name   string
		change Change
		run    *RunSnapshot
		want   string
	}{
		{name: "chat", want: "building"},
		{name: "ended run", run: &RunSnapshot{Run: &Run{Status: RunDone}}, want: "building"},
		{name: "agent", run: &RunSnapshot{Run: &Run{Status: RunRunning}, activity: Activity{State: activityAgentWorking}}, want: "building · helper working"},
		{name: "slot", run: &RunSnapshot{Run: &Run{Status: RunRunning}, activity: Activity{State: activityQueued}}, want: "building · waiting for a free slot"},
		{name: "working check", run: &RunSnapshot{Run: &Run{Status: RunRunning}, activity: Activity{State: ActivityWorking}}, want: "checking"},
		{name: "landing gates before any approval row", run: &RunSnapshot{Run: &Run{Status: RunRunning, CurrentStage: "gates"}, Definition: landing}, want: "checking"},
		{name: "landing penultimate stage", run: &RunSnapshot{Run: &Run{Status: RunRunning, CurrentStage: "approval"}, Definition: landing}, want: "checking"},
		{name: "landing ship after approval", run: &RunSnapshot{Run: &Run{Status: RunRunning, CurrentStage: "ship"}, Definition: landing}, want: "going live"},
		{name: "working final stage", run: &RunSnapshot{Run: &Run{Status: RunRunning, CurrentStage: "ship"}, Definition: &schema.Spec{Role: "working", Stages: landing.Stages}}, want: "checking"},
		{name: "queued landing", run: &RunSnapshot{Run: &Run{Status: RunDone}, LandingQueued: true}, want: "going live · queued"},
		{name: "held helper", run: &RunSnapshot{Run: &Run{Status: RunRunning}, activity: Activity{Held: true, holdingStep: heldAgent, Detail: reason}}, want: "build stopped"},
		{name: "held script", run: &RunSnapshot{Run: &Run{Status: RunRunning}, activity: Activity{Held: true, holdingStep: heldScript, Detail: reason}}, want: "publish needs a person"},
		{name: "held approval", run: &RunSnapshot{Run: &Run{Status: RunRunning}, activity: Activity{Held: true, holdingStep: &workflowActivityStep{kind: "approval", state: StatusNeedsHuman}}}, want: "approval needs a person"},
		{name: "held rules", run: &RunSnapshot{Run: &Run{Status: RunRunning}, activity: Activity{Held: true, holdingStep: &workflowActivityStep{kind: "policy/current", state: StatusFail}}}, want: "rules still current needs a person"},
		{name: "unmerged work", run: &RunSnapshot{Run: &Run{Status: RunRunning}, activity: Activity{Held: true, holdingStep: &workflowActivityStep{kind: "scm/merged", state: StatusNeedsHuman}}}, want: "merged needs a person"},
		{name: "failed check", run: &RunSnapshot{Run: &Run{Status: RunRunning}, activity: Activity{Held: true, holdingStep: &workflowActivityStep{kind: "tests", state: StatusFail}, failedChecks: 1}}, want: "Tests failed"},
		{name: "check could not run", run: &RunSnapshot{Run: &Run{Status: RunRunning}, activity: Activity{Held: true, holdingStep: &workflowActivityStep{kind: "tests", state: StatusError}}}, want: "Tests could not run"},
		{name: "multiple checks", run: &RunSnapshot{Run: &Run{Status: RunRunning}, activity: Activity{Held: true, holdingStep: &workflowActivityStep{kind: "tests", state: StatusFail}, failedChecks: 3}}, want: "3 checks failed"},
		{name: "approval", run: &RunSnapshot{Run: &Run{Status: RunRunning}, activity: Activity{Approval: idstest.N(10)}}, want: "waiting for approval"},
		{name: "ready", run: &RunSnapshot{Run: &Run{Status: RunRunning}, activity: Activity{State: ActivityReady}}, want: "ready to continue"},
		{name: "landed", change: Change{State: ChangeShipped}, want: "landed"},
		{name: "recorded", change: Change{State: ChangeShipped, ClosedAs: closedRecord}, want: "finished"},
		{name: "abandoned", change: Change{State: ChangeAbandoned}, want: "abandoned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.change.State == 0 {
				tc.change.State = ChangeInProgress
			}
			got := waitingOn(&tc.change, tc.run)
			if got.Says != tc.want || strings.Contains(got.Says, "\n") {
				t.Fatalf("label = %+v, want %q", got, tc.want)
			}
			if got.Kind == "work" || got.Kind == "chat" || got.Kind == "queued" {
				if got.Tone != "busy" {
					t.Fatalf("tone = %q", got.Tone)
				}
			}
		})
	}
	w := sayWaitingOn(WaitingOn{Kind: "approval", You: "yes", Who: []Decider{{Kind: "person"}}}, Activity{}, nil)
	if w.Says != "waiting for you" {
		t.Fatalf("personal approval = %+v", w)
	}
}

// A slot wait belongs to the queued attempt, including inside a called workflow.
func TestSlotWaitIdentifiesItsStepWithoutOfferingActs(t *testing.T) {
	t.Parallel()
	parent, child, call, queued, later := idstest.N(1), idstest.N(2), idstest.N(3), idstest.N(4), idstest.N(5)
	for _, called := range []bool{false, true} {
		steps := []workflowActivityStep{{id: queued, attempt: 2, name: "review", kind: "agent/review", state: StatusPending}, {id: later, name: "later", kind: "agent/pi", state: StatusPending}}
		graph := workflowActivityGraph{parent: {id: parent, projectID: parent, state: RunRunning, steps: steps}}
		holding := parent
		if called {
			holding = child
			graph[parent].steps = []workflowActivityStep{{id: call, attempt: 1, kind: "workflow/call", state: StatusRunning, evidence: json.RawMessage(`{"childRunId":"00000000-0002-7000-8000-000000000000","workflow":"child"}`)}}
			graph[child] = &workflowActivityRun{id: child, projectID: parent, parentID: parent, parentStepID: call, parentAttempt: 1, state: RunRunning, steps: steps}
		}
		a, err := graph.classify(parent, func(id ids.ID) (string, int, bool) { return "local", 1, id == queued }, map[ids.ID]bool{})
		if err != nil {
			t.Fatal(err)
		}
		run := &Run{ID: parent, Status: RunRunning}
		w := waitingOn(nil, &RunSnapshot{Run: run, activity: a})
		raw, err := json.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Queue *struct {
				Binding string
				Ahead   int
				Says    string
			}
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		if wire.Queue == nil || wire.Queue.Binding != "local" || wire.Queue.Ahead != 1 || wire.Queue.Says != slotWaitWords("local", 1) {
			t.Fatalf("missing queue facts: %s", raw)
		}
		if a.State != activityQueued || a.Held || w.Kind != "work" || w.Run != holding || w.Step != queued || w.Attempt != 2 {
			t.Fatalf("wrong queued identity: %+v / %+v", a, w)
		}
		if acts := nextActs(run, []*Step{{ID: queued, Type: "agent/review", Status: StatusPending}}, a, nil, false); len(acts) > 0 {
			t.Fatalf("queued work offers acts: %+v", acts)
		}
	}
}
