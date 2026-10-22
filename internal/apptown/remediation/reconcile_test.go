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
		action Action
		state  string
		reason string
	}{
		{"working", func(o *Observation) {}, ActionWait, "", ""},
		{"missing change", func(o *Observation) { o.ChangeFound = false }, ActionStuck, "", "Change disappeared"},
		{"missing run", func(o *Observation) { o.Execution.Run.ID = ids.Nil }, ActionStuck, "", "run disappeared"},
		{"unstarted", func(o *Observation) { o.Item.RunID = ids.Nil }, ActionStuck, "", "no run was started"},
		{"preparation", func(o *Observation) { o.Execution.Unready = true }, ActionPrepare, "", ""},
		{"await approval", func(o *Observation) { o.Execution.Steps[0].Status = workflow.StatusPass }, ActionPhase, ItemAccepting, "waiting on you"},
		{"sent back", func(o *Observation) { o.Item.State = ItemAccepting }, ActionPhase, ItemWorking, ""},
		{"human blocker", func(o *Observation) { o.Execution.Steps[0].Status = workflow.StatusNeedsHuman }, ActionWait, "", ""},
		{"gate failure", func(o *Observation) { o.Execution.Steps[0].Status = workflow.StatusFail }, ActionStuck, "", "cannot rework"},
		{"agent rework", func(o *Observation) {
			o.Execution.Steps[0].Status = workflow.StatusFail
			o.Execution.Steps[0].Type = "agent/pi"
		}, ActionWait, "", ""},
		{"rework exhausted", func(o *Observation) {
			o.Execution.Steps[0].Status = workflow.StatusFail
			o.Execution.Reworks = 3
		}, ActionStuck, "", "kept failing"},
		{"provider refusal", func(o *Observation) {
			o.Execution.Steps[0].Status = workflow.StatusFail
			o.Execution.Steps[0].Evidence = json.RawMessage(`{"reason":"429 rate limit"}`)
		}, ActionBackoff, "", "429"},
		{"refusal exhausted", func(o *Observation) {
			o.Execution.Steps[0].Status = workflow.StatusFail
			o.Execution.Steps[0].Evidence = json.RawMessage(`{"reason":"429"}`)
			o.Item.TransientRetries = remMaxTransients
		}, ActionStuck, "", "kept refusing"},
		{"decision requested", func(o *Observation) {
			o.Execution.Run.Status = workflow.RunAbandoned
			o.Execution.Steps[0].Status = workflow.StatusFail
			o.Execution.Steps[0].Evidence = json.RawMessage(`{"reason":"a question"}`)
		}, ActionStuck, "", "needs a decision"},
		{"accepted", func(o *Observation) { o.Execution.Run.Status = workflow.RunDone }, ActionPhase, ItemAccepting, "landing is starting"},
		{"landing", func(o *Observation) { o.ChangeState = workflow.ChangeLanding }, ActionPhase, ItemLanding, ""},
		{"landing retry", func(o *Observation) {
			o.ChangeState = workflow.ChangeLanding
			o.Item.State = ItemLanding
			o.Execution.Steps[0].Status = workflow.StatusFail
			o.Execution.Steps[0].Evidence = json.RawMessage(`{"reason":"provider overloaded"}`)
		}, ActionRecheckLanding, "", "overloaded"},
		{"landed", func(o *Observation) { o.ChangeState = workflow.ChangeShipped }, ActionLanded, "", ""},
		{"verifying", func(o *Observation) { o.Item.State = ItemVerifying }, ActionVerify, "", ""},
		{"newly landed stuck", func(o *Observation) {
			o.Item.State = ItemStuck
			o.ChangeState = workflow.ChangeShipped
			o.AttemptOutcome = "stuck"
		}, ActionLanded, "", ""},
		{"already reviewed", func(o *Observation) {
			o.Item.State = ItemStuck
			o.ChangeState = workflow.ChangeShipped
			o.AttemptOutcome = "landed"
		}, ActionWait, "", ""},
		{"skipped", func(o *Observation) { o.Item.State = ItemSkipped }, ActionWait, "", ""},
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
