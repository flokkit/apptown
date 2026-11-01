package workflow

import "context"

// stepRunResult distinguishes queued execution from a synchronous assertion
// result. HTTP status and rendering remain with the transport.
type stepRunResult struct {
	Queued bool
	Step   *Step
}

func (s *Service) RunStep(ctx context.Context, in RetryCommand) (stepRunResult, error) {
	if _, err := s.stepAct(ctx, in.StepID, actionRunStep); err != nil {
		return stepRunResult{}, err
	}
	step, err := readStep(ctx, s.db, in.StepID)
	if err != nil {
		return stepRunResult{}, err
	}
	if s.stepAttempts.automatic != nil && s.stepAttempts.automatic(step.Type) {
		_, err := s.RetryStep(ctx, in)
		return stepRunResult{Queued: err == nil}, err
	}
	if step.isAssertion() {
		updated, err := s.Evaluate(ctx, in.StepID)
		return stepRunResult{Step: updated}, err
	}
	return stepRunResult{}, &RetryError{Kind: RetryInvalid, Reason: "step is not runnable; approvals use /approve"}
}
