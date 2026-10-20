package workflow

import (
	"context"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

// InferenceObservation is a step attempt's run state, whether the attempt is
// still the live one, and the run's billing owner, read in one snapshot.
type InferenceObservation struct {
	State       RunState
	LiveAttempt bool
	Owner       string
	// OwnerPrincipal is the owner's principal when the owner is a person;
	// zero for AppTown, a run, or one not resolved to a person.
	OwnerPrincipal ids.ID
}

// ObserveStepAttemptIn reads, for a step attempt named by a runtime
// credential, the run it belongs to, whether this attempt is the step's live
// running attempt, and the run's billing owner, in one statement snapshot. It
// grants no credential or account authority: an unknown step is sql.ErrNoRows,
// and callers admit only a running run whose live attempt this is. Historical
// runs without a recorded initiator keep their run attribution; a blank
// recorded owner stays distinct from an absent owner record.
func ObserveStepAttemptIn(ctx context.Context, q database.Queryer, stepID ids.ID, attempt int64) (RunRef, InferenceObservation, error) {
	if stepID.IsZero() || attempt < 0 {
		return RunRef{}, InferenceObservation{}, fmt.Errorf("a step attempt needs a step and a valid attempt")
	}
	row, err := workflowsql.New(q.SQL()).StepAttemptObservation(ctx, workflowsql.StepAttemptObservationParams{
		StepID: stepID, Attempt: attempt, RunningStepState: StatusRunning.String(),
	})
	if err != nil {
		return RunRef{}, InferenceObservation{}, err
	}
	state, err := ParseRunState(row.Status)
	if err != nil {
		return RunRef{}, InferenceObservation{}, err
	}
	return RunRef{ID: row.RunID, ProjectID: row.ProjectID}, InferenceObservation{State: state, LiveAttempt: row.LiveAttempt, Owner: row.Owner, OwnerPrincipal: row.OwnerPrincipal}, nil
}
