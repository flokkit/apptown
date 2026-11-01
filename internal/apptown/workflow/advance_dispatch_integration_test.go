package workflow

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/schema"
)

func TestAdvanceDispatchesWhatTheNextStageReadies(t *testing.T) {
	t.Parallel()
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
		{Name: "work", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"approvers": []string{"owner"}, "means": "the work is ready"}}}},
		{Name: "after", Steps: []schema.Step{{Name: "build", Uses: "script", With: map[string]any{"script": "true"}}}},
	}}
	a := stepAuthoritySpecFixture(t, spec)
	a.Work.Stop()
	ctx := stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"})
	if _, err := a.Work.Approve(ctx, Approval{StepID: a.StepID, Decision: VoteApprove, Comment: "ready"}); err != nil {
		t.Fatal(err)
	}
	var ran atomic.Value // the step id a runner ran
	a.Work = New(context.Background(), a.DB, Dependencies{Authority: a.Authorizer, Automatic: []string{"script"}, Runners: map[string]Runner{
		"script": func(task *Execution) { ran.Store(task.Step().ID) },
	}})
	t.Cleanup(a.Work.Stop)
	a.Work.advancer = newAdvancer(a.DB, advanceRuntime{stages: a.Work.stages, assertions: a.Work.assertions, checks: advancementScopeChecks{refresh: func(context.Context, *Run) error { return nil }}, completion: completionRecorderFunc(noCompletionEffects)})
	adv, err := a.Work.Advance(ctx, a.RunID)
	if err != nil || !adv.Advanced || adv.Stage != "after" {
		t.Fatalf("advance: %+v err=%v", adv, err)
	}
	a.Work.Wait()
	if ran.Load() == nil {
		t.Fatal("the advance entered the next stage but its script was never dispatched")
	}
}
