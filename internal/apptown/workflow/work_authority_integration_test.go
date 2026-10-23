package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
)

func TestWorkAuthorityAppliesWithoutRouteGuards(t *testing.T) {
	t.Parallel()
	a := stepAuthorityFixture(t, ApprovalConfig{})
	ctx := context.Background()
	owner := access.Principal{Kind: "user", Name: "owner", Via: "token"}
	service := a.Work
	readers := []func(context.Context, ids.ID) (*Change, error){service.Change, NewChanges(a.DB, a.Authorizer, nil).Change}
	ch, err := service.Create(stepVerifiedContext(ctx, owner), ChangeDraft{ProjectID: a.ProjectID, Title: "owned change"})
	if err != nil {
		t.Fatal(err)
	}
	title := "changed"
	for _, tc := range []struct {
		pr   access.Principal
		code string
	}{
		{access.Principal{Kind: "user", Name: "outsider", Via: "token"}, access.DenyOutsideScope},
		{access.Principal{Kind: "user", Name: "owner", Via: "oauth", Scopes: []string{"apptown:user:read"}}, access.DenyDelegationNarrow},
		{access.Principal{Kind: "instrument", Name: "run/1", Instrument: "run", Via: "run", ProjectID: a.ProjectID}, access.DenyDelegationNarrow},
		{access.Principal{Kind: "instrument", Name: "owner via console/c", Person: "owner", Instrument: "console", Via: "console", ProjectID: a.OtherProjectID}, access.DenyOutsideScope},
	} {
		got, err := a.Work.Edit(stepVerifiedContext(ctx, tc.pr), ChangeEdit{ID: ch.ID, Title: &title, ExpectedRevision: revision(0)})
		var denied *access.Denied
		if got != nil || !errors.As(err, &denied) || denied.Code != tc.code {
			t.Fatalf("unauthorized %+v: %+v %v", tc.pr, got, err)
		}
	}
	disabled := true
	if err := a.Access.SaveUser(ctx, identity.AccountChange{Username: "owner", Disabled: &disabled}, access.Principal{Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	for _, read := range readers {
		if got, err := read(stepVerifiedContext(ctx, owner), ch.ID); got != nil || err == nil {
			t.Fatal("disabled caller read Change")
		}
	}
	disabled = false
	if err := a.Access.SaveUser(ctx, identity.AccountChange{Username: "owner", Disabled: &disabled}, access.Principal{Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	for _, read := range readers {
		dbtest.FailNextStatement(t, a.DB, "-- name: UserByName :one")
		if _, err := read(stepVerifiedContext(ctx, owner), ch.ID); !(errors.Is(err, database.ErrInjectedFailure) || err != nil && strings.Contains(err.Error(), "forge_injected_failure")) {
			t.Fatalf("failed authority read hidden: %v", err)
		}
		if got, err := read(stepVerifiedContext(ctx, owner), ch.ID); err != nil || got.ID != ch.ID {
			t.Fatalf("authorized change lookup: %+v %v", got, err)
		}
	}
	if _, err := service.Edit(stepVerifiedContext(ctx, owner), ChangeEdit{ID: ch.ID, Title: &title, ExpectedRevision: revision(0)}); err != nil {
		t.Fatal(err)
	}
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
	for _, read := range readers {
		if got, err := read(stepVerifiedContext(ctx, owner), ch.ID); got != nil || err == nil {
			t.Fatal("bound change reader cached revoked grant")
		}
	}
}

func TestChangeSendBackAuthorityWithoutRoute(t *testing.T) {
	t.Parallel()
	a := stepAuthorityFixture(t, ApprovalConfig{})
	for _, pr := range []access.Principal{
		{Kind: "user", Name: "outsider", Via: "token"},
		{Kind: "user", Name: "owner", Via: "oauth", Scopes: []string{"apptown:user:read"}},
		{Kind: "instrument", Name: "owner via console/c", Person: "owner", Instrument: "console", Via: "console", ProjectID: a.OtherProjectID},
		{Kind: "instrument", Name: "run/1", Instrument: "run", Via: "run", ProjectID: a.ProjectID},
	} {
		_, err := a.Work.SendBack(stepVerifiedContext(context.Background(), pr), SendBack{ChangeID: a.ChangeID, Note: "again"})
		var denied *access.Denied
		if !errors.As(err, &denied) {
			t.Fatalf("unauthorized %+v: %v", pr, err)
		}
	}
	svc := a.Work
	disabled := true
	if err := a.Access.SaveUser(context.Background(), identity.AccountChange{Username: "owner", Disabled: &disabled}, access.Principal{Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SendBack(stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"}), SendBack{ChangeID: a.ChangeID, Note: "again"}); err == nil {
		t.Fatal("disabled user admitted")
	}
	disabled = false
	if err := a.Access.SaveUser(context.Background(), identity.AccountChange{Username: "owner", Disabled: &disabled}, access.Principal{Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	grants, err := a.Access.GrantsAt(context.Background(), access.ScopeProject, fmt.Sprint(a.ProjectID))
	if err != nil {
		t.Fatal(err)
	}
	revoked := 0
	for _, grant := range grants {
		if grant.Subject == "owner" {
			if _, err := a.Access.Revoke(context.Background(), grant.ID, "boss"); err != nil {
				t.Fatal(err)
			}
			revoked++
		}
	}
	if revoked != 2 {
		t.Fatalf("expected two owner grants revoked, got %d", revoked)
	}
	if _, err := svc.SendBack(stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"}), SendBack{ChangeID: a.ChangeID, Note: "again"}); err == nil {
		t.Fatal("revoked authority admitted")
	}
	// sql-guarantee: no workflow read enumerates continuations; the total proves the refused command wrote none.
	completionCount(t, a.DB, 0, `SELECT COUNT(*) FROM workflow_continuations`)
}

func TestRunAdvanceAuthorityBeforePreparationWithoutRoute(t *testing.T) {
	t.Parallel()
	a := stepAuthorityFixture(t, ApprovalConfig{})
	eventsBefore := countEvents(storedEvents(t, a.DB), ofKind("run.done", "advance.ok", "run.ready"))
	for _, pr := range []access.Principal{
		{Kind: "user", Name: "outsider", Via: "token"},
		{Kind: "user", Name: "owner", Via: "oauth", Scopes: []string{"apptown:user:read"}},
		{Kind: "instrument", Name: "owner via console/c", Person: "owner", Instrument: "console", Via: "console", ProjectID: a.OtherProjectID},
	} {
		_, err := a.Work.Advance(stepVerifiedContext(context.Background(), pr), a.RunID)
		var denied *access.Denied
		if !errors.As(err, &denied) {
			t.Fatalf("unauthorized %+v: %v", pr, err)
		}
	}
	wantEventCount(t, a.DB, eventsBefore, ofKind("run.done", "advance.ok", "run.ready"))
}

func TestCommittedApprovalSurvivesAdvancementFailure(t *testing.T) {
	t.Parallel()
	a := stepAuthoritySpecFixture(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
		{Name: "finish", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"approvers": []string{"owner"}}}}},
		{Name: "next", Steps: []schema.Step{{Name: "review", Uses: "approval"}}},
	}})
	ctx := stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"})
	run, err := readRun(context.Background(), a.DB, a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	reported := make(chan error, 1)
	a.Work.Stop()
	a.Work = New(context.Background(), a.DB, Dependencies{Authority: a.Authorizer, Report: func(err error) { reported <- err }})
	t.Cleanup(a.Work.Stop)
	a.Work.advancer = newAdvancer(a.DB, advanceRuntime{Stages: a.Work.stages, Assertions: a.Work.assertions, Checks: advancementScopeChecks{refresh: func(context.Context, *Run) error { return nil }}, Completion: completionRecorderFunc(noCompletionEffects)})
	dbtest.FailNextStatement(t, a.DB, "UPDATE runs SET current_stage")
	step, err := a.Work.Approve(ctx, Approval{StepID: snapshot.Steps[0].ID, Decision: VoteApprove})
	if err != nil || step.Status != StatusPass {
		t.Fatalf("committed approval reported as failed: %+v %v", step, err)
	}
	select {
	case err := <-reported:
		if !errors.Is(err, database.ErrInjectedFailure) {
			t.Fatal(err)
		}
	default:
		t.Fatal("advancement failure was not reported")
	}
	wantRunEventCount(t, a.DB, run.ID, 1, ofKind("approval"))
	wantCurrentStage(t, a.DB, run.ID, "finish")
	if out, err := a.Work.Advance(ctx, run.ID); err != nil || !out.Advanced {
		t.Fatalf("explicit advancement: %+v %v", out, err)
	}
	a.Work.Wait()
	wantCurrentStage(t, a.DB, run.ID, "next")
}

func TestWorkRetryCommandsDispatchTheirCommittedWork(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"retry", "run step", "send back"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			owner, run, agent, review, _ := retryStageFixture(t)
			ctx := context.Background()
			db := owner.db
			var calls atomic.Int32
			deps := Dependencies{Automatic: []string{"agent/pi", "agent/review"}}
			{
				runner := func(task *Execution) {
					calls.Add(1)
					wantRetryRevision(t, db, run.ID, 1)
					_, err := task.Claim()
					if err != nil {
						t.Error(err)
						return
					}
					if err := task.Finish(StepOutcome{Status: StatusPass}, nil); err != nil {
						t.Error(err)
					}
				}
				deps.Runners = map[string]Runner{"agent/review": runner}
				if command == "send back" {
					deps.Runners["agent/pi"] = runner
				}
			}
			a := newServiceWithAuthority(db, deps, allowProject(run.ProjectID))
			t.Cleanup(a.Stop)
			invoke := func() error {
				switch command {
				case "retry":
					_, err := a.RetryStep(ctx, RetryCommand{StepID: review})
					return err
				case "run step":
					out, err := a.RunStep(ctx, RetryCommand{StepID: review})
					if err == nil && !out.Queued {
						t.Error("retry was not queued")
					}
					return err
				default:
					_, err := a.SendBack(ctx, SendBack{ChangeID: run.ChangeID, Note: "revise"})
					return err
				}
			}
			before := stepRollbackSnapshot(t, db)
			dbtest.FailNextStatement(t, db, "INSERT INTO run_events")
			if err := invoke(); !errors.Is(err, database.ErrInjectedFailure) {
				t.Fatalf("receipt failure: %v", err)
			}
			a.Wait()
			if calls.Load() != 0 || stepRollbackSnapshot(t, db) != before {
				t.Fatal("failed command published work")
			}
			if err := invoke(); err != nil {
				t.Fatalf("explicit rerun: %v", err)
			}
			a.Wait()
			want := int32(1)
			if command == "send back" {
				want = 2
			}
			if calls.Load() != want {
				t.Fatalf("direct command ran %d attempts, want %d", calls.Load(), want)
			}
			id := review
			if command == "send back" {
				id = agent
			}
			wantEqual(t, "attempt", wantStepState(t, db, id, StatusPass).Attempt, 2)
		})
	}
}

func TestWorkDecisionsAdvanceOnlyAfterCommit(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"approve", "override", "assertion"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			gate := schema.Step{Name: "gate", Uses: "approval", With: map[string]any{"approvers": []string{"owner"}}}
			if command != "approve" {
				gate.Uses = "artifact/require"
				gate.With = map[string]any{"kind": "report"}
			}
			a := stepAuthoritySpecFixture(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
				{Name: "finish", Steps: []schema.Step{gate}}, {Name: "next", Steps: []schema.Step{{Name: "review", Uses: "approval"}}},
			}})
			ctx := stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"})
			run, err := readRun(context.Background(), a.DB, a.RunID)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := a.Work.Snapshot(ctx, run.ID, DetailView)
			if err != nil {
				t.Fatal(err)
			}
			id := snapshot.Steps[0].ID
			if command == "assertion" {
				if _, err := AddArtifactIn(ctx, a.DB, run.ID, "report", "report", "", "{}", time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			a.Work.Stop()
			original := a.Authorizer
			a.Authorizer = stepPermissionFunc(func(ctx context.Context, p access.Permission) (access.Caller, error) {
				if p.Capability == access.CapRunOperate && !p.Resource.RunID.IsZero() && p.Resource.ChangeID.IsZero() {
					return access.Caller{}, &access.Denied{Reason: "person cannot advance runs"}
				}
				return original.Require(ctx, p)
			})
			a.Work = New(context.Background(), a.DB, Dependencies{Authority: a.Authorizer})
			t.Cleanup(a.Work.Stop)
			a.Work.advancer = newAdvancer(a.DB, advanceRuntime{Stages: a.Work.stages, Assertions: a.Work.assertions, Checks: advancementScopeChecks{refresh: func(context.Context, *Run) error { return nil }}, Completion: completionRecorderFunc(noCompletionEffects)})
			invoke := func() (*Step, error) {
				switch command {
				case "approve":
					return a.Work.Approve(ctx, Approval{StepID: id, Decision: VoteApprove, Comment: "reviewed"})
				case "override":
					return a.Work.Override(ctx, Override{StepID: id, Reason: "reviewed"})
				default:
					return a.Work.Evaluate(ctx, id)
				}
			}
			dbtest.FailNextStatement(t, a.DB, "INSERT INTO run_events")
			if step, err := invoke(); step != nil || !errors.Is(err, database.ErrInjectedFailure) {
				t.Fatalf("receipt failure: %+v %v", step, err)
			}
			a.Work.Wait()
			wantCurrentStage(t, a.DB, run.ID, "finish")
			wantStepCount(t, a.DB, 0, func(s *Step) bool { return s.RunID == run.ID && s.Stage == "next" })
			if step, err := invoke(); err != nil || step.Status != StatusPass {
				t.Fatalf("explicit rerun: %+v %v", step, err)
			}
			a.Work.Wait()
			snapshot, err = a.Work.Snapshot(ctx, run.ID, DetailView)
			if err != nil || snapshot.Run.CurrentStage != "next" {
				t.Fatalf("command did not advance: %+v %v", snapshot, err)
			}
			kind := "approval"
			if command == "override" {
				kind = "override"
			}
			if command != "assertion" {
				wantRunEventCount(t, a.DB, run.ID, 1, kindWith(kind, "by", "owner"))
			}
		})
	}
}

// Both entry points check current authority before admitting the same atomic uplift.
func TestUpliftAuthorityWithoutDispatcher(t *testing.T) {
	t.Parallel()
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprint("independent=", direct), func(t *testing.T) {
			t.Parallel()
			a := stepAuthorityFixture(t, ApprovalConfig{})
			if _, err := a.Access.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: "owner", Role: access.RoleProjectOwner, Scope: access.ScopeProject, ScopeID: fmt.Sprint(a.ProjectID), Origin: "fixture", By: "boss"}, ""); err != nil {
				t.Fatal(err)
			}
			open := a.Work.Uplift
			if direct {
				open = NewChanges(a.DB, a.Authorizer, nil).Uplift
			}
			ctx := context.Background()
			if err := a.DB.InTx(ctx, func(tx *database.Tx) error {
				return adoption.RecordIn(ctx, tx, a.ProjectID, 1, adoption.AdoptionCreate, "authority fixture", adoption.EmptyReceipt(), "owner", time.Now())
			}); err != nil {
				t.Fatal(err)
			}
			current, err := adoption.CurrentIn(ctx, a.DB, a.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			in := Uplift{ProjectID: a.ProjectID, ExpectedRevision: current.Revision, Words: []string{"updated guidance"}, Acceptance: Acceptance{TargetDigest: "exact-digest"}, Target: testTarget{digest: "exact-digest"}}
			for _, caller := range []context.Context{
				ctx,
				stepVerifiedContext(ctx, access.Principal{Kind: "user", Name: "outsider", Via: "token"}),
				stepVerifiedContext(ctx, access.Principal{Kind: "user", Name: "owner", Via: "oauth", Scopes: []string{"apptown:user:read"}}),
			} {
				if ch, err := open(caller, in); ch != nil || err == nil {
					t.Fatalf("unauthorized admission: %+v %v", ch, err)
				}
			}
			owner := stepVerifiedContext(ctx, access.Principal{Kind: "user", Name: "owner", Via: "token"})
			dbtest.FailNextStatement(t, a.DB, "-- name: UserByName :one")
			if ch, err := open(owner, in); ch != nil || err == nil {
				t.Fatalf("failed authority lookup: %+v %v", ch, err)
			}
			disabled := true
			if err := a.Access.SaveUser(ctx, identity.AccountChange{Username: "owner", Disabled: &disabled}, access.Principal{Kind: "user", Name: "boss", Via: "session"}); err != nil {
				t.Fatal(err)
			}
			if ch, err := open(owner, in); ch != nil || err == nil {
				t.Fatalf("disabled admission: %+v %v", ch, err)
			}
			// sql-guarantee: a refused uplift must leave no uplift Change in any project; no workflow read enumerates Changes across projects.
			completionCount(t, a.DB, 0, `SELECT count(*) FROM changes WHERE type='uplift'`)
			disabled = false
			if err := a.Access.SaveUser(ctx, identity.AccountChange{Username: "owner", Disabled: &disabled}, access.Principal{Kind: "user", Name: "boss", Via: "session"}); err != nil {
				t.Fatal(err)
			}
			ch, err := open(owner, in)
			if err != nil {
				t.Fatal(err)
			}
			if c := storedChange(t, a.DB, ch.ID); c.ProjectID != a.ProjectID || c.TargetAdoptionID.IsZero() {
				t.Fatalf("uplift: %+v", c)
			}
			wantReceiptCount(t, a.DB, 1, inProject(a.ProjectID, receiptWith("wow.uplift.opened", "owner")))
		})
	}
}
