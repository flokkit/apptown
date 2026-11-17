package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/schema"
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
	if err := a.Access.SaveUser(context.Background(), identity.AccountChange{Username: "boss", Role: "admin", Disabled: &disabled}, access.Principal{ID: testPrincipalOf(t, a.DB, "other-admin"), Kind: "user", Name: "other-admin", Via: "session"}); err != nil {
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
	setTestFeatureAs(t, a, "conformance-estate", true, "boss")
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

func TestExceptionRequiresApprovalBeforeTakingEffect(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	a.addGrant("user", "kid", access.RoleProjectMaintainer, "", access.ScopeProject, idstest.N(1).String(), "boss", "")
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
	a.addGrant("user", "kid", "", string(access.CapExceptionApprove), access.ScopeProject, idstest.N(1).String(), "boss", "")
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

// Rechecking coordinates standards admission and security scans through their
// real transport and owners, preserving independent project authority.
func TestConformanceRecheckRequestsOnlyUnknownNamedScansAsCaller(t *testing.T) {
	for _, tc := range []struct {
		name, verdict                              string
		denied, noRecheck, estate, noConsent, busy bool
		want                                       int
	}{
		{name: "failed", verdict: "error", want: 1},
		{name: "not yet scanned", want: 1},
		{name: "known", verdict: "pass"},
		{name: "known with findings", verdict: "fail"},
		{name: "no change creation authority", denied: true},
		{name: "ordinary review", noRecheck: true},
		{name: "whole estate", estate: true},
		{name: "standing refusal", noConsent: true},
		{name: "busy queue", busy: true, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := authzActors(t)
			person, err := a.Access.EnsurePerson(t.Context(), "boss")
			if err != nil {
				t.Fatal(err)
			}
			admin := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: person.PrincipalID, Kind: "user", Name: "boss", Via: "session"}, Verified: true})
			a.SyncStock(admin)
			pid, main, git := fleetRepo(t, a, "clock", map[string]string{"README.md": "source"})
			platform, err := a.Ways.Pack(admin, "apptown")
			if err != nil {
				t.Fatal(err)
			}
			if err := a.DB.InTx(admin, func(tx *database.Tx) error {
				return adoption.SetMembershipIn(admin, tx, pid, []adoption.Membership{{Pack: platform.Name, Version: platform.Version}}, "fixture", "boss", a.cfg.clock.Now())
			}); err != nil {
				t.Fatal(err)
			}
			if err := recordCatalogFixture(a.Adoption, admin, pid, "boss"); err != nil {
				t.Fatal(err)
			}
			if !tc.noConsent {
				if _, err := a.standingRules.Save(admin, workflow.JanitorChoicesCommand{Choices: workflow.JanitorChoices{Dependencies: "routine", SecurityFixes: true}}); err != nil {
					t.Fatal(err)
				}
			}
			release := make(chan struct{})
			rebuildWork(t, a, func(d *workflow.Dependencies) {
				d.Runners["scan"] = func(e *workflow.Execution) { <-release }
			})
			t.Cleanup(func() { close(release) })
			if tc.verdict != "" {
				now := a.Work.Now()
				o := conformance.ScanObservation{ID: ids.New(now), ProjectID: pid, RunID: ids.New(now), StepID: ids.New(now), Attempt: 1,
					Subject: conformance.ScanSubject{On: "source", Commit: main, Tree: strings.TrimSpace(git("rev-parse", "HEAD^{tree}"))},
					Scanner: "osv", ExpectedScanners: []string{"osv"}, Verdict: tc.verdict, ObservedAt: now}
				if tc.verdict == "fail" {
					o.Findings = []conformance.Finding{{Finding: check.Finding{Identifiers: []string{"CVE-TEST"}}}}
				}
				if err := a.DB.InTx(admin, func(tx *database.Tx) error { return conformance.RecordScanObservationIn(admin, tx, o) }); err != nil {
					t.Fatal(err)
				}
			}
			directory := a.Access
			caller := admin
			if tc.denied {
				p, err := directory.EnsurePerson(t.Context(), "reviewer")
				if err != nil {
					t.Fatal(err)
				}
				for _, cap := range []access.Capability{access.CapProjectRead, access.CapConformanceRev, access.CapChangeInvokeLane} {
					if _, err := directory.PutGrant(t.Context(), identity.Spec{SubjectType: "user", Subject: "reviewer", Capability: string(cap), Scope: access.ScopeInstance, By: "bls"}); err != nil {
						t.Fatal(err)
					}
				}
				caller = access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: p.PrincipalID, Kind: "user", Name: "reviewer", Via: "session"}, Verified: true})
				if _, err := a.Work.MayOpenWork(caller, pid); fault.KindOf(err) != fault.Forbidden {
					t.Fatalf("expected real work authority refusal: %v", err)
				}
			}
			scans, err := a.Work.LatestMainScans(caller, pid, 0)
			if err != nil || scans.Known != (tc.verdict == "pass" || tc.verdict == "fail") || scans.Failed != (tc.verdict == "error") {
				t.Fatalf("scan precondition: %+v %v", scans, err)
			}
			h := httpapi.NewConformanceReviews(a.Projects, fleetReviewsFor(a), a.Work, changeactions.Schedules{Packs: fleetReviewsFor(a), Standing: &a.standingRules, Actions: a.changeActions, Adoption: a.Adoption, Projects: a.Projects, DB: a.DB}, a.cfg.clock.Now)
			body := `{"projects":["clock"],"recheck":true}`
			if tc.noRecheck {
				body = `{"projects":["clock"]}`
			}
			if tc.estate {
				body = `{"recheck":true}`
			}
			call := func() {
				t.Helper()
				r := httptest.NewRequest("POST", "/api/v1/conformance/review", strings.NewReader(body)).WithContext(caller)
				response := httptest.NewRecorder()
				h.RunConformanceReview(response, r)
				var review conformance.Review
				if err := json.Unmarshal(response.Body.Bytes(), &review); err != nil || response.Code != 202 || review.ID.IsZero() || review.Status != conformance.ReviewQueued {
					t.Fatalf("review admission: %d %s %v", response.Code, response.Body.String(), err)
				}
			}
			call()
			if tc.busy {
				call()
			}
			attempts, err := a.Work.Attempts(admin, pid, "", 20)
			if err != nil || len(attempts) != tc.want {
				t.Fatalf("security attempts: got %+v want %d: %v", attempts, tc.want, err)
			}
			requests, err := audit.Recent(admin, a.DB, audit.Filter{ProjectID: pid, KindPrefix: "schedule.run-requested"})
			if err != nil || len(requests) != tc.want {
				t.Fatalf("scan request attribution: %+v %v", requests, err)
			}
			for _, request := range requests {
				if request.ActorPrincipal != person.PrincipalID {
					t.Fatalf("scan requester: %+v", request)
				}
			}
			for _, attempt := range attempts {
				if attempt.Queue != "security" || attempt.Schedule != "janitor-scan" {
					t.Fatalf("wrong queue: %+v", attempt)
				}
				runs, err := a.Work.ChangeRuns(admin, attempt.ChangeID, workflow.ChangeRuns{})
				if err != nil || len(runs) != 1 {
					t.Fatalf("security runs: %+v %v", runs, err)
				}
				if runs[0].ProjectID != pid || runs[0].WorkflowName != "janitor-scan" {
					t.Fatalf("wrong scan run: %+v", runs[0])
				}
			}
		})
	}
}
