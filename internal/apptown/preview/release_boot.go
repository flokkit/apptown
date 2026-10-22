package preview

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/worker"
)

type releaseBoots struct{ service *Service }

// BootReleasePreview executes only under the command's Runtime ownership.
// A release published to a registry boots from its bundle's images by
// digest, building nothing: on the worker when changes' previews boot
// there, else on this AppTown's Docker. A release recorded before then boots
// the image it recorded on this AppTown's Docker.
func (h releaseBoots) BootReleasePreview(ctx context.Context, selected Release) (ReleaseResult, error) {
	s := h.service
	if err := s.refusesCopyPreview(ctx, selected.ProjectID); err != nil {
		return ReleaseResult{}, err
	}
	p, err := s.projects.ByID(ctx, selected.ProjectID)
	if err != nil {
		return ReleaseResult{}, err
	}
	n := selected.Number
	tree, cleanup, err := (deploy.SelectedSource{Repository: s.hosts.ProjectRepository(p.Slug), Revision: selected.SourceRevision}).Stage(ctx, "")
	staged := tree.Directory
	if err != nil {
		return ReleaseResult{}, &ReleaseSourceError{Cause: fmt.Errorf("reading this release's selected source: %w", err)}
	}
	defer cleanup()
	committed, err := docker.ReadSelectedContract(staged)
	if err != nil {
		return ReleaseResult{}, &ReleaseSourceError{Cause: fmt.Errorf("reading this release's native contract: %w", err)}
	}
	label := docker.ReleasePreviewLabel(p.Slug, n)
	name := docker.PreviewLabelName(label)
	stackID := docker.ReleaseStackID(p.Slug, n)
	b := selected.Bundle
	s.Viewed(ctx, name) // a boot is a look
	if b != nil && s.PreviewsOnWorker() {
		return s.bootReleaseOnWorker(ctx, selected.ProjectID, p.Slug, label, stackID, staged, committed, b, tree.Revision)
	}
	// A look at a release is a preview: it runs under the preview profile.
	c, applied, err := s.env.Profiled(ctx, p.ID, "preview", staged, committed)
	if err != nil {
		return ReleaseResult{}, err
	}
	c, _, err = s.env.DefaultMemory(ctx, p.ID, c, applied)
	if err != nil {
		return ReleaseResult{}, err
	}
	// Held while it boots: the release of ended previews' keys leaves a
	// booting preview alone.
	defer s.holdPreview(name)()
	if port, _ := docker.ContainerHostPort(ctx, s.hosts.Local(), name, 0); port > 0 {
		return ReleaseResult{Result: Result{URL: s.env.RoutesFor(ctx, selected.ProjectID).PreviewURL(label, port), HostPort: port, Revision: tree.Revision}, Reused: true}, nil
	}
	// Asked of this machine's daemon, which runs this look whatever runs
	// other previews, before the earlier look is removed: a part larger
	// than it refuses with nothing changed.
	if err := docker.AdmitResources(ctx, s.hosts.Local(), c); err != nil {
		return ReleaseResult{}, err
	}
	// Every look starts with disposable data, including retries after a failed
	// boot. Named standing-environment volumes are never part of this stack.
	if err := docker.RemovePreview(context.WithoutCancel(ctx), s.hosts.Local(), name, stackID); err != nil {
		return ReleaseResult{}, fmt.Errorf("removing what an earlier look at this version left: %w", err)
	}
	req := docker.BootRequest{Name: name, StackID: stackID, Workspace: staged,
		Slug: p.Slug, Deployment: "preview", Docker: s.hosts.Local(), Probe: 90 * time.Second}
	if b != nil {
		req.Recorded = &docker.RecordedImages{Parts: b.Parts, Init: b.Init, Login: s.env.BundleLogin(ctx, selected.ProjectID, b), Scratch: s.hosts.BootScratch()}
	} else {
		req.ReleaseImage = selected.Image
	}
	res, err := s.env.Host(p.ID, s.env.NeedCredentials(deployment.PreviewNeedScope(name)), func(string) deployment.KeyOwner { return s.previewKeys(p.ID, name) }).Boot(ctx, req, c)
	if err != nil {
		if rerr := docker.RemovePreview(context.WithoutCancel(ctx), s.hosts.Local(), name, stackID); rerr != nil {
			log.Printf("%s: removing a look that did not start: %v", name, rerr)
		}
		return ReleaseResult{}, fmt.Errorf("could not start this version: %w", err)
	}
	return ReleaseResult{Result: Result{URL: s.env.RoutesFor(ctx, selected.ProjectID).PreviewURL(label, res.HostPort), HostPort: res.HostPort, Revision: tree.Revision}}, nil
}

// bootReleaseOnWorker boots a published release's preview on the worker,
// from its images by digest, with the values a change's preview is given
// (the preview credential among them). The worker keeps a running
// incarnation that matches. AppTown keeps its record, so the worker's
// reconciliation leaves it and visitors are forwarded to it.
func (s *Service) bootReleaseOnWorker(ctx context.Context, projectID ids.ID, slug, label, stackID, tree string, c *appcontract.Contract, b *deployment.ReleaseBundle, revision string) (ReleaseResult, error) {
	name := docker.PreviewLabelName(label)
	defer s.holdPreview(name)()
	discards := s.discardsSoFar(name)
	prepared, err := s.preparePreview(ctx, projectID, slug, name, tree, c, nil)
	if err != nil {
		return ReleaseResult{}, err
	}
	if err := s.admit(ctx, docker.ResourceAdmissionContract(prepared.Contract, prepared.Services)); err != nil {
		return ReleaseResult{}, err
	}
	if prepared, err = s.withPreviewKeys(ctx, projectID, name, prepared); err != nil {
		return ReleaseResult{}, err
	}
	public := prepared.Public()
	incarnation := fmt.Sprintf("bundle-%d+%s", b.ID, public.Digest())
	if _, err := s.bootOnWorker(ctx, worker.PreviewSpec{Name: name, StackID: stackID, Slug: slug, Incarnation: incarnation, ProbeSeconds: 90,
		Images: &worker.PreviewImages{Parts: b.Parts, Init: b.Init}}, prepared, tree); err != nil {
		return ReleaseResult{}, fmt.Errorf("could not start this version: %w", err)
	}
	if !s.recordServedSince(name, discards, servedRecord{Incarnation: incarnation, Source: revision, Public: public, At: s.now()}) {
		log.Printf("%s: its preview was discarded while it booted; AppTown keeps no record of it", name)
	}
	return ReleaseResult{Result: Result{URL: s.env.RoutesFor(ctx, projectID).PreviewURL(label, 0), Revision: revision, Incarnation: incarnation, Public: &public}}, nil
}

// ServedOnWorker says whether AppTown keeps a record of a preview it
// booted on the worker by this name: where a release's preview is reached.
func (s *Service) ServedOnWorker(name string) bool {
	if !s.PreviewsOnWorker() {
		return false
	}
	_, ok := s.servedRecordOf(name)
	return ok
}
