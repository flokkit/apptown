package workflow

import (
	"context"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/fault"
)

// StepStop stops a running step's agent and names what it stopped: its
// recorded tasks, confirmed by their backend, or its container. Work decides
// who may stop a step and when; the caller supplies the effect.
type StepStop func(context.Context, *Step) ([]string, error)

var (
	errStepNotInterruptible = fault.New(fault.Unprocessable, "only an agent or review step can be interrupted")
	errStepNotRunning       = fault.New(fault.Conflict, "the step is not running")
)

// InterruptStep stops a running agent or review step through the product.
// Stopping a step is working on the change, so it needs the authority that
// starts a lane: a person who may only look cannot stop another's step. The
// step's runner sees its process end and records the outcome; the run's
// record names who stopped it.
func (s *Service) InterruptStep(ctx context.Context, stepID ids.ID, stop StepStop) ([]string, error) {
	actor, err := s.stepAct(ctx, stepID, actionInterruptStep)
	if err != nil {
		return nil, err
	}
	step, err := readStep(ctx, s.db, stepID)
	if err != nil {
		return nil, err
	}
	if step.Type != "agent/pi" && step.Type != "agent/review" {
		return nil, errStepNotInterruptible
	}
	if step.Status != StatusRunning {
		return nil, errStepNotRunning
	}
	stopped, err := stop(ctx, step)
	if err != nil {
		return nil, err
	}
	RecordNoticeOrLog(context.WithoutCancel(ctx), s.db, step.RunID, "step.interrupted", map[string]any{"step": step.Name, "by": actor.Receipt, "containers": stopped}, s.now())
	return stopped, nil
}
