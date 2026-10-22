package workflow

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/dbtest"
)

func TestWorkRetryAuthorityWithoutRouteAndCurrentGrants(t *testing.T) {
	t.Parallel()
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "gate", Uses: "agent/review"}}}}}
	a := stepAuthoritySpecFixture(t, spec)
	a.Work.Stop()
	a.Work = New(context.Background(), a.DB, Dependencies{Authority: a.Authorizer, Automatic: []string{"agent/review"}, Runners: map[string]Runner{
		"agent/review": func(task *Execution) {
			if _, err := task.Claim(); err != nil {
				t.Error(err)
				return
			}
			if err := task.Finish(StepOutcome{Status: StatusFail}, nil); err != nil {
				t.Error(err)
			}
		},
	}})
	t.Cleanup(a.Work.Stop)
	owner := access.Principal{Kind: "user", Name: "owner", Via: "token"}
	ctx := stepVerifiedContext(context.Background(), owner)
	if err := a.Work.ExecuteStep(ctx, a.StepID); err != nil {
		t.Fatal(err)
	}
	step, err := ReadStepIn(ctx, a.DB, a.StepID)
	if err != nil || step.Status != StatusFail {
		t.Fatalf("failed gate fixture: %+v %v", step, err)
	}
	for _, pr := range []access.Principal{
		{Kind: "user", Name: "outsider", Via: "token"},
		{Kind: "user", Name: "owner", Via: "oauth", Scopes: []string{"apptown:user:read"}},
		{Kind: "instrument", Name: "owner via console/c", Person: "owner", Instrument: "console", Via: "console", ProjectID: a.OtherProjectID},
		{Kind: "instrument", Name: "run/1", Instrument: "run", Via: "run", ProjectID: a.ProjectID},
	} {
		result, err := a.Work.RetryStep(stepVerifiedContext(context.Background(), pr), RetryCommand{StepID: a.StepID})
		var denied *access.Denied
		if result != (Retried{}) || !errors.As(err, &denied) {
			t.Fatalf("unauthorized %+v: %+v %v", pr, result, err)
		}
	}
	disabled := true
	if err := a.Access.SaveUser(ctx, identity.AccountChange{Username: "owner", Disabled: &disabled}, access.Principal{Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.RetryStep(ctx, RetryCommand{StepID: a.StepID}); err == nil {
		t.Fatal("disabled owner retried")
	}
	disabled = false
	if err := a.Access.SaveUser(ctx, identity.AccountChange{Username: "owner", Disabled: &disabled}, access.Principal{Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, a.DB, "-- name: UserByName :one")
	if _, err := a.Work.RetryStep(ctx, RetryCommand{StepID: a.StepID}); err == nil {
		t.Fatal("failed authority read admitted retry")
	}
	if _, err := a.Work.RetryStep(ctx, RetryCommand{StepID: a.StepID}); err != nil {
		t.Fatalf("verified retry: %v", err)
	}
	a.Work.Wait()
	wantRunEventCount(t, a.DB, a.RunID, 1, kindWith("step.retried", "by", "owner"))
	grants, err := a.Access.GrantsAt(ctx, access.ScopeProject, fmt.Sprint(a.ProjectID))
	if err != nil {
		t.Fatal(err)
	}
	revoked := 0
	for _, grant := range grants {
		if grant.SubjectType == "user" && grant.Subject == "owner" {
			if _, err := a.Access.Revoke(ctx, grant.ID, "boss"); err != nil {
				t.Fatal(err)
			}
			revoked++
		}
	}
	if revoked != 2 {
		t.Fatalf("expected two owner grants revoked, got %d", revoked)
	}
	if _, err := a.Work.RetryStep(ctx, RetryCommand{StepID: a.StepID}); err == nil {
		t.Fatal("bound service cached revoked grant")
	}
}
