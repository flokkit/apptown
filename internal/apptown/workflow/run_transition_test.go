package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func TestRunTransitionRequiresCurrentStateStageAndRevision(t *testing.T) {
	t.Parallel()
	for _, changed := range []string{"state", "stage", "revision", "missing"} {
		t.Run(changed, func(t *testing.T) {
			t.Parallel()
			owner, run := abandonFixture(t)
			db := owner.db
			switch changed {
			case "state":
				if n, err := workflowsql.New(db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), NextStage: run.CurrentStage, NextStatus: RunDone.String(), ID: run.ID, ExpectedStatus: run.Status.String(), CurrentStage: run.CurrentStage, RetryRevision: run.RetryRevision}); err != nil || n != 1 {
					t.Fatalf("move stored run state: %d %v", n, err)
				}
			case "stage":
				if n, err := workflowsql.New(db.SQL()).TransitionRun(context.Background(), workflowsql.TransitionRunParams{At: time.Now(), NextStage: "later", NextStatus: run.Status.String(), ID: run.ID, ExpectedStatus: run.Status.String(), CurrentStage: run.CurrentStage, RetryRevision: run.RetryRevision}); err != nil || n != 1 {
					t.Fatalf("move stored run stage: %d %v", n, err)
				}
			case "revision":
				invalidateScopeRun(t, db, run.ID)
			case "missing":
				if err := workflowsql.New(db.SQL()).DeleteProjectRuns(context.Background(), run.ProjectID); err != nil {
					t.Fatal(err)
				}
			}
			before := *run
			err := db.InTx(context.Background(), func(tx *database.Tx) error {
				return TransitionRunIn(context.Background(), tx, run, RunAbandoned, run.CurrentStage, time.Now())
			})
			if !errors.Is(err, ErrRunMoved) {
				t.Fatalf("stale %s accepted: %v", changed, err)
			}
			if *run != before {
				t.Fatal("detached snapshot changed")
			}
			wantRunCount(t, db, 0, func(r *Run) bool { return r.Status == RunAbandoned })
		})
	}
}

// These stored-record mutations use the generated guards against the current row.
// Reading here does not replace the detached snapshot held by the operation under
// test; callers still mutate at their original interference or fixture point.
func transitionScopeRun(t *testing.T, db database.Queryer, in workflowsql.TransitionRunParams) {
	t.Helper()
	q := workflowsql.New(db.SQL())
	row, err := q.RunByID(context.Background(), workflowsql.RunByIDParams{ID: in.ID})
	if err != nil {
		t.Fatal(err)
	}
	in.ExpectedStatus, in.CurrentStage, in.RetryRevision = row.Status, row.CurrentStage, row.RetryRevision
	if in.NextStatus == "" {
		in.NextStatus = row.Status
	}
	if in.NextStage == "" {
		in.NextStage = row.CurrentStage
	}
	if n, err := q.TransitionRun(context.Background(), in); err != nil || n != 1 {
		t.Fatalf("transition stored run: %d %v", n, err)
	}
}
func transitionScopeChange(t *testing.T, db database.Queryer, in workflowsql.TransitionChangeParams) {
	t.Helper()
	q := workflowsql.New(db.SQL())
	row, err := q.ChangeByID(context.Background(), workflowsql.ChangeByIDParams{ID: in.ID})
	if err != nil {
		t.Fatal(err)
	}
	in.ProjectID, in.OldState, in.OldRunID, in.Branch = row.ProjectID, row.State, row.RunID, row.Branch
	if in.State == "" {
		in.State = row.State
	}
	if in.RunID.IsZero() {
		in.RunID = row.RunID
	}
	if n, err := q.TransitionChange(context.Background(), in); err != nil || n != 1 {
		t.Fatalf("transition stored change: %d %v", n, err)
	}
}
func recordScopeStep(t *testing.T, db database.Queryer, in workflowsql.RecordApprovalParams) {
	t.Helper()
	q := workflowsql.New(db.SQL())
	row, err := q.StepByID(context.Background(), in.ID)
	if err != nil {
		t.Fatal(err)
	}
	in.Attempt, in.ExpectedStatus = row.Attempt, row.Status
	if in.EvidenceJson == nil {
		in.EvidenceJson = row.EvidenceJson
	}
	if n, err := q.RecordApproval(context.Background(), in); err != nil || n != 1 {
		t.Fatalf("record stored step: %d %v", n, err)
	}
}
func invalidateScopeRun(t *testing.T, db database.Queryer, id ids.ID) {
	t.Helper()
	q := workflowsql.New(db.SQL())
	row, err := q.RunByID(context.Background(), workflowsql.RunByIDParams{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := q.InvalidateRunDecision(context.Background(), workflowsql.InvalidateRunDecisionParams{At: time.Now(), ID: id, CurrentStage: row.CurrentStage, RetryRevision: row.RetryRevision}); err != nil || n != 1 {
		t.Fatalf("invalidate stored run: %d %v", n, err)
	}
}
func recordScopeRunSteps(t *testing.T, db database.Queryer, id ids.ID, status string) {
	t.Helper()
	rows, err := workflowsql.New(db.SQL()).RunSteps(context.Background(), workflowsql.RunStepsParams{RunID: id})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		recordScopeStep(t, db, workflowsql.RecordApprovalParams{At: time.Now(), ID: row.ID, Status: status})
	}
}
func retryScopeStep(t *testing.T, db database.Queryer, id ids.ID, config json.RawMessage) {
	t.Helper()
	q := workflowsql.New(db.SQL())
	row, err := q.StepByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := q.RetryStep(context.Background(), workflowsql.RetryStepParams{ID: id, Attempt: row.Attempt, ConfigJson: config}); err != nil || n != 1 {
		t.Fatalf("reset stored step: %d %v", n, err)
	}
}

func overrideScopeStep(t *testing.T, db database.Queryer, in workflowsql.OverrideStepParams) {
	t.Helper()
	q := workflowsql.New(db.SQL())
	row, err := q.StepByID(context.Background(), in.ID)
	if err != nil {
		t.Fatal(err)
	}
	in.Attempt, in.ExpectedStatus = row.Attempt, row.Status
	if n, err := q.OverrideStep(context.Background(), in); err != nil || n != 1 {
		t.Fatalf("override stored step: %d %v", n, err)
	}
}
func scopeChangeBase(t *testing.T, db database.Queryer, id ids.ID, tip string) {
	t.Helper()
	q := workflowsql.New(db.SQL())
	row, err := q.ChangeByID(context.Background(), workflowsql.ChangeByIDParams{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := q.SetChangeWorkspaceBase(context.Background(), workflowsql.SetChangeWorkspaceBaseParams{ChangeID: id, ProjectID: row.ProjectID, Tip: tip, Branch: row.Branch, OldBranch: row.Branch, OldBaseSha: row.BaseSha, OldLandedSha: row.LandedSha, OldState: row.State, OldRunID: row.RunID}); err != nil || n != 1 {
		t.Fatalf("record stored change base: %d %v", n, err)
	}
}

func incrementScopeAttempt(t *testing.T, db database.Queryer, id ids.ID) {
	t.Helper()
	q := workflowsql.New(db.SQL())
	row, err := q.StepByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if row.FinishedAt.Valid {
		t.Fatal("attempt interference requires an unfinished row")
	}
	if n, err := q.StartParentCall(context.Background(), workflowsql.StartParentCallParams{ID: id, Attempt: row.Attempt, OldStatus: row.Status, Status: row.Status, StartedAt: row.StartedAt, EvidenceJson: row.EvidenceJson}); err != nil || n != 1 {
		t.Fatalf("increment stored attempt: %d %v", n, err)
	}
}
