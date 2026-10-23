package dataprotection

import (
	"context"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/deploy"
)

// TriggerAppTownBackup names a set taken for a whole-AppTown backup
// (stock/docs/guides/apptown-backups.md). It is an ordinary set on
// AppTown's disk, counted in the sets kept per deployment and moved to the
// backup store by the daily sweep like any other.
const TriggerAppTownBackup = "apptown-backup"

// TakeStoppedSet takes a set of a built-in deployment's data with every
// application part stopped, as a take-down's final set is, keeps it on
// AppTown's disk (the archive holds its files), and starts the parts again.
// Nothing writes while it is copied, so the set is one moment of the app.
// ErrNothingToBackUp when the deployment holds no data. A part that does not
// start again is named in the error beside the set, which was taken.
func (s *Service) TakeStoppedSet(ctx context.Context, dep *deployment.Deployment, actor string) (*Backup, error) {
	release, err := s.dataWork.claim(dep.ID, "a backup of AppTown")
	if err != nil {
		return nil, err
	}
	defer release()
	t, err := s.targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return nil, err
	}
	if t.DataAccess() != deployment.DataOnDockerHost {
		return nil, fmt.Errorf("%s is not on a Docker host", dep.Name)
	}
	p, err := project.ByIDIn(ctx, s.db, dep.ProjectID)
	if err != nil {
		return nil, err
	}
	dk, err := s.hosts.DockerScope(ctx, t)
	if err != nil {
		return nil, err
	}
	owned, err := s.deployments.Owned(ctx, dep.ID)
	if err != nil {
		return nil, fmt.Errorf("reading what %s owns: %w", dep.Name, err)
	}
	// The release names data the list may lack; unread, the list alone is
	// planned, as a take-down's final set is.
	c, _ := s.releaseContract(ctx, dep)
	plan, named, err := s.heldFrom(ctx, dk, p.Slug, dep, owned, c)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: %v", ErrDataNotKnown, err)
	case !named:
		return nil, ErrNothingToBackUp
	case len(plan) == 0:
		return nil, ErrHeldDataAbsent
	}
	for i := range plan {
		if plan[i].Kind == "volume" && plan[i].Unresolved == "" {
			// Every application part is stopped before the copy: nothing writes.
			plan[i].Copy, plan[i].How = CopyFiles, "tar of the volume "+plan[i].Volume
		}
	}
	if err := oneFileEach(plan); err != nil {
		return nil, err
	}
	// The writers: what the deployment's list records, and the public part
	// and every other part its release names, as a restore stops them.
	names, err := dep.HostNames(p.Slug)
	if err != nil {
		return nil, err
	}
	app := dep.Container
	if app == "" {
		app = names.App()
	}
	writers := append([]deploy.Owned{{Kind: "container", ID: app, Name: app}}, owned...)
	if c != nil {
		for _, part := range c.Parts {
			if !part.Public {
				name := names.Service(part.Name)
				writers = append(writers, deploy.Owned{Kind: "container", ID: name, Name: name})
			}
		}
	}
	stopped, err := stopParts(ctx, dk, writers, nil)
	if err != nil {
		why := err.Error()
		if failed := stopped.restart(ctx); len(failed) > 0 {
			why += "; these could not be started again, so the deployment is not serving: " + strings.Join(failed, "; ")
		}
		return nil, fmt.Errorf("stopping %s before its copy: %s", dep.Name, why)
	}
	b, err := s.takeSetWhere(ctx, dk, dep.ProjectID, p.Slug, dep.Name, plan, TriggerAppTownBackup, actor, true)
	if failed := stopped.restart(ctx); len(failed) > 0 {
		return b, fmt.Errorf("%s's parts could not be started again after its copy, so it is not serving: %s", dep.Name, strings.Join(failed, "; "))
	}
	return b, err
}
