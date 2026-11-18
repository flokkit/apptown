package deployment

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// ErrRebuildNotApplicable means this deployment cannot receive an automatic rebuild.
var ErrRebuildNotApplicable = errors.New("automatic rebuild is not applicable")

// RequestRebuild requests a fresh image of the currently serving release's
// commit. The main feed supplies authority and its revision; ordinary approval,
// routine holds and release windows apply. The release job starts the same
// pinned system run used for missing images. It refuses another release in
// flight, and never substitutes the newest bundle or current main commit.
func (s *Service) RequestRebuild(ctx context.Context, projectID ids.ID, name, reason string) (NumberedRelease, error) {
	if s.imageRebuilder == nil || s.release.Queue == nil {
		return NumberedRelease{}, fmt.Errorf("rebuild execution is unavailable")
	}
	dep, err := s.Deployments.ByName(ctx, projectID, name)
	if err != nil {
		return NumberedRelease{}, err
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (NumberedRelease, error) {
		if _, err := releasesql.New(tx.SQL()).LockReleaseProject(ctx, projectID); err != nil {
			return NumberedRelease{}, err
		}
		dep, err := lockDeploymentRecordIn(ctx, tx, dep.ID)
		if err != nil {
			return NumberedRelease{}, err
		}
		if dep.Feed != "main" || dep.ServingReleaseID.IsZero() {
			return NumberedRelease{}, fmt.Errorf("%w: deployment does not follow main with a running release", ErrRebuildNotApplicable)
		}
		busy, err := releasesql.New(tx.SQL()).ReleaseInFlight(ctx, releasesql.ReleaseInFlightParams{ProjectID: projectID, DeploymentID: dep.ID, OpenState: releaseWaiting.String()})
		if err != nil {
			return NumberedRelease{}, err
		}
		if busy {
			return NumberedRelease{}, fmt.Errorf("%w: %w", ErrRebuildNotApplicable, errReleaseInFlight)
		}
		current, err := s.Releases.record(deploymentsql.New(tx.SQL()).ReleaseByID(ctx, dep.ServingReleaseID))
		if err != nil {
			return NumberedRelease{}, err
		}
		setter, err := releasePrincipal(dep.feedAuthority)
		if err != nil {
			return NumberedRelease{}, err
		}
		rel, err := startReleaseIn(ctx, tx, ReleaseDraft{Automatic: true, Source: ReleaseFromBundle, ProjectID: projectID, DeploymentID: dep.ID, BundleID: current.BundleID, RunID: current.RunID, Deployment: dep.Name, Revision: current.CommitSHA, Actor: followsMainActor + setter.Name, Notes: reason, At: s.now()}, reason)
		if err != nil {
			if errors.Is(err, errReleaseInFlight) {
				return NumberedRelease{}, fmt.Errorf("%w: %w", ErrRebuildNotApplicable, err)
			}
			return NumberedRelease{}, err
		}
		if err := s.admitAutomaticReleaseIn(ctx, tx, dep, rel, setter.Name, "", reason, true); err != nil {
			return NumberedRelease{}, err
		}
		if _, err := deploymentsql.New(tx.SQL()).MarkReleaseRebuilding(ctx, rel.ID); err != nil {
			return NumberedRelease{}, err
		}
		return rel, nil
	})
}
