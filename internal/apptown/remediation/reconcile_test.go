package remediation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestRemediationDecisionsFromFacts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*Observation)
		action remediationAction
		state  string
		reason string
	}{
		{"working", func(o *Observation) {}, actionWait, "", ""},
		{"missing change", func(o *Observation) { o.ChangeFound = false }, actionStuck, "", "Change disappeared"},
		{"missing run", func(o *Observation) { o.Execution.Run.ID = ids.Nil }, actionStuck, "", "run disappeared"},
		{"unstarted", func(o *Observation) { o.Item.RunID = ids.Nil }, actionStuck, "", "no run was started"},
		{"preparation", func(o *Observation) { o.Execution.Unready = true }, actionPrepare, "", ""},
		{"await approval", func(o *Observation) { o.Execution.Steps[0].Status = workflow.StatusPass }, actionPhase, ItemAccepting, "waiting on you"},
		{"sent back", func(o *Observation) { o.Item.State = ItemAccepting }, actionPhase, ItemWorking, ""},
		{"human blocker", func(o *Observation) { o.Execution.Steps[0].Status = workflow.StatusNeedsHuman }, actionWait, "", ""},
		{"gate failure", func(o *Observation) { o.Execution.Steps[0].Status = workflow.StatusFail }, actionStuck, "", "cannot rework"},
		{"agent rework", func(o *Observation) {
			o.Execution.Steps[0].Status = workflow.StatusFail
			o.Execution.Steps[0].Type = "agent/pi"
		}, actionWait, "", ""},
		{"rework exhausted", func(o *Observation) {
			o.Execution.Steps[0].Status = workflow.StatusFail
			o.Execution.Reworks = 3
		}, actionStuck, "", "kept failing"},
		{"provider refusal", func(o *Observation) {
			o.Execution.Steps[0].Status = workflow.StatusFail
			o.Execution.Steps[0].Evidence = json.RawMessage(`{"reason":"429 rate limit"}`)
		}, actionBackoff, "", "429"},
		{"refusal exhausted", func(o *Observation) {
			o.Execution.Steps[0].Status = workflow.StatusFail
			o.Execution.Steps[0].Evidence = json.RawMessage(`{"reason":"429"}`)
			o.Item.TransientRetries = remMaxTransients
		}, actionStuck, "", "kept refusing"},
		{"decision requested", func(o *Observation) {
			o.Execution.Run.Status = workflow.RunAbandoned
			o.Execution.Steps[0].Status = workflow.StatusFail
			o.Execution.Steps[0].Evidence = json.RawMessage(`{"reason":"a question"}`)
		}, actionStuck, "", "needs a decision"},
		{"accepted", func(o *Observation) { o.Execution.Run.Status = workflow.RunDone }, actionPhase, ItemAccepting, "landing is starting"},
		{"landing", func(o *Observation) { o.ChangeState = workflow.ChangeLanding }, actionPhase, ItemLanding, ""},
		{"landing retry", func(o *Observation) {
			o.ChangeState = workflow.ChangeLanding
			o.Item.State = ItemLanding
			o.Execution.Steps[0].Status = workflow.StatusFail
			o.Execution.Steps[0].Evidence = json.RawMessage(`{"reason":"provider overloaded"}`)
		}, ActionRecheckLanding, "", "overloaded"},
		{"landed", func(o *Observation) { o.ChangeState = workflow.ChangeShipped }, actionLanded, "", ""},
		{"verifying", func(o *Observation) { o.Item.State = itemVerifying }, actionVerify, "", ""},
		{"newly landed stuck", func(o *Observation) {
			o.Item.State = ItemStuck
			o.ChangeState = workflow.ChangeShipped
			o.AttemptOutcome = "stuck"
		}, actionLanded, "", ""},
		{"already reviewed", func(o *Observation) {
			o.Item.State = ItemStuck
			o.ChangeState = workflow.ChangeShipped
			o.AttemptOutcome = "landed"
		}, actionWait, "", ""},
		{"skipped", func(o *Observation) { o.Item.State = ItemSkipped }, actionWait, "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			o := Observation{Item: Item{State: ItemWorking, ChangeID: idstest.N(1), RunID: idstest.N(1)}, ChangeFound: true, ChangeState: workflow.ChangeInProgress, Execution: workflow.ExecutionObservation{Run: workflow.Run{ID: idstest.N(1), Status: workflow.RunRunning}, Steps: []*workflow.Step{
				{Name: "build", Type: "script", Status: workflow.StatusRunning}, {Name: "accept", Type: "approval", Status: workflow.StatusPending},
			}}}
			test.change(&o)
			decision := Decide(o)
			if decision.Action != test.action || decision.State != test.state || !strings.Contains(decision.Reason, test.reason) {
				t.Fatalf("decision: %+v", decision)
			}
		})
	}
}
