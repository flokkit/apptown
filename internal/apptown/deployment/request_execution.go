package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

type ReleaseSecrets interface {
	ResolveRelease(context.Context, Application, string, string, *appcontract.Contract) (deploy.ConsumerSecrets, []string, error)
}
type ReleaseDependencies struct {
	Authority access.Authorizer
	Still     operations.Authorities
	Sources   ReleaseSources
	Secrets   ReleaseSecrets
	Queue     ReleaseQueue
}

// ExecuteRelease executes previously authorized queued work. It commits a claim
// before preparing source and secrets, then rechecks the accepted placement and
// source revision before attempt execution. A closed or already-claimed request
// returns nil without repeating work; nil alone does not establish deployment
// success. Preparation failures are recorded through FailUnstartedRelease.
// Managed needs and artifact rendering may perform provider I/O before attempt
// admission. No transaction spans provider or repository I/O.
func (s *Service) ExecuteRelease(ctx context.Context, id ids.ID) error {
	now := s.now()
	if s.release.Sources == nil || s.release.Secrets == nil {
		return errors.New("release execution is not configured")
	}
	var rel *Release
	var placement deploymentsql.ReleaseRequest
	claimed := false
	err := s.db.InTx(ctx, func(tx *database.Tx) error {
		q := deploymentsql.New(tx.SQL())
		row, err := q.RequestedReleaseForUpdate(ctx, id)
		if err != nil {
			return err
		}
		rel, err = releaseRecord(row.Release, nil)
		if err != nil {
			return err
		}
		placement = row.ReleaseRequest
		if rel.State != Releasing || placement.ClaimedAt.Valid {
			return nil
		}
		n, err := q.ClaimReleaseRequest(ctx, deploymentsql.ClaimReleaseRequestParams{ReleaseID: id, ClaimedAt: sql.NullTime{Time: now, Valid: true}})
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("release execution claim was not recorded")
		}
		claimed = true
		return nil
	})
	if err != nil || !claimed {
		return err
	}
	fail := func(cause error) error {
		_, err := s.Outcomes.FailUnstartedRelease(ctx, id, cause)
		return errors.Join(cause, err)
	}
	var rawAuthority json.RawMessage
	if placement.AuthorityJson != nil {
		rawAuthority = *placement.AuthorityJson
	}
	captured, err := releasePrincipal(rawAuthority)
	if err != nil {
		return fail(err)
	}
	allowed := func(ctx context.Context) error {
		return s.requireReleaseAuthority(ctx, captured, rel.DeploymentID, placement.RequirePromotion)
	}
	if err := allowed(ctx); err != nil {
		return fail(err)
	}
	var attrs Attributes
	if err := json.Unmarshal(placement.AttributesJson, &attrs); err != nil {
		return fail(err)
	}
	dep, err := s.Deployments.ByID(ctx, rel.DeploymentID)
	if err != nil {
		return fail(err)
	}
	// Before any preparation: App Platform's needs and rendering reach the
	// provider before an attempt is admitted.
	if err := dep.HeldAfterRestore(); err != nil {
		return fail(err)
	}
	running, err := s.handBundle(ctx, dep)
	if err != nil {
		return fail(err)
	}
	if err := dep.matchesRelease(placement.TargetID, placement.BaseGeneration, attrs, rel.BundleID, running); err != nil {
		return fail(err)
	}
	t, err := s.Targets.ByID(ctx, placement.TargetID)
	if err != nil {
		return fail(err)
	}
	if err := matchesProviderBinding(t, placement.ProviderBindingJson); err != nil {
		return fail(err)
	}
	var address *AddressSnapshot
	if placement.IngressJson != nil {
		address, err = addressSnapshotFrom(*placement.IngressJson)
		if err != nil {
			return fail(err)
		}
	}
	if err := s.checkAddressSnapshot(ctx, dep, t, address); err != nil {
		return fail(err)
	}
	bundle, err := s.Bundles.ByID(ctx, rel.BundleID)
	if err != nil {
		return fail(err)
	}
	if bundle.ProjectID != rel.ProjectID {
		return fail(ErrBundleMissing)
	}
	if err := s.refusesPlacement(ctx, rel.ProjectID, t); err != nil {
		return fail(err)
	}
	if err := s.providers.RefusesBundle(ctx, bundle, t); err != nil {
		return fail(err)
	}
	if d, err := s.providers.Driver(ctx, t, dep); err != nil {
		return fail(err)
	} else if r, ok := d.(releaseRefuser); ok {
		if err := r.refusesRelease(bundle); err != nil {
			return fail(err)
		}
	}
	project, err := s.release.Sources.Project(ctx, rel.ProjectID)
	if err != nil {
		return fail(err)
	}
	c, revision, err := s.release.Sources.Contract(ctx, project, rel.CommitSHA)
	if err != nil {
		return fail(err)
	}
	if c == nil || revision != rel.CommitSHA {
		return fail(errors.New("accepted source revision changed"))
	}
	if why, err := s.env.RefusesContract(ctx, t, c); err != nil || why != "" {
		return fail(errors.Join(err, refusal(why)))
	}
	if why, err := s.env.RefusesBrowserValues(ctx, t.Kind, rel.ProjectID, c); err != nil || why != "" {
		return fail(errors.Join(err, refusal(why)))
	}
	req := preparedRelease{Allowed: allowed, Bundle: bundle, Source: deploy.Source{Revision: revision, Slug: project.Slug, Contract: c, Images: bundle.Parts, Init: bundle.Init, Published: bundle.ByDigest(), Platform: bundle.BuiltFrom.Platform}, Actor: rel.ApprovedBy, BaseGeneration: &placement.BaseGeneration}
	req.Address = address
	if address != nil {
		ingress := address.Ingress.Clone()
		req.Source.Ingress = &ingress
	}
	var staged string
	var cleanup func()
	docked := false // a standing deployment on a Docker host: booted at its recorded port
	if t.Kind == KindDockerHost {
		docked = dep.Standing()
		// A digest bundle's images are pulled by digest; without local
		// Docker a bundle of image IDs is reused on the target that holds
		// them; otherwise the host builds the source, as before.
		req.Source.Images, req.Source.Init, err = s.bundleImages(ctx, bundle, t)
		if err != nil {
			return fail(err)
		}
		var art deploy.Artifact
		art, cleanup, err = docker.PrepareSource(ctx, project.Repository, revision, project.Slug, nil)
		if err == nil {
			staged, req.Source.Contract, req.Source.Revision = art.Source.Workspace, art.Source.Contract, art.Source.Revision
			req.Artifact = &art
		}
	} else {
		var tree deploy.SourceTree
		tree, cleanup, err = (deploy.SelectedSource{Repository: project.Repository, Revision: revision}).Stage(ctx, "")
		staged = tree.Directory
	}
	if err != nil {
		return fail(fmt.Errorf("staging the committed tree: %w", err))
	}
	defer cleanup()
	req.Source.Workspace = staged
	// The contract this release runs: the deployment's profile and its
	// operators' overrides over the committed declaration, recorded with
	// the generation so a rebuild runs the same. A target that rounds
	// resources to its own sizes answers what each part runs on before any
	// effect: before a need is registered or fulfilled, or an artifact
	// rendered.
	d, err := s.providers.Driver(ctx, t, dep)
	if err != nil {
		return fail(err)
	}
	req.Committed = req.Source.Contract
	effective, applied, err := s.effectiveContract(ctx, dep, staged, req.Committed)
	if err != nil {
		return fail(err)
	}
	// The same early check a landing makes before its build, before any
	// secret is generated, need registered or target changed (Preflight).
	check := PreflightCheck{DeploymentID: dep.ID, Deployment: dep.Name, TargetID: t.ID, Commit: revision, CheckedAt: s.now()}
	if err := s.preflight(ctx, check, rel.ProjectID, t, project.Slug, staged, effective); err != nil {
		return fail(err)
	}
	if req.Applied, err = applied.Fitted(ctx, d, effective); err != nil {
		return fail(err)
	}
	req.Source.Contract = effective
	if err := allowed(ctx); err != nil {
		return fail(err)
	}
	req.Secrets, req.SecretNames, err = s.release.Secrets.ResolveRelease(ctx, project.Application, dep.Name, staged, req.Source.Contract)
	if err != nil {
		return fail(err)
	}
	// Needs fulfilled by registering (an AI models key): a Docker host
	// registers them as its boot reads the app's values; any other target
	// is given them with the app's secrets.
	if t.Kind != KindDockerHost {
		if err := s.env.refuseRegisteredSecretCollisions(ctx, req.Source.Contract, req.Secrets); err != nil {
			return fail(err)
		}
		registered, err := s.env.registerNeedsChecked(ctx, req.Source.Contract, deploymentKeys(s.db, s.clock, dep, project.Slug), allowed)
		if err != nil {
			return fail(err)
		}
		if len(registered) > 0 {
			req.Secrets = registeredSecrets{inner: req.Secrets, env: registered}
			req.SecretNames = append(req.SecretNames, slices.Sorted(maps.Keys(registered))...)
		}
		// A Docker host and a driver that says so (integrationsGiver) give
		// an app with a runtime its attached integrations' values; a static
		// site is given them as browser values where it can be at all
		// (RefusesBrowserValues).
		if err := allowed(ctx); err != nil {
			return fail(err)
		}
		if _, gives := d.(integrationsGiver); !gives && !StaticOnly(req.Source.Contract) {
			if err := s.noteUndeliveredIntegrations(ctx, rel.ID, rel.ProjectID, t); err != nil {
				return fail(err)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	// A standing deployment on a Docker host boots at its one recorded
	// port, picked now if this is its first boot, as a landing's is: Docker
	// keeps a port it was given at every start, and picks a port it chose
	// again (StablePort).
	if docked {
		names, err := dep.HostNames()
		if err != nil {
			return fail(err)
		}
		if dep.HostPort, err = s.Deployments.StablePort(ctx, rel.ProjectID, dep.Name, names.App()); err != nil {
			return fail(fmt.Errorf("reserving the deployment's host port: %w", err))
		}
	}
	_, err = s.execute(ctx, NumberedRelease{ID: rel.ID, Number: rel.Number}, dep, t, req)
	if docked {
		s.Deployments.ForgetTakenPort(ctx, rel.ProjectID, dep.Name, err)
	}
	return err
}

// noteUndeliveredIntegrations says in the release's record which values
// the project's attached integrations (error reporting, sign-in, telemetry)
// give an app on a Docker host or Portainer and this target does not
// deliver yet:
// integrations are not needs, the app runs without them, and that is said
// rather than left silent. Only names are written. Values that cannot be
// read fail the release: a note naming nothing would say nothing is lost.
func (s *Service) noteUndeliveredIntegrations(ctx context.Context, release, projectID ids.ID, t *Target) error {
	if s.env.integrations == nil {
		return nil
	}
	env, err := s.env.integrations.Environment(ctx, projectID)
	if err != nil {
		return fmt.Errorf("reading what the attached integrations give the app: %w", err)
	}
	var names []string
	for _, kv := range env {
		if name, _, ok := strings.Cut(kv, "="); ok && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	slices.Sort(names)
	s.log(release, "the project's attached integrations give an app on a Docker host %s; %s does not deliver them yet, so this release runs without them", strings.Join(names, ", "), t.Name)
	return nil
}
