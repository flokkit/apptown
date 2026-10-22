package workflow

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A run, a change and a step judged in memory: no database, no service.

func landingSpec(t *testing.T) string {
	t.Helper()
	spec := schema.Spec{Kind: "lifecycle", Role: "landing", Stages: []schema.Stage{
		{Name: "review", Steps: []schema.Step{{Name: "tests", Uses: "script"}, {Name: "approve", Uses: "approval"}}, Gate: &schema.Gate{Require: []string{"tests", "approve"}}},
		{Name: "land", Steps: []schema.Step{{Name: "ship", Uses: "script"}}, Gate: &schema.Gate{Require: []string{"ship"}}},
	}}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRunAdvanceJudgesItsStage(t *testing.T) {
	t.Parallel()
	run := Run{ID: idstest.N(7), Status: RunRunning, CurrentStage: "review", DefinitionJSON: landingSpec(t)}
	steps := []*Step{
		{Stage: "review", Name: "tests", Type: "script", Status: StatusPass},
		{Stage: "review", Name: "approve", Type: "approval", Status: StatusNeedsHuman},
	}

	d, err := run.Advance(steps, nil)
	if err != nil || d.Passed() || len(d.Blocked) != 1 || d.Blocked[0].Name != "approve" || d.Next != "" || d.Merge || d.Complete {
		t.Fatalf("blocked by the approval: %+v err=%v", d, err)
	}

	steps[1].Status = StatusPass
	d, err = run.Advance(steps, nil)
	if err != nil || !d.Passed() || d.Next != "land" || !d.Merge || d.Complete {
		t.Fatalf("review passed on a landing run merges then enters land: %+v err=%v", d, err)
	}

	run.CurrentStage = "land"
	d, err = run.Advance([]*Step{{Stage: "land", Name: "ship", Type: "script", Status: StatusPass}}, nil)
	if err != nil || !d.Complete || d.Next != "" {
		t.Fatalf("last stage passed completes the run: %+v err=%v", d, err)
	}

	run.Status = RunDone
	if _, err := run.Advance(nil, nil); err == nil {
		t.Fatal("a finished run has nothing to advance")
	}
}

func TestChangeApplyRefusesWrongActs(t *testing.T) {
	t.Parallel()
	change := ChangeLifecycle{ID: idstest.N(3), ProjectID: idstest.N(1), State: ChangeProposed}

	started, err := change.Apply(ChangeMove{Action: StartChange, NameBranch: true})
	if err != nil || started.State != ChangeInProgress || started.Branch != "apptown/change-"+idstest.N(3).String() {
		t.Fatalf("start names the branch: %+v err=%v", started, err)
	}
	if _, err := started.Apply(ChangeMove{Action: LandChange}); err == nil {
		t.Fatal("landing needs the run doing the landing")
	}
	shipped, err := started.Apply(ChangeMove{Action: ShipChange})
	if err != nil || shipped.State != ChangeShipped {
		t.Fatalf("ship: %+v err=%v", shipped, err)
	}
	if _, err := shipped.Apply(ChangeMove{Action: StartChange}); !errors.Is(err, ErrChangeTransition) {
		t.Fatalf("shipped work never reopens: %v", err)
	}
	landing, err := started.Apply(ChangeMove{Action: LandChange, RunID: idstest.N(42)})
	if err != nil || landing.State != ChangeLanding || landing.RunID != idstest.N(42) {
		t.Fatalf("land attaches the landing run: %+v err=%v", landing, err)
	}
	cancelled, err := landing.Apply(ChangeMove{Action: CancelChangeLanding})
	if err != nil || cancelled.State != ChangeInProgress {
		t.Fatalf("cancelled landing goes back to work: %+v err=%v", cancelled, err)
	}
	same, err := cancelled.Apply(ChangeMove{Action: CancelChangeLanding})
	if err != nil || same != cancelled {
		t.Fatalf("nothing to cancel is not an error: %+v err=%v", same, err)
	}
	if _, err := landing.Apply(ChangeMove{Action: StartChange, RunID: idstest.N(42)}); err == nil {
		t.Fatal("start cannot attach a run")
	}
}

func TestStepApproveReachesQuorum(t *testing.T) {
	t.Parallel()
	step := Step{ID: idstest.N(9), RunID: idstest.N(7), Stage: "review", Name: "approve", Type: "approval", Status: StatusNeedsHuman,
		Config: json.RawMessage(`{"quorum":2,"approvers":["ann","bob"]}`)}

	ann, bob := ApprovalVote{Approver: "ann", Principal: idstest.N(1), Via: "session", Decision: VoteApprove}, ApprovalVote{Approver: "bob", Principal: idstest.N(2), Via: "session", Decision: VoteApprove}
	first, err := step.Approve(ann, time.Now())
	if err != nil || !first.Pending || first.Status != StatusNeedsHuman || first.Round.Approvals != 1 {
		t.Fatalf("one of two: %+v err=%v", first, err)
	}
	step.Evidence = first.Evidence
	if _, err := step.Approve(ann, time.Now()); !errors.Is(err, errStepDecisionConflict) {
		t.Fatalf("one vote per person: %v", err)
	}
	second, err := step.Approve(bob, time.Now())
	if err != nil || second.Pending || second.Status != StatusPass || second.Round.Approvals != 2 {
		t.Fatalf("quorum passes the step: %+v err=%v", second, err)
	}
	rejected, err := step.Approve(ApprovalVote{Approver: "bob", Decision: VoteReject, Comment: "no"}, time.Now())
	if err != nil || rejected.Pending || rejected.Status != StatusFail {
		t.Fatalf("a rejection closes the round: %+v err=%v", rejected, err)
	}

	step.Status = StatusPass
	if _, err := step.Approve(ApprovalVote{Approver: "bob", Decision: VoteApprove}, time.Now()); !errors.Is(err, errStepDecisionConflict) {
		t.Fatalf("decided steps take no votes: %v", err)
	}
	script := Step{Type: "script", Status: StatusFail}
	if _, err := script.Approve(ApprovalVote{Approver: "ann", Decision: VoteApprove}, time.Now()); !errors.Is(err, errStepApprovalInput) {
		t.Fatalf("only approval steps take votes: %v", err)
	}
}

func TestStepOverrideNeedsAReasonAndAPerson(t *testing.T) {
	t.Parallel()
	step := Step{ID: idstest.N(9), RunID: idstest.N(7), Stage: "review", Name: "tests", Type: "script", Status: StatusFail}
	if _, err := step.Override("ann", "  "); !errors.Is(err, errOverrideReason) {
		t.Fatalf("reason required: %v", err)
	}
	if _, err := step.Override("", "flaky"); !errors.Is(err, errStepDecisionCaller) {
		t.Fatalf("person required: %v", err)
	}
	if next, err := step.Override("ann", "flaky"); err != nil || next != StatusPass {
		t.Fatalf("override passes: %v err=%v", next, err)
	}
	step.Status = StatusRunning
	if _, err := step.Override("ann", "flaky"); !errors.Is(err, errStepDecisionConflict) {
		t.Fatalf("a running step cannot be overridden: %v", err)
	}
	step.Status, step.OverrideBy = StatusFail, "bob"
	if _, err := step.Override("ann", "again"); !errors.Is(err, errStepDecisionConflict) {
		t.Fatalf("one override per step: %v", err)
	}

	run := Run{ID: idstest.N(7), Status: RunRunning, CurrentStage: "review"}
	if err := run.Decides(&step); err != nil {
		t.Fatal(err)
	}
	run.CurrentStage = "land"
	if err := run.Decides(&step); !errors.Is(err, errStepDecisionConflict) {
		t.Fatalf("a decision belongs to the current stage: %v", err)
	}
}
