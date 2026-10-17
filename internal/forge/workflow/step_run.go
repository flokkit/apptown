package workflow

import "context"

// StepRunResult distinguishes queued execution from a synchronous assertion
// result. HTTP status and rendering remain with the transport.
type StepRunResult struct {
	Queued bool
	Step   *Step
}

func (s *Service) RunStep(ctx context.Context, in RetryCommand) (StepRunResult, error) {
	if _, err := s.stepAct(ctx, in.StepID, actionRunStep); err != nil {
		return StepRunResult{}, err
	}
	step, err := readStep(ctx, s.db, in.StepID)
	if err != nil {
		return StepRunResult{}, err
	}
	if s.stepAttempts.automatic != nil && s.stepAttempts.automatic(step.Type) {
		_, err := s.RetryStep(ctx, in)
		return StepRunResult{Queued: err == nil}, err
	}
	if step.isAssertion() {
		updated, err := s.Evaluate(ctx, in.StepID)
		return StepRunResult{Step: updated}, err
	}
	return StepRunResult{}, &RetryError{Kind: RetryInvalid, Reason: "step is not runnable; approvals use /approve"}
}
