package deployment

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// runningBundle is the bundle recorded for the settled observed generation,
// or Nil when it is not known. Missing attempt inputs cannot be reconstructed
// from release names or matching revisions: those do not identify applied images.
// Unknown is never a match for a manual redeploy or a data-copy exemption.
func (s *Service) runningBundle(ctx context.Context, dep *Deployment) (ids.ID, error) {
	if dep.ReleaseSHA == "" || dep.ObservedGeneration == 0 || dep.ObservedGeneration != dep.DesiredGeneration || dep.Readiness != Reconciled {
		return ids.Nil, nil
	}
	q := deploymentsql.New(s.db.SQL())
	at, err := q.CompletedAttempt(ctx, deploymentsql.CompletedAttemptParams{DeploymentID: dep.ID, Generation: dep.ObservedGeneration, Status: AttemptDone.String()})
	if found, err := database.Lookup(err); err != nil {
		return ids.Nil, err
	} else if found {
		in, err := releasesql.New(s.db.SQL()).DeploymentInputs(ctx, at.ID)
		if found, err := database.Lookup(err); err != nil {
			return ids.Nil, err
		} else if found {
			if in.Revision != dep.ReleaseSHA {
				return ids.Nil, nil
			}
			b, err := s.generationBundle(ctx, dep.ProjectID, Inputs{BundleID: in.BundleID, ReleaseID: in.ReleaseID, ReleaseRun: in.ReleaseRun})
			if err != nil || b == nil || b.ContractRevision != dep.ReleaseSHA {
				return ids.Nil, err
			}
			return b.ID, nil
		}
	}
	return ids.Nil, nil
}
