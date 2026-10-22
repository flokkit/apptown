package deployment

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/database"
)

// Hold paths in the operations ledger (internal/apptown/operations): one
// spelling of a deployment's identity, shared by release admission here and
// by the operations data protection admits on its deployments, so each meets
// the other without either importing the other.

// SubjectOf is a deployment's hold path in the operations ledger: by its
// id, so a deployment made again under a taken-down one's name is another
// subject. A take-down holds it whole, which a release meets.
func SubjectOf(dep *Deployment) string {
	return fmt.Sprintf("project:%d/deployment:%d", dep.ProjectID, dep.ID)
}

// DataHold is what an operation on a deployment's data holds: two such
// operations exclude each other, a take-down (which holds the whole
// deployment) excludes them, and a release does not meet them.
func DataHold(dep *Deployment) string { return SubjectOf(dep) + "/data" }

// releaseHold is what a release of a deployment checks: a take-down holds
// it, data operations do not.
func releaseHold(projectID, deploymentID ids.ID) string {
	return fmt.Sprintf("project:%d/deployment:%d/release", projectID, deploymentID)
}

// LockProjectIn takes a project's release lock in the caller's
// transaction: the first lock of every release admission and of every
// operation admitted on its deployments.
func LockProjectIn(ctx context.Context, tx *database.Tx, projectID ids.ID) error {
	_, err := releasesql.New(tx.SQL()).LockReleaseProject(ctx, projectID)
	return err
}

// takeDownHeldIn refuses a release of a deployment while a take-down holds
// it. The caller holds the project's release lock, which every admission
// takes first.
func takeDownHeldIn(ctx context.Context, tx *database.Tx, projectID, deploymentID ids.ID) error {
	if deploymentID.IsZero() {
		return nil
	}
	holder, err := operations.ConflictsIn(ctx, tx, releaseHold(projectID, deploymentID))
	if err != nil {
		return err
	}
	if holder != nil {
		return &operations.HeldError{Holder: holder}
	}
	return nil
}

// RunningOperation is the live operation on a deployment, nil when none is.
func (s *Service) RunningOperation(ctx context.Context, dep *Deployment) (*operations.Operation, error) {
	if s.ops == nil {
		return nil, nil
	}
	return s.ops.LiveOn(ctx, SubjectOf(dep))
}
