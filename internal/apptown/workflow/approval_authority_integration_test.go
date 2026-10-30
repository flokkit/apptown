package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type stepAuthorityTestRig struct {
	DB             *database.DB
	Work           *Service
	Access         *identity.Service
	Authorizer     access.ReadAuthorizer
	StepID         ids.ID
	OverrideStepID ids.ID
	RunID          ids.ID
	ProjectID      ids.ID
	OtherProjectID ids.ID
	ChangeID       ids.ID
}

func stepVerifiedContext(ctx context.Context, principal access.Principal) context.Context {
	return access.WithCaller(ctx, access.Caller{Principal: principal, Verified: true})
}

type stepPermissionFunc func(context.Context, access.Permission) (access.Caller, error)

func (f stepPermissionFunc) Require(ctx context.Context, p access.Permission) (access.Caller, error) {
	return f(ctx, p)
}

func stepAuthorityFixture(t *testing.T, approval ApprovalConfig) *stepAuthorityTestRig {
	with := map[string]any{}
	if approval.Quorum != 0 {
		with["quorum"] = approval.Quorum
	}
	if approval.Approvers != nil {
		with["approvers"] = approval.Approvers
	}
	if approval.Means != "" {
		with["means"] = approval.Means
	}
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: with}}}}}
	return stepAuthoritySpecFixture(t, spec)
}

func stepAuthoritySpecFixture(t *testing.T, spec schema.Spec) *stepAuthorityTestRig {
	t.Helper()
	return stepAuthorityRunnerFixture(t, spec, nil)
}

// Runners enter through the owner's constructor; no live service is replaced.
// The fixture retains real project-scoped grants and the approval policy.
func stepAuthorityRunnerFixture(t *testing.T, spec schema.Spec, runners map[string]Runner) *stepAuthorityTestRig {
	t.Helper()
	var automatic []string
	for kind := range runners {
		automatic = append(automatic, kind)
	}
	slices.Sort(automatic)
	db := testDB(t)
	directory := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := directory.SeedAdministrator(context.Background(), "boss"); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"owner", "outsider"} {
		if _, err := directory.EnsureUser(context.Background(), user); err != nil {
			t.Fatal(err)
		}
	}
	projectID := workflowTestProject(t, db, "game", "G", "")
	otherID := workflowTestProject(t, db, "other", "O", "")
	for _, g := range []identity.Spec{
		{SubjectType: "user", Subject: "owner", Role: access.RoleProjectApprover, Scope: access.ScopeProject, ScopeID: fmt.Sprint(projectID), Origin: "fixture", By: "boss"},
		{SubjectType: "user", Subject: "owner", Role: access.RoleProjectMaintainer, Scope: access.ScopeProject, ScopeID: fmt.Sprint(projectID), Origin: "fixture", By: "boss"},
		{SubjectType: "user", Subject: "outsider", Role: access.RoleProjectApprover, Scope: access.ScopeProject, ScopeID: fmt.Sprint(otherID), Origin: "fixture", By: "boss"},
		{SubjectType: "user", Subject: "outsider", Role: access.RoleProjectMaintainer, Scope: access.ScopeProject, ScopeID: fmt.Sprint(otherID), Origin: "fixture", By: "boss"},
	} {
		if _, err := directory.Grant(context.Background(), g, ""); err != nil {
			t.Fatal(err)
		}
	}
	change, err := NewCreator(db, nil).Create(context.Background(), ChangeDraft{ProjectID: projectID, Title: "t"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	admissions := newRunAdmissions(db, NewStages(nil, func(kind string) bool { return slices.Contains(automatic, kind) }), nil)
	workflowName := "test"
	if spec.Role == "landing" {
		workflowName = "land"
	}
	run, err := admissions.Create(context.Background(), StartRequest{ProjectID: projectID, ChangeID: change.ID, WorkflowName: workflowName, InputsJSON: "{}", RequestedBy: "owner"}, PreparedRun{Workflow: DefinitionRef{Name: workflowName, Version: 1}, Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	if err := admissions.Prepared(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	stepID := admittedStepID(t, queries{db: db}, run, spec.Stages[0].Steps[0].Name)
	projects := project.New(db, identity.Scoped(directory, "project"))
	authorizer := access.Checked{Authority: identity.Scoped(directory, "workflow"), Projects: projects}
	service := New(context.Background(), db, Dependencies{Authority: authorizer, Automatic: automatic, Runners: runners})
	t.Cleanup(service.Stop)
	return &stepAuthorityTestRig{DB: db, Work: service, Access: directory, Authorizer: authorizer, StepID: stepID, RunID: run.ID, ProjectID: projectID, OtherProjectID: otherID, ChangeID: change.ID}
}

func stepOverrideAuthorityFixture(t *testing.T) *stepAuthorityTestRig {
	t.Helper()
	a := stepAuthorityFixture(t, ApprovalConfig{})
	id := insertStoredStep(t, a.DB, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: a.RunID, Stage: "review", Name: "gate", Type: "script", Status: StatusRunning.String()})
	if err := requireAttemptWrite(workflowsql.New(a.DB.SQL()).StartParentCall(context.Background(), workflowsql.StartParentCallParams{ID: id, Attempt: 0, OldStatus: StatusRunning.String(), Status: StatusFail.String(), EvidenceJson: []byte(`{"reason":"failed check"}`)})); err != nil {
		t.Fatal(err)
	}
	a.OverrideStepID = id
	return a
}

func stepRollbackFixture(t *testing.T) (*stepAuthorityTestRig, context.Context, ids.ID) {
	t.Helper()
	spec := retryWorkflowSpec()
	spec.Stages[0].Steps = append(spec.Stages[0].Steps, schema.Step{Name: "accept", Uses: "approval", With: map[string]any{"approvers": []string{"owner"}}})
	store, run, _, _ := retryFixtureWithSpec(t, spec)
	db := store.db
	id := admittedStepID(t, queries{db: db}, run, "accept")
	directory := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := directory.SeedAdministrator(context.Background(), "boss"); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.EnsureUser(context.Background(), "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: "owner", Role: access.RoleProjectApprover, Scope: access.ScopeProject, ScopeID: fmt.Sprint(run.ProjectID), Origin: "fixture", By: "boss"}, ""); err != nil {
		t.Fatal(err)
	}
	checked := access.Checked{Authority: identity.Scoped(directory, "workflow"), Projects: project.New(db, identity.Scoped(directory, "project"))}
	work := New(context.Background(), db, Dependencies{Authority: checked})
	t.Cleanup(work.Stop)
	return &stepAuthorityTestRig{DB: db, Work: work, Access: directory, Authorizer: checked, StepID: id, RunID: run.ID}, stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"}), id
}

// Keep the original independent observation of Remediation alongside Work's
// rows: a failed vote must not publish an unexpected item after the transaction.
func stepRollbackSnapshot(t *testing.T, db *database.DB) string {
	t.Helper()
	var snapshot string
	// sql-guarantee: an independent whole-table snapshot of runs, steps, events and Remediation items; matching writer/reader bugs must not hide a rollback leak.
	err := db.SQL().QueryRowContext(context.Background(), `SELECT jsonb_build_array(
        (SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM runs r),
        (SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM steps s),
        (SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM run_events e),
        (SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM remediation_items m))::text`).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestStepApprovalEnforcesAuthorityWithoutHTTP(t *testing.T) {
	t.Parallel()
	a := stepAuthorityFixture(t, ApprovalConfig{Quorum: 3, Approvers: []string{"owner", "outsider", "boss", "run/1"}})
	for _, tc := range []struct {
		caller access.Principal
		code   string
	}{
		{access.Principal{Kind: "user", Name: "outsider", Via: "token"}, access.DenyOutsideScope},
		{access.Principal{Kind: "user", Name: "owner", Via: "oauth", Scopes: []string{"apptown:read"}}, access.DenyDelegationNarrow},
		{access.Principal{Kind: "instrument", Name: "run/1", Instrument: "run", Via: "run", ProjectID: idstest.N(1)}, access.DenyDelegationNarrow},
		{access.Principal{Kind: "instrument", Name: "boss via console/c", Person: "boss", Instrument: "console", Via: "console", ProjectID: idstest.N(1)}, access.DenyDelegationNarrow},
	} {
		step, err := a.Work.Approve(stepVerifiedContext(context.Background(), tc.caller), Approval{StepID: a.StepID, Decision: VoteApprove, Comment: "reviewed"})
		var denied *DecisionDenied
		if step != nil || !errors.As(err, &denied) || denied.Code != tc.code {
			t.Fatalf("unauthorized %+v: %+v, %v", tc.caller, step, err)
		}
	}
	caller := access.Principal{Kind: "user", Name: "owner", Via: "token"}
	disabled := true
	if err := a.Access.SaveUser(context.Background(), identity.AccountChange{Username: "owner", Disabled: &disabled}, testAdmittedPrincipal(t, a.Access, "boss")); err != nil {
		t.Fatal(err)
	}
	_, err := a.Work.Approve(stepVerifiedContext(context.Background(), caller), Approval{StepID: a.StepID, Decision: VoteApprove, Comment: ""})
	var denied *DecisionDenied
	if !errors.As(err, &denied) || denied.Code != access.DenyAccountDisabled {
		t.Fatalf("disabled caller: %v", err)
	}
	disabled = false
	if err := a.Access.SaveUser(context.Background(), identity.AccountChange{Username: "owner", Disabled: &disabled}, testAdmittedPrincipal(t, a.Access, "boss")); err != nil {
		t.Fatal(err)
	}
	wantEventCount(t, a.DB, 0, ofKind("approval"))
	a.DB.Pool().SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	step, err := a.Work.Approve(stepVerifiedContext(ctx, caller), Approval{StepID: a.StepID, Decision: VoteApprove, Comment: "checked"})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := ReadApprovalEvidence(step.Evidence)
	if err != nil || len(evidence.Decisions) != 1 || evidence.Decisions[0].Approver != "owner" {
		t.Fatalf("verified vote: %+v %v", evidence, err)
	}
	grants, err := a.Access.AllGrants(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range grants {
		if grant.SubjectType == "user" && grant.Subject == "owner" {
			if _, err := a.Access.Revoke(ctx, grant.ID, "boss"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := a.Work.Approve(stepVerifiedContext(ctx, caller), Approval{StepID: a.StepID, Decision: VoteReject, Comment: "changed my mind"}); err == nil {
		t.Fatal("revoked authority voted")
	}
	wantEventCount(t, a.DB, 1, ofKind("approval"))
}

func TestStepApprovalChecksCurrentApproverMembership(t *testing.T) {
	t.Parallel()
	for _, membership := range []string{"active", "revoked", "expired", "retired group", "same named user"} {
		t.Run(membership, func(t *testing.T) {
			t.Parallel()
			approvers := []string{"group:reviewers"}
			if membership == "same named user" {
				approvers = []string{"reviewers"}
			}
			a := stepAuthorityFixture(t, ApprovalConfig{Approvers: approvers})
			if err := a.Access.SaveGroup(context.Background(), identity.GroupChange{Name: "reviewers", Members: []string{"owner"}}, testAdmittedPrincipal(t, a.Access, "boss")); err != nil {
				t.Fatal(err)
			}
			switch membership {
			case "revoked":
				if err := a.Access.SaveGroup(context.Background(), identity.GroupChange{Name: "reviewers"}, testAdmittedPrincipal(t, a.Access, "boss")); err != nil {
					t.Fatal(err)
				}
			case "expired":
				// sql-guarantee: Expire the existing reviewer membership before approval while leaving its group live; membership commands insert or delete rows and cannot test the expiry filter.
				completionExec(t, a.DB, `UPDATE group_members SET expires_at=now()-interval '1 second' WHERE group_name='reviewers'`)
			case "retired group":
				// sql-guarantee: Retire the existing reviewer group while retaining its membership; SaveGroup preserves retired_at and deleting the group would mask the retirement check.
				completionExec(t, a.DB, `UPDATE groups SET retired_at=now() WHERE name='reviewers'`)
			}
			_, err := a.Work.Approve(stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"}), Approval{StepID: a.StepID, Decision: VoteApprove, Comment: ""})
			if membership == "active" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var denied *DecisionDenied
			if !errors.As(err, &denied) || denied.Code != access.DenyNotEligible {
				t.Fatalf("%s: %v", membership, err)
			}
			wantEventCount(t, a.DB, 0, ofKind("approval"))
		})
	}
}

func TestStepApprovalRequiredAuthorityReadsFailClosed(t *testing.T) {
	t.Parallel()
	for _, statement := range []string{"-- name: UserByName :one", "SELECT p.default_approvers", "SELECT m.group_name", "-- name: ActiveGrants :many", "-- name: ProjectByID :one"} {
		t.Run(statement, func(t *testing.T) {
			t.Parallel()
			a := stepAuthorityFixture(t, ApprovalConfig{Approvers: []string{"group:reviewers", "role:project-approver"}})
			if err := a.Access.SaveGroup(context.Background(), identity.GroupChange{Name: "reviewers"}, testAdmittedPrincipal(t, a.Access, "boss")); err != nil {
				t.Fatal(err)
			}
			dbtest.FailNextStatement(t, a.DB, statement)
			_, err := a.Work.Approve(stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"}), Approval{StepID: a.StepID, Decision: VoteApprove, Comment: ""})
			var denied *DecisionDenied
			if err == nil || errors.As(err, &denied) {
				t.Fatalf("unavailable authority became a policy decision: %v", err)
			}
			wantEventCount(t, a.DB, 0, ofKind("approval"))
			wantEqual(t, "step evidence", string(wantStepState(t, a.DB, a.StepID, StatusPending).Evidence), "{}")
		})
	}
}

func TestStepDecisionsRejectMissingCaller(t *testing.T) {
	t.Parallel()
	s := New(context.Background(), testDB(t), Dependencies{})
	t.Cleanup(s.Stop)
	s.Stop()
	for _, caller := range []access.Principal{{}, {Kind: "user"}, {Kind: "anonymous", Name: "anonymous"}, {Kind: "invalid", Name: "alice"}} {
		if _, err := s.Override(stepVerifiedContext(context.Background(), caller), Override{StepID: idstest.N(1), Reason: "reviewed"}); !errors.Is(err, ErrCallerRequired) {
			t.Fatal(err)
		}
		if _, err := s.Approve(stepVerifiedContext(context.Background(), caller), Approval{StepID: idstest.N(1), Decision: VoteApprove, Comment: ""}); !errors.Is(err, ErrCallerRequired) {
			t.Fatal(err)
		}
	}
}

// Two approvers deciding a quorum of two at once both count, from the saved
// evidence: the step passes with both votes, who cast them and the meaning
// of approval the step declared, each kept in the evidence.
func TestStepApprovalConcurrentQuorumUsesCurrentEvidence(t *testing.T) {
	t.Parallel()
	a := stepAuthorityFixture(t, ApprovalConfig{Quorum: 2, Approvers: []string{"owner", "boss"}, Means: "we both read it"})
	arrived := make(chan struct{}, 2)
	ready := make(chan struct{})
	original := a.Authorizer
	a.Authorizer = stepPermissionFunc(func(ctx context.Context, p access.Permission) (access.Caller, error) {
		actor, err := original.Require(ctx, p)
		if p.Capability != access.CapChangeApprove || err != nil {
			return actor, err
		}
		arrived <- struct{}{}
		select {
		case <-ready:
		case <-ctx.Done():
			return access.Caller{}, ctx.Err()
		}
		return actor, err
	})
	a.Work = New(context.Background(), a.DB, Dependencies{Authority: a.Authorizer})
	t.Cleanup(a.Work.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errs := make(chan error, 2)
	for _, who := range []string{"owner", "boss"} {
		go func() {
			_, err := a.Work.Approve(stepVerifiedContext(ctx, access.Principal{Kind: "user", Name: who, Via: "session"}), Approval{StepID: a.StepID, Decision: VoteApprove, Comment: ""})
			errs <- err
		}()
	}
	for range 2 {
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("authority reads did not finish")
		}
	}
	close(ready)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	evidence := mustApprovalEvidence(t, wantStepState(t, a.DB, a.StepID, StatusPass).Evidence)
	var voters []string
	for _, vote := range evidence.Decisions {
		voters = append(voters, vote.Approver)
	}
	slices.Sort(voters)
	if !slices.Equal(voters, []string{"boss", "owner"}) || evidence.Quorum != 2 || evidence.Approvals != 2 || evidence.Means != "we both read it" {
		t.Fatalf("the evidence must keep every vote, its voter and the declared meaning: %+v", evidence)
	}
	wantEventCount(t, a.DB, 2, ofKind("approval"))
}

func TestStepOverrideEnforcesAuthorityWithoutHTTP(t *testing.T) {
	t.Parallel()
	a := stepOverrideAuthorityFixture(t)

	eventsBefore := len(storedEvents(t, a.DB))
	for _, caller := range []access.Principal{
		{Kind: "user", Name: "outsider", Via: "token"},
		{Kind: "user", Name: "owner", Via: "oauth", Scopes: []string{"apptown:read"}},
		{Kind: "instrument", Name: "run/1", Instrument: "run", Via: "run", ProjectID: idstest.N(1)},
		{Kind: "instrument", Name: "owner via console/c", Person: "owner", Instrument: "console", Via: "console", ProjectID: idstest.N(1)},
	} {
		step, err := a.Work.Override(stepVerifiedContext(context.Background(), caller), Override{StepID: a.OverrideStepID, Reason: "reviewed"})
		var denied *DecisionDenied
		if step != nil || !errors.As(err, &denied) {
			t.Fatalf("unauthorized %+v: %+v, %v", caller, step, err)
		}
	}
	wantEventCount(t, a.DB, eventsBefore, anyEvent)
	caller := access.Principal{Kind: "user", Name: "owner", Via: "token"}
	disabled := true
	if err := a.Access.SaveUser(context.Background(), identity.AccountChange{Username: "owner", Disabled: &disabled}, testAdmittedPrincipal(t, a.Access, "boss")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.Override(stepVerifiedContext(context.Background(), caller), Override{StepID: a.OverrideStepID, Reason: "reviewed"}); err == nil {
		t.Fatal("disabled actor retained access")
	}
	disabled = false
	if err := a.Access.SaveUser(context.Background(), identity.AccountChange{Username: "owner", Disabled: &disabled}, testAdmittedPrincipal(t, a.Access, "boss")); err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, a.DB, "-- name: UserByName :one")
	if _, err := a.Work.Override(stepVerifiedContext(context.Background(), caller), Override{StepID: a.OverrideStepID, Reason: "reviewed"}); err == nil {
		t.Fatal("failed account lookup permitted an override")
	}
	grants, err := a.Access.AllGrants(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range grants {
		if grant.SubjectType == "user" && grant.Subject == "owner" {
			if _, err := a.Access.Revoke(context.Background(), grant.ID, "boss"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := a.Work.Override(stepVerifiedContext(context.Background(), caller), Override{StepID: a.OverrideStepID, Reason: "reviewed"}); err == nil {
		t.Fatal("revoked grants retained access")
	}
	wantEventCount(t, a.DB, eventsBefore, anyEvent)
	step, err := a.Work.Override(stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "boss", Via: "token", Role: "admin"}), Override{StepID: a.OverrideStepID, Reason: "reviewed"})
	if err != nil || step.OverrideBy != "boss" {
		t.Fatalf("authorized override: %+v, %v", step, err)
	}
}

func TestStepOverrideAuthorityDoesNotHoldAPoolConnection(t *testing.T) {
	t.Parallel()
	a := stepOverrideAuthorityFixture(t)

	a.DB.Pool().SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	step, err := a.Work.Override(stepVerifiedContext(ctx, access.Principal{Kind: "user", Name: "owner", Via: "token"}), Override{StepID: a.OverrideStepID, Reason: "reviewed"})
	if err != nil || step == nil || step.OverrideBy != "owner" {
		t.Fatalf("one-connection admission: %+v, %v", step, err)
	}
}

func TestStepApprovalPublishesNothingUntilRequiredTransactionCommits(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"vote", "receipt", "missing receipt", "commit", "return snapshot"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			a, ctx, id := stepRollbackFixture(t)
			var effects atomic.Int64
			a.Work.Stop()
			a.Work = New(context.Background(), a.DB, Dependencies{Authority: a.Authorizer, Automatic: []string{"agent/pi", "agent/review"}, Runners: map[string]Runner{"agent/pi": func(*Execution) { effects.Add(1) }}})
			t.Cleanup(a.Work.Stop)
			before := stepRollbackSnapshot(t, a.DB)
			switch failure {
			case "vote":
				dbtest.FailNextStatement(t, a.DB, "-- name: RecordApproval :execrows")
			case "receipt":
				dbtest.FailNextStatement(t, a.DB, "INSERT INTO run_events")
			case "return snapshot":
				dbtest.FailNextStatement(t, a.DB, "-- name: StepByID :one")
			default:
				body, trigger := "RETURN NULL;", "CREATE TRIGGER reject_vote_receipt BEFORE INSERT ON run_events"
				if failure == "commit" {
					body = "RAISE EXCEPTION 'vote receipt refused at commit';"
					trigger = "CREATE CONSTRAINT TRIGGER reject_vote_receipt AFTER INSERT ON run_events DEFERRABLE INITIALLY DEFERRED"
				}
				completionExec(t, a.DB, `CREATE FUNCTION reject_vote_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$`)
				completionExec(t, a.DB, trigger+` FOR EACH ROW EXECUTE FUNCTION reject_vote_receipt()`)
			}
			step, err := a.Work.Approve(ctx, Approval{StepID: id, Decision: VoteReject, Comment: "needs more work"})
			if step != nil || err == nil {
				t.Fatalf("%s admitted: %+v %v", failure, step, err)
			}
			if after := stepRollbackSnapshot(t, a.DB); after != before {
				t.Fatalf("%s left partial Work state", failure)
			}
			a.Work.Wait()
			if effects.Load() != 0 {
				t.Fatalf("%s published before commit", failure)
			}
			completionExec(t, a.DB, `DROP FUNCTION IF EXISTS reject_vote_receipt() CASCADE`)
			if _, err := a.Work.Approve(ctx, Approval{StepID: id, Decision: VoteReject, Comment: "needs more work"}); err != nil {
				t.Fatalf("explicit rerun: %v", err)
			}
			a.Work.Wait()
			if effects.Load() != 1 {
				t.Fatalf("committed rejection should dispatch once: %d", effects.Load())
			}
		})
	}
}

// This command-only double deliberately cannot authorize aggregate reads.
func (stepPermissionFunc) VisibleProjects(context.Context, access.Capability) ([]ids.ID, error) {
	return nil, access.ErrAuthorityUnavailable
}

// MayApprove answers the same question a vote does, without voting: the
// change page says "waiting on you" only to someone the rules let decide.
func TestMayApproveAsksTheVoteRulesWithoutVoting(t *testing.T) {
	t.Parallel()
	a := stepAuthorityFixture(t, ApprovalConfig{Approvers: []string{"role:" + access.RoleProjectApprover}})
	owner := stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"})
	if err := a.Work.MayApprove(owner, a.StepID); err != nil {
		t.Fatalf("the project's approver may approve: %v", err)
	}
	outsider := stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "outsider", Via: "token"})
	var denied *DecisionDenied
	if err := a.Work.MayApprove(outsider, a.StepID); !errors.As(err, &denied) {
		t.Fatalf("another project's approver is refused: %v", err)
	}
	got, err := a.Work.Step(owner, a.StepID)
	if err != nil || got.Step.Status != StatusPending {
		t.Fatalf("asking decided the step: %+v %v", got.Step, err)
	}
	evidence, err := ReadApprovalEvidence(got.Step.Evidence)
	if err != nil || len(evidence.Decisions) != 0 {
		t.Fatalf("asking voted: %+v %v", evidence, err)
	}
}

// The offer to close a change follows the abandon permission, and asking
// closes nothing.
func TestMayAbandonFollowsTheAbandonPermissionWithoutAbandoning(t *testing.T) {
	t.Parallel()
	a := stepAuthorityFixture(t, ApprovalConfig{Approvers: []string{"owner"}})
	owner := stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "token"})
	if err := a.Work.MayAbandon(owner, a.ChangeID); err != nil {
		t.Fatalf("the project's maintainer may close its change: %v", err)
	}
	outsider := stepVerifiedContext(context.Background(), access.Principal{Kind: "user", Name: "outsider", Via: "token"})
	if err := a.Work.MayAbandon(outsider, a.ChangeID); err == nil {
		t.Fatal("another project's maintainer was offered the close")
	}
	got, err := a.Work.Change(owner, a.ChangeID)
	if err != nil || got.State != ChangeInProgress {
		t.Fatalf("asking closed the change: %+v %v", got, err)
	}
}

// An approval's receipt in the run's history names the credential the vote
// came through, as its evidence does: a vote through a person's CLI token
// does not read as the person clicking Approve.
func TestAnApprovalReceiptNamesTheCredentialItCameThrough(t *testing.T) {
	t.Parallel()
	a := stepAuthorityFixture(t, ApprovalConfig{Approvers: []string{"owner"}})
	caller := access.Principal{Kind: "user", Name: "owner", Via: "token", Credential: "token:claude-migrations-owner"}
	if _, err := a.Work.Approve(stepVerifiedContext(context.Background(), caller), Approval{StepID: a.StepID, Decision: VoteApprove, Comment: "delegated"}); err != nil {
		t.Fatal(err)
	}
	snap, err := a.Work.Snapshot(stepVerifiedContext(context.Background(), caller), a.RunID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var receipts []map[string]string
	for _, e := range snap.Events {
		if e.Kind == "approval" {
			var p map[string]string
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			receipts = append(receipts, p)
		}
	}
	if len(receipts) != 1 || receipts[0]["by"] != "owner" || receipts[0]["via"] != "token-cli:claude-migrations-owner" {
		t.Fatalf("approval receipts: %v", receipts)
	}
}
