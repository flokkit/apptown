package workflow

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/scm"
)

// StepPush is what a step attempt's runtime token may push: its scratch ref
// (scm.ScratchRef), and the branch of the change it works on, which a push
// builds on but never writes. Actor is who the push is recorded as.
type StepPush struct {
	Ref, Branch, Actor string
}

// StepPush answers what an admitted step attempt (identity.Workload from
// inference.Identity.AuthenticateStep) may push: the one scratch ref its
// token may write, for the change its run works on. The rule lives with the
// run and change it reads, not in the Git transport that enforces it. A run
// without a change has nothing to check, and a step agent's instrument must
// be one that runs checks. Any error refuses the push.
func (s *Service) StepPush(ctx context.Context, who identity.Workload) (StepPush, error) {
	if !access.InstrumentMay("run", access.CapChangeRunChecks) {
		return StepPush{}, fmt.Errorf("a step's token does not run checks")
	}
	source, err := ReadSourceIn(ctx, s.db, RunRef{ID: who.RunID, ProjectID: who.ProjectID})
	if err != nil {
		return StepPush{}, fmt.Errorf("reading the step's run: %w", err)
	}
	if source.ChangeID.IsZero() {
		return StepPush{}, fmt.Errorf("this step's run works on no change, so it has no scratch ref")
	}
	ch, err := ReadChangeIn(ctx, s.db, source.ChangeID)
	if err != nil {
		return StepPush{}, fmt.Errorf("reading the step's change: %w", err)
	}
	return StepPush{Ref: scm.ScratchRef(source.ChangeID, who.StepID, who.Attempt), Branch: ch.Branch, Actor: fmt.Sprintf("run/%d", who.RunID)}, nil
}
