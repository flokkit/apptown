package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type standingProcedureFixture struct {
	means string
	err   error
}

func (f *standingProcedureFixture) StandingProcedure(_ context.Context, _ database.Queryer, _ ids.ID, name string) (StandingProcedure, error) {
	return StandingProcedure{Acceptance: StandingPurpose{Workflow: name, Version: 1, Step: "accept", Means: f.means, Approvers: []string{"role:project-approver"}}, Landing: StandingPurpose{Workflow: "land", Version: 1, Step: "accept", Means: "land this qualified candidate", Approvers: []string{"role:project-approver"}}}, f.err
}
func standingFixture(t *testing.T) (*stepAuthorityTestRig, StandingRules, *standingProcedureFixture, context.Context, StandingDefinition) {
	t.Helper()
	a := stepAuthorityFixture(t, ApprovalConfig{})
	if _, err := a.Access.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: "owner", Role: access.RoleProjectOwner, Scope: access.ScopeProject, ScopeID: a.ProjectID.String(), Origin: "fixture", By: "boss"}, ""); err != nil {
		t.Fatal(err)
	}
	p := &standingProcedureFixture{means: "accept bounded routine updates after all checks pass"}
	s := NewStandingRules(a.DB, StandingDependencies{Authority: a.Authorizer, Identity: a.Access, Procedures: p})
	ctx := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "owner"))
	in := StandingDefinition{Projects: []StandingProject{{ProjectID: a.ProjectID, Workflow: "quick-change"}}, Categories: StandingCategories{Dependencies: "patch", SecurityFixes: true}, Delivery: "land-only"}
	return a, s, p, ctx, in
}
func enableStanding(t *testing.T, s StandingRules, ctx context.Context, in StandingDefinition) StandingRevision {
	t.Helper()
	q, err := s.Preview(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Enable(ctx, StandingCommand{Definition: in, ShownDigest: q.Digest})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestStandingConsentRetainsMeaningAndRefusesStaleEdits(t *testing.T) {
	a, s, p, ctx, in := standingFixture(t)
	r := enableStanding(t, s, ctx, in)
	if !r.Enabled || r.Issuer.Principal.IsZero() || r.Issuer.Name != "owner" {
		t.Fatalf("wrong consent attribution: %+v", r)
	}
	q, err := s.Preview(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	p.means = "different meaning"
	if _, err = s.Enable(ctx, StandingCommand{RuleID: r.RuleID, ExpectedRevision: 1, Definition: in, ShownDigest: q.Digest}); fault.KindOf(err) != fault.Conflict {
		t.Fatalf("stale shown meaning accepted: %v", err)
	}
	q, err = s.Preview(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.FailNextStatement(t, a.DB, "-- name: InsertStandingRevision :exec")
	if _, err = s.Enable(ctx, StandingCommand{RuleID: r.RuleID, ExpectedRevision: 1, Definition: in, ShownDigest: q.Digest}); err == nil {
		t.Fatal("failed history write reported success")
	}
	h, err := s.History(ctx, r.RuleID)
	if err != nil || len(h) != 1 || h[0].Question.Shown != r.Question.Shown {
		t.Fatalf("failed edit changed immutable history: %+v %v", h, err)
	}
	dbtest.FailNextStatement(t, a.DB, "-- name: AppendReceipt :execrows")
	if _, err = s.Enable(ctx, StandingCommand{RuleID: r.RuleID, ExpectedRevision: 1, Definition: in, ShownDigest: q.Digest}); err == nil {
		t.Fatal("failed audit receipt reported success")
	}
	h, err = s.History(ctx, r.RuleID)
	if err != nil || len(h) != 1 || h[0].Question.Shown != r.Question.Shown {
		t.Fatalf("failed receipt changed head/history: %+v %v", h, err)
	}
	updated, err := s.Enable(ctx, StandingCommand{RuleID: r.RuleID, ExpectedRevision: 1, Definition: in, ShownDigest: q.Digest})
	if err != nil || updated.Revision != 2 {
		t.Fatalf("valid revision: %+v %v", updated, err)
	}
	if _, err = s.Disable(ctx, r.RuleID, 1); fault.KindOf(err) != fault.Conflict {
		t.Fatalf("stale disable accepted: %v", err)
	}
	disabled, err := s.Disable(ctx, r.RuleID, 2)
	if err != nil || disabled.Enabled || disabled.Revision != 3 {
		t.Fatalf("disable: %+v %v", disabled, err)
	}
	h, err = s.History(ctx, r.RuleID)
	if err != nil || len(h) != 3 || h[0].Question.Shown != r.Question.Shown || h[2].Issuer.Principal != r.Issuer.Principal {
		t.Fatalf("history changed: %+v %v", h, err)
	}
}
func TestStandingConsentRequiresDirectVerifiedHumanEvenForAdministrator(t *testing.T) {
	a, s, _, _, in := standingFixture(t)
	base := testAdmittedPrincipal(t, a.Access, "boss")
	for _, way := range []string{"oauth", "agent", "run", "console", "system", "unknown", "unverified", "zero-id"} {
		t.Run(way, func(t *testing.T) {
			pr := base
			verified := true
			switch way {
			case "oauth":
				pr.Via = "oauth"
			case "agent":
				pr.Via = "token"
				pr.Purpose = access.TokenAgent
			case "run", "console", "system":
				pr.Kind = "instrument"
				pr.Instrument = way
			case "unknown":
				pr.Via = "unknown"
			case "unverified":
				verified = false
			case "zero-id":
				pr.ID = ids.Nil
			}
			c := access.WithCaller(t.Context(), access.Caller{Principal: pr, Verified: verified})
			if _, err := s.Preview(c, in); err == nil {
				t.Fatal("instrument supplied standing human assent")
			}
		})
	}
}
func TestStandingConsentRefusesIndependentAndUnavailablePolicy(t *testing.T) {
	a, s, p, ctx, in := standingFixture(t)
	projects := project.New(a.DB, identity.Scoped(a.Access, "project"))
	if err := projects.SetIndependentApproval(ctx, a.ProjectID, true); err != nil {
		t.Fatal(err)
	}
	boss := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "boss"))
	if _, err := s.Preview(boss, in); fault.KindOf(err) != fault.Conflict {
		t.Fatalf("zero-change setup bypassed independent policy: %v", err)
	}
	if err := projects.SetIndependentApproval(ctx, a.ProjectID, false); err != nil {
		t.Fatal(err)
	}
	p.err = errors.New("policy storage unavailable")
	if _, err := s.Preview(ctx, in); !errors.Is(err, p.err) {
		t.Fatalf("unavailable policy hidden: %v", err)
	}
}
func TestStandingConsentConcurrentEditsKeepOneExpectedHead(t *testing.T) {
	_, s, _, ctx, in := standingFixture(t)
	r := enableStanding(t, s, ctx, in)
	q, err := s.Preview(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Enable(ctx, StandingCommand{RuleID: r.RuleID, ExpectedRevision: 1, Definition: in, ShownDigest: q.Digest})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if fault.KindOf(err) == fault.Conflict {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent edits: success %d conflict %d", success, conflict)
	}
	h, err := s.History(ctx, r.RuleID)
	if err != nil || len(h) != 2 {
		t.Fatalf("unexpected history: %d %v", len(h), err)
	}
}
func TestRestoredStandingConsentIsDisabledOnceWithHistoryPreserved(t *testing.T) {
	a, s, _, ctx, in := standingFixture(t)
	r := enableStanding(t, s, ctx, in)
	dbtest.FailNextStatement(t, a.DB, "-- name: InsertStandingRevision :exec")
	if err := DisableRestoredStandingRules(ctx, a.DB, time.Now()); err == nil {
		t.Fatal("restore ignored required disabling write")
	}
	h, err := s.History(ctx, r.RuleID)
	if err != nil || len(h) != 1 || !h[0].Enabled {
		t.Fatalf("failed restore disabling partly committed: %+v %v", h, err)
	}
	for range 2 {
		if err := DisableRestoredStandingRules(ctx, a.DB, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	h, err = s.History(ctx, r.RuleID)
	if err != nil || len(h) != 2 || h[1].Enabled || h[1].ChangedBy.Name != "restore" || h[0].Question.Shown != h[1].Question.Shown || h[0].Issuer.Principal != h[1].Issuer.Principal {
		t.Fatalf("restored consent/history: %+v %v", h, err)
	}
}
func TestStandingDefinitionNeverSelectsImplicitOrForeignIdentity(t *testing.T) {
	valid := StandingDefinition{Projects: []StandingProject{{ProjectID: idstest.N(1), Workflow: "quick-change", Environments: []StandingEnvironment{{DeploymentID: idstest.N(2), TargetID: idstest.N(3)}}}}, Categories: StandingCategories{Dependencies: "patch"}, Delivery: "full"}
	for i, change := range []func(*StandingDefinition){func(d *StandingDefinition) { d.Projects[0].Environments = nil }, func(d *StandingDefinition) { d.Projects[0].Environments[0].TargetID = ids.Nil }, func(d *StandingDefinition) { d.Delivery = "land-only" }, func(d *StandingDefinition) { d.Categories.Dependencies = "major" }, func(d *StandingDefinition) { d.Projects = append(d.Projects, d.Projects[0]) }, func(d *StandingDefinition) { d.Projects[0].Workflow = "" }} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			d := valid
			d.Projects = append([]StandingProject(nil), valid.Projects...)
			d.Projects[0].Environments = append([]StandingEnvironment(nil), valid.Projects[0].Environments...)
			change(&d)
			if _, err := normalizeStanding(d); err == nil {
				t.Fatal("invalid or implicit selection admitted")
			}
		})
	}
}

type standingDestinationFixture struct {
	project   ids.ID
	promotion bool
}

func (f standingDestinationFixture) StandingDestination(_ context.Context, _ database.Queryer, _ ids.ID, _ StandingEnvironment) (StandingDestination, error) {
	r := access.Resource{Type: "project", ID: f.project.String(), ProjectID: f.project}
	pr := r
	pr.Type = "release-target"
	pr.ID = "customer"
	return StandingDestination{Resource: r, PromotionResource: pr, PromotionRequired: f.promotion}, nil
}
func TestStandingDeliveryRequiresCapturedPromotionAndCurrentIssuerGrant(t *testing.T) {
	a, s, _, ctx, in := standingFixture(t)
	s.deps.Destinations = standingDestinationFixture{project: a.ProjectID, promotion: true}
	in.Delivery = "full"
	in.Projects[0].Environments = []StandingEnvironment{{DeploymentID: idstest.N(51), TargetID: idstest.N(52)}}
	if _, err := s.Preview(ctx, in); fault.KindOf(err) != fault.Conflict {
		t.Fatalf("promotion purpose silently added: %v", err)
	}
	in.Projects[0].Environments[0].Promotion = true
	if _, err := s.Preview(ctx, in); err == nil {
		t.Fatal("missing promotion grant accepted")
	}
	grant, err := a.Access.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "owner", Capability: string(access.CapReleasePromote), Scope: access.ScopeProject, ScopeID: a.ProjectID.String(), Origin: "fixture", By: "boss"}, "")
	if err != nil {
		t.Fatal(err)
	}
	r := enableStanding(t, s, ctx, in)
	statuses, err := s.List(ctx, a.ProjectID)
	if err != nil || len(statuses) != 1 || !statuses[0].Eligible || statuses[0].ProcessingAvailable {
		t.Fatalf("saved consent eligibility: %+v %v", statuses, err)
	}
	if _, err = a.Access.Revoke(t.Context(), grant, "boss"); err != nil {
		t.Fatal(err)
	}
	statuses, err = s.List(ctx, a.ProjectID)
	if err != nil || len(statuses) != 1 || statuses[0].Eligible {
		t.Fatalf("revoked issuer grant still eligible: %+v %v", statuses, err)
	}
	if _, err = s.Disable(ctx, r.RuleID, r.Revision); err != nil {
		t.Fatalf("manager could not disable after issuer lost deployment grant: %v", err)
	}
}

func TestAdministrativeStandingConsentCapturesExactSetAndDoesNotLeakHistory(t *testing.T) {
	a, s, _, _, in := standingFixture(t)
	boss := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "boss"))
	in.Administrative = true
	in.Projects = append(in.Projects, StandingProject{ProjectID: a.OtherProjectID, Workflow: "quick-change"})
	r := enableStanding(t, s, boss, in)
	if len(r.Question.Definition.Projects) != 2 {
		t.Fatal("administrative set was not captured")
	}
	outsider := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "outsider"))
	if _, err := s.History(outsider, r.RuleID); err == nil {
		t.Fatal("one-project reader saw another project's consent")
	}
	later := workflowTestProject(t, a.DB, "later", "Later", "")
	statuses, err := s.List(boss, later)
	if err != nil || len(statuses) != 0 {
		t.Fatalf("captured administrative set expanded to a new project: %+v %v", statuses, err)
	}
}
func TestStandingConsentCurrentEligibilityUsesEnabledIssuerIdentity(t *testing.T) {
	a, s, _, ctx, in := standingFixture(t)
	r := enableStanding(t, s, ctx, in)
	bossPrincipal := testAdmittedPrincipal(t, a.Access, "boss")
	boss := stepVerifiedContext(t.Context(), bossPrincipal)
	disabled := true
	if err := a.Access.SaveUser(boss, identity.AccountChange{Username: "owner", Disabled: &disabled}, bossPrincipal); err != nil {
		t.Fatal(err)
	}
	statuses, err := s.List(boss, a.ProjectID)
	if err != nil || len(statuses) != 1 || statuses[0].Eligible {
		t.Fatalf("disabled issuer still eligible: %+v %v", statuses, err)
	}
	if _, err := s.Disable(boss, r.RuleID, r.Revision); err != nil {
		t.Fatalf("administrator could not disable former issuer's consent: %v", err)
	}
}

func TestStandingRevocationAcceptsEitherManagementScopeWithoutAssent(t *testing.T) {
	for _, administrative := range []bool{false, true} {
		t.Run(fmt.Sprint(administrative), func(t *testing.T) {
			a, s, _, _, in := standingFixture(t)
			boss := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "boss"))
			in.Administrative = administrative
			if administrative {
				in.Projects = append(in.Projects, StandingProject{ProjectID: a.OtherProjectID, Workflow: "quick-change"})
			}
			r := enableStanding(t, s, boss, in)
			manager := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "outsider"))
			grant := func(cap access.Capability, scope access.ScopeKind, id string) {
				t.Helper()
				if _, err := a.Access.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "outsider", Capability: string(cap), Scope: scope, ScopeID: id, Origin: "fixture", By: "boss"}, ""); err != nil {
					t.Fatal(err)
				}
			}
			if administrative {
				grant(access.CapProjectStandingApproval, access.ScopeProject, a.ProjectID.String())
				if _, err := s.Disable(manager, r.RuleID, r.Revision); err == nil {
					t.Fatal("partial project management revoked captured set")
				}
				grant(access.CapProjectStandingApproval, access.ScopeProject, a.OtherProjectID.String())
			} else {
				grant(access.CapInstanceStandingApproval, access.ScopeInstance, "")
			}
			disabled, err := s.Disable(manager, r.RuleID, r.Revision)
			if err != nil || disabled.Enabled {
				t.Fatalf("management-only revocation: %+v %v", disabled, err)
			}
		})
	}
}

type standingInspectorFunc func(context.Context, Source, StandingCategories) (StandingInspection, error)

func (f standingInspectorFunc) InspectRoutine(ctx context.Context, s Source, b StandingCategories) (StandingInspection, error) {
	return f(ctx, s, b)
}

type standingPresenceFixture struct{ clear bool }

func (standingPresenceFixture) Gone(context.Context, string) (bool, error) {
	return false, errors.New("inspection must not remove a chat")
}
func (standingPresenceFixture) ChatEnded(context.Context, string) (bool, error) { return true, nil }
func (standingPresenceFixture) UnsettledRuns(context.Context, database.Queryer) ([]ids.ID, error) {
	return nil, nil
}
func (standingPresenceFixture) FolderClear(context.Context, ids.ID) (bool, error) {
	return false, errors.New("inspection must not remove a runtime")
}
func (f standingPresenceFixture) FolderUnwritten(context.Context, ids.ID) (bool, error) {
	return f.clear, nil
}

func TestStandingAssessmentChecksCurrentAuthorityAndRuleAfterInspection(t *testing.T) {
	a, s, _, ctx, in := standingFixture(t)
	if err := project.New(a.DB, nil).SetDefaultBranch(ctx, a.ProjectID, "main"); err != nil {
		t.Fatal(err)
	}
	in.Projects[0].Workflow = "test"
	rule := enableStanding(t, s, ctx, in)
	s.deps.Presence = standingPresenceFixture{clear: true}
	calls := 0
	s.deps.Inspector = standingInspectorFunc(func(_ context.Context, source Source, bounds StandingCategories) (StandingInspection, error) {
		calls++
		if source.RunID != a.RunID || source.ChangeID != a.ChangeID || bounds.Dependencies != "patch" {
			t.Fatalf("wrong admitted inspection %+v %+v", source, bounds)
		}
		return StandingInspection{Classification: "routine-dependency", Candidate: "exact-candidate"}, nil
	})
	outsider := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "outsider"))
	if _, err := s.Assess(outsider, rule.RuleID, a.RunID); err == nil || calls != 0 {
		t.Fatalf("unauthorized inspection ran: %v calls=%d", err, calls)
	}
	result, err := s.Assess(ctx, rule.RuleID, a.RunID)
	if err != nil || calls != 1 || result.ProcessingAvailable || result.RuleRevision != rule.Revision {
		t.Fatalf("authorized inspection: %+v %v calls=%d", result, err, calls)
	}

	checked := s.deps.Authority
	s.deps.Authority = stepPermissionFunc(func(ctx context.Context, p access.Permission) (access.Caller, error) {
		if p.Capability == access.CapChangeRunChecks {
			return access.Caller{}, &access.Denied{Reason: "check execution withdrawn"}
		}
		return checked.Require(ctx, p)
	})
	if _, e := s.Assess(ctx, rule.RuleID, a.RunID); e == nil || calls != 1 {
		t.Fatalf("read permission substituted for check execution: %v calls=%d", e, calls)
	}
	s.deps.Authority = checked
	s.deps.Presence = standingPresenceFixture{clear: false}
	if _, err = s.Assess(ctx, rule.RuleID, a.RunID); err == nil || calls != 1 {
		t.Fatalf("live writer inspected: %v calls=%d", err, calls)
	}
	s.deps.Presence = standingPresenceFixture{clear: true}

	s.deps.Inspector = standingInspectorFunc(func(context.Context, Source, StandingCategories) (StandingInspection, error) {
		calls++
		q, e := s.Preview(ctx, in)
		if e != nil {
			t.Fatal(e)
		}
		updated, e := s.Enable(ctx, StandingCommand{RuleID: rule.RuleID, ExpectedRevision: rule.Revision, Definition: in, ShownDigest: q.Digest})
		if e != nil {
			t.Fatal(e)
		}
		rule = updated
		return StandingInspection{Classification: "routine-dependency"}, nil
	})
	if _, e := s.Assess(ctx, rule.RuleID, a.RunID); e == nil || calls != 2 {
		t.Fatalf("revised rule reused an older classification: %v calls=%d", e, calls)
	}
	s.deps.Inspector = standingInspectorFunc(func(context.Context, Source, StandingCategories) (StandingInspection, error) {
		calls++
		if _, e := s.Disable(ctx, rule.RuleID, rule.Revision); e != nil {
			t.Fatal(e)
		}
		return StandingInspection{Classification: "routine-dependency"}, nil
	})
	if _, err = s.Assess(ctx, rule.RuleID, a.RunID); err == nil || calls != 3 {
		t.Fatalf("revoked rule produced usable classification: %v calls=%d", err, calls)
	}
}

type routineTargetParticipant struct {
	owner     *adoption.Service
	candidate adoption.Candidate
}

func (p routineTargetParticipant) RecordTargetIn(ctx context.Context, tx *database.Tx, in TargetChange) (RecordedTarget, error) {
	r, err := p.owner.RecordTargetIn(ctx, tx, adoption.Target{ProjectID: in.ProjectID, ChangeID: in.ChangeID, Candidate: p.candidate, Actor: in.Actor})
	return RecordedTarget{ID: r.ID, Candidate: r.Candidate}, err
}

type routineAdoptionInspection func(context.Context, Source, StandingCategories) (StandingInspection, error)

func (f routineAdoptionInspection) InspectRoutine(context.Context, Source, StandingCategories) (StandingInspection, error) {
	return StandingInspection{}, errors.New("uplift lost its checked adoption")
}
func (f routineAdoptionInspection) InspectRoutineWithAdoption(ctx context.Context, s Source, c StandingCategories) (StandingInspection, error) {
	return f(ctx, s, c)
}

func TestStandingUpliftRechecksActualAdoptionConsentAfterSourceInspection(t *testing.T) {
	a, s, _, ctx, definition := standingFixture(t)
	caller, _ := access.CallerFrom(ctx)
	caller.Receipt = "owner"
	ctx = access.WithCaller(ctx, caller)
	if err := project.New(a.DB, nil).SetDefaultBranch(ctx, a.ProjectID, "main"); err != nil {
		t.Fatal(err)
	}
	boss := access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: testAdmittedPrincipal(t, a.Access, "boss").ID, Name: "boss", Kind: "user", Via: "session"}, Verified: true})
	publications := wow.NewRoutineTransitions(a.DB, a.Authorizer, a.Access, nil)
	ways := wow.New(a.DB, wow.Dependencies{})
	adoptions := adoption.New(a.DB, adoption.Dependencies{Ways: ways, Authority: a.Authorizer, RoutinePublications: publications})
	block, err := ways.CreateBlock(ctx, "team/routine", "Original instructions.", "", "owner")
	if err != nil {
		t.Fatal(err)
	}
	receipt := adoption.EmptyReceipt()
	receipt.Guidance = []adoption.ReceiptBlock{{Name: block.Name, BlockID: block.ID, Version: 1}}
	if _, err = adoptions.Advance(ctx, a.ProjectID, adoption.AdoptionEdit, "fixture guidance", "owner", func(*database.Tx, *adoption.RecordedRevision, time.Time) (adoption.Receipt, error) {
		return receipt, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = ways.BumpBlock(ctx, block.ID, "Clearer instructions.", "[]", "owner", block.Name); err != nil {
		t.Fatal(err)
	}
	question, err := publications.Preview(boss, wow.RoutinePublicationTransition{Kind: "block", ID: block.ID, From: 1, To: 2})
	if err != nil {
		t.Fatal(err)
	}
	consent, err := publications.Classify(boss, wow.RoutinePublicationConsent{RequestID: ids.New(time.Now()), Transition: question.Transition, ShownDigest: question.Digest})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := adoptions.Prepare(ctx, a.ProjectID, adoption.Request{Additions: []string{block.Name + "@2"}})
	if err != nil {
		t.Fatal(err)
	}
	changes := NewChanges(a.DB, a.Authorizer, nil)
	change, err := changes.Uplift(ctx, Uplift{ProjectID: a.ProjectID, ExpectedRevision: candidate.ExpectedRevision, Acceptance: Acceptance{TargetDigest: candidate.Digest}, Words: candidate.Words(), Target: routineTargetParticipant{owner: adoptions, candidate: *candidate}})
	if err != nil {
		t.Fatal(err)
	}
	spec := schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}
	admissions := newRunAdmissions(a.DB, NewStages(nil, func(string) bool { return false }), nil)
	run, err := admissions.Create(ctx, StartRequest{ProjectID: a.ProjectID, ChangeID: change.ID, WorkflowName: "test", InputsJSON: "{}", RequestedBy: "owner"}, PreparedRun{Workflow: DefinitionRef{Name: "test", Version: 1}, Spec: spec, AdoptionID: change.TargetAdoptionID})
	if err != nil {
		t.Fatal(err)
	}
	if err = admissions.Prepared(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	definition.Projects[0].Workflow = "test"
	definition.Categories.WaysOfWorking = true
	rule := enableStanding(t, s, ctx, definition)
	s.deps.Adoptions = adoptions
	s.deps.Presence = standingPresenceFixture{clear: true}
	calls := 0
	s.deps.Inspector = routineAdoptionInspection(func(_ context.Context, source Source, _ StandingCategories) (StandingInspection, error) {
		calls++
		if source.ChangeID != change.ID {
			t.Fatal("wrong actual uplift")
		}
		return StandingInspection{Classification: "routine-adoption", Candidate: "exact-source"}, nil
	})
	got, err := s.Assess(ctx, rule.RuleID, run.ID)
	if err != nil || got.Adoption == nil || len(got.Adoption.Classifications) != 1 || got.Adoption.Classifications[0].ID != consent.ID || got.ProcessingAvailable || calls != 1 {
		t.Fatalf("checked uplift: %+v %v calls=%d", got, err, calls)
	}
	outside := definition
	outside.Categories.WaysOfWorking = false
	outsideRule := enableStanding(t, s, ctx, outside)
	beforeOutside := calls
	if _, err = s.Assess(ctx, outsideRule.RuleID, run.ID); err == nil || calls != beforeOutside {
		t.Fatalf("uplift escaped the selected ways-of-working category: %v calls=%d", err, calls)
	}
	s.deps.Inspector = routineAdoptionInspection(func(context.Context, Source, StandingCategories) (StandingInspection, error) {
		calls++
		_, e := publications.Revoke(boss, consent.ID)
		return StandingInspection{Classification: "routine-adoption", Candidate: "exact-source"}, e
	})
	if _, err = s.Assess(ctx, rule.RuleID, run.ID); err == nil {
		t.Fatal("revocation during source inspection left an eligible uplift")
	}
	beforeCalls := calls
	if _, err = s.Assess(ctx, rule.RuleID, run.ID); err == nil || calls != beforeCalls {
		t.Fatalf("revoked target reached source executor: %v calls=%d", err, calls)
	}
}

func TestStandingMixedCategoryMeaningRequiresNewHumanConfirmation(t *testing.T) {
	a, s, _, ctx, definition := standingFixture(t)
	definition.Categories.WaysOfWorking = true
	question, err := s.Preview(ctx, definition)
	if err != nil {
		t.Fatal(err)
	}
	const precise = "Mixed supported updates require every component to belong to an enabled category and pass its complete independent check. Any unsupported component requires a person. "
	if !strings.Contains(question.Shown, precise) {
		t.Fatal("new question does not state complete mixed-category coverage")
	}
	// Reproduce the exact previous owner wording, retaining its valid old digest.
	// The immutable owner revision writer models a pre-upgrade stored rule; the
	// current Enable door correctly cannot accept this no-longer-shown meaning.
	oldQuestion := question
	oldQuestion.Shown = strings.Replace(oldQuestion.Shown, precise, "", 1)
	oldQuestion.Shown = strings.Replace(oldQuestion.Shown, "weaker checks, unsupported changes and storage-engine transitions", "weaker checks, unsupported or mixed changes and storage-engine transitions", 1)
	oldQuestion.Digest = ""
	raw, err := json.Marshal(oldQuestion)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	oldQuestion.Digest = hex.EncodeToString(digest[:])
	actor, err := s.human(ctx, a.DB)
	if err != nil {
		t.Fatal(err)
	}
	old := StandingRevision{RuleID: ids.New(time.Now()), Revision: 1, Enabled: true, Question: oldQuestion, Issuer: actor, EnabledAt: time.Now(), ChangedAt: time.Now(), ChangedBy: actor, Reason: "explicit human consent"}
	if err = a.DB.InTx(ctx, func(tx *database.Tx) error { return writeStandingRevision(ctx, tx, old, true) }); err != nil {
		t.Fatal(err)
	}
	statuses, err := s.List(ctx, a.ProjectID)
	if err != nil || len(statuses) != 1 || statuses[0].Eligible || !strings.Contains(statuses[0].Reason, "new human confirmation") {
		t.Fatalf("old stored meaning silently reinterpreted: %+v %v", statuses, err)
	}
	if _, err = s.Enable(ctx, StandingCommand{RuleID: old.RuleID, ExpectedRevision: 1, Definition: definition, ShownDigest: oldQuestion.Digest}); err == nil {
		t.Fatal("old shown meaning enabled new coverage")
	}
	confirmed, err := s.Enable(ctx, StandingCommand{RuleID: old.RuleID, ExpectedRevision: 1, Definition: definition, ShownDigest: question.Digest})
	if err != nil {
		t.Fatal(err)
	}
	statuses, err = s.List(ctx, a.ProjectID)
	if err != nil || len(statuses) != 1 || !statuses[0].Eligible || statuses[0].Rule.Revision != confirmed.Revision {
		t.Fatalf("new explicit confirmation not eligible: %+v %v", statuses, err)
	}
	history, err := s.History(ctx, old.RuleID)
	if err != nil || len(history) != 2 {
		t.Fatalf("history: %+v %v", history, err)
	}
	for _, r := range history {
		if r.Revision == 1 && (r.Question.Shown != oldQuestion.Shown || r.Question.Digest != oldQuestion.Digest) {
			t.Fatal("old meaning was rewritten")
		}
	}
}
