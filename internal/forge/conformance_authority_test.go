package forge

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/httpapi"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/schema"
)

func TestConformanceServiceEnforcesCurrentCapabilitiesAndProjectScope(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	revokeConformanceDefaultRead(t, a)
	outsider := verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "outsider", Via: "token"})
	if _, err := a.conformance.Dispositions(outsider, idstest.N(1), true); err == nil {
		t.Fatal("outsider read private project")
	}
	if _, err := a.conformance.Dispositions(outsider, idstest.N(2), true); err != nil {
		t.Fatalf("project grant refused: %v", err)
	}
	if _, err := a.conformance.Reviews(outsider, 10); err == nil {
		t.Fatal("project grant admitted fleet read")
	}
	if _, err := a.conformance.BeginReview(outsider, conformance.ReviewRequest{}); err == nil {
		t.Fatal("outsider admitted review")
	}
	request := conformance.ExceptionRequest{ProjectID: idstest.N(1), Identifier: "fixture", Kind: conformance.AcceptedRisk, Justification: "reviewed"}
	if _, err := a.conformance.CreateDisposition(outsider, request); err == nil {
		t.Fatal("outsider created an exception")
	}
	if reviews, dispositions := conformanceRecordCounts(t, a, idstest.N(1), idstest.N(2)); reviews != 0 || dispositions != 0 {
		t.Fatalf("refused commands recorded %d reviews, %d dispositions", reviews, dispositions)
	}
	boss := verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "boss", Via: "token", Role: "admin"})
	saved, err := a.conformance.CreateDisposition(boss, request)
	if err != nil || saved.By != "boss" {
		t.Fatalf("verified actor: %+v %v", saved, err)
	}
	if _, err := a.conformance.RevokeDisposition(outsider, saved.ID); err == nil {
		t.Fatal("outsider revoked another project's exception")
	}
	if _, err := a.conformance.RevokeDisposition(boss, saved.ID); err != nil {
		t.Fatal(err)
	}
	run := verifiedContext(context.Background(), access.Principal{Kind: "instrument", Name: "run/1", Instrument: "run", Via: "run", ProjectID: idstest.N(1)})
	if _, err := a.conformance.Dispositions(run, idstest.N(1), true); err != nil {
		t.Fatalf("own run read: %v", err)
	}
	if _, err := a.conformance.Dispositions(run, idstest.N(2), true); err == nil {
		t.Fatal("run read another project")
	}
	if _, err := a.conformance.View(run, ids.Nil); err == nil {
		t.Fatal("run read fleet")
	}
	if _, err := a.conformance.CreateDisposition(run, request); err == nil {
		t.Fatal("run made a human exception decision")
	}
	testAdministrator(t, a, "other-admin")
	disabled := true
	if err := a.Access.SaveUser(context.Background(), identity.AccountChange{Username: "boss", Role: "admin", Disabled: &disabled}, access.Principal{Kind: "user", Name: "other-admin", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.conformance.Dispositions(boss, idstest.N(1), true); err == nil {
		t.Fatal("disabled actor retained access")
	}
}

func TestConformanceCallerMustBeExplicitEvenOnAnOpenInstance(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	if _, err := a.conformance.Reviews(context.Background(), 10); !errors.Is(err, conformance.ErrCallerRequired) {
		t.Fatalf("missing internal caller: %v", err)
	}
	testAccounts(t, a.DB, "operator")
	if _, err := a.conformance.Reviews(testConformanceContext("operator"), 10); err != nil {
		t.Fatalf("explicit bootstrap operator: %v", err)
	}
	dbtest.FailNextStatement(t, a.DB, "-- name: UserByName :one")
	if _, err := a.conformance.Reviews(testConformanceContext("operator"), 10); err == nil {
		t.Fatal("unavailable account check became bootstrap permission")
	}
}

func TestConformanceHTTPUsesServiceAuthority(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	revokeConformanceDefaultRead(t, a)
	h := a.Handler()
	for _, path := range []string{"/api/v1/conformance", "/api/v1/conformance/reviews", "/api/v1/conformance/reviews/0000000f-423f-7000-8000-000000000000"} {
		response := authzCall(h, "outsider", "GET", path, "")
		if response.Code != 403 {
			t.Fatalf("%s: unauthorized HTTP %d %s", path, response.Code, response.Body.String())
		}
	}
	response := authzCall(h, "boss", "GET", "/api/v1/conformance/reviews", "")
	if response.Code != 200 {
		t.Fatalf("authorized HTTP %d %s", response.Code, response.Body.String())
	}
	// Direct adapters do not implicitly mint an internal caller either.
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	httpapi.NewConformance(a.conformance, a.Projects, nil).ListReviews(rec, req.WithContext(callerContext(req)))
	if rec.Code != 403 {
		t.Fatalf("missing context HTTP %d", rec.Code)
	}
}

func TestConformanceWorkerReadMandateUsesRecordedRunScope(t *testing.T) {
	t.Parallel()
	a, _, run := completionScenario(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}})
	ctx := conformance.WithRun(context.Background(), a.Work.RunScope(run.ID))
	if _, err := a.conformance.Dispositions(ctx, run.ProjectID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.conformance.Dispositions(ctx, idstest.N(999999), true); err == nil {
		t.Fatal("worker scope expanded")
	}
	if _, err := a.conformance.Reviews(ctx, 10); err == nil {
		t.Fatal("worker read estate")
	}
	if _, err := a.conformance.BeginReview(ctx, conformance.ReviewRequest{Projects: []ids.ID{run.ProjectID}}); err == nil {
		t.Fatal("ordinary worker admitted review")
	}
}

func TestReviewWithMissingRepositoryFailsExplicitly(t *testing.T) {
	t.Parallel()
	a, id := reviewFixture(t)
	testAdministrator(t, a, "operator")
	if err := os.RemoveAll(a.projectRepoPath("reviewed")); err != nil {
		t.Fatal(err)
	}
	review, verdicts, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("operator"), "manual", conformance.ReviewOptions{Projects: []ids.ID{id}})
	if !errors.Is(err, conformance.ErrSource) || review == nil || review.Status != conformance.ReviewError || len(verdicts) != 0 {
		t.Fatalf("missing source looked successful: %+v %+v %v", review, verdicts, err)
	}
	saved, err := savedReview(a, review.ID)
	if err != nil || saved.Status != conformance.ReviewError || saved.Errors != 1 {
		t.Fatalf("saved failure: %+v %v", saved, err)
	}
}

func TestHistoryMaintenanceRequiresItsOwnCapability(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	a.addGrant("user", "outsider", "", string(access.CapConformanceRev), access.ScopeInstance, "", "fixture", "boss", "")
	ctx := verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "outsider", Via: "token"})
	review, err := a.conformance.BeginReview(ctx, conformance.ReviewRequest{Projects: []ids.ID{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Fixes.ClearHistory(ctx)
	var denied *access.Denied
	if !errors.As(err, &denied) || result != nil {
		t.Fatalf("review authority became history deletion: %+v %v", result, err)
	}
	if _, err := review.Finish(context.Background(), conformance.Completion{}, conformance.ReviewDone); err != nil {
		t.Fatal(err)
	}
	boss := verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "boss", Via: "token", Role: "admin"})
	if _, err := a.Fixes.ClearHistory(boss); err != nil {
		t.Fatalf("instance administration refused: %v", err)
	}
}

func TestExceptionRequiresApprovalBeforeTakingEffect(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	a.addGrant("user", "kid", access.RoleProjectMaintainer, "", access.ScopeProject, idstest.N(1).String(), "fixture", "boss", "")
	ctx := verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "kid", Via: "session"})
	request := conformance.ExceptionRequest{ProjectID: idstest.N(1), Identifier: "review-me", Kind: conformance.AcceptedRisk, Justification: "risk reviewed"}
	_, err := a.conformance.CreateDisposition(ctx, request)
	var denied *access.Denied
	if !errors.As(err, &denied) || denied.Code != access.DenyOutsideScope {
		t.Fatalf("maintainer accepted risk without approval: %v", err)
	}
	response := authzCall(a.Handler(), "kid", "POST", "/api/v1/projects/game/dispositions", `{"identifier":"review-me","status":"accepted","justification":"risk reviewed"}`)
	if response.Code != 403 {
		t.Fatalf("HTTP accepted risk without approval: %d %s", response.Code, response.Body.String())
	}
	if _, dispositions := conformanceRecordCounts(t, a, idstest.N(1), idstest.N(2)); dispositions != 0 {
		t.Fatalf("refused exception recorded %d dispositions", dispositions)
	}
	if n := len(ledgerEntries(t, a, ids.Nil, "disposition.created")); n != 0 {
		t.Fatalf("refused exception left %d receipts", n)
	}
	a.addGrant("user", "kid", "", string(access.CapExceptionApprove), access.ScopeProject, idstest.N(1).String(), "fixture", "boss", "")
	saved, err := a.conformance.CreateDisposition(ctx, request)
	if err != nil || saved.By != "kid" {
		t.Fatalf("explicit approval authority: %+v %v", saved, err)
	}
}

func TestConformancePolicyReadFailurePrecedesReview(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	// This is a policy read failure, not a deny decision. Neither can admit work.
	dbtest.FailNextStatement(t, a.DB, "-- name: ActiveGrants :many")
	if review, err := a.conformance.BeginReview(asPerson("boss"), conformance.ReviewRequest{Projects: []ids.ID{}}); err == nil || review != nil {
		t.Fatalf("failed policy read admitted review: %+v %v", review, err)
	}
	if reviews, _ := conformanceRecordCounts(t, a); reviews != 0 {
		t.Fatalf("failed policy read recorded %d reviews", reviews)
	}
}

// Revoke through Access so these integration tests exercise the live grant
// lifecycle. The administrative scope read is complete and has no row limit.
func revokeConformanceDefaultRead(t *testing.T, a *App) {
	t.Helper()
	grants, err := a.Access.GrantsAt(context.Background(), access.ScopeInstance, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range grants {
		if grant.SubjectType == "group" && grant.Subject == identity.GroupAllUsers && grant.RoleName == access.RoleAuditor {
			revoked, err := a.Access.Revoke(context.Background(), grant.ID, "boss")
			if err != nil || !revoked {
				t.Fatalf("revoke default read: %t %v", revoked, err)
			}
			return
		}
	}
	t.Fatal("missing default read grant")
}
