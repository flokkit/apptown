package deployment

import (
	"context"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/project"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// releaseSource determines which work a release contains: a bundle's
// selected changes, or a branch's changes shipped since the last release.
type releaseSource string

const (
	ReleaseFromBundle releaseSource = "bundle"
	ReleaseFromBranch releaseSource = "branch"
)

// ReleaseDraft is what a release is cut from.
type ReleaseDraft struct {
	Automatic                                          bool
	Source                                             releaseSource
	ProjectID, DeploymentID, BundleID, RunID, ChangeID ids.ID
	Deployment, Revision, Actor, Notes                 string
	// At is AppTown's time of the release, which release membership
	// compares with when Changes closed. Start stamps it; createReleaseIn
	// requires it.
	At time.Time
}

// NumberedRelease is a release just minted: its row and its number.
type NumberedRelease struct {
	ID     ids.ID
	Number int64
}

var errReleaseInFlight = fault.New(fault.Conflict, "a release is already on its way — watch it finish first")

// Start admits an explicit release: under the project lock it refuses a
// second release in flight (on the deployment, or project-wide for a
// branch release), mints the number, records the membership and writes the
// first line of the log, returning after commit. Preflight and the release
// work itself remain with the caller.
func (s *Releases) Start(ctx context.Context, d ReleaseDraft, description string) (NumberedRelease, error) {
	d.At = s.now()
	return database.Transact(ctx, s.db, func(tx *database.Tx) (NumberedRelease, error) { return startReleaseIn(ctx, tx, d, description) })
}

func startReleaseIn(ctx context.Context, tx *database.Tx, d ReleaseDraft, description string) (NumberedRelease, error) {
	if d.Source != ReleaseFromBundle && d.Source != ReleaseFromBranch {
		return NumberedRelease{}, fmt.Errorf("cannot start release source %q", d.Source)
	}
	q := releasesql.New(tx.SQL())
	if _, err := q.LockReleaseProject(ctx, d.ProjectID); err != nil {
		return NumberedRelease{}, err
	}
	busy, err := q.ReleaseInFlight(ctx, releasesql.ReleaseInFlightParams{
		ProjectID: d.ProjectID, DeploymentID: d.DeploymentID, ProjectWide: d.Source == ReleaseFromBranch, OpenState: Releasing.String(),
	})
	if err != nil {
		return NumberedRelease{}, err
	}
	if busy {
		return NumberedRelease{}, errReleaseInFlight
	}
	r, err := createReleaseIn(ctx, tx, d)
	if err != nil {
		return NumberedRelease{}, err
	}
	if err := appendReleaseEvent(ctx, tx, r.ID, fmt.Sprintf("release #%d %s", r.Number, description), d.At); err != nil {
		return NumberedRelease{}, err
	}
	return r, nil
}

// createReleaseIn mints a release and links the changes it carries inside
// the caller's transaction, which holds the project lock. Landing follow-ups
// create requests only after Work has committed completion.
func createReleaseIn(ctx context.Context, tx *database.Tx, d ReleaseDraft) (NumberedRelease, error) {
	if d.At.IsZero() {
		return NumberedRelease{}, fmt.Errorf("a release needs AppTown's time it was made")
	}
	// Every release door (explicit, workflow completion) holds the project
	// lock here: a project that is not active releases nothing.
	if err := project.RefuseUnlessActiveIn(ctx, tx, d.ProjectID); err != nil {
		return NumberedRelease{}, err
	}
	switch d.Source {
	case ReleaseFromBundle, ReleaseFromBranch:
	default:
		return NumberedRelease{}, fmt.Errorf("unknown release source %q", d.Source)
	}
	// A release and a take-down of one deployment are decided under the
	// same project lock (the caller holds it): while a take-down runs, no
	// release of that deployment is minted, by any door.
	deploymentID := d.DeploymentID
	if deploymentID.IsZero() {
		dep, err := deploymentsql.New(tx.SQL()).DeploymentByName(ctx, deploymentsql.DeploymentByNameParams{ProjectID: d.ProjectID, Name: d.Deployment})
		if found, err := database.Lookup(err); err != nil {
			return NumberedRelease{}, err
		} else if found {
			deploymentID = dep.Deployment.ID
		}
	}
	if !deploymentID.IsZero() {
		dep, err := lockDeploymentRecordIn(ctx, tx, deploymentID)
		if err != nil {
			return NumberedRelease{}, err
		}
		if dep.runtimeBusy() {
			return NumberedRelease{}, errRuntimeBusy
		}
	}
	if !d.Automatic {
		if err := takeDownHeldIn(ctx, tx, d.ProjectID, deploymentID); err != nil {
			return NumberedRelease{}, err
		}
		if err := heldAfterRestoreIn(ctx, tx, deploymentID); err != nil {
			return NumberedRelease{}, err
		}
	}
	q := releasesql.New(tx.SQL())
	r, err := q.CreateRelease(ctx, releasesql.CreateReleaseParams{ID: ids.New(d.At),
		FromBranch: d.Source == ReleaseFromBranch,
		State:      Releasing.String(), ProjectID: d.ProjectID, DeploymentID: d.DeploymentID,
		Deployment: d.Deployment, BundleID: d.BundleID, RunID: d.RunID,
		Revision: d.Revision, Actor: d.Actor, Notes: d.Notes, At: d.At,
	})
	if err != nil {
		return NumberedRelease{}, err
	}
	links, err := q.LinkReleaseChanges(ctx, releasesql.LinkReleaseChangesParams{
		FromBundle: d.Source == ReleaseFromBundle, FromBranch: d.Source == ReleaseFromBranch,
		ProjectID: d.ProjectID, ReleaseID: r.ID,
		BundleID: d.BundleID, Deployment: d.Deployment, ReleasedState: Released.String(),
	})
	if err != nil {
		return NumberedRelease{}, err
	}
	if links.SelectedCount != links.InsertedCount {
		return NumberedRelease{}, fmt.Errorf("release %d did not record its selected changes", r.ID)
	}
	return NumberedRelease{ID: r.ID, Number: r.Number}, nil
}
