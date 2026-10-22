package workflow

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Terminal runs take precedence over their step activity. The owner query
// projection must preserve this vocabulary when a run has been abandoned.
// This calls the service boundary so list consumers and detail consumers share
// the same persisted run scope and classifier.
func TestWorkflowActivityUsesAbandonedRunState(t *testing.T) {
	t.Parallel()
	q, root, _ := activityFixture(t)
	{
		current, err := workflowsql.New(q.db.SQL()).RunByID(context.Background(), workflowsql.RunByIDParams{ID: root.ID})
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := workflowsql.New(q.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: root.ID, ExpectedStatus: current.Status, CurrentStage: current.CurrentStage, RetryRevision: current.RetryRevision, NextStage: current.CurrentStage, NextStatus: "abandoned"}); err != nil || rows != 1 {
			t.Fatalf("stored run transition: rows=%d err=%v", rows, err)
		}
	}
	activities, err := newService(q.db, allowProject(root.ProjectID), Runtime{}).Activities(context.Background(), []ids.ID{root.ID})
	if err != nil {
		t.Fatal(err)
	}
	activity, ok := activities[root.ID]
	if !ok || activity.State != ActivityAbandoned {
		t.Fatalf("abandoned run activity: %+v", activity)
	}
}

// Detail reads use the same persisted classifier as list activity. A failed
// gate must remain visible as blocked even while the run row is still open,
// once no work of the stage is still to run; while some is, it is named.
func TestWorkflowDetailPreservesBlockedActivity(t *testing.T) {
	t.Parallel()
	db, run, _ := completionFixture(t, "working")
	insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "build-v1", Type: "agent/pi", ConfigJson: []byte(`{}`), Status: StatusFail.String()})
	insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "review", Type: "agent/review", ConfigJson: []byte(`{}`), Status: StatusFail.String()})
	detail, err := newService(db, allowProject(run.ProjectID), Runtime{}).Snapshot(context.Background(), run.ID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != ActivityBlocked {
		t.Fatalf("failed gate detail state: %q (%s)", detail.State, detail.StateDetail)
	}
	insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: "boot", Type: "preview", ConfigJson: []byte(`{}`), Status: StatusPending.String()})
	detail, err = newService(db, allowProject(run.ProjectID), Runtime{}).Snapshot(context.Background(), run.ID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != ActivityWorking || detail.StateDetail != "boot next; review (fail) so far" {
		t.Fatalf("failed gate with a boot still to run: %q (%s)", detail.State, detail.StateDetail)
	}
}

// A verdict given before the stage's latest agent work is about to be reset
// and checked again: list and detail both say the run is working, not
// blocked, in the moments before the reset commits (wait called such runs
// wedged three times). A verdict given after the work still blocks.
func TestActivityTreatsAVerdictOlderThanTheReworkAsBeingRechecked(t *testing.T) {
	t.Parallel()
	for _, fresh := range []bool{false, true} {
		s, run := recheckFixture(t)
		if fresh {
			// sql-guarantee: a verdict finished after the agent's work; no step mutation accepts finished_at.
			completionExec(t, s.db, `INSERT INTO steps(id,run_id,stage,name,type,status,evidence_json,finished_at,override_by) VALUES (?,?,'finish','late-review','agent/review','fail','{}',now(),'')`, ids.New(time.Now()), run.ID)
		}
		want := ActivityWorking
		if fresh {
			want = ActivityBlocked
		}
		svc := newService(s.db, allowProject(run.ProjectID), Runtime{})
		detail, err := svc.Snapshot(context.Background(), run.ID, DetailView)
		if err != nil {
			t.Fatal(err)
		}
		listed, err := svc.Activities(context.Background(), []ids.ID{run.ID})
		if err != nil {
			t.Fatal(err)
		}
		if detail.State != want || listed[run.ID].State != want {
			t.Fatalf("fresh=%v: detail %q (%s), list %q (%s), want %q", fresh, detail.State, detail.StateDetail, listed[run.ID].State, listed[run.ID].Detail, want)
		}
	}
}

// A landing started from accepted work, before it takes that acceptance, is
// AppTown's move: its approval is about to be judged, not waiting on a
// person. Once the acceptance is applied the approval waits as usual.
func TestActivityShowsALandingTakingItsAcceptanceAsWorking(t *testing.T) {
	t.Parallel()
	for _, applied := range []bool{false, true} {
		q, work, _ := activityFixture(t)
		landing := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: work.ProjectID, ChangeID: work.ChangeID, WorkflowName: "land", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "land", DefinitionJson: json.RawMessage(`{"kind":"pipeline","role":"landing","stages":[{"name":"land"}]}`)})
		insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: landing.ID, Stage: "land", Name: "ship", Type: "approval", Status: StatusPending.String()})
		// sql-guarantee: the handoff row exactly as the landing start and apply leave it; reaching it through them needs the whole landing runtime.
		completionExec(t, q.db, `INSERT INTO workflow_landings(work_run_id,landing_run_id,acceptance_applied) VALUES (?,?,?)`, work.ID, landing.ID, applied)
		want := ActivityWorking
		if applied {
			want = ActivityWaiting
		}
		svc := newService(q.db, allowProject(landing.ProjectID), Runtime{})
		detail, err := svc.Snapshot(context.Background(), landing.ID, DetailView)
		if err != nil {
			t.Fatal(err)
		}
		listed, err := svc.Activities(context.Background(), []ids.ID{landing.ID})
		if err != nil {
			t.Fatal(err)
		}
		if detail.State != want || listed[landing.ID].State != want {
			t.Fatalf("applied=%v: detail %q, list %q, want %q", applied, detail.State, listed[landing.ID].State, want)
		}
	}
}

// Accepted work is done before its landing run exists. Until the landing
// starts, the change's detail says its landing is queued rather than
// leaving a finished run that reads as a person's move.
func TestDoneWorkWithAReservedLandingReadsAsQueued(t *testing.T) {
	t.Parallel()
	for _, reserved := range []bool{false, true} {
		db, run, _ := completionFixture(t, "working")
		svc := newService(db, allowProject(run.ProjectID), Runtime{})
		// sql-guarantee: a finished working run; completing it through the engine also starts its follow-up jobs.
		completionExec(t, db, `UPDATE runs SET status='done' WHERE id=?`, run.ID)
		if reserved {
			// sql-guarantee: the reservation completion makes (EnsureLanding) before the landing job runs.
			completionExec(t, db, `INSERT INTO workflow_landings(work_run_id) VALUES (?)`, run.ID)
		}
		detail, err := svc.Snapshot(context.Background(), run.ID, DetailView)
		if err != nil {
			t.Fatal(err)
		}
		// The queued landing is a stated fact, not only the words.
		if reserved && (detail.State != ActivityQueued || detail.StateDetail != "landing" || !detail.LandingQueued) || !reserved && (detail.State != "" || detail.LandingQueued) {
			t.Fatalf("reserved=%v: %q %q", reserved, detail.State, detail.StateDetail)
		}
	}
}

// The snapshot offers what a person can do now, each act naming its step, so
// no client chooses a target from the steps. Another round for the agent sits
// beside a pending approval (a retry resets it); a gate that turns the waiting
// into "blocked" takes the approval away; running work offers nothing.
func TestTheSnapshotOffersTheActsAPersonCanTakeNow(t *testing.T) {
	t.Parallel()
	type step struct {
		name, kind string
		state      StepState
	}
	scan := []schema.Step{{Name: "scan", Uses: "scan", With: map[string]any{"on": "source"}}}
	for _, c := range []struct {
		name  string
		role  string // "" is a working run
		gate  []schema.Step
		steps []step
		want  []NextAct // StepID is filled from the step of that name
	}{
		{"waiting on an approval", "", nil,
			[]step{{"work", "agent/pi", StatusPass}, {"work2", "agent/pi", StatusPass}, {"first-look", "approval", StatusPass}, {"accept", "approval", StatusPending}, {"sign-off", "approval", StatusPending}},
			[]NextAct{{Act: NextApprove, Step: "accept"}, {Act: NextApprove, Step: "sign-off"}, {Act: NextRework, Step: "work2"}}},
		{"blocked by a failed check", "", nil,
			[]step{{"work", "agent/pi", StatusPass}, {"tests", "tests", StatusFail}, {"boot", "preview", StatusError}, {"custom", "x/unrunnable", StatusFail}, {"accept", "approval", StatusPending}},
			[]NextAct{{Act: NextOverride, Step: "tests"}, {Act: NextOverride, Step: "boot"}, {Act: NextOverride, Step: "custom"},
				{Act: NextRetry, Step: "tests"}, {Act: NextRetry, Step: "boot"}, {Act: NextRework, Step: "work"}}},
		{"a rejection with no agent to rework it", "", nil,
			[]step{{"tests", "tests", StatusFail}, {"accept", "approval", StatusFail}},
			[]NextAct{{Act: NextOverride, Step: "tests"}, {Act: NextRetry, Step: "tests"}}},
		// An unmerged merge check records needs-a-person; running it again
		// is how it reads the merge once it happened.
		{"checks that need a person", "", nil,
			[]step{{"work", "agent/pi", StatusPass}, {"lint", "script", StatusNeedsHuman}, {"merged", "scm/merged", StatusNeedsHuman}},
			[]NextAct{{Act: NextRetry, Step: "lint"}, {Act: NextRetry, Step: "merged"}, {Act: NextRework, Step: "work"}}},
		{"a gate missing evidence", "", scan,
			[]step{{"work", "agent/pi", StatusPass}, {"accept", "approval", StatusPending}},
			[]NextAct{{Act: NextRework, Step: "work"}}},
		{"ready to advance", "", nil,
			[]step{{"work", "agent/pi", StatusPass}, {"accept", "approval", StatusPass}},
			[]NextAct{{Act: NextAdvance}}},
		{"a landing whose rules changed", "landing", nil,
			[]step{{"law", "policy/current", StatusNeedsHuman}, {"accept", "approval", StatusPending}},
			[]NextAct{{Act: NextRestartLanding}}},
		{"a working run with a stopped rules check", "", nil,
			[]step{{"law", "policy/current", StatusNeedsHuman}},
			[]NextAct{}},
		{"the agent queued for another round", "", nil,
			[]step{{"work", "agent/pi", StatusPending}, {"tests", "tests", StatusFail}},
			[]NextAct{}},
		{"a check running", "", nil,
			[]step{{"work", "agent/pi", StatusPass}, {"tests", "tests", StatusRunning}, {"accept", "approval", StatusPending}},
			[]NextAct{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			role := c.role
			if role == "" {
				role = "working"
			}
			db, run, _ := completionSpecFixture(t, schema.Spec{Kind: "pipeline", Role: role, Stages: []schema.Stage{{Name: "finish", Steps: c.gate}}})
			stepIDs := map[string]ids.ID{}
			for _, s := range c.steps {
				stepIDs[s.name] = insertStoredStep(t, db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: run.ID, Stage: "finish", Name: s.name, Type: s.kind, Status: s.state.String()})
			}
			detail, err := newService(db, allowProject(run.ProjectID), Runtime{Automatic: []string{"agent/pi", "tests", "preview", "script"}}).Snapshot(context.Background(), run.ID, DetailView)
			if err != nil {
				t.Fatal(err)
			}
			want := []NextAct{}
			for _, a := range c.want {
				a.StepID = stepIDs[a.Step]
				want = append(want, a)
			}
			if !slices.Equal(detail.Next, want) {
				t.Fatalf("%s (%s): next %+v, want %+v", detail.State, detail.StateDetail, detail.Next, want)
			}
		})
	}
}

// A run waiting on a called workflow offers nothing of its own: the approval
// waited on is the child's, and the call step is still running, so neither
// the parent's pending approval nor another agent round is available. An
// overridden check is not overridden again.
func TestAParentWaitingOnItsChildOffersNothing(t *testing.T) {
	t.Parallel()
	run := &Run{Status: RunRunning, CurrentStage: "outer"}
	steps := []*Step{
		{ID: idstest.N(1), Stage: "outer", Name: "work", Type: "agent/pi", Status: StatusPass},
		{ID: idstest.N(2), Stage: "outer", Name: "call", Type: "workflow/call", Status: StatusRunning},
		{ID: idstest.N(3), Stage: "outer", Name: "accept", Type: "approval", Status: StatusPending},
	}
	if acts := nextActs(run, steps, Activity{State: ActivityWaiting, Detail: "ship (in land)", Approval: idstest.N(99)}, nil, false); len(acts) != 0 {
		t.Fatalf("parent offered %+v", acts)
	}
	steps = []*Step{{ID: idstest.N(4), Stage: "outer", Name: "tests", Type: "tests", Status: StatusFail, OverrideBy: "bls"}}
	if acts := nextActs(run, steps, Activity{State: ActivityBlocked}, nil, false); len(acts) != 0 {
		t.Fatalf("an overridden check offered %+v", acts)
	}
}

// A change lands when it is in progress with nothing running and no landing
// queued; its work goes back to the agent while its current run is blocked
// and its landing has not merged.
func TestTheChangeOffersLandingAndSendingBack(t *testing.T) {
	t.Parallel()
	run := func(status RunState, state ActivityState, queued bool) *RunSnapshot {
		return &RunSnapshot{Run: &Run{Status: status}, State: state, LandingQueued: queued}
	}
	land, sendBack := NextAct{Act: NextLand}, NextAct{Act: NextSendBack}
	for _, c := range []struct {
		name    string
		state   ChangeState
		current *RunSnapshot
		merged  bool
		want    []NextAct
	}{
		{"no run yet", ChangeInProgress, nil, false, []NextAct{land}},
		{"work done", ChangeInProgress, run(RunDone, "", false), false, []NextAct{land}},
		{"work stopped", ChangeInProgress, run(RunAbandoned, ActivityAbandoned, false), false, []NextAct{land}},
		{"its landing queued", ChangeInProgress, run(RunDone, ActivityQueued, true), false, []NextAct{}},
		{"work running", ChangeInProgress, run(RunRunning, ActivityWorking, false), false, []NextAct{}},
		{"work blocked", ChangeInProgress, run(RunRunning, ActivityBlocked, false), false, []NextAct{sendBack}},
		{"landing blocked", ChangeLanding, run(RunRunning, ActivityBlocked, false), false, []NextAct{sendBack}},
		{"landing blocked after its merge", ChangeLanding, run(RunRunning, ActivityBlocked, false), true, []NextAct{}},
		{"waiting on a person", ChangeLanding, run(RunRunning, ActivityWaiting, false), false, []NextAct{}},
		{"shipped", ChangeShipped, run(RunDone, "", false), true, []NextAct{}},
		{"abandoned", ChangeAbandoned, nil, false, []NextAct{}},
	} {
		if got := ChangeActs(&Change{State: c.state}, c.current, c.merged); !slices.Equal(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}
