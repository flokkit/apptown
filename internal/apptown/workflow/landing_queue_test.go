package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Keep the real admission, completion and acceptance owners; fake only the
// external preparation, policy and dispatch effects so the test drives time.
func queuedLandingService(t *testing.T) (*Service, ids.ID) {
	t.Helper()
	db := testDB(t)
	project := workflowTestProject(t, db, "queue", "Queue", "")
	if err := db.InTx(t.Context(), func(tx *database.Tx) error {
		return adoption.RecordIn(t.Context(), tx, project, 1, adoption.AdoptionCreate, "queue", adoption.EmptyReceipt(), "alice", time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	adopted, err := adoption.CurrentReceiptIDIn(t.Context(), db, project)
	if err != nil {
		t.Fatal(err)
	}
	s := newServiceWithAuthority(db, Dependencies{Completion: completionRecorderFunc(noCompletionEffects), Abandonment: abandonRecorderFunc(noAbandonEffects)}, allowProject(project))
	s.Stop()
	s.starter.runtime.resolve = func(_ context.Context, req StartRequest, _ StartSource) (PreparedRun, error) {
		role := "working"
		stages := []schema.Stage{{Name: "accept", Steps: []schema.Step{{Name: "accept", Uses: "approval", With: map[string]any{"means": landingMeans}}}}}
		if req.WorkflowName == "land" {
			role = "landing"
			stages = append(stages, schema.Stage{Name: "ship", Steps: []schema.Step{{Name: "ship", Uses: "approval"}}})
		}
		return PreparedRun{AdoptionID: adopted, Workflow: DefinitionRef{Name: req.WorkflowName, Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: role, Stages: stages}}, nil
	}
	s.starter.runtime.prepare = func(context.Context, *Run) error { return nil }
	s.starter.runtime.dispatch = func(context.Context, *Run) error { return nil }
	s.landings.eligible = anyoneMayApprove
	s.advancer = newAdvancer(db, advanceRuntime{stages: s.stages, completion: completionRecorderFunc(noCompletionEffects), checks: advancementScopeChecks{refresh: func(context.Context, *Run) error { return nil }}, carry: s.landings.carry})
	return s, project
}

func queueAcceptedWork(t *testing.T, s *Service, project ids.ID, title string) *Run {
	t.Helper()
	ch, err := NewCreator(s.db, nil).Create(t.Context(), ChangeDraft{ProjectID: project, Title: title}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.StartRun(t.Context(), StartChangeRun{ChangeID: ch.ID, Workflow: "work"})
	if err != nil {
		t.Fatal(err)
	}
	steps, err := s.RunSteps(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Transact(t.Context(), s.db, func(tx *database.Tx) (reworkedResult, error) {
		return (reworkStore{db: s.db}).approveIn(t.Context(), tx, run, steps[0], ApprovalVote{Approver: "alice", Principal: idstest.N(21), Via: "session", Decision: VoteApprove, AlsoApproves: []string{landingMeans}})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestRequestLandingStartsQueuedAcceptedWorkAndCarriesAcceptance(t *testing.T) {
	s, project := queuedLandingService(t)
	work := queueAcceptedWork(t, s, project, "Accepted work")
	landing, err := s.RequestLanding(t.Context(), work.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(t.Context(), landing.ID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	if landing.CurrentStage != "ship" || snap.Steps[0].Status != StatusPass {
		t.Fatalf("acceptance was not carried: %+v", snap)
	}
	// The linked work's handoff receipt is an owner read, not a storage count.
	history, err := s.Snapshot(t.Context(), work.ID, DetailView)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range history.Events {
		if event.Kind == "landing.autostarted" {
			found = true
		}
	}
	if !found {
		t.Fatal("Land started an unlinked landing")
	}
}

func TestQueuedAcceptedWorkSelectsOldestAndRequestHonoursOccupancy(t *testing.T) {
	s, project := queuedLandingService(t)
	a := queueAcceptedWork(t, s, project, "First")
	b := queueAcceptedWork(t, s, project, "Second")
	c := queueAcceptedWork(t, s, project, "Third")
	if got, err := s.OldestQueuedAcceptedWork(t.Context(), project); err != nil || got != a.ID {
		t.Fatalf("oldest=%s: %v", got, err)
	}
	if _, err := s.RequestLanding(t.Context(), a.ChangeID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestLanding(t.Context(), b.ChangeID); fault.CodeOf(err) != "landing-occupied" {
		t.Fatalf("occupied: %v", err)
	}
	if got, err := s.OldestQueuedAcceptedWork(t.Context(), project); err != nil || got != b.ID {
		t.Fatalf("next=%s: %v", got, err)
	}
	if got, err := oldestQueuedAcceptedWork(t.Context(), s.db, project, c.ChangeID); err != nil || got != c.ID {
		t.Fatalf("scoped=%s: %v", got, err)
	}
}

func TestCurrentAcceptedQueueWorkWaitsToLandDespiteEarlierFailure(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued", true: "landing"}[started], func(t *testing.T) {
			s, ch, _, _, _ := automatedFixture(t)
			janitorTry(t, s, ch.ProjectID, StandingRevision{}, time.Now().Add(-24*time.Hour), true)
			current := janitorTry(t, s, ch.ProjectID, StandingRevision{}, time.Now(), false)
			run := admitAutomatedRun(t, s, current, "script")
			settleScanTestSteps(t, s, run, StatusPass)
			if err := (advanceStore{db: s.db, stages: s.stages, completion: completionRecorderFunc(noCompletionEffects)}).complete(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			if started {
				// Admission performs the actual linked landing transition.
				_, err := s.runAdmissions.records.create(t.Context(), StartRequest{ProjectID: ch.ProjectID, ChangeID: current.ID, WorkflowName: "land", InputsJSON: "{}", acceptedRunID: run.ID, requestedBy: "alice"}, PreparedRun{Workflow: DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "approval", Steps: []schema.Step{{Name: "approve", Uses: "approval"}}}}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			state, err := s.QueueState(t.Context(), ch.ProjectID, "updates")
			if err != nil || state.State != "landing" || state.Words != "waiting to land" || state.Failed != 1 {
				t.Fatalf("current queue: %+v %v", state, err)
			}
		})
	}
}

func TestLandingUpliftAlwaysBlocksAnotherAdoption(t *testing.T) {
	for _, automated := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "automated packs"}[automated], func(t *testing.T) {
			rig, rules, procedures, _, _ := standingFixture(t)
			procedures.trigger = "publication"
			ctx := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, rig.Access, "boss"))
			rules.deps.Portfolio = &standingPortfolioFixture{}
			rule := enableStanding(t, rules, ctx, JanitorChoices{Dependencies: "off", Packs: true})
			s := rig.Work
			if err := s.db.InTx(t.Context(), func(tx *database.Tx) error {
				return adoption.RecordIn(t.Context(), tx, rig.ProjectID, 1, adoption.AdoptionCreate, "packs", adoption.EmptyReceipt(), "boss", time.Now())
			}); err != nil {
				t.Fatal(err)
			}
			s.standing = &rules
			input := Uplift{ProjectID: rig.ProjectID, ExpectedRevision: 1, Words: []string{"new pack"}, Acceptance: Acceptance{TargetDigest: "first"}, Target: testTarget{digest: "first"}}
			open := func() (*Change, error) { return s.Uplift(ctx, input) }
			if automated {
				input.RuleID, input.RuleRevision, input.Queue, input.Workflow = rule.RuleID, rule.Revision, "packs", "test"
				ctx = access.WithCaller(t.Context(), access.Caller{Verified: true, Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "schedule", ProjectID: rig.ProjectID}})
				open = func() (*Change, error) { return s.OpenPackUplift(ctx, input) }
			}
			ch, err := open()
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.runAdmissions.records.create(ctx, StartRequest{ProjectID: rig.ProjectID, ChangeID: ch.ID, WorkflowName: "land", InputsJSON: "{}", requestedBy: "boss"}, PreparedRun{AdoptionID: ch.TargetAdoptionID, Workflow: DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing", Stages: []schema.Stage{{Name: "approve", Steps: []schema.Step{{Name: "approve", Uses: "approval"}}}}}})
			if err != nil {
				t.Fatal(err)
			}
			input.Acceptance.TargetDigest, input.Target = "distinct", testTarget{digest: "distinct"}
			if next, err := open(); next != nil || fault.KindOf(err) != fault.Conflict {
				t.Fatalf("landing admitted another uplift: %+v %v", next, err)
			}
			if automated {
				attempts, err := s.Attempts(ctx, rig.ProjectID, "packs", 20)
				if err != nil || len(attempts) != 1 || attempts[0].OpenChange != ch.ID {
					t.Fatalf("extra adoption: %+v %v", attempts, err)
				}
			}
		})
	}
}

func TestRequestLandingReturnsAdmittedQueuedRunWhenPreparationFails(t *testing.T) {
	s, project := queuedLandingService(t)
	work := queueAcceptedWork(t, s, project, "Accepted work")
	failure := errors.New("workspace unavailable")
	s.starter.runtime.prepare = func(context.Context, *Run) error { return failure }
	run, err := s.RequestLanding(t.Context(), work.ChangeID)
	if !errors.Is(err, failure) || run == nil || run.ChangeID != work.ChangeID {
		t.Fatalf("admitted landing lost on failure: %+v %v", run, err)
	}
}
