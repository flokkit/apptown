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

// RunningImages is the observed software for each deployment. It never infers
// a serving bundle from the desired release. Unknown and daemon-local bundles
// produce a deployment row with a reason, so callers cannot call them clean.
type RunningImages struct {
	DeploymentID ids.ID            `json:"deploymentId"`
	Deployment   string            `json:"deployment"`
	BundleID     ids.ID            `json:"bundleId,omitzero"`
	Images       map[string]string `json:"images"`
	Reason       string            `json:"reason,omitempty"`
}

func (s *Service) RunningImages(ctx context.Context, project ids.ID) ([]RunningImages, error) {
	deps, err := s.Deployments.ForProject(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make([]RunningImages, 0, len(deps))
	for _, dep := range deps {
		row := RunningImages{DeploymentID: dep.ID, Deployment: dep.Name, Images: map[string]string{}}
		id, err := s.runningBundle(ctx, dep)
		if err != nil {
			return nil, err
		}
		row.BundleID = id
		if id.IsZero() {
			row.Reason = "the running bundle is not known"
			out = append(out, row)
			continue
		}
		bundle, err := s.Bundles.ByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if !bundle.ByDigest() {
			row.Reason = "the running bundle uses local image IDs"
			out = append(out, row)
			continue
		}
		for name, image := range bundle.Parts {
			row.Images[name] = image
		}
		for name, image := range bundle.Init {
			row.Images["init/"+name] = image
		}
		if len(row.Images) == 0 {
			row.Reason = "the running bundle has no recorded images"
		}
		out = append(out, row)
	}
	return out, nil
}
