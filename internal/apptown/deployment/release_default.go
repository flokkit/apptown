package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// LegacyReleaseSetup is imported intent, not a verified provider association.
type LegacyReleaseSetup struct {
	Version         int             `json:"version"`
	IntegrationID   ids.ID          `json:"integrationId"`
	IntegrationName string          `json:"integrationName"`
	TargetID        ids.ID          `json:"targetId"`
	DeploymentName  string          `json:"deploymentName"`
	Evidence        json.RawMessage `json:"evidence"`
}

// ReleaseDefault is an explicit project choice, separate from landing and labels.
type ReleaseDefault struct {
	LegacySetup      *LegacyReleaseSetup `json:"legacySetup,omitempty"`
	EligibilityIssue string              `json:"eligibilityIssue,omitempty"`
	ProjectID        ids.ID              `json:"projectId"`
	DeploymentID     ids.ID              `json:"deploymentId,omitzero"`
	Revision         int64               `json:"revision"`
}

// ReadReleaseDefault reads only the recorded choice. An absent choice stays absent.
func (s *Service) ReadReleaseDefault(ctx context.Context, projectID ids.ID) (ReleaseDefault, error) {
	out := ReleaseDefault{ProjectID: projectID}
	if s.configuration.Authority == nil || s.configuration.Projects == nil {
		return out, access.ErrAuthorityUnavailable
	}
	if _, err := s.configuration.Authority.Require(ctx, access.Permission{Capability: access.CapDeploymentRead, Resource: access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}, Context: access.Context{Route: "deployment.default-read"}}); err != nil {
		return out, err
	}
	row, err := deploymentsql.New(s.db.SQL()).ProjectReleaseSettings(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.DeploymentID, out.Revision = row.DefaultDeploymentID, row.Revision
	if row.LegacySetupJson != nil {
		var setup LegacyReleaseSetup
		if err := json.Unmarshal(*row.LegacySetupJson, &setup); err != nil {
			return out, err
		}
		setup.Evidence = append(json.RawMessage(nil), (*row.LegacySetupJson)...)
		out.LegacySetup = &setup
	}
	if !out.DeploymentID.IsZero() {
		q := deploymentsql.New(s.db.SQL())
		dep, err := deploymentRecord(q.DeploymentByID(ctx, out.DeploymentID))
		if err != nil {
			return out, err
		}
		target, err := targetRecord(q.TargetByID(ctx, dep.TargetID))
		if err != nil {
			return out, err
		}
		if err := s.defaultEligible(ctx, q, projectID, dep, target); err != nil {
			out.EligibilityIssue = err.Error()
		}
	}
	return out, nil
}

// SetReleaseDefault chooses a deployment UUID or clears it; no provider is contacted.
func (s *Service) SetReleaseDefault(ctx context.Context, projectID, deploymentID ids.ID, expectedRevision int64) (ReleaseDefault, error) {
	if s.configuration.Authority == nil || s.configuration.Projects == nil {
		return ReleaseDefault{}, access.ErrAuthorityUnavailable
	}
	actor, err := s.configuration.Authority.Require(ctx, access.Permission{Capability: access.CapDeploymentBind, Resource: access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}, Context: access.Context{Route: "deployment.default", Consequential: true}})
	if err != nil {
		return ReleaseDefault{}, err
	}
	if actor.Principal.Name == "" {
		return ReleaseDefault{}, access.ErrAuthorityUnavailable
	}
	if expectedRevision < 0 {
		return ReleaseDefault{}, fault.New(fault.Invalid, "default settings revision must not be negative")
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (ReleaseDefault, error) {
		if _, err := releasesql.New(tx.SQL()).LockReleaseProject(ctx, projectID); err != nil {
			return ReleaseDefault{}, err
		}
		if err := project.RefuseUnlessActiveIn(ctx, tx, projectID); err != nil {
			return ReleaseDefault{}, err
		}
		q := deploymentsql.New(tx.SQL())
		if !deploymentID.IsZero() {
			dep, err := deploymentRecord(q.DeploymentByID(ctx, deploymentID))
			if err != nil {
				return ReleaseDefault{}, err
			}
			if dep.ProjectID != projectID {
				return ReleaseDefault{}, fault.New(fault.Invalid, "choose a deployment in this project")
			}
			target, err := targetRecord(q.TargetByID(ctx, dep.TargetID))
			if err != nil {
				return ReleaseDefault{}, err
			}
			target, err = targetRecord(q.TargetByNameForUpdate(ctx, target.Name))
			if err != nil {
				return ReleaseDefault{}, err
			}
			dep, err = deploymentRecord(q.DeploymentForUpdate(ctx, deploymentID))
			if err != nil {
				return ReleaseDefault{}, err
			}
			if err := s.defaultEligible(ctx, q, projectID, dep, target); err != nil {
				return ReleaseDefault{}, err
			}
		}
		var row deploymentsql.ProjectReleaseSetting
		if expectedRevision == 0 {
			row, err = q.ChooseDefaultDeployment(ctx, deploymentsql.ChooseDefaultDeploymentParams{ProjectID: projectID, DefaultDeploymentID: deploymentID, ExpectedRevision: expectedRevision})
		} else {
			row, err = q.UpdateDefaultDeployment(ctx, deploymentsql.UpdateDefaultDeploymentParams{ProjectID: projectID, DefaultDeploymentID: deploymentID, ExpectedRevision: expectedRevision})
		}
		if errors.Is(err, sql.ErrNoRows) {
			return ReleaseDefault{}, fault.New(fault.Conflict, "release default changed; reload before choosing")
		}
		if err != nil {
			return ReleaseDefault{}, err
		}
		if err := audit.At(s.now()).RecordIn(ctx, tx, actor.Principal.Name, projectID, "deployment.default-chosen", projectID.String(), map[string]any{"deploymentId": deploymentID, "revision": row.Revision}); err != nil {
			return ReleaseDefault{}, err
		}
		return ReleaseDefault{ProjectID: projectID, DeploymentID: row.DefaultDeploymentID, Revision: row.Revision}, nil
	})
}

func (s *Service) defaultEligible(ctx context.Context, q *deploymentsql.Queries, projectID ids.ID, dep *Deployment, target *Target) error {
	if dep.Name == "main" {
		return fault.New(fault.Invalid, "main follows landing; choose a separately released deployment")
	}
	if err := dep.DeployableByHand(); err != nil {
		return err
	}
	if !target.Status.Offerable() {
		return fault.New(fault.Conflict, "default release target is unavailable; choose an eligible deployment")
	}
	if err := s.refusesPlacement(ctx, projectID, target); err != nil {
		return err
	}
	proj, err := s.configuration.Projects.Project(ctx, projectID)
	if err != nil {
		return err
	}
	offer, err := s.Targets.offerFor(ctx, q, target, proj.OwnerGroup)
	if err != nil {
		return err
	}
	if why := offer.Permits(target, proj.Slug, dep.releaseAttributes(), 0); why != "" {
		return &OfferDeniedError{why}
	}
	return nil
}

// preserveDefaultConfiguration runs in the existing binding transaction, so a
// default cannot silently become a landing role or a refused provider placement.
func preserveDefaultConfiguration(ctx context.Context, tx *database.Tx, dep *Deployment) error {
	q := deploymentsql.New(tx.SQL())
	settings, err := q.ProjectReleaseSettings(ctx, dep.ProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if settings.DefaultDeploymentID != dep.ID {
		return nil
	}
	if dep.Name == "main" || dep.DeployableByHand() != nil {
		return fault.New(fault.Conflict, "clear or replace the release default before making this deployment ineligible")
	}
	target, err := targetRecord(q.TargetByID(ctx, dep.TargetID))
	if err != nil {
		return err
	}
	if !target.Status.Offerable() {
		return fault.New(fault.Conflict, "clear or replace the release default before changing to this target")
	}
	return nil
}

var (
	ErrNoReleaseDefault      = fault.New(fault.Unprocessable, "choose a release environment in project settings before releasing")
	ErrReleaseDefaultChanged = fault.New(fault.Conflict, "release environment changed; reload before releasing")
)

// DefaultReleaseSelection keeps address and data acknowledgements identical to
// named admission, while the environment comes only from the recorded UUID.
type DefaultReleaseSelection struct {
	ProjectID           ids.ID
	ExpectedRevision    *int64
	AddressRevision     *int64
	CustomHostname      *string
	AcceptUncopiedData  bool
	AcceptUncheckedCopy bool
}

// RequestDefaultRelease selects once, then uses the named release's admission
// transaction and durable execution. A concurrent default edit refuses this
// request; a later edit cannot retarget the admitted release.
func (s *Service) RequestDefaultRelease(ctx context.Context, in DefaultReleaseSelection) (AcceptedRelease, error) {
	if s.configuration.Projects == nil || s.release.Authority == nil {
		return AcceptedRelease{}, errors.New("release default configuration is not configured")
	}
	if _, err := s.requireDeploy(ctx, in.ProjectID, "", false, "deployment.request-default"); err != nil {
		return AcceptedRelease{}, err
	}
	if in.ExpectedRevision != nil && *in.ExpectedRevision < 0 {
		return AcceptedRelease{}, fault.New(fault.Invalid, "default settings revision must not be negative")
	}
	row, err := deploymentsql.New(s.db.SQL()).ProjectReleaseSettings(ctx, in.ProjectID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AcceptedRelease{}, err
	}
	if errors.Is(err, sql.ErrNoRows) || row.DefaultDeploymentID.IsZero() {
		return AcceptedRelease{}, ErrNoReleaseDefault
	}
	if in.ExpectedRevision != nil && row.Revision != *in.ExpectedRevision {
		return AcceptedRelease{}, ErrReleaseDefaultChanged
	}
	selected := ReleaseDefault{ProjectID: in.ProjectID, DeploymentID: row.DefaultDeploymentID, Revision: row.Revision}
	return s.requestRelease(ctx, DeploySelection{ProjectID: in.ProjectID, AddressRevision: in.AddressRevision, CustomHostname: in.CustomHostname, AcceptUncopiedData: in.AcceptUncopiedData, AcceptUncheckedCopy: in.AcceptUncheckedCopy}, &selected)
}

// ReleaseDestination describes the explicit default without configuring it or
// inferring an address for a provider that does not own deployment addresses.
type ReleaseDestination struct {
	Default ReleaseDefault
	Name    string
	Label   string
	Address *AddressReading
}

func (s *Service) ReadReleaseDestination(ctx context.Context, projectID ids.ID) (ReleaseDestination, error) {
	selected, err := s.ReadReleaseDefault(ctx, projectID)
	out := ReleaseDestination{Default: selected}
	if err != nil || selected.DeploymentID.IsZero() {
		return out, err
	}
	dep, err := s.Deployments.ByID(ctx, selected.DeploymentID)
	if err != nil {
		return out, err
	}
	out.Name, out.Label = dep.Name, dep.DisplayLabel
	target, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return out, err
	}
	if target.Kind == KindPortainer {
		address, err := s.ReadAddress(ctx, projectID, dep.ID)
		if err != nil {
			return out, err
		}
		out.Address = &address
	}
	return out, nil
}
