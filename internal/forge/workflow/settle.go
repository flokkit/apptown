package workflow

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"flokkit.io/forge/internal/poll"
)

// Settlement bounds how a step's outcome write is tried again when it fails
// for a reason other than the step no longer being this attempt's. Zero
// Tries means three; zero Wait means one second. The wait doubles between
// tries, so the whole bound is known: 1 s + 2 s with the defaults.
type Settlement struct {
	Tries int
	Wait  time.Duration
}

// Retry runs settle, trying the same write again a bounded number of times.
// A step no longer this attempt's (ErrStepAttemptLost) is final at once:
// the attempt fence refuses every later settlement, including one whose
// earlier try committed but reported an error. ctx ending stops the tries
// between two of them: the write's failure is returned, as at the bound.
func (s Settlement) Retry(ctx context.Context, settle func() error) error {
	tries, wait := s.Tries, s.Wait
	if tries <= 0 {
		tries = 3
	}
	if wait <= 0 {
		wait = time.Second
	}
	var err error
	for i := range tries {
		if i > 0 {
			if cerr := poll.Wait(ctx, wait); cerr != nil {
				return fmt.Errorf("%w; not tried again: %w", err, cerr)
			}
			wait *= 2
		}
		if err = settle(); err == nil || errors.Is(err, ErrStepAttemptLost) {
			return err
		}
	}
	return err
}

// ErrShuttingDown is Settle's answer to a verdict other than a pass while
// the forge stops: nothing was recorded, and the step stays running.
var ErrShuttingDown = errors.New("AppTown is stopping: the step's outcome is not recorded, and startup repair takes the step up")

// Settle records outcome as Finish does, trying a failed write again within
// the default Settlement bound, so one transient database failure does not
// leave the step running with its verdict lost. Every failed try is
// reported (Finish); a write still failing at the bound is returned, and the
// step stays running for startup repair. An outcome that could not be
// encoded is not tried again: the same encoding fails the same way.
//
// While the forge stops (BeginShutdown), only a pass is recorded. A stopping
// forge's processes are killed with it (systemd signals the whole service,
// a check's container among it), so an error or failure seen now may be the
// stop's doing, not the work's: the step stays running (ErrShuttingDown),
// as if the forge had stopped a moment earlier, and startup repair resumes
// it, or ends it as a restart does for a kind not continued. A failure
// while the forge keeps running is recorded as ever.
func (e *Execution) Settle(outcome StepOutcome, encodingError error) error {
	if encodingError != nil {
		return e.Finish(outcome, encodingError)
	}
	if outcome.Status != StatusPass && e.service.dispatcher.isShuttingDown() {
		log.Printf("step %d: %s not recorded: %v", e.step.ID, outcome.Status, ErrShuttingDown)
		return ErrShuttingDown
	}
	return Settlement{}.Retry(e.ctx, func() error { return e.Finish(outcome, nil) })
}
