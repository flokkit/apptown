package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// EndedByRestart is the reason a step a restart does not continue ends with.
const EndedByRestart = "AppTown restarted while this step ran; it is not continued: run the step again"

// InterruptedSteps describes changes in the caller's startup transaction. It is
// a committed report only after every startup participant and the caller commit.
type InterruptedSteps struct{ Interrupted, PreservedCalls int64 }

// InterruptStepsIn is startup-only: call before admitting execution or serving
// writes. It preserves matching same-project live child calls, and the steps
// in preserve: those whose attempt has a recorded task, which the task
// reconciler observes after startup instead of the step being failed while
// its task may still run. No execution or new recovery loop starts here.
//
// Every other running step ends in error. A step of a type in ended is not
// continued by the restart (EndedByRestart): it stays in error until someone
// runs it again. Any other is marked orphaned, which the dispatcher resumes.
func InterruptStepsIn(ctx context.Context, tx *database.Tx, preserve []ids.ID, ended []string, at time.Time) (InterruptedSteps, error) {
	var out InterruptedSteps
	next, err := StatusRunning.Transition(MoveStepFinish, StatusError)
	if err != nil {
		return out, err
	}
	evidence, err := json.Marshal(map[string]string{"reason": EndedByRestart})
	if err != nil {
		return out, err
	}
	row, err := workflowsql.New(tx.SQL()).InterruptStartupSteps(ctx, workflowsql.InterruptStartupStepsParams{At: at, RunningStatus: StatusRunning.String(), ChildStatus: RunRunning.String(), NextStatus: next.String(), PreservedSteps: append([]ids.ID{}, preserve...),
		EndedTypes: append([]string{}, ended...), EndedEvidence: evidence})
	if err != nil {
		return InterruptedSteps{}, fmt.Errorf("repairing interrupted steps: %w", err)
	}
	out.Interrupted = row.Interrupted
	expected := row.Expected
	if out.Interrupted != expected {
		return InterruptedSteps{}, fmt.Errorf("interrupted steps: recorded %d of %d updates", out.Interrupted, expected)
	}
	out.PreservedCalls, err = workflowsql.New(tx.SQL()).InterruptPreservedCalls(ctx, StatusRunning.String())
	if err != nil {
		return InterruptedSteps{}, fmt.Errorf("counting work preserved for recovery: %w", err)
	}
	return out, nil
}
