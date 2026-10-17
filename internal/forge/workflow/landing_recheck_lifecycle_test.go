package workflow

import (
	"context"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/ids/idstest"
)

// Rechecking a landing re-arms only failed automatic gates, never an approval.
func TestRecheckLandingRearmsFailedGatesOnly(t *testing.T) {
	t.Parallel()
	a := stepAuthorityRunnerFixture(t, schema.Spec{
		Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "gates", Steps: []schema.Step{
			{Name: "tests", Uses: "tests"},
			{Name: "scan-source:semgrep", Uses: "scan", With: map[string]any{"scanner": "nope"}},
			{Name: "approve-landing", Uses: "approval", With: map[string]any{"approvers": []string{"owner"}}},
		}}},
	}, map[string]Runner{
		"tests": func(task *Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if err := task.Finish(StepOutcome{Status: StatusPass}, nil); err != nil {
				t.Error(err)
			}
		},
		"scan": func(task *Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if err := task.Finish(StepOutcome{Status: StatusFail}, nil); err != nil {
				t.Error(err)
			}
		},
	})
	a.Work.Stop()
	ctx := stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"})
	run, err := a.Work.Run(ctx, a.RunID)
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := a.Work.Snapshot(ctx, run.ID, DetailView)
	if err != nil || len(snapshot.Steps) != 3 {
		t.Fatalf("landing steps: %+v %v", snapshot, err)
	}
	for _, step := range snapshot.Steps[:2] {
		if err := a.Work.ExecuteStep(ctx, step.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Work.Approve(ctx, Approval{StepID: snapshot.Steps[2].ID, Decision: VoteReject}); err != nil {
		t.Fatal(err)
	}
	rechecked, err := a.Work.RecheckLanding(ctx, LandingRecheck{ChangeID: a.ChangeID})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	if len(rechecked.Names) != 1 || rechecked.Names[0] != "scan-source:semgrep" {
		t.Fatalf("only the failed scan should be re-run, got %v", rechecked.Names)
	}
	fresh, err := a.Work.Snapshot(ctx, run.ID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var approval *Step
	var scan *Step
	for _, step := range fresh.Steps {
		if step.Name == "approve-landing" {
			approval = step
		}
		if step.Name == "scan-source:semgrep" {
			scan = step
		}
	}
	if approval == nil || approval.Status != StatusFail {
		t.Fatalf("approval changed during recheck: %+v", approval)
	}
	if scan == nil || scan.Status == StatusFail {
		t.Fatalf("failed scan should have been re-armed: %+v", scan)
	}
	if _, err := a.Work.RecheckLanding(ctx, LandingRecheck{ChangeID: idstest.N(999999)}); err == nil {
		t.Fatal("a change with no landing in progress must be refused")
	}
}
