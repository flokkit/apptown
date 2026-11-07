package deployment

import (
	"context"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// selectRelease binds a chosen release or a recorded backup-time release before
// admission. All later authority, approval and execution use the normal path.
func selectRelease(ctx context.Context, s *Service, dep *Deployment, in DeploySelection) (DeploySelection, ids.ID, ids.ID, error) {
	var rollbackOf, restoreBackup ids.ID
	if in.ReleaseNumber < 0 || in.BeforeRelease < 0 || (in.ReleaseNumber != 0 && in.BeforeRelease != 0) || ((!in.BundleID.IsZero() || !in.ExpectedSourceBundleID.IsZero()) && (in.ReleaseNumber != 0 || in.BeforeRelease != 0)) {
		return in, ids.Nil, ids.Nil, fault.New(fault.Invalid, "choose one release, bundle or rollback")
	}
	if in.BeforeRelease != 0 {
		before, e := s.Releases.ByNumber(ctx, in.ProjectID, in.BeforeRelease)
		if e != nil {
			return in, ids.Nil, ids.Nil, e
		}
		if before.DeploymentID != dep.ID || before.BackupID.IsZero() || s.release.Rollbacks == nil {
			return in, ids.Nil, ids.Nil, fault.New(fault.Invalid, "this release has no recorded pre-deploy backup for this deployment")
		}
		selected, e := s.release.Rollbacks.ReleaseBefore(ctx, in.ProjectID, dep.Name, before.BackupID)
		if e != nil {
			return in, ids.Nil, ids.Nil, e
		}
		in.ReleaseNumber, restoreBackup = selected, before.BackupID
	}
	if in.ReleaseNumber != 0 {
		selected, e := s.Releases.ByNumber(ctx, in.ProjectID, in.ReleaseNumber)
		if e != nil {
			return in, ids.Nil, ids.Nil, e
		}
		if (selected.State != Released && selected.State != ReleaseFailed) || selected.BundleID.IsZero() {
			return in, ids.Nil, ids.Nil, fault.New(fault.Invalid, "choose a successful or failed release with a recorded bundle")
		}
		in.BundleID = selected.BundleID
		if selected.DeploymentID == dep.ID {
			rollbackOf = selected.ID
		}
	}

	return in, rollbackOf, restoreBackup, nil
}
