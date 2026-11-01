package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
)

type landingAvailabilityFunc func(context.Context, ids.ID) (bool, error)

func (f landingAvailabilityFunc) CanLand(ctx context.Context, id ids.ID) (bool, error) {
	return f(ctx, id)
}
func TestAcceptedLandingChecksChangeScopeBeforeAvailability(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	projectID := workflowTestProject(t, db, "completion", "Completion", "")
	other := workflowTestProject(t, db, "other", "Other", "")
	change := admissionTestChange(t, db, other, "Foreign")
	work := insertStoredRun(t, db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: projectID, ChangeID: change, WorkflowName: "test", WorkflowVersion: 1, Kind: "pipeline", Status: RunDone.String(), CurrentStage: "finish", DefinitionJson: []byte(`{"kind":"pipeline","role":"working","stages":[{"name":"finish"}]}`)})
	q := queries{db: db}
	calls := 0
	runtime := acceptedLandingRuntime{Availability: landingAvailabilityFunc(func(context.Context, ids.ID) (bool, error) { calls++; return true, nil })}
	got, err := approvingLandings(q.db, nil).advanceAccepted(context.Background(), work.ID, runtime)
	if !errors.Is(err, ErrRunMoved) || got.Advance != nil || calls != 0 {
		t.Fatalf("cross-project preparation: %+v %v calls=%d", got, err, calls)
	}
	wantRunCount(t, q.db, 1, nil)
	wantEventCount(t, q.db, 0, anyEvent)
}
func TestAcceptedLandingReplayDoesNotPrepareOrAdvanceANewerStage(t *testing.T) {
	t.Parallel()
	q, work, accept := activityFixture(t)
	if rows, err := workflowsql.New(q.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), NextStage: work.CurrentStage, NextStatus: RunDone.String(), ID: work.ID, ExpectedStatus: RunRunning.String(), CurrentStage: work.CurrentStage, RetryRevision: work.RetryRevision}); err != nil || rows != 1 {
		t.Fatalf("finish work run: rows=%d err=%v", rows, err)
	}
	if rows, err := workflowsql.New(q.db.SQL()).RecordApproval(context.Background(), workflowsql.RecordApprovalParams{At: time.Now(), ID: accept, Attempt: 0, ExpectedStatus: StatusPending.String(), Status: StatusPass.String(), EvidenceJson: []byte(acceptedShowing(landingMeans))}); err != nil || rows != 1 {
		t.Fatalf("finish acceptance: rows=%d err=%v", rows, err)
	}
	landing := insertStoredRun(t, q.db, workflowsql.InsertRunParams{ID: ids.New(time.Now()), ProjectID: work.ProjectID, ChangeID: work.ChangeID, WorkflowName: "land", WorkflowVersion: 1, Kind: "pipeline", Status: RunRunning.String(), CurrentStage: "start", DefinitionJson: []byte(`{"kind":"pipeline","role":"landing","stages":[{"name":"start","steps":[{"name":"accept","uses":"approval"}]},{"name":"later"}]}`)}).ID
	insertStoredStep(t, q.db, workflowsql.InsertStageStepParams{ID: ids.New(time.Now()), RunID: landing, Stage: "start", Name: "accept", Type: "approval", ConfigJson: []byte(`{"quorum":1,"means":"` + landingMeans + `"}`), Status: StatusPending.String()})
	if _, err := workflowsql.New(q.db.SQL()).EnsureLanding(context.Background(), work.ID); err != nil {
		t.Fatal(err)
	}
	if rows, err := workflowsql.New(q.db.SQL()).AttachAcceptedLanding(context.Background(), workflowsql.AttachAcceptedLandingParams{WorkRunID: work.ID, LandingRunID: landing}); err != nil || rows != 1 {
		t.Fatalf("attach acceptance: rows=%d err=%v", rows, err)
	}
	owner := approvingLandings(q.db, nil)
	if _, err := owner.apply(context.Background(), work.ID, landing); err != nil {
		t.Fatal(err)
	}
	if err := requireRunWrite(workflowsql.New(q.db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), ID: landing, ExpectedStatus: RunRunning.String(), CurrentStage: "start", NextStatus: RunRunning.String(), NextStage: "later"})); err != nil {
		t.Fatal(err)
	}
	if err := requireRunWrite(workflowsql.New(q.db.SQL()).InvalidateRunDecision(context.Background(), workflowsql.InvalidateRunDecisionParams{At: time.Now(), ID: landing, CurrentStage: "later"})); err != nil {
		t.Fatal(err)
	}
	got, err := owner.advanceAccepted(context.Background(), work.ID, acceptedLandingRuntime{})
	if err != nil || !got.RunID.IsZero() || got.Advance != nil {
		t.Fatalf("replay entered a newer stage: %+v %v", got, err)
	}
	wantRunState(t, q.db, landing, RunRunning)
	wantCurrentStage(t, q.db, landing, "later")
	wantRetryRevision(t, q.db, landing, 1)
	wantEventCount(t, q.db, 0, ofKind("approval"))
	wantEventCount(t, q.db, 2, ofKind("landing.autostarted"))
}

func TestAcceptedLandingClosedChangeDoesNotConsultAvailability(t *testing.T) {
	t.Parallel()
	for _, state := range []ChangeState{ChangeShipped, ChangeAbandoned} {
		t.Run(state.String(), func(t *testing.T) {
			t.Parallel()
			q, work, _ := activityFixture(t)
			{
				current, err := workflowsql.New(q.db.SQL()).ChangeByID(context.Background(), workflowsql.ChangeByIDParams{ID: work.ChangeID})
				if err != nil {
					t.Fatal(err)
				}
				if rows, err := workflowsql.New(q.db.SQL()).TransitionChange(context.Background(), workflowsql.TransitionChangeParams{At: time.Now(), ID: work.ChangeID, ProjectID: current.ProjectID, OldState: current.State, OldRunID: current.RunID, State: state.String(), RunID: current.RunID, Branch: current.Branch}); err != nil || rows != 1 {
					t.Fatalf("stored change transition: rows=%d err=%v", rows, err)
				}
			}
			calls := 0
			runtime := acceptedLandingRuntime{Availability: landingAvailabilityFunc(func(context.Context, ids.ID) (bool, error) { calls++; return true, nil })}
			got, err := approvingLandings(q.db, nil).advanceAccepted(context.Background(), work.ID, runtime)
			if err != nil || !got.RunID.IsZero() || got.Advance != nil || calls != 0 {
				t.Fatalf("closed change attempted landing: %+v %v calls=%d", got, err, calls)
			}
			wantRunCount(t, q.db, 1, nil)
			wantEventCount(t, q.db, 0, func(e Event) bool { return strings.HasPrefix(e.Kind, "landing.") })
		})
	}
}
