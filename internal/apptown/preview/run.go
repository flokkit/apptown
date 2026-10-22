package preview

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/worker"
)

// RunInput is a run's preview step: the commit to boot and the services
// that replace the contract's sidecars.
type RunInput struct {
	// Source names the run's commit to boot: Workspace is the repository
	// that holds it and Revision the commit (verification.SelectCommit).
	Source   deploy.Source
	Services []deploy.ServiceSpec
}

// ContractError is a commit whose contract cannot be read: the app's
// to fix.
type ContractError struct {
	Commit string
	Cause  error
}

func (e *ContractError) Error() string {
	return fmt.Sprintf("the contract in commit %s: %v", e.Commit, e.Cause)
}
func (e *ContractError) Unwrap() error { return e.Cause }

// RunResult is a run's booted preview: the boot, and the digest of
// the image its app runs, read where it runs.
type RunResult struct {
	Boot   docker.BootResult
	Digest string
	// Incarnation is what the preview serves: its tree and the digest of
	// its prepared public values.
	Incarnation string
	// URL is where the run's preview is reached, booted or not.
	URL string
}

// RunPreview boots a run's preview for Work's claimed preview step: the
// run's commit, wherever previews boot, keeping the record of what it serves.
func (s *Service) RunPreview(ctx context.Context, claim deployment.ClaimedExecution, in RunInput) (RunResult, error) {
	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}
	if claim == nil {
		return RunResult{}, errors.New("preview requires a claimed Work execution")
	}
	projectID, runID, stepID, err := claim.ClaimedStep("preview")
	if err != nil {
		return RunResult{}, err
	}
	if projectID.IsZero() || runID.IsZero() || stepID.IsZero() || in.Source.Workspace == "" || in.Source.Revision == "" {
		return RunResult{}, errors.New("invalid preview execution inputs")
	}
	if err := s.refusesCopyPreview(ctx, projectID); err != nil {
		return RunResult{}, err
	}
	p, err := s.projects.ByID(ctx, projectID)
	if err != nil {
		return RunResult{}, err
	}
	if p.Slug != in.Source.Slug {
		return RunResult{}, errors.New("preview source belongs to another project")
	}
	name := docker.PreviewRunName(runID)
	s.Viewed(ctx, name) // a boot is a look
	// Held until its record is kept: a reconciliation, and the release of
	// ended previews' keys, leave a booting preview alone.
	defer s.holdPreview(name)()
	discards := s.discardsSoFar(name)
	// A run's preview boots the run's commit: a snapshot of the commit its
	// gates read, from the history that holds it (the folder, or the
	// project's repository when the folder is behind its branch), never the
	// folder with whatever it holds besides. Everything the boot reads is
	// prepared here, wherever it boots, and the incarnation it serves is
	// labelled on it: that commit and the prepared public values.
	source := deploy.PreviewSource{History: deploy.SelectedSource{Repository: in.Source.Workspace, Revision: in.Source.Revision},
		Snapshots: s.runSnapshots(runID)}
	tree, err := source.Prepare(ctx)
	if err != nil {
		return RunResult{}, fmt.Errorf("the run's commit to preview: %w", err)
	}
	c, err := docker.ReadSelectedContract(tree.Directory)
	if errors.Is(err, docker.ErrNoContract) {
		return RunResult{}, fmt.Errorf("the run's commit %s: %w", tree.Revision, err)
	}
	if err != nil {
		return RunResult{}, &ContractError{Commit: tree.Revision, Cause: err}
	}
	prepared, err := s.preparePreview(ctx, projectID, p.Slug, name, tree.Directory, c, in.Services)
	if err != nil {
		return RunResult{}, err
	}
	if err := s.admit(ctx, docker.ResourceAdmissionContract(prepared.Contract, prepared.Services)); err != nil {
		return RunResult{}, err
	}
	if prepared, err = s.withPreviewKeys(ctx, projectID, name, prepared); err != nil {
		return RunResult{}, err
	}
	public := prepared.Public()
	incarnation := tree.Revision + "+" + public.Digest()
	// The host a visitor's request carries at the run's preview, the
	// address the result names.
	routes, label := s.env.RoutesFor(ctx, projectID), fmt.Sprintf("run-%d", runID)
	visitor := docker.VisitorHost(routes.PreviewURL(label, 0))
	var out RunResult
	if s.PreviewsOnWorker() {
		out, err = s.runPreviewOnWorker(ctx, stepID, p.Slug, name, incarnation, visitor, prepared, tree.Directory)
	} else {
		out, err = s.runPreviewHere(ctx, stepID, p.Slug, name, incarnation, visitor, prepared, tree.Directory)
	}
	out.URL = routes.PreviewURL(label, out.Boot.HostPort)
	if err != nil {
		return out, err
	}
	if !s.recordServedSince(name, discards, servedRecord{Incarnation: incarnation, Source: tree.Revision, Public: public, At: s.now()}) {
		log.Printf("run %d: its preview was discarded while it booted; AppTown keeps no record of it", runID)
	}
	if rerr := source.Retain(tree); rerr != nil {
		log.Printf("run %d: removing snapshots its preview no longer serves: %v", runID, rerr)
	}
	return out, nil
}

func (s *Service) runSnapshots(runID ids.ID) string {
	return filepath.Join(s.dataDir, "workspaces", fmt.Sprintf("asof-run-%d", runID))
}

func (s *Service) runPreviewHere(ctx context.Context, stepID ids.ID, slug, name, incarnation, visitor string, prepared docker.Prepared, tree string) (RunResult, error) {
	life, cancel := context.WithTimeout(context.WithoutCancel(ctx), docker.BootLifetime)
	defer cancel()
	result, err := s.env.PreparedHost(prepared).Boot(life, docker.BootRequest{Docker: s.hosts.Local().WithLifetime(life), Name: name, StackID: docker.RunStepStackID(stepID), Workspace: tree, Slug: slug,
		Deployment: "preview", Services: prepared.Services, Probe: 90 * time.Second, PageCheck: true, VisitorHost: visitor, Incarnation: incarnation, Scratch: s.hosts.BootScratch()}, prepared.Contract)
	if err != nil {
		// A boot that failed leaves nothing of the step's preview, at
		// whatever phase; the run's other preview steps' stacks go with
		// the run.
		if rerr := docker.RemovePreview(context.WithoutCancel(ctx), s.hosts.Local(), name, docker.RunStepStackID(stepID)); rerr != nil {
			log.Printf("%s: removing a preview that did not come up: %v", name, rerr)
		}
		return RunResult{Boot: result}, err
	}
	out := RunResult{Boot: result, Incarnation: incarnation}
	if image := result.Image(); image != "" {
		out.Digest = s.hosts.Local().ImageDigest(ctx, image)
	}
	return out, nil
}

// runPreviewOnWorker boots a run's preview on the worker. The stack
// reported is the one the worker booted: the same contract and services,
// resolved the same way, with the image it ran.
func (s *Service) runPreviewOnWorker(ctx context.Context, stepID ids.ID, slug, name, incarnation, visitor string, prepared docker.Prepared, tree string) (RunResult, error) {
	st, err := s.bootOnWorker(ctx, worker.PreviewSpec{Name: name, StackID: docker.RunStepStackID(stepID), Slug: slug, Incarnation: incarnation,
		ProbeSeconds: 90, PageCheck: true, VisitorHost: visitor}, prepared, tree)
	if err != nil {
		return RunResult{Boot: docker.BootResult{Page: st.Page}}, err
	}
	stack, err := s.env.ResolveDockerStack(ctx, prepared.Contract, prepared.Services, s.env.NeedCredentials(deployment.PreviewNeedScope(name)))
	if err != nil {
		return RunResult{}, err
	}
	stack.Secrets.Receipts, stack.Secrets.Profile = prepared.Receipts, prepared.Profile
	if public := stack.Plan.PublicPart(); public != nil && st.Image != "" {
		public.Image = st.Image
	}
	return RunResult{Boot: docker.BootResult{Stack: stack, Page: st.Page}, Digest: st.Digest, Incarnation: incarnation}, nil
}

// DiscardRunPreview removes a run's preview (on its completion or failure):
// its app, the data volume that dies with it, each of its preview steps'
// throwaway stacks, wherever it booted, and its snapshots. A removal that
// is not confirmed is an error: the caller's job tries again.
func (s *Service) DiscardRunPreview(ctx context.Context, runID ids.ID, stepIDs []ids.ID) error {
	name := docker.PreviewRunName(runID)
	s.discardDecided(name) // a reconciliation removes it if this does not
	var err error
	if s.PreviewsOnWorker() {
		err = s.removeOnWorker(ctx, name)
	} else {
		dk := s.hosts.Local()
		err = errors.Join(dk.RemoveDisposable(ctx, name))
		for _, id := range stepIDs {
			err = errors.Join(err, dk.RemoveDisposableStack(ctx, docker.RunStepStackID(id)))
		}
	}
	if err != nil {
		return fmt.Errorf("removing run %d's preview: %w", runID, err)
	}
	return os.RemoveAll(s.runSnapshots(runID))
}
