package executiontest

import (
	"context"

	"github.com/flokkit/apptown/internal/execution"
)

// Ensure admits the holder before exercising the rest of the task lifecycle.
func Ensure(ctx context.Context, driver execution.TaskDriver, spec execution.TaskSpec) (execution.TaskObservation, error) {
	key := spec.Key
	if spec.SharedWorkspace != "" {
		key = spec.SharedWorkspace
	}
	for {
		if err := ctx.Err(); err != nil {
			return execution.TaskObservation{}, err
		}
		_, admitted, err := driver.Admit(ctx, key)
		if err != nil {
			return execution.TaskObservation{}, err
		}
		if admitted {
			return driver.Ensure(ctx, spec)
		}
	}
}
