package apptown

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/preview"

	"github.com/flokkit/apptown/internal/fault"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"

	"github.com/flokkit/apptown/internal/apptown/changeconsole"
	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// previewSources translates Work records and Console labels. Eligibility and
// preview permission belong to Preview; Work checks its own read door.
//
// A change a chat on the worker holds is previewed as saved: the chat saves
// first, and the preview boots the saved commit, never an older tree. A save
// refused or not confirmed refuses the preview.
type previewSources struct {
	changes  workflow.Changes
	sessions *console.Service
	db       *database.DB
	saves    *changeconsole.Saves
	releases *deployment.Releases
	dataDir  string
}

// A change being landed is previewed as its landing's pinned candidate,
// the commit its gates check and the merge takes: so the preview is one the
// landing's runtime checks can be judged against, and nothing the folder
// holds besides. A landing not pinned yet previews the folder.
func (d previewSources) Change(ctx context.Context, id ids.ID) (preview.Change, error) {
	ch, err := d.changes.Change(ctx, id)
	if err != nil {
		return preview.Change{}, err
	}
	out := previewChange(ch)
	// A shipped change with no code of its own landed nothing: it is what
	// its release shipped, main as it stood, never a folder it never had.
	if ch.State == workflow.ChangeShipped && ch.LandedSHA == "" {
		noCode, err := workflow.CarriesNoCodeIn(ctx, d.db, ch)
		if err != nil {
			return preview.Change{}, err
		}
		if noCode {
			shipped, err := d.releases.ShippedBy(ctx, ch.ProjectID, ch.ID)
			if err != nil {
				return preview.Change{}, err
			}
			if shipped == nil || shipped.Commit == "" {
				return preview.Change{}, fault.Wrap(fault.Conflict, fmt.Errorf("change %d has no code of its own and no release of it recorded what it shipped", ch.Number))
			}
			out.SourceRevision = shipped.Commit
		}
	}
	if ch.State == workflow.ChangeLanding && !ch.RunID.IsZero() {
		landing, pinned, err := workflow.LandingCandidateIn(ctx, d.db, ch.RunID)
		switch {
		case errors.Is(err, workflow.ErrLandingUnpinned):
		case err != nil:
			return preview.Change{}, err
		case pinned:
			out.Pinned = landing.Candidate
		}
	}
	return out, nil
}

// Pin saves a chat on the worker that holds the change, after the preview
// was authorized, and pins the preview to the commit that save left on the
// branch.
func (d previewSources) Pin(ctx context.Context, in preview.Change) (preview.Change, error) {
	ch, err := d.changes.Change(ctx, in.ID)
	if err != nil {
		return preview.Change{}, err
	}
	holder, err := d.saves.HeldBy(ctx, ch)
	if err != nil || holder == "" {
		return in, err
	}
	saved, err := d.saves.SaveAndReport(ctx, holder)
	if err != nil {
		return preview.Change{}, fault.Wrap(fault.Conflict, fmt.Errorf("change %d was not previewed: %w", ch.Number, err))
	}
	if saved == "" {
		return preview.Change{}, fault.Wrap(fault.Conflict, fmt.Errorf("change %d was not previewed: its chat's save named no commit", ch.Number))
	}
	in.Pinned = saved
	return in, nil
}

func (d previewSources) Console(ctx context.Context, id string) (preview.Console, error) {
	session, err := d.sessions.Get(ctx, id)
	if err != nil {
		return preview.Console{}, err
	}
	return preview.Console{ChangeID: conversation.Of(session).ChangeID, ProjectID: conversation.Of(session).ProjectID, Actor: session.Actor}, nil
}

func previewChange(ch *workflow.Change) preview.Change {
	return preview.Change{ID: ch.ID, ProjectID: ch.ProjectID, SourceRevision: ch.LandedSHA}
}

// changePreviewViews says what a change's preview button shows, with the
// change's state from Work.
type changePreviewViews struct{ previews *preview.Service }

func (v changePreviewViews) State(ctx context.Context, ch *workflow.Change) preview.Status {
	return v.previews.ChangeStatus(ctx, ch.ProjectID, ch.ID, ch.State == workflow.ChangeInProgress)
}
func changePreviewViewsFor(a *App) changePreviewViews { return changePreviewViews{previews: a.Preview} }

// previewDependencies is what Preview reads and runs on: Deployment's
// records and collaborators, the same instances Deployment uses.
func previewDependencies(a *App, changes workflow.Changes) preview.Dependencies {
	var worker preview.Worker
	if a.workerPreviews != nil {
		worker = a.workerPreviews
	}
	source := a.docker.Host
	if a.cfg.worker != nil {
		source = a.cfg.worker.URL
	}
	return preview.Dependencies{ObservationQueue: previewObservationRequests{db: a.DB, now: a.cfg.clock.Now}, ObservationSource: source,
		Authority: access.Checked{Authority: a.Access, Projects: a.Projects},
		Sources:   previewSources{changes: changes, sessions: a.Console, db: a.DB, saves: a.chatSaves, releases: a.Deployment.Releases, dataDir: a.cfg.dataDir},
		Worker:    worker, Hosts: a.hosts, Environment: a.environment,
		Targets: a.targets, Deployments: a.deployments, Releases: a.Deployment.Releases, Bundles: a.Deployment.Bundles, Retained: a.Deployment.Retained,
		Projects: a.Projects, Integrations: a.Integrations, DataDir: a.cfg.dataDir, Now: a.cfg.clock.Now,
	}
}

type previewObservationRequests struct {
	db  *database.DB
	now func() time.Time
}

func (r previewObservationRequests) RequestPreviewObservation(ctx context.Context, id ids.ID) error {
	return r.db.InTx(ctx, func(tx *database.Tx) error {
		return jobQueue{}.EnqueueIn(ctx, tx, jobs.Request{Operation: preview.ObservationJob, Key: id.String() + "/" + ids.New(r.now()).String()})
	})
}
