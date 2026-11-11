package deployment

import (
	"context"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// serving is the inputs of the attempt that serves; false when AppTown did
// not apply what runs (adopted after a restore) or kept no inputs.
func (s *Service) serving(ctx context.Context, dep *Deployment) (Inputs, bool, error) {
	if dep.ServingAttemptID.IsZero() {
		return Inputs{}, false, nil
	}
	in, err := s.Outcomes.InputsFor(ctx, Attempt{ID: dep.ServingAttemptID, DeploymentID: dep.ID, Generation: dep.ObservedGeneration})
	found, err := database.Lookup(err)
	return in, found, err
}

// runningBundle is the serving attempt's bundle, Nil when it is not known.
// Unknown is never a match for a manual redeploy or a data-copy exemption.
func (s *Service) runningBundle(ctx context.Context, dep *Deployment) (ids.ID, error) {
	in, ok, err := s.serving(ctx, dep)
	if err != nil || !ok {
		return ids.Nil, err
	}
	b, err := s.generationBundle(ctx, dep.ProjectID, Inputs{BundleID: in.BundleID, ReleaseID: dep.ServingReleaseID, ReleaseRun: in.ReleaseRun})
	if err != nil || b == nil {
		return ids.Nil, err
	}
	return b.ID, nil
}
