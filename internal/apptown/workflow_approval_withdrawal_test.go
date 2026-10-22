package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/remediation"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
)

// This integration test stays at the App boundary because interpolation is an
// App concern. Remediation owns the detailed withdrawal transaction proofs.
func TestRemediationSkipWithdrawalBecomesFeedback(t *testing.T) {
	t.Parallel()
	a, starts, item, def := remediationAttemptFixture(t, false)
	// The named approver also needs the project approval capability.
	a.addGrant("user", "alice", access.RoleProjectApprover, "", access.ScopeProject, fmt.Sprint(item.ProjectID), "", "system", "")
	def.Run.Spec.Stages[0].Steps[0].With = map[string]any{"quorum": 2, "approvers": []string{"alice", "bob"}}
	installFakeGit(t, a, nil)
	d := installDispatchFixture(t, a)
	d.runners["script"] = func(task *workflow.Execution) {
		if _, err := task.Claim(); err != nil {
			t.Error(err)
			return
		}
		if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass, Evidence: json.RawMessage(`{}`)}, nil); err != nil {
			t.Error(err)
		}
	}
	run, err := starts.Start(context.Background(), item, def, "worker")
	if err != nil || run == nil {
		t.Fatalf("admission: %+v %v", run, err)
	}
	if err := a.Work.DispatchStarted(run.ID); err != nil {
		t.Fatal(err)
	}
	d.wait()
	snapshot, err := a.Work.Snapshot(asPerson("alice"), run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var approval ids.ID
	for _, step := range snapshot.Steps {
		if step.Type == "approval" {
			approval = step.ID
		}
	}
	if approval.IsZero() {
		t.Fatal("missing approval")
	}
	if _, err := a.Work.Approve(asPerson("alice"), workflow.Approval{StepID: approval, Decision: workflow.VoteApprove}); err != nil {
		t.Fatal(err)
	}
	item, err = remediation.ReadItem(context.Background(), a.DB, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	reconcile := remediationReconciliationFor(a)
	observed, err := reconcile.Observe(context.Background(), item)
	if err != nil || observed == nil {
		t.Fatalf("observation: %+v %v", observed, err)
	}
	if applied, err := reconcile.Apply(context.Background(), *observed, remediation.Decide(*observed)); err != nil || applied == nil {
		t.Fatalf("accepting: %+v %v", applied, err)
	}
	if err := starts.Skip(context.Background(), item.RemediationID, item.ID, "operator", "  not needed  "); err != nil {
		t.Fatal(err)
	}
	feedback := workflow.Interpolate(t.Context(), a.DB, "${{feedback.latest}}", run, nil, "completion")
	if feedback != "acceptance withdrawn: not needed (— operator)" {
		t.Fatalf("withdrawal became unrelated feedback: %q", feedback)
	}
}
