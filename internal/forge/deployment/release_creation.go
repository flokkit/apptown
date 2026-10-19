package deployment

import (
	"context"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/project"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
	"flokkit.io/forge/internal/forge/deployment/internal/releasesql"
)

// ReleaseSource determines which work a release contains: a bundle's
// selected changes, a branch's changes shipped since the last release, or a
// workflow receipt describing a deployment already performed.
type ReleaseSource string

const (
	ReleaseFromBundle   ReleaseSource = "bundle"
	ReleaseFromBranch   ReleaseSource = "branch"
	ReleaseFromWorkflow ReleaseSource = "workflow"
)

// InitialState is the state a release is cut in: a workflow receipt
// describes a deployment already performed, so it is released; the others
// start releasing and settle with their deployment attempt.
func (src ReleaseSource) InitialState() ReleaseState {
	if src == ReleaseFromWorkflow {
		return Released
	}
	return Releasing
}

// ReleaseDraft is what a release is cut from.
type ReleaseDraft struct {
	Source                                             ReleaseSource
	ProjectID, DeploymentID, BundleID, RunID, ChangeID ids.ID
	Deployment, Revision, Actor, Notes                 string
	// At is the forge's time of the release, which release membership
	// compares with when Changes closed. Start stamps it; CreateReleaseIn
	// requires it.
	At time.Time
}

// NumberedRelease is a release just minted: its row and its number.
type NumberedRelease struct {
	ID     ids.ID
	Number int64
}

var ErrReleaseInFlight = fault.New(fault.Conflict, "a release is already on its way — watch it finish first")

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
		return NumberedRelease{}, ErrReleaseInFlight
	}
	r, err := CreateReleaseIn(ctx, tx, d)
	if err != nil {
		return NumberedRelease{}, err
	}
	if err := AppendReleaseEvent(ctx, tx, r.ID, fmt.Sprintf("release #%d %s", r.Number, description), d.At); err != nil {
		return NumberedRelease{}, err
	}
	return r, nil
}

// CreateReleaseIn mints a release and links the changes it carries inside
// the caller's transaction, which holds the project lock. Workflow
// completion uses its own transaction so release creation, run outcome and
// follow-up intent stay atomic.
func CreateReleaseIn(ctx context.Context, tx *database.Tx, d ReleaseDraft) (NumberedRelease, error) {
	if d.At.IsZero() {
		return NumberedRelease{}, fmt.Errorf("a release needs AppTown's time it was made")
	}
	// Every release door (explicit, workflow completion) holds the project
	// lock here: a project that is not active releases nothing.
	if err := project.RefuseUnlessActiveIn(ctx, tx, d.ProjectID); err != nil {
		return NumberedRelease{}, err
	}
	switch d.Source {
	case ReleaseFromBundle, ReleaseFromBranch, ReleaseFromWorkflow:
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
			deploymentID = dep.ID
		}
	}
	if err := takeDownHeldIn(ctx, tx, d.ProjectID, deploymentID); err != nil {
		return NumberedRelease{}, err
	}
	q := releasesql.New(tx.SQL())
	r, err := q.CreateRelease(ctx, releasesql.CreateReleaseParams{ID: ids.New(d.At),
		FromWorkflow: d.Source == ReleaseFromWorkflow, FromBranch: d.Source == ReleaseFromBranch,
		State: d.Source.InitialState().String(), ProjectID: d.ProjectID, DeploymentID: d.DeploymentID,
		Deployment: d.Deployment, BundleID: d.BundleID, RunID: d.RunID,
		Revision: d.Revision, Actor: d.Actor, Notes: d.Notes, At: d.At,
	})
	if err != nil {
		return NumberedRelease{}, err
	}
	links, err := q.LinkReleaseChanges(ctx, releasesql.LinkReleaseChangesParams{
		FromWorkflow: d.Source == ReleaseFromWorkflow, FromBundle: d.Source == ReleaseFromBundle, FromBranch: d.Source == ReleaseFromBranch,
		ProjectID: d.ProjectID, ReleaseID: r.ID, ChangeID: d.ChangeID,
		BundleID: d.BundleID, Deployment: d.Deployment, ReleasedState: Released.String(),
	})
	if err != nil {
		return NumberedRelease{}, err
	}
	if links.SelectedCount != links.InsertedCount || (d.Source == ReleaseFromWorkflow && links.InsertedCount != 1) {
		return NumberedRelease{}, fmt.Errorf("release %d did not record its selected changes", r.ID)
	}
	return NumberedRelease{ID: r.ID, Number: r.Number}, nil
}
