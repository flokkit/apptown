package deployment

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/oci"
)

// imageRebuilder starts an ordinary claimed build run, pinned to the recorded
// commit. It grants no authority to release the resulting bundle.
type imageRebuilder interface {
	StartRebuild(context.Context, ids.ID, ids.ID, string, int64) (ids.ID, error)
}

// releaseNeedsRebuild asks the spoke first, then only the current hub. An old
// registry outside that hub retains its normal provider failure, never a build.
func (s *Service) releaseNeedsRebuild(ctx context.Context, b *ReleaseBundle, t *Target, dep *Deployment) (bool, error) {
	driver, err := s.providers.Driver(ctx, t, dep)
	if err != nil {
		return false, err
	}
	presence, _ := driver.(deploy.ImagePresence)
	var repo oci.Repository
	if b.ByDigest() {
		repo, err = s.env.ReleaseRepository(ctx, b.ProjectID)
		if err != nil {
			return false, err
		}
		if repo.Name != b.BuiltFrom.Registry {
			return false, nil
		}
	}
	missing := false
	for _, ref := range bundleImages(b) {
		if presence != nil {
			held, err := presence.HasImages(ctx, dep.Placement(), []string{ref})
			// Async Portainer cannot report local presence. Its stack operation
			// still runs normally, but unknown presence never starts a rebuild.
			if errors.Is(err, portainer.ErrCannotBackUp) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if held {
				continue
			}
		}
		if !b.ByDigest() {
			// Only image IDs read from a Docker host can be gone from it; a
			// driver that cannot say, or a bundle of anything else, keeps the
			// driver's own failure.
			missing = missing || (presence != nil && !b.BuiltFrom.TargetID.IsZero())
			continue
		}
		err := oci.CheckManifest(ctx, oci.Source{Reference: ref, Credential: repo.Credential})
		if oci.ManifestMissing(err) {
			missing = true
			continue
		}
		if err != nil {
			return false, err
		}
	}
	return missing, nil
}

func (s *Service) startMissingImageRebuild(ctx context.Context, rel *Release, b *ReleaseBundle) error {
	if s.imageRebuilder == nil {
		return fmt.Errorf("the original image is gone and rebuild execution is unavailable")
	}
	from := rel.Number
	if !rel.RollbackOf.IsZero() {
		old, err := s.Releases.ByID(ctx, rel.RollbackOf)
		if err != nil {
			return err
		}
		from = old.Number
	}
	if err := s.CheckRelease(ctx, rel.ID); err != nil {
		return err
	}
	if _, err := deploymentsql.New(s.db.SQL()).MarkReleaseRebuilding(ctx, rel.ID); err != nil {
		return err
	}
	run, err := s.imageRebuilder.StartRebuild(ctx, rel.ProjectID, rel.ID, b.ContractRevision, from)
	if err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		return appendReleaseEvent(ctx, tx, rel.ID, fmt.Sprintf("original image is gone; rebuilding release %d from commit %s in run %s, not recovering the original image", from, b.ContractRevision, run), s.now())
	})
}

// FailRebuild settles a release whose rebuild did not produce an image. The
// rebuild run cannot be abandoned like a change, so without this the release
// would stay releasing and refuse every later release of its deployment. The
// person asks again; a later retry of the build finds the release settled.
func (s *Service) FailRebuild(ctx context.Context, releaseID ids.ID, why string) error {
	rel, err := s.Releases.ByID(ctx, releaseID)
	if err != nil {
		return err
	}
	if rel.State != Releasing {
		return nil
	}
	_, err = s.Outcomes.FailUnstartedRelease(ctx, releaseID, fmt.Errorf("the rebuild from commit %s did not produce an image, so this release did not run: %s", rel.CommitSHA, why))
	return err
}

// ResumeRebuiltRelease resumes only the accepted request, with a fresh claimed
// bundle from its pinned build run. It preserves requester identity, rechecks
// current authority/placement and requires ordinary approval for the new image.
// The ordinary executor takes the pre-release backup after this admission.
func (s *Service) ResumeRebuiltRelease(ctx context.Context, releaseID, runID ids.ID) error {
	rel, err := s.Releases.ByID(ctx, releaseID)
	if err != nil {
		return err
	}
	bundle, err := s.Bundles.OfRun(ctx, rel.ProjectID, runID)
	if err != nil {
		return err
	}
	if bundle.BuiltFrom.RebuiltFrom == 0 || bundle.ContractRevision != rel.CommitSHA {
		return fmt.Errorf("the rebuilt bundle does not match the recorded release commit")
	}
	if rel.BundleID == bundle.ID {
		return s.ExecuteRelease(ctx, releaseID)
	}
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		q := deploymentsql.New(tx.SQL())
		row, err := q.RequestedReleaseForUpdate(ctx, releaseID)
		if err != nil {
			return err
		}
		current, err := s.Releases.record(row.Release, nil)
		if err != nil {
			return err
		}
		if current.BundleID == bundle.ID {
			return nil
		}
		if current.State != Releasing {
			// Settled meanwhile (its rebuild failed and was run again): the
			// new bundle stays for a later release; nothing is resumed.
			return nil
		}
		var authority []byte
		if row.ReleaseRequest.AuthorityJson != nil {
			authority = *row.ReleaseRequest.AuthorityJson
		}
		captured, err := releasePrincipal(authority)
		if err != nil {
			return err
		}
		if err := releaseAllowed(ctx, s, current, row.ReleaseRequest, captured); err != nil {
			return err
		}
		dep, err := s.Deployments.ByID(ctx, current.DeploymentID)
		if err != nil {
			return err
		}
		if dep.TargetID != row.ReleaseRequest.TargetID || dep.DesiredGeneration != row.ReleaseRequest.BaseGeneration {
			return errPlacementChanged
		}
		state := Releasing
		if dep.ApprovalRole != "" {
			state = releaseWaiting
		}
		note := fmt.Sprintf("rebuilt from commit %s, not the original image (release %d)", bundle.ContractRevision, bundle.BuiltFrom.RebuiltFrom)
		notes := strings.TrimSpace(current.Notes + "\n" + note)
		n, err := q.ReplaceRebuiltReleaseBundle(ctx, deploymentsql.ReplaceRebuiltReleaseBundleParams{ID: releaseID, BundleID: bundle.ID, OriginalBundleID: current.BundleID, Notes: notes, State: state.String(), ApprovalRole: dep.ApprovalRole, RebuiltFrom: bundle.BuiltFrom.RebuiltFrom, RebuiltCommit: bundle.ContractRevision})
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("the release changed during its rebuild")
		}
		if _, err := q.ResetRebuiltReleaseClaim(ctx, releaseID); err != nil {
			return err
		}
		if err := appendReleaseEvent(ctx, tx, releaseID, note, s.now()); err != nil {
			return err
		}
		if state == releaseWaiting {
			return appendReleaseEvent(ctx, tx, releaseID, "rebuilt image is waiting for approval", s.now())
		}
		return s.release.Queue.EnqueueReleaseIn(ctx, tx, releaseID)
	})
	if err != nil {
		return err
	}
	return s.ExecuteRelease(ctx, releaseID)

}
