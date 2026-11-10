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
	Grants interface {
		ActiveGrants(context.Context, access.Principal) ([]access.Grant, error)
	}
	Authority access.Authorizer
	Still     operations.Authorities
	Sources   ReleaseSources
	Secrets   ReleaseSecrets
	Queue     releaseQueue
	Data      releaseData
	Rollbacks releaseRollbacks
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
		rel, err = s.Releases.record(row.Release, nil)
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
	_, err = s.executeRequestedRelease(ctx, rel, placement, nil)
	return err
}

// executeRequestedRelease shares preparation and current-authority checks with
// the single restart retry. A retry keeps its generation and backup, and never
// restores data or takes another pre-deploy copy.
func (s *Service) executeRequestedRelease(ctx context.Context, rel *Release, placement deploymentsql.ReleaseRequest, retry *Attempt) (ExecutionResult, error) {
	id := rel.ID
	var result ExecutionResult
	var original Inputs
	if retry != nil {
		var err error
		original, err = s.Outcomes.InputsFor(ctx, *retry)
		if err != nil {
			return result, err
		}
		placement.BaseGeneration = retry.Generation
	}
	ctx = deploy.WithStageReporter(ctx, func(stage deploy.DeployStage) error {
		return s.recordReleaseStage(ctx, id, stage)
	})
	restored := false
	fail := func(cause error) error {
		if restored {
			cause = fmt.Errorf("%w; the restored data stays in place and the app remains stopped", cause)
		}
		if retry != nil {
			return cause
		}
		_, err := s.Outcomes.FailUnstartedRelease(ctx, id, cause)
		return errors.Join(cause, err)
	}
	if err := deploy.ReportStage(ctx, deploy.Preparing); err != nil {
		return result, fail(err)
	}
	var rawAuthority json.RawMessage
	if placement.AuthorityJson != nil {
		rawAuthority = *placement.AuthorityJson
	}
	if placement.FeedRevision.Valid && len(rawAuthority) == 0 {
		return result, fail(errFeedUnconfirmed)
	}
	captured, err := releasePrincipal(rawAuthority)
	if err != nil {
		return result, fail(err)
	}
	allowed := func(ctx context.Context) error { return releaseAllowed(ctx, s, rel, placement, captured) }
	if err := allowed(ctx); err != nil {
		return result, fail(err)
	}
	var attrs Attributes
	if err := json.Unmarshal(placement.AttributesJson, &attrs); err != nil {
		return result, fail(err)
	}
	dep, err := s.Deployments.ByID(ctx, rel.DeploymentID)
	if err != nil {
		return result, fail(err)
	}
	if op, err := s.runningOperation(ctx, dep); err != nil {
		return result, fail(err)
	} else if op != nil && slices.Contains(op.Holds, SubjectOf(dep)) {
		return result, fail(&operations.HeldError{Holder: op})
	}
	// Before any preparation: App Platform's needs and rendering reach the
	// provider before an attempt is admitted.
	if err := dep.HeldAfterRestore(); err != nil {
		return result, fail(err)
	}
	running, err := s.handBundle(ctx, dep)
	if err != nil {
		return result, fail(err)
	}
	checkDep := *dep
	if placement.FeedRevision.Valid {
		checkDep.Feed = "manual"
		attrs.Feed = "manual"
	}
	if err := checkDep.matchesRelease(placement.TargetID, placement.BaseGeneration, attrs, rel.BundleID, running); err != nil {
		return result, fail(err)
	}

	t, err := s.Targets.ByID(ctx, placement.TargetID)
	if err != nil {
		return result, fail(err)
	}
	if err := matchesProviderBinding(t, placement.ProviderBindingJson); err != nil {
		return result, fail(err)
	}
	var address *AddressSnapshot
	if placement.IngressJson != nil {
		address, err = addressSnapshotFrom(*placement.IngressJson)
		if err != nil {
			return result, fail(err)
		}
	}
	if err := s.checkAddressSnapshot(ctx, dep, t, address); err != nil {
		return result, fail(err)
	}
	bundle, err := s.Bundles.ByID(ctx, rel.BundleID)
	if err != nil {
		return result, fail(err)
	}
	if bundle.ProjectID != rel.ProjectID {
		return result, fail(errBundleMissing)
	}
	if err := s.refusesPlacement(ctx, rel.ProjectID, t); err != nil {
		return result, fail(err)
	}
	if why := t.refusesAudience(dep); why != "" {
		return result, fail(refused("%s", why))
	}
	if err := s.providers.RefusesBundle(ctx, bundle, t); err != nil {
		return result, fail(err)
	}
	if d, err := s.providers.Driver(ctx, t, dep); err != nil {
		return result, fail(err)
	} else if r, ok := d.(releaseRefuser); ok {
		if err := r.refusesRelease(bundle); err != nil {
			return result, fail(err)
		}
	}
	project, err := s.release.Sources.Project(ctx, rel.ProjectID)
	if err != nil {
		return result, fail(err)
	}
	c, revision, err := s.release.Sources.Contract(ctx, project, rel.CommitSHA)
	if err != nil {
		return result, fail(err)
	}
	if c == nil || revision != rel.CommitSHA {
		return result, fail(errors.New("accepted source revision changed"))
	}
	if why, err := s.env.RefusesContract(ctx, t, c); err != nil || why != "" {
		return result, fail(errors.Join(err, refusal(why)))
	}
	if why, err := s.env.RefusesBrowserValues(ctx, t.Kind, rel.ProjectID, c); err != nil || why != "" {
		return result, fail(errors.Join(err, refusal(why)))
	}
	req := preparedRelease{releaseRun: rel.RunID, allowed: allowed, bundle: bundle, source: deploy.Source{Revision: revision, Slug: project.Slug, Contract: c, Images: bundle.Parts, Init: bundle.Init, Published: bundle.ByDigest(), Platform: bundle.BuiltFrom.Platform}, actor: rel.ApprovedBy, baseGeneration: &placement.BaseGeneration}
	req.retry, req.original = retry, original
	req.address = address
	if address != nil {
		ingress := address.Ingress.Clone()
		req.source.Ingress = &ingress
	}
	var staged string
	var cleanup func()
	docked := false // a standing deployment on a Docker host: booted at its recorded port
	if t.Kind == KindDockerHost {
		docked = dep.Standing()
		// A digest bundle's images are pulled by digest; without local
		// Docker a bundle of image IDs is reused on the target that holds
		// them; otherwise the host builds the source, as before.
		req.source.Images, req.source.Init, err = s.bundleImages(ctx, bundle, t)
		if err != nil {
			return result, fail(err)
		}
		var art deploy.Artifact
		art, cleanup, err = docker.PrepareSource(ctx, project.Repository, revision, project.Slug, nil)
		if err == nil {
			staged, req.source.Contract, req.source.Revision = art.Source.Workspace, art.Source.Contract, art.Source.Revision
			req.artifact = &art
		}
	} else {
		var tree deploy.SourceTree
		tree, cleanup, err = (deploy.SelectedSource{Repository: project.Repository, Revision: revision}).Stage(ctx, "")
		staged = tree.Directory
	}
	if err != nil {
		return result, fail(fmt.Errorf("staging the committed tree: %w", err))
	}
	defer cleanup()
	req.source.Workspace = staged
	// The contract this release runs: the deployment's profile and its
	// operators' overrides over the committed declaration, recorded with
	// the generation so a rebuild runs the same. A target that rounds
	// resources to its own sizes answers what each part runs on before any
	// effect: before a need is registered or fulfilled, or an artifact
	// rendered.
	d, err := s.providers.Driver(ctx, t, dep)
	if err != nil {
		return result, fail(err)
	}
	req.committed = req.source.Contract
	var effective *appcontract.Contract
	var applied AppliedProfile
	if retry != nil {
		applied = original.Applied
		effective = applied.On(req.committed)
	} else {
		effective, applied, err = s.effectiveContract(ctx, dep, staged, req.committed)
	}
	if err != nil {
		return result, fail(err)
	}
	// The same early check a landing makes before its build, before any
	// secret is generated, need registered or target changed (Preflight).
	check := preflightCheck{DeploymentID: dep.ID, Deployment: dep.Name, TargetID: t.ID, Commit: revision, CheckedAt: s.now()}
	if err := s.preflight(ctx, check, rel.ProjectID, t, project.Slug, staged, effective); err != nil {
		return result, fail(err)
	}
	req.applied = applied
	if retry == nil {
		req.applied, err = applied.Fitted(ctx, d, effective)
	}
	if err != nil {
		return result, fail(err)
	}
	req.source.Contract = effective
	if err := allowed(ctx); err != nil {
		return result, fail(err)
	}
	var witness ReleaseWitness
	if retry == nil {
		if err := deploy.ReportStage(ctx, deploy.BackingUp); err != nil {
			return result, fail(err)
		}
		if !rel.RestoreBackupID.IsZero() {
			if err := allowed(ctx); err != nil {
				return result, fail(err)
			}
			if s.release.Rollbacks == nil {
				return result, fail(errors.New("rollback restore is not configured"))
			}
			if err := s.release.Rollbacks.RestoreForRelease(ctx, rel.ProjectID, dep.Name, rel.RestoreBackupID, rel.ID, captured); err != nil {
				return result, fail(err)
			}
			restored = true
			s.log(rel.ID, "Data restored from backup %s; the app is stopped until the selected release starts", rel.RestoreBackupID)
		}
		acceptUncopied := s.acceptsUncopied(ctx, placement.UncopiedAcceptanceJson, dep, t, captured, placement.FeedRevision.Valid)
		if s.release.Data != nil {
			witness, err = s.release.Data.BeforeRelease(ctx, rel.ProjectID, dep.Name, dep.DataClass == "synthetic")
			if err != nil || witness == nil {
				cause := err
				if cause == nil {
					cause = errors.New("copy before release could not be assessed")
				}
				raw, e := json.Marshal(map[string]any{"copy": map[string]string{"outcome": "unavailable", "reason": cause.Error()}})
				if e != nil {
					return result, fail(e)
				}
				if _, e := deploymentsql.New(s.db.SQL()).RecordReleaseBackup(context.WithoutCancel(ctx), deploymentsql.RecordReleaseBackupParams{ReleaseID: rel.ID, BackupEvidenceJson: rawJSON(raw)}); e != nil {
					return result, fail(errors.Join(cause, e))
				}
				if !acceptUncopied {
					return result, fail(fmt.Errorf("copy before release failed: %w", cause))
				}
				s.log(rel.ID, "Released with recorded acceptance of uncopied data: %s", cause)
			}
			if witness != nil {
				q := deploymentsql.New(s.db.SQL())
				evidence, err := json.Marshal(map[string]json.RawMessage{"copy": witness.CopyEvidence()})
				if err != nil {
					return result, fail(err)
				}
				if _, err := q.RecordReleaseBackup(ctx, deploymentsql.RecordReleaseBackupParams{ReleaseID: rel.ID, BackupEvidenceJson: rawJSON(evidence)}); err != nil {
					return result, fail(err)
				}
				if _, err := q.AttachReleaseBackup(ctx, deploymentsql.AttachReleaseBackupParams{ID: rel.ID, BackupID: witness.BackupID()}); err != nil {
					return result, fail(err)
				}
				req.backup = witness.BackupID()
				s.log(rel.ID, "%s: %s", deploy.BackingUp, witness.CopyWords())
				if !witness.Complete() && !acceptUncopied {
					return result, fail(fmt.Errorf("release refused: %s; make the data copyable before releasing", witness.CopyWords()))
				}
				if !witness.Complete() {
					s.log(rel.ID, "Released with recorded acceptance of uncopied data: %s", witness.CopyWords())
				}
			}
		}
	} else {
		req.backup = original.Backup
	}
	req.secrets, req.secretNames, err = s.release.Secrets.ResolveRelease(ctx, project.Application, dep.Name, staged, req.source.Contract)
	if err != nil {
		return result, fail(err)
	}
	// Needs fulfilled by registering (an AI models key): a Docker host
	// registers them as its boot reads the app's values; any other target
	// is given them with the app's secrets.
	if t.Kind != KindDockerHost {
		if err := s.env.refuseRegisteredSecretCollisions(ctx, req.source.Contract, req.secrets); err != nil {
			return result, fail(err)
		}
		registered, err := s.env.registerNeedsChecked(ctx, req.source.Contract, deploymentKeys(s.db, s.clock, dep, project.Slug), allowed)
		if err != nil {
			return result, fail(err)
		}
		if len(registered) > 0 {
			req.secrets = registeredSecrets{inner: req.secrets, env: registered}
			req.secretNames = append(req.secretNames, slices.Sorted(maps.Keys(registered))...)
		}
		// A Docker host and a driver that says so (integrationsGiver) give
		// an app with a runtime its attached integrations' values; a static
		// site is given them as browser values where it can be at all
		// (RefusesBrowserValues).
		if err := allowed(ctx); err != nil {
			return result, fail(err)
		}
		if _, gives := d.(integrationsGiver); !gives && !staticOnly(req.source.Contract) {
			if err := s.noteUndeliveredIntegrations(ctx, rel.ID, rel.ProjectID, t); err != nil {
				return result, fail(err)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return result, fail(err)
	}
	// A standing deployment on a Docker host boots at its one recorded
	// port, picked now if this is its first boot, as a landing's is: Docker
	// keeps a port it was given at every start, and picks a port it chose
	// again (stablePort).
	if docked {
		names, err := dep.HostNames()
		if err != nil {
			return result, fail(err)
		}
		if dep.HostPort, err = s.Deployments.stablePort(ctx, rel.ProjectID, dep.Name, names.App()); err != nil {
			return result, fail(fmt.Errorf("reserving the deployment's host port: %w", err))
		}
	}
	result, err = s.execute(ctx, NumberedRelease{ID: rel.ID, Number: rel.Number}, dep, t, req)
	if err != nil && restored {
		observed := s.ObserveContainerNow(ctx, dep)
		if observed.State == ContainerStopped {
			s.log(rel.ID, "The restored data stays in place and the app remains stopped. Release failed: %v", err)
		} else {
			s.log(rel.ID, "The rollback restored the data. Release failed: %v; application state after the attempt: %s", err, observed.State)
		}
	}
	if docked {
		s.Deployments.forgetTakenPort(ctx, rel.ProjectID, dep.Name, err)
	}
	if err == nil && result.Outcome == Applied {
		s.log(rel.ID, "%s: release %d is running", deploy.Live, rel.Number)
		if s.release.Data != nil {
			s.release.Data.AfterRelease(rel.ProjectID, dep.Name)
		}
		if witness != nil {
			problems, notes := witness.After(ctx, c.Needs)
			evidence := struct {
				Copy     json.RawMessage `json:"copy"`
				Problems []string        `json:"problems,omitempty"`
				Notes    []string        `json:"notes,omitempty"`
			}{witness.CopyEvidence(), problems, notes}
			raw, e := json.Marshal(evidence)
			if e != nil {
				return result, e
			}
			if _, e := deploymentsql.New(s.db.SQL()).RecordReleaseBackup(ctx, deploymentsql.RecordReleaseBackupParams{ReleaseID: rel.ID, BackupEvidenceJson: rawJSON(raw)}); e != nil {
				return result, e
			}
			for _, note := range notes {
				s.log(rel.ID, "Not compared: %s", note)
			}
			if len(problems) > 0 {
				cause := fmt.Errorf("release is running, but its data continuity check failed: %s; this check restored nothing and changed no data", strings.Join(problems, "; "))
				s.log(rel.ID, "%s", cause)
				// Released remains the running-code fact. Continuity is recorded separately.
				return result, cause
			}
		}
	}

	return result, err
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

func rawJSON(raw []byte) *json.RawMessage { value := json.RawMessage(raw); return &value }
