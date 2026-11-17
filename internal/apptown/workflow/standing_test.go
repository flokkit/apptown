package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

type standingProcedureFixture struct {
	means         string
	trigger       string
	projectMeans  map[ids.ID]string
	projectErrors map[ids.ID]error
	err           error
}

func (f *standingProcedureFixture) standingProcedure(_ context.Context, _ database.Queryer, pid ids.ID, name string) (StandingProcedure, error) {
	if err := f.projectErrors[pid]; err != nil {
		return StandingProcedure{}, err
	}
	means := f.means
	if current, ok := f.projectMeans[pid]; ok {
		means = current
	}
	trigger := f.trigger
	if trigger == "" {
		trigger = "schedule"
	}
	return StandingProcedure{Trigger: trigger, Acceptance: StandingPurpose{Workflow: name, Version: 1, Step: "accept", Means: means, Approvers: []string{"role:project-approver"}}, Landing: StandingPurpose{Workflow: "land", Version: 1, Step: "accept", Means: "land this qualified candidate", Approvers: []string{"role:project-approver"}}}, f.err
}
func standingFixture(t *testing.T) (*stepAuthorityTestRig, StandingRules, *standingProcedureFixture, context.Context, JanitorChoices) {
	t.Helper()
	a := stepAuthorityFixture(t, ApprovalConfig{})
	if _, err := a.Access.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: "owner", Role: access.RoleProjectOwner, Scope: access.ScopeProject, ScopeID: a.ProjectID.String(), By: "boss"}, ""); err != nil {
		t.Fatal(err)
	}
	p := &standingProcedureFixture{means: "accept bounded routine updates after all checks pass"}
	s := NewStandingRules(a.DB, StandingDependencies{Authority: a.Authorizer, Identity: a.Access, Procedures: p, Portfolio: &standingPortfolioFixture{}})
	ctx := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "boss"))
	in := JanitorChoices{Dependencies: "routine", SecurityFixes: true, BaseImages: true, ToolchainPatches: true}
	return a, s, p, ctx, in
}
func enableStanding(t *testing.T, s StandingRules, ctx context.Context, in JanitorChoices) StandingRevision {
	t.Helper()
	old, err := latestStanding(ctx, s.db)
	if err != nil {
		t.Fatal(err)
	}
	var rev int64
	if old != nil {
		rev = old.Revision
	}
	r, err := s.Save(ctx, JanitorChoicesCommand{Choices: in, ExpectedRevision: rev})
	if err != nil {
		t.Fatal(err)
	}
	return r
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
			if _, err := s.Save(c, JanitorChoicesCommand{Choices: in}); err == nil {
				t.Fatal("instrument supplied standing human assent")
			}
		})
	}
}

func TestRestoredStandingConsentIsDisabledOnceWithHistoryPreserved(t *testing.T) {
	a, s, _, ctx, in := standingFixture(t)
	enableStanding(t, s, ctx, in)
	dbtest.FailNextStatement(t, a.DB, "-- name: InsertStandingRevision :exec")
	if err := StartJanitorChoices(ctx, a.DB, time.Now(), true); err == nil {
		t.Fatal("restore ignored required disabling write")
	}
	h, err := s.History(ctx)
	if err != nil || len(h) != 1 || !h[0].Enabled {
		t.Fatalf("failed restore disabling partly committed: %+v %v", h, err)
	}
	for range 2 {
		if err := StartJanitorChoices(ctx, a.DB, time.Now(), true); err != nil {
			t.Fatal(err)
		}
	}
	h, err = s.History(ctx)
	if err != nil || len(h) != 2 || h[1].Enabled || h[1].Issuer.Name != "restore" || h[0].Issuer.Principal != h[1].Issuer.Principal {
		t.Fatalf("restored consent/history: %+v %v", h, err)
	}
}

type standingInspectorFunc func(context.Context, Source, JanitorChoices) (StandingInspection, error)

func (f standingInspectorFunc) InspectRoutine(ctx context.Context, s Source, b JanitorChoices) (StandingInspection, error) {
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
	rule := enableStanding(t, s, ctx, in)
	s.deps.Presence = standingPresenceFixture{clear: true}
	calls := 0
	s.deps.Inspector = standingInspectorFunc(func(_ context.Context, source Source, bounds JanitorChoices) (StandingInspection, error) {
		calls++
		if source.RunID != a.RunID || source.ChangeID != a.ChangeID || bounds.Dependencies != "routine" {
			t.Fatalf("wrong admitted inspection %+v %+v", source, bounds)
		}
		return StandingInspection{Classification: "routine-dependency", Candidate: "exact-candidate"}, nil
	})
	outsider := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "outsider"))
	if _, err := s.Assess(outsider, a.RunID); err == nil || calls != 0 {
		t.Fatalf("unauthorized inspection ran: %v calls=%d", err, calls)
	}
	result, err := s.Assess(ctx, a.RunID)
	if err != nil || calls != 1 || !result.ProcessingAvailable || result.RuleRevision != rule.Revision {
		t.Fatalf("authorized inspection: %+v %v calls=%d", result, err, calls)
	}

	checked := s.deps.Authority
	s.deps.Authority = stepPermissionFunc(func(ctx context.Context, p access.Permission) (access.Caller, error) {
		if p.Capability == access.CapChangeRunChecks {
			return access.Caller{}, &access.Denied{Reason: "check execution withdrawn"}
		}
		return checked.Require(ctx, p)
	})
	if _, e := s.Assess(ctx, a.RunID); e == nil || calls != 1 {
		t.Fatalf("read permission substituted for check execution: %v calls=%d", e, calls)
	}
	s.deps.Authority = checked
	s.deps.Presence = standingPresenceFixture{clear: false}
	if _, err = s.Assess(ctx, a.RunID); err == nil || calls != 1 {
		t.Fatalf("live writer inspected: %v calls=%d", err, calls)
	}
	s.deps.Presence = standingPresenceFixture{clear: true}

	s.deps.Inspector = standingInspectorFunc(func(context.Context, Source, JanitorChoices) (StandingInspection, error) {
		calls++
		updated, e := s.Save(ctx, JanitorChoicesCommand{Choices: in, ExpectedRevision: rule.Revision})
		if e != nil {
			t.Fatal(e)
		}
		rule = updated
		return StandingInspection{Classification: "routine-dependency"}, nil
	})
	if _, e := s.Assess(ctx, a.RunID); e == nil || calls != 2 {
		t.Fatalf("revised rule reused an older classification: %v calls=%d", e, calls)
	}
	s.deps.Inspector = standingInspectorFunc(func(context.Context, Source, JanitorChoices) (StandingInspection, error) {
		calls++
		if _, e := s.Save(ctx, JanitorChoicesCommand{Choices: JanitorChoices{Dependencies: "off"}, ExpectedRevision: rule.Revision}); e != nil {
			t.Fatal(e)
		}
		return StandingInspection{Classification: "routine-dependency"}, nil
	})
	if _, err = s.Assess(ctx, a.RunID); err == nil || calls != 3 {
		t.Fatalf("revoked rule produced usable classification: %v calls=%d", err, calls)
	}
}

func (f *standingProcedureFixture) StandingProcedures(ctx context.Context, q database.Queryer, id ids.ID, names []string) (map[string]StandingProcedureResult, error) {
	out := map[string]StandingProcedureResult{}
	for _, name := range names {
		proc, err := f.standingProcedure(ctx, q, id, name)
		out[name] = StandingProcedureResult{Procedure: proc, Err: err}
	}
	return out, nil
}

func TestJanitorChoicesRequireExpectedRevisionAndKeepThePerson(t *testing.T) {
	_, s, _, ctx, in := standingFixture(t)
	r, err := s.Save(ctx, JanitorChoicesCommand{Choices: in})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(ctx, JanitorChoicesCommand{Choices: in}); fault.KindOf(err) != fault.Conflict {
		t.Fatalf("stale save: %v", err)
	}
	r2, err := s.Save(ctx, JanitorChoicesCommand{Choices: JanitorChoices{Dependencies: "off", Packs: true}, ExpectedRevision: r.Revision})
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Current(ctx)
	if err != nil || current == nil || current.RuleID != r.RuleID || current.Revision != 2 || current.Issuer.Principal != r.Issuer.Principal || current.At.IsZero() {
		t.Fatalf("current: %+v %v", current, err)
	}
	history, err := s.History(ctx)
	if err != nil || len(history) != 2 || history[0].Choices != in || history[1].Choices != r2.Choices {
		t.Fatalf("history: %+v %v", history, err)
	}
}
