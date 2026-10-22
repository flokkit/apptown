package deployment

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// runningBundle is the bundle a settled deployment runs, or Nil when that is
// not known. Settled: its observed generation is its desired one and it reads
// reconciled, so no newer deploy (a landing, say) is under way. The bundle is
// the one the completed attempt at that generation ran (generationBundle:
// a pinned bundle, its release's, or its landing run's); for a release made
// before attempts recorded their inputs, it is the deployment's newest
// released release, when its bundle is at the revision it runs (those
// releases did not always record their revision). Unknown is
// never read as a match: deploying "what runs" by hand needs to know what
// runs.
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
	// The newest release that was released, not the newest of any state: a
	// request queued or failed after it (this one, being executed) admitted
	// no attempt and changes nothing about what runs.
	released, err := q.LatestReleasedOn(ctx, deploymentsql.LatestReleasedOnParams{Deployment: dep.Name, State: Released.String()})
	if err != nil {
		return ids.Nil, err
	}
	// Such a release recorded its bundle but not always its revision, so the
	// bundle's own revision must be the one that runs.
	for _, r := range released {
		if r.ProjectID != dep.ProjectID || r.BundleID.IsZero() {
			continue
		}
		b, err := lookupBundle(s.Bundles.ByID(ctx, r.BundleID))
		if err != nil || b == nil || b.ProjectID != dep.ProjectID || b.ContractRevision != dep.ReleaseSHA {
			return ids.Nil, err
		}
		return b.ID, nil
	}
	return ids.Nil, nil
}
