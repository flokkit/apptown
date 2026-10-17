package deployment

import (
	"context"

	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
)

// Recovery reads the admitted inputs even if the deployment now serves a
// different generation. Missing inputs are an error, never guessed from HEAD.
func (s *Outcomes) InputsFor(ctx context.Context, at Attempt) (Inputs, error) {
	r, err := deploymentsql.New(s.db.SQL()).AttemptInputs(ctx, deploymentsql.AttemptInputsParams{ID: at.ID, DeploymentID: at.DeploymentID, Generation: at.Generation})
	if err != nil {
		return Inputs{}, err
	}
	applied, err := appliedFrom(r.Applied)
	return Inputs{ReleaseID: r.ReleaseID, ReleaseRun: r.ReleaseRun, Revision: r.Revision, ImageDigest: r.ImageDigest, ArtifactHash: r.ArtifactHash, DockerSource: r.DockerSource, Rebuild: r.Rebuild, BundleID: r.BundleID, Applied: applied}, err
}
