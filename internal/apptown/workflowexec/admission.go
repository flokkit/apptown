package workflowexec

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/apptown/model"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
)

// admitTask waits before a new attempt starts any execution clock or record.
func admitTask(ctx context.Context, task *workflow.Execution, driver execution.TaskDriver, key string, db *database.DB, capacity *model.Scheduler, now func() time.Time) error {
	started := now()
	waited := false
	clear := func() {}
	defer func() { clear() }()
	if capacity != nil {
		clear = capacity.SetWaiting(task.Step().ID, "worker", 0)
	}
	for {
		if err := task.Current(); err != nil {
			return err
		}
		ahead, admitted, err := driver.Admit(ctx, key)
		if err != nil {
			return err
		}
		if admitted {
			if waited || now().After(started) {
				recordEvent(task.Context(), db, task.Step().RunID, "step.queued", map[string]any{"step": task.Step().Name, "reason": "waited for a slot on worker", "ms": now().Sub(started).Milliseconds()}, now())
			}
			return task.Current()
		}
		waited = true
		clear()
		if capacity != nil {
			clear = capacity.SetWaiting(task.Step().ID, "worker", ahead)
		}
	}
}
