package preview

import (
	"context"
	"fmt"
	projectrepo "github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/worker"
)

type changeBoots struct{ service *Service }

// changeBoot is a change preview prepared: its source and tree, the
// prepared values, and the incarnation they make.
type changeBoot struct {
	slug     string
	source   deploy.PreviewSource
	tree     deploy.SourceTree
	prepared docker.Prepared
	public   docker.PublicPrepared
	// commit is the commit the tree is, "" when it is the change's folder
	// (which is not a commit, and so never evidence of one).
	commit      string
	incarnation string
}

// prepareChange prepares a change's preview without any effect on the
// running one: its tree (the folder, or a snapshot of the pinned or selected
// commit) and every value its boot reads. The incarnation names the commit
// when the tree is one, and the folder's fingerprint otherwise.
func (s *Service) prepareChange(ctx context.Context, ch Change) (changeBoot, error) {
	project, err := s.projects.ByID(ctx, ch.ProjectID)
	if err != nil {
		return changeBoot{}, err
	}
	repository, err := s.hosts.ProjectRepository(projectrepo.RepositoryRef{ID: project.ID, Slug: project.Slug})
	if err != nil {
		return changeBoot{}, err
	}
	b := changeBoot{slug: project.Slug, source: deploy.PreviewSource{
		Worktree:  filepath.Join(s.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID)),
		History:   deploy.SelectedSource{Repository: repository, Revision: ch.SourceRevision},
		Snapshots: s.changeSnapshots(ch.ID),
	}}
	// A landed change is what it landed as, never its folder, which keeps
	// the branch tip and any leftovers until the workspace sweep; the
	// folder is the source only of a change that has not landed. A pinned
	// save is the saved commit.
	switch {
	case ch.Pinned != "":
		b.source.Worktree, b.source.History.Revision = "", ch.Pinned
	case ch.SourceRevision != "":
		b.source.Worktree = ""
	}
	if b.tree, err = b.source.Prepare(ctx); err != nil {
		return changeBoot{}, err
	}
	c, err := docker.ReadSelectedContract(b.tree.Directory)
	if err == docker.ErrNoContract {
		return changeBoot{}, fmt.Errorf("nothing runnable yet — the app has no runtime contract")
	}
	if err != nil {
		return changeBoot{}, err
	}
	if b.prepared, err = s.preparePreview(ctx, ch.ProjectID, project.Slug, docker.PreviewChangeName(ch.ID), b.tree.Directory, c, nil); err != nil {
		return changeBoot{}, err
	}
	b.public = b.prepared.Public()
	source := docker.WorktreeFingerprint(b.tree.Directory)
	if b.tree.Directory != b.source.Worktree {
		b.commit, source = b.tree.Revision, b.tree.Revision
	}
	b.incarnation = source + "+" + b.public.Digest()
	return b, nil
}

// ChangePreviewIdentity is the incarnation a change's preview would be,
// prepared now, without booting it or touching the running one.
func (s *Service) ChangePreviewIdentity(ctx context.Context, ch Change) (string, error) {
	b, err := s.prepareChange(ctx, ch)
	return b.incarnation, err
}

// BootPreview runs under Runtime ownership, including restart reaping.
func (h changeBoots) BootPreview(ctx context.Context, ch Change, restart bool) (out Result, err error) {
	s := h.service
	if err := s.refusesCopyPreview(ctx, ch.ProjectID); err != nil {
		return Result{}, err
	}
	name := docker.PreviewChangeName(ch.ID)
	// A boot is a look, recorded before the boot holds the preview: the
	// expiry of what it kept reads it under that hold (looks.go).
	s.Viewed(ctx, name)
	// Held until the record below is kept, released last: a reconciliation,
	// and the release of ended previews' keys, leave a booting preview alone.
	defer s.holdPreview(name)()
	// The boot keeps its record only if no one discards the preview from
	// now on: its own replacement of an earlier one is not a discard.
	discards := s.discardsSoFar(name)
	defer func() {
		if out.URL != "" {
			s.recordPreview(ctx, ch.ProjectID, ch.ID, name, out.HostPort, out.URL)
		}
	}()
	// Everything the boot reads is prepared first, before anything is
	// removed: a failure leaves the running preview as it is
	// (design/operations/workers.md, "Previews on the worker").
	b, err := s.prepareChange(ctx, ch)
	if err != nil {
		return Result{}, err
	}
	// A part or need larger than the daemon it boots on can give a
	// container is refused before its keys are registered or the running
	// preview is replaced, a restart's included.
	if err := s.admit(ctx, docker.ResourceAdmissionContract(b.prepared.Contract, b.prepared.Services)); err != nil {
		return Result{}, err
	}
	if b.prepared, err = s.withPreviewKeys(ctx, ch.ProjectID, name, b.prepared); err != nil {
		return Result{}, err
	}
	// Once the preview serves this tree, no container mounts another
	// snapshot of the change's history, and AppTown records what it
	// serves.
	defer func() {
		if err == nil {
			if !s.recordServedSince(name, discards, servedRecord{Incarnation: b.incarnation, Source: b.commit, Public: b.public, At: s.now()}) {
				log.Printf("change %d: its preview was discarded while it booted; AppTown keeps no record of it", ch.ID)
			}
			if rerr := b.source.Retain(b.tree); rerr != nil {
				log.Printf("change %d: removing snapshots its preview no longer serves: %v", ch.ID, rerr)
			}
		}
	}()
	ws, c, prepared, public, incarnation := b.tree.Directory, b.prepared.Contract, b.prepared, b.public, b.incarnation
	label := fmt.Sprintf("change-%d", ch.ID)
	// A restart owns the same interval as normal boots before removing
	// anything. A removal that fails stops here: the container that survived
	// is never taken for the new incarnation.
	if restart {
		if err := s.removeChangePreview(ctx, ch.ID); err != nil {
			return Result{}, fmt.Errorf("removing the preview before starting it again: %w", err)
		}
	}
	if s.PreviewsOnWorker() {
		// The worker keeps a running incarnation that matches, and replaces
		// anything else wholly, under its own lock for this preview.
		if _, err := s.bootOnWorker(ctx, worker.PreviewSpec{Name: name, StackID: docker.ChangeStackID(ch.ID), Slug: b.slug,
			Incarnation: incarnation, ProbeSeconds: 45}, prepared, ws); err != nil {
			return Result{}, wordChangeBootError(err)
		}
		return Result{URL: s.env.RoutesFor(ctx, ch.ProjectID).PreviewURL(label, 0), Revision: b.tree.Revision, Incarnation: incarnation, Public: &public}, nil
	}

	// A running preview pins what it serves as of ITS boot: the source and
	// the prepared values, whatever the form. Ask
	// the container which incarnation it is (the label its boot stamped)
	// and compare it with this one. Anything else (another tree, an
	// attachment edited since, a container from before the label, a
	// restart's rebuild from main) is removed so the reuse check below
	// cannot hand back the wrong software.
	if s.hosts.Local().Running(ctx, name) && s.servedIncarnation(ctx, name) != incarnation {
		if err := s.removeChangePreview(ctx, ch.ID); err != nil {
			return Result{}, fmt.Errorf("removing the preview of another version: %w", err)
		}
	}
	// Reuse only a container that says it is this incarnation.
	if s.servedIncarnation(ctx, name) == incarnation {
		if prt, err := docker.ContainerHostPort(ctx, s.hosts.Local(), name, c.PublicPart().Port); err == nil && prt > 0 {
			// A container with a port is not yet an app that answers: the chat's
			// own rehearsal may be mid-boot when the helper runs `apptown preview`,
			// and handing back the URL then reads as "connection refused".
			// Ask the app the same question the cold boot asks,
			// briefly; a container that never answers is removed so the fresh
			// boot below starts clean rather than reusing a corpse.
			probe := docker.RenderColdBootProbe(c.PublicPart(), s.env.FrontDoorHeaders())
			if up, _ := s.env.Probe().Check(ctx, s.hosts.Local(), name, s.hosts.Local().ProbeURL(prt), 45*time.Second, probe); up {
				return Result{URL: s.env.RoutesFor(ctx, ch.ProjectID).PreviewURL(label, prt), Revision: b.tree.Revision, HostPort: prt,
					Incarnation: incarnation, Public: &public}, nil
			}
			// A caller that went away cut the probe short: its health is
			// unknown, not dead, and nothing is removed for it.
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			if err := s.removeChangePreview(ctx, ch.ID); err != nil {
				return Result{}, fmt.Errorf("removing a preview that does not answer: %w", err)
			}
		}
	}
	// The one sequence (boot.go): needs up and healthy → init containers to
	// completion → non-public parts → the app → probe. A throwaway stack, a
	// LOOK never a restore: --rm and blank each boot, torn down with the
	// preview and swept at restart.
	life, cancel := context.WithTimeout(context.WithoutCancel(ctx), docker.BootLifetime)
	defer cancel()
	res, err := s.env.PreparedHost(prepared).Boot(life, docker.BootRequest{Docker: s.hosts.Local().WithLifetime(life),
		Name: name, StackID: docker.ChangeStackID(ch.ID), Workspace: ws, Slug: b.slug,
		Deployment: "preview", Probe: 45 * time.Second, Incarnation: incarnation, Scratch: s.hosts.BootScratch(),
	}, prepared.Contract)
	if err != nil {
		// A boot that failed leaves nothing of the preview, at whatever
		// phase, as on the worker: a dead app must not satisfy the reuse
		// check, and services a later phase never reached must not run on.
		if derr := s.removeChangePreview(ctx, ch.ID); derr != nil {
			log.Printf("change %d: removing a preview that did not come up: %v", ch.ID, derr)
		}
		return Result{}, wordChangeBootError(err)
	}
	return Result{URL: s.env.RoutesFor(ctx, ch.ProjectID).PreviewURL(label, res.HostPort), Revision: b.tree.Revision, HostPort: res.HostPort,
		Incarnation: incarnation, Public: &public}, nil
}

func (s *Service) recordPreview(ctx context.Context, projectID, changeID ids.ID, container string, port int, url string) {
	ctx = context.WithoutCancel(ctx) // the preview runs: its row is recorded though ctx has ended
	target, err := s.targets.ByName(ctx, s.hosts.BuiltInTarget())
	if err != nil {
		log.Printf("change %d: resolving its preview deployment target: %v", changeID, err)
		return
	}
	if err := s.deployments.RecordPreview(ctx, projectID, changeID, target.ID, container, port, url); err != nil {
		log.Printf("change %d: recording its preview deployment: %v", changeID, err)
	}
}

// DiscardChangePreview is the trusted completion/cleanup participant. It drops
// only this change's disposable stack and transient inventory row. A removal
// that is not confirmed is an error: the caller's job tries again, and the
// row and AppTown's record stay until it succeeds.
func (s *Service) DiscardChangePreview(ctx context.Context, changeID ids.ID) error {
	if err := s.discardChangePreview(ctx, changeID); err != nil {
		return fmt.Errorf("removing change %d's preview: %w", changeID, err)
	}
	return nil
}

// discardChangePreview is AppTown deciding it no longer wants the
// change's preview: the decision is counted, so a boot already under way
// keeps no record, and the preview is removed.
func (s *Service) discardChangePreview(ctx context.Context, changeID ids.ID) error {
	s.discardDecided(docker.PreviewChangeName(changeID))
	return s.removeChangePreview(ctx, changeID)
}

// removeChangePreview removes the change's preview and forgets its record
// and row; a removal that fails keeps the row. A boot replacing its own
// earlier preview uses it: that is not a discard.
func (s *Service) removeChangePreview(ctx context.Context, changeID ids.ID) error {
	name := docker.PreviewChangeName(changeID)
	// AppTown no longer keeps it, whether or not the worker confirms its
	// removal now: a reconciliation removes it later if not.
	s.forgetServed(name)
	var err error
	if s.PreviewsOnWorker() {
		err = s.removeOnWorker(ctx, name)
	} else {
		// AppTown has decided: the local removal is a clean-up, finished
		// though the caller stops.
		err = docker.RemovePreview(context.WithoutCancel(ctx), s.hosts.Local(), name, docker.ChangeStackID(changeID))
	}
	if err != nil {
		return err
	}
	if ferr := s.deployments.ForgetPreview(ctx, changeID); ferr != nil {
		log.Printf("change %d: forgetting its preview deployment: %v", changeID, ferr)
	}
	return nil
}

// servedIncarnation is the incarnation a preview container says it serves
// (its AppTown.incarnation label); "" for none, or a container without one.
func (s *Service) servedIncarnation(ctx context.Context, name string) string {
	return strings.TrimSpace(s.hosts.Local().Text(ctx, "inspect", "-f", `{{index .Config.Labels "`+dockerlabel.Incarnation+`"}}`, name))
}

// RehearseChange is the existing trusted chat/engine participant. The caller
// admits the rehearsal; the same preview interval owns preparation and cleanup.
// User-facing requests go through RequestChangePreview.
func (s *Service) RehearseChange(ctx context.Context, ch Change, restart bool) (Result, error) {
	return s.runtime.BootChange(ctx, ch, restart, s.host)
}

// preparePreview resolves everything a preview boot of c from ws reads, for
// the "preview" deployment, with services replacing the contract's sidecars
// (a workflow's services:), and the needs' credentials of the preview
// named: the same whenever it is prepared, so preparing has no effect and
// its incarnation is stable (deployment.PreviewNeedScope).
func (s *Service) preparePreview(ctx context.Context, projectID ids.ID, slug, preview, ws string, c *appcontract.Contract, services []deploy.ServiceSpec) (docker.Prepared, error) {
	p, err := s.env.PrepareBoot(ctx, projectID, slug, "preview", ws, c, s.env.NeedCredentials(deployment.PreviewNeedScope(preview)))
	if err != nil {
		return p, err
	}
	p.Services, _, err = deployment.SuppliedServiceDefaults(p.Contract, services)
	return p, err
}

// withPreviewKeys gives a prepared preview boot the values its registered
// needs inject, its AI models key among them, owned by the preview named.
// Preparing alone makes no key: only a boot does.
func (s *Service) withPreviewKeys(ctx context.Context, projectID ids.ID, preview string, p docker.Prepared) (docker.Prepared, error) {
	return s.env.WithRegistered(ctx, p, s.previewKeys(projectID, preview))
}
