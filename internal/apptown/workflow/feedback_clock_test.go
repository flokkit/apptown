package workflow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// steppingClock is an AppTown clock an hour ahead of the real one (and the
// database's) that moves a second at every read, so each stamp is
// strictly after the last.
func steppingClock() func() time.Time {
	var n atomic.Int64
	base := time.Now().Add(time.Hour)
	return func() time.Time { return base.Add(time.Duration(n.Add(1)) * time.Second) }
}

// feedbackClockFixture is a run of an agent, a reviewer and an approval,
// commanded by a Work whose clock runs an hour ahead of the database's.
func feedbackClockFixture(t *testing.T) (*Service, *Run, map[string]ids.ID) {
	t.Helper()
	q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish",
		OnEnter: []schema.Step{{Name: "build", Uses: "agent/pi"}},
		Steps:   []schema.Step{{Name: "review", Uses: "agent/review"}, {Name: "accept", Uses: "approval"}},
	}}})
	svc := newServiceWithAuthority(q.db, Dependencies{Now: steppingClock(), Automatic: []string{"agent/pi", "agent/review"}}, allowProject(run.ProjectID))
	svc.dispatcher.stop()
	stepIDs := map[string]ids.ID{}
	for _, name := range []string{"build", "review", "accept"} {
		stepIDs[name] = admittedStepID(t, q, run, name)
	}
	finishForTest(t, svc, stepIDs["build"], StatusPass)
	return svc, run, stepIDs
}

func finishForTest(t *testing.T, svc *Service, id ids.ID, status StepState) {
	t.Helper()
	snapshot, err := readStep(context.Background(), svc.db, id)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := svc.stepAttempts.start(context.Background(), snapshot, snapshot.Type, ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.stepAttempts.finish(context.Background(), claim, StepOutcome{Status: status}); err != nil {
		t.Fatal(err)
	}
}

func stepName(s *Step) string {
	if s == nil {
		return "none"
	}
	return s.Name
}

func rejectForTest(t *testing.T, svc *Service, id ids.ID, comment string) {
	t.Helper()
	if _, err := svc.Approve(context.Background(), Approval{StepID: id, Decision: VoteReject, Comment: comment}); err != nil {
		t.Fatal(err)
	}
}

// The feedback an agent is given is the newest: a rejection's, or a
// machine verdict's. Both times are AppTown's — the vote's and the
// step's finish — so with AppTown's clock an hour ahead of the
// database's the newer one still wins, either way round.
//
// Kills: a step's finish stamped by the database (the verdict after a
// rejection reads an hour older than it); a vote stamped by the real clock
// (the rejection after a verdict reads an hour older than it).
func TestTheLatestFeedbackIsTheNewestByAppTownsClock(t *testing.T) {
	t.Parallel()
	t.Run("a verdict after a rejection", func(t *testing.T) {
		t.Parallel()
		svc, run, stepIDs := feedbackClockFixture(t)
		rejectForTest(t, svc, stepIDs["accept"], "not yet")
		finishForTest(t, svc, stepIDs["review"], StatusFail)
		got, err := latestFeedbackIn(context.Background(), svc.db, RunRef{ID: run.ID, ProjectID: run.ProjectID})
		if err != nil || got == nil || got.ID != stepIDs["review"] {
			t.Fatalf("latest feedback %s, want the review: %v", stepName(got), err)
		}
	})
	t.Run("a rejection after a verdict", func(t *testing.T) {
		t.Parallel()
		svc, run, stepIDs := feedbackClockFixture(t)
		finishForTest(t, svc, stepIDs["review"], StatusFail)
		rejectForTest(t, svc, stepIDs["accept"], "not yet")
		got, err := latestFeedbackIn(context.Background(), svc.db, RunRef{ID: run.ID, ProjectID: run.ProjectID})
		if err != nil || got == nil || got.ID != stepIDs["accept"] {
			t.Fatalf("latest feedback %s, want the approval: %v", stepName(got), err)
		}
	})
}

// What a person last asked of a run is the newest of its send-back notes
// and its rejections, by AppTown's clock on both: a send-back's receipt
// and a vote. With AppTown's clock an hour ahead of the database's, the
// newer one wins either way round.
//
// Kills: a send-back receipt stamped by the real clock (the note after a
// rejection reads an hour older than it); a vote stamped by the real clock
// (the rejection after a note reads an hour older than it).
func TestAPersonsLatestFeedbackIsTheNewestByAppTownsClock(t *testing.T) {
	t.Parallel()
	sendBack := func(t *testing.T, svc *Service, id ids.ID, note string) {
		t.Helper()
		if _, err := svc.stepRetries.retryExpected(context.Background(), retryIntent{RetryCommand: RetryCommand{StepID: id, Mode: RetryContinue, Feedback: note}, Actor: commandActor{Receipt: "alice"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("a note after a rejection", func(t *testing.T) {
		t.Parallel()
		svc, run, stepIDs := feedbackClockFixture(t)
		finishForTest(t, svc, stepIDs["review"], StatusFail)
		rejectForTest(t, svc, stepIDs["accept"], "the rejection")
		finishForTest(t, svc, stepIDs["build"], StatusPass) // the revision the rejection asked for
		sendBack(t, svc, stepIDs["build"], "the note")
		got, err := LatestPersonFeedbackIn(context.Background(), svc.db, RunRef{ID: run.ID, ProjectID: run.ProjectID})
		if err != nil || got == nil || got.Note != "the note" {
			t.Fatalf("person feedback %+v: %v", got, err)
		}
	})
	t.Run("a rejection after a note", func(t *testing.T) {
		t.Parallel()
		svc, run, stepIDs := feedbackClockFixture(t)
		finishForTest(t, svc, stepIDs["review"], StatusFail)
		sendBack(t, svc, stepIDs["build"], "the note")
		rejectForTest(t, svc, stepIDs["accept"], "the rejection")
		got, err := LatestPersonFeedbackIn(context.Background(), svc.db, RunRef{ID: run.ID, ProjectID: run.ProjectID})
		if err != nil || got == nil || got.Note != "the rejection" {
			t.Fatalf("person feedback %+v: %v", got, err)
		}
	})
}

// A rejection in an earlier stage stays on record once its approval is
// given; a note sent back in a later stage is newer and is what the person
// last asked.
//
// Kills: a send-back receipt stamped by the real clock (the note reads an
// hour older than the planning rejection).
func TestANoteInALaterStageIsNewerThanAnEarlierRejection(t *testing.T) {
	t.Parallel()
	q, run := admitWorkflowSpec(t, schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{
		{Name: "plan", OnEnter: []schema.Step{{Name: "write-plan", Uses: "agent/pi"}}, Steps: []schema.Step{{Name: "accept-plan", Uses: "approval"}}},
		{Name: "finish", OnEnter: []schema.Step{{Name: "build", Uses: "agent/pi"}}},
	}})
	svc := newServiceWithAuthority(q.db, Dependencies{Now: steppingClock(), Automatic: []string{"agent/pi", "agent/review"}}, allowProject(run.ProjectID))
	svc.dispatcher.stop()
	ctx := context.Background()
	people(t, q.db, "verified-person")
	plan, accept := admittedStepID(t, q, run, "write-plan"), admittedStepID(t, q, run, "accept-plan")
	finishForTest(t, svc, plan, StatusPass)
	rejectForTest(t, svc, accept, "the rejection")
	finishForTest(t, svc, plan, StatusPass) // the revision the rejection asked for
	if _, err := svc.Approve(ctx, Approval{StepID: accept, Decision: VoteApprove, Comment: "fine"}); err != nil {
		t.Fatal(err)
	}
	svc.advancer = newAdvancer(q.db, advanceRuntime{stages: svc.stages, checks: policyAdvanceChecks{q.db}, clocked: svc.clocked})
	if _, err := svc.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	advanced, err := readRun(ctx, q.db, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	build := admittedStepID(t, q, advanced, "build")
	finishForTest(t, svc, build, StatusPass)
	if _, err := svc.stepRetries.retryExpected(ctx, retryIntent{RetryCommand: RetryCommand{StepID: build, Mode: RetryContinue, Feedback: "the note"}, Actor: commandActor{Receipt: "alice"}}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := LatestPersonFeedbackIn(ctx, q.db, RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil || got == nil || got.Note != "the note" {
		t.Fatalf("person feedback %+v: %v", got, err)
	}
}

// A step's start and finish are both AppTown's: with its clock an hour
// ahead of the database's, the step took the seconds AppTown counted, not
// an hour less.
//
// Kills: the start stamped by the database (the step reads as taking an
// hour and a second).
func TestAStepsStartAndFinishAreAppTownsClock(t *testing.T) {
	t.Parallel()
	svc, _, stepIDs := feedbackClockFixture(t)
	step, err := readStep(context.Background(), svc.db, stepIDs["build"])
	if err != nil {
		t.Fatal(err)
	}
	if !step.StartedAt.Valid || !step.FinishedAt.Valid {
		t.Fatalf("the finished step's times: %+v %+v", step.StartedAt, step.FinishedAt)
	}
	if took := step.FinishedAt.Time.Sub(step.StartedAt.Time); took <= 0 || took > time.Minute {
		t.Fatalf("the step took %s by its stored times, want AppTown's few seconds", took)
	}
}

// A Change's making, and a run's making and every move after, are the
// AppTown's times: a listing compares them with when Changes close, and a
// run's span is its last move less its making. With AppTown's clock an
// hour ahead of the database's, each reads an hour ahead.
//
// Kills: a Change made, a run made, its decisions fenced or the run moved
// at the database's or the real clock.
func TestARunsAndChangesOwnTimesAreAppTownsClock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := steppingClock()
	db := testDB(t)
	creator := NewCreator(db, nil)
	creator.records.clocked = clocked{now}
	ch, err := creator.Create(ctx, ChangeDraft{ProjectID: changeTestProject(t, db, "changes"), Title: "A change"}, "creator")
	if err != nil {
		t.Fatal(err)
	}
	admissions := newRunAdmissions(db, NewStages(nil, func(string) bool { return false }), nil)
	admissions.records.clocked = clocked{now}
	run, err := admissions.records.create(ctx, StartRequest{ProjectID: ch.ProjectID, ChangeID: ch.ID, WorkflowName: "test", InputsJSON: "{}", requestedBy: "operator"}, PreparedRun{Workflow: DefinitionRef{Name: "test", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "finish"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTx(ctx, func(tx *database.Tx) error {
		return invalidateRunDecisionIn(ctx, tx, run, now())
	}); err != nil {
		t.Fatal(err)
	}
	fenced, err := readRun(ctx, db, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTx(ctx, func(tx *database.Tx) error {
		return TransitionRunIn(ctx, tx, fenced, RunDone, fenced.CurrentStage, now())
	}); err != nil {
		t.Fatal(err)
	}
	moved, err := readRun(ctx, db, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	ahead := time.Now().Add(30 * time.Minute)
	for name, at := range map[string]string{"the Change's making": ch.CreatedAt, "the run's making": moved.CreatedAt, "the run's decision fence": fenced.UpdatedAt, "the run's last move": moved.UpdatedAt} {
		if parsed, err := time.Parse(time.RFC3339Nano, at); err != nil || !parsed.After(ahead) {
			t.Errorf("%s is %q, want AppTown's time, an hour ahead: %v", name, at, err)
		}
	}
}

// A run that continues sent-back work is made at AppTown's time, as its
// source was: with AppTown's clock an hour ahead of the database's, its
// making and its last move read an hour ahead.
//
// Kills: a continued run made at the database's clock.
func TestAContinuedRunIsMadeByAppTownsClock(t *testing.T) {
	t.Parallel()
	s, source, _ := completedSendBackFixture(t)
	s.clocked = clocked{steppingClock()}
	result, err := s.sendBack(context.Background(), source.ChangeID, "again", "alice", "", source.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	continued, err := readRun(context.Background(), s.db, result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	ahead := time.Now().Add(30 * time.Minute)
	for name, at := range map[string]string{"its making": continued.CreatedAt, "its last move": continued.UpdatedAt} {
		if parsed, err := time.Parse(time.RFC3339Nano, at); err != nil || !parsed.After(ahead) {
			t.Errorf("the continued run's time of %s is %q, want AppTown's, an hour ahead: %v", name, at, err)
		}
	}
}
