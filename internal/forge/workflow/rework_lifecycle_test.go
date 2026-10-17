package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/ids"
)

func TestRejectionStartsRework(t *testing.T) {
	t.Parallel()
	a := stepAuthorityRunnerFixture(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "build", Steps: []schema.Step{{Name: "build-app", Uses: "agent/pi"}, {Name: "accept", Uses: "approval", With: map[string]any{"approvers": []string{"owner"}}}}}}}, map[string]Runner{"agent/pi": func(task *Execution) {
		if _, err := task.Claim(); err != nil {
			t.Error(err)
			return
		}
		if err := task.Finish(StepOutcome{Status: StatusPass}, nil); err != nil {
			t.Error(err)
		}
	}})
	a.Work.Stop()
	ctx := stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"})
	run, err := a.Work.Run(ctx, a.RunID)
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := a.Work.Snapshot(ctx, run.ID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var agent, approval ids.ID
	for _, step := range snapshot.Steps {
		if step.Type == "approval" {
			approval = step.ID
		} else {
			agent = step.ID
		}
	}
	if agent.IsZero() || approval.IsZero() {
		t.Fatalf("rework steps: %+v", snapshot.Steps)
	}
	if err := a.Work.ExecuteStep(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.Approve(ctx, Approval{StepID: approval, Decision: VoteReject, Comment: "the To list includes me"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = a.Work.Snapshot(ctx, run.ID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var agentStep, approvalStep *Step
	for _, step := range snapshot.Steps {
		if step.ID == agent {
			agentStep = step
		}
		if step.ID == approval {
			approvalStep = step
		}
	}
	if agentStep == nil || approvalStep == nil {
		t.Fatalf("reworked steps missing: %+v", snapshot.Steps)
	}
	var config struct {
		RetryMode string `json:"retryMode"`
	}
	if err := json.Unmarshal(agentStep.Config, &config); err != nil {
		t.Fatal(err)
	}
	if agentStep.Status != StatusPending || config.RetryMode != "continue" {
		t.Fatalf("agent step must be pending in continue mode: %+v", agentStep)
	}
	if approvalStep.Status != StatusPending || !strings.Contains(string(approvalStep.Evidence), "the To list includes me") || !strings.Contains(string(approvalStep.Evidence), "lastRejection") {
		t.Fatalf("approval must reopen with the rejection on record: %+v", approvalStep)
	}
	// The words reach the agent as feedback.latest even though the approval is pending again.
	if fb := Interpolate(ctx, a.DB, "${{feedback.latest}}", run, nil, "completion"); !strings.Contains(fb, "the To list includes me") {
		t.Fatalf("feedback.latest must carry the rejection: %q", fb)
	}
	// A reopened approval waits for the revision before accepting a fresh vote.
	if _, err := a.Work.Approve(ctx, Approval{StepID: approval, Decision: VoteApprove, Comment: "too soon"}); err == nil {
		t.Fatal("approval accepted while rework is pending")
	}
	if err := a.Work.ExecuteStep(ctx, agent); err != nil {
		t.Fatal(err)
	}
	// Deciding again is allowed (the old vote is history, not a block).
	if _, err := a.Work.Approve(ctx, Approval{StepID: approval, Decision: VoteApprove, Comment: "better"}); err != nil {
		t.Fatalf("a fresh decision must be accepted: %v", err)
	}
}
