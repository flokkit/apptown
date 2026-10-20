package forge

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// backgroundShutdownGrace caps how long shutdown waits for the owners below.
const backgroundShutdownGrace = 5 * time.Second

// ShutdownBackground closes all background owners before waiting for any.
// The daemon separately cancels its jobs, scheduler and health-controller
// contexts. An expired wait leaves active operations to process-exit recovery;
// it does not claim their underlying subprocesses or database calls have ended.
func (a *App) ShutdownBackground(ctx context.Context) error {
	defer a.endLife()
	// Work first: from here a step's failure is not recorded (the processes
	// it ran are being killed with AppTown), and the sooner Work knows,
	// the fewer such failures reach it before it does.
	workflows := a.Work.BeginShutdown()
	a.StopConsoles(ctx) // cancel local Docker control; session containers survive
	ctx, cancel := context.WithTimeout(ctx, backgroundShutdownGrace)
	defer cancel()
	var errs []error
	for _, owner := range []struct {
		name string
		done <-chan struct{}
	}{
		{"workflow dispatch", workflows},
	} {
		// A completed owner stays complete even when the shared budget expired.
		select {
		case <-owner.done:
			continue
		default:
		}
		select {
		case <-owner.done:
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("waiting for %s: %w", owner.name, ctx.Err()))
		}
	}
	return errors.Join(errs...)
}
