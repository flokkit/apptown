package deployment

import (
	"context"
	"fmt"
	"sync"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
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
// operation admitted on its deployments, then the deployment's row, then
// the operations ledger's admission lock.
func LockProjectIn(ctx context.Context, tx *database.Tx, projectID ids.ID) error {
	_, err := releasesql.New(tx.SQL()).LockReleaseProject(ctx, projectID)
	return err
}

// runtimeActs are the deployments a runtime act is under way on in this
// process, what QuietIn sees of an act no record shows yet. Ids are unique,
// so one process-wide set serves.
var runtimeActs sync.Map

// beginAct admits a runtime act, a person's or a health act: it takes the
// deployment's act slot, then decides under the project's release lock and
// the deployment's row lock, refused while a take-down or an in-place
// restore holds the deployment. end releases the slot once the act is over.
func (s *Service) beginAct(ctx context.Context, projectID, id ids.ID, decide func(*database.Tx, *Deployment) error) (end func(), err error) {
	if _, busy := runtimeActs.LoadOrStore(id, struct{}{}); busy {
		return nil, errRuntimeBusy
	}
	end = func() { runtimeActs.Delete(id) }
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		if err := LockProjectIn(ctx, tx, projectID); err != nil {
			return err
		}
		dep, err := lockDeploymentRecordIn(ctx, tx, id)
		if err == nil {
			err = takeDownHeldIn(ctx, tx, projectID, id)
		}
		if err != nil {
			return err
		}
		return decide(tx, dep)
	})
	if err != nil {
		end()
		return nil, err
	}
	return end, nil
}

// QuietIn refuses while anything may start the deployment's writers: a
// runtime act recorded or under way, an open attempt, or a release in flight
// other than except (a rollback's own restore runs inside its release). A
// take-down and an in-place restore are admitted only through it.
func QuietIn(ctx context.Context, tx *database.Tx, id, except ids.ID) error {
	dep, err := lockDeploymentRecordIn(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, acting := runtimeActs.Load(id); acting || dep.runtimeBusy() {
		return errRuntimeBusy
	}
	busy, err := deploymentsql.New(tx.SQL()).HasOpenAttempt(ctx, deploymentsql.HasOpenAttemptParams{DeploymentID: id, RunningState: AttemptRunning.String(), UncertainState: AttemptUncertain.String()})
	if err == nil && !busy {
		busy, err = releasesql.New(tx.SQL()).ReleaseInFlight(ctx, releasesql.ReleaseInFlightParams{ProjectID: dep.ProjectID, DeploymentID: id, OpenState: Releasing.String(), ExceptID: except})
	}
	if err == nil && busy {
		err = ErrRemovalBusy
	}
	return err
}

// takeDownHeldIn refuses a release of a deployment while a take-down or an
// in-place restore holds it. The caller holds the project's release lock,
// which every admission takes first.
func takeDownHeldIn(ctx context.Context, tx *database.Tx, projectID, deploymentID ids.ID) error {
	holder, err := operations.ConflictsIn(ctx, tx, releaseHold(projectID, deploymentID))
	if err != nil {
		return err
	}
	if holder != nil {
		return &operations.HeldError{Holder: holder}
	}
	return nil
}
