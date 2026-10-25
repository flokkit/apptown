package deployment

import (
	"context"
	"database/sql"
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

// ReleaseSetup selects an exact placement; imported intent only prefills these values.
type ReleaseSetup struct {
	TargetID         ids.ID `json:"targetId"`
	DeploymentName   string `json:"deploymentName"`
	ExpectedRevision int64  `json:"expectedRevision"`
	TargetRevision   *int64 `json:"targetRevision"`
	RequirePromotion bool   `json:"requirePromotion"`
}

// SetupReleaseDefault records placement, policy and choice atomically. It does not release software.
func (s *Service) SetupReleaseDefault(ctx context.Context, projectID ids.ID, in ReleaseSetup) (ReleaseDefault, error) {
	if s.configuration.Authority == nil || s.configuration.Projects == nil {
		return ReleaseDefault{}, access.ErrAuthorityUnavailable
	}
	actor, err := s.configuration.Authority.Require(ctx, access.Permission{Capability: access.CapDeploymentBind, Resource: access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}, Context: access.Context{Route: "deployment.default-setup", Consequential: true}})
	if err != nil {
		return ReleaseDefault{}, err
	}
	if actor.Principal.Name == "" {
		return ReleaseDefault{}, access.ErrAuthorityUnavailable
	}
	if in.ExpectedRevision < 0 || in.TargetRevision == nil || *in.TargetRevision < 0 {
		return ReleaseDefault{}, fault.New(fault.Invalid, "current default and environment revisions are required")
	}
	attrs := Attributes{Feed: "promotion", Audience: "organisation", DataClass: "non-production", PromoteAfter: "main"}
	if err := validateStanding(in.DeploymentName, attrs); err != nil {
		return ReleaseDefault{}, err
	}
	if in.DeploymentName == "main" {
		return ReleaseDefault{}, fault.New(fault.Invalid, "main follows landing; choose a separately released deployment")
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (ReleaseDefault, error) {
		if _, err := releasesql.New(tx.SQL()).LockReleaseProject(ctx, projectID); err != nil {
			return ReleaseDefault{}, err
		}
		if err := project.RefuseUnlessActiveIn(ctx, tx, projectID); err != nil {
			return ReleaseDefault{}, err
		}
		q := deploymentsql.New(tx.SQL())
		settings, err := q.ProjectReleaseSettings(ctx, projectID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return ReleaseDefault{}, err
		}
		if (errors.Is(err, sql.ErrNoRows) && in.ExpectedRevision != 0) || (err == nil && settings.Revision != in.ExpectedRevision) {
			return ReleaseDefault{}, fault.New(fault.Conflict, "release default changed; reload before setup")
		}
		// An imported legacy release always keeps its promotion gate, including
		// an explicit replacement of its unavailable environment.
		required := in.RequirePromotion || settings.LegacySetupJson != nil
		if required {
			if _, err := s.configuration.Authority.Require(ctx, access.Permission{Capability: access.CapReleasePromote, Resource: access.Resource{Type: "release-target", ID: in.DeploymentName, ProjectID: projectID}, Context: access.Context{Route: "deployment.default-setup", Consequential: true}}); err != nil {
				return ReleaseDefault{}, err
			}
		}
		target, err := targetRecord(q.TargetByID(ctx, in.TargetID))
		if err != nil {
			return ReleaseDefault{}, err
		}
		target, err = targetRecord(q.TargetByNameForUpdate(ctx, target.Name))
		if err != nil {
			return ReleaseDefault{}, err
		}
		if target.ID != in.TargetID || target.SettingsRevision != *in.TargetRevision {
			return ReleaseDefault{}, ErrTargetSettingsMoved
		}
		proj, err := s.configuration.Projects.Project(ctx, projectID)
		if err != nil {
			return ReleaseDefault{}, err
		}
		offer, err := s.Targets.offerFor(ctx, q, target, proj.OwnerGroup)
		if err != nil {
			return ReleaseDefault{}, err
		}
		count, err := q.StandingCount(ctx, deploymentsql.StandingCountParams{ProjectID: projectID, TargetID: target.ID, Kind: Standing.String(), Excluding: in.DeploymentName})
		if err != nil {
			return ReleaseDefault{}, err
		}
		if why := offer.Permits(target, proj.Slug, attrs, int(count)); why != "" {
			return ReleaseDefault{}, &OfferDeniedError{why}
		}
		if err := s.refusesPlacement(ctx, projectID, target); err != nil {
			return ReleaseDefault{}, err
		}
		if target.Kind == KindPortainer {
			if _, err := s.providers.PortainerConfig(ctx, target); err != nil {
				return ReleaseDefault{}, err
			}
		}
		dep, err := deploymentRecord(q.DeploymentByName(ctx, deploymentsql.DeploymentByNameParams{ProjectID: projectID, Name: in.DeploymentName}))
		if err == nil {
			dep, err = deploymentRecord(q.DeploymentForUpdate(ctx, dep.ID))
			if err != nil {
				return ReleaseDefault{}, err
			}
			if dep.TargetID != target.ID || dep.Kind != Standing || dep.releaseAttributes() != attrs || dep.RequirePromotion != required {
				return ReleaseDefault{}, fault.New(fault.Conflict, "this deployment already has different settings; choose it explicitly or use a new name")
			}
		} else if errors.Is(err, sql.ErrNoRows) {
			dep, err = ensureStandingIn(ctx, tx, projectID, in.DeploymentName, target.ID, attrs, s.now())
			if err != nil {
				return ReleaseDefault{}, err
			}
			if required {
				dep, err = deploymentRecord(q.EditPromotionPolicy(ctx, deploymentsql.EditPromotionPolicyParams{ID: dep.ID, ExpectedRevision: dep.PolicyRevision, RequirePromotion: true}))
				if err != nil {
					return ReleaseDefault{}, err
				}
			}
		} else {
			return ReleaseDefault{}, err
		}
		if err := s.defaultEligible(ctx, q, projectID, dep, target); err != nil {
			return ReleaseDefault{}, err
		}
		var selected deploymentsql.ProjectReleaseSetting
		if in.ExpectedRevision == 0 {
			selected, err = q.ChooseDefaultDeployment(ctx, deploymentsql.ChooseDefaultDeploymentParams{ProjectID: projectID, DefaultDeploymentID: dep.ID, ExpectedRevision: 0})
		} else {
			selected, err = q.UpdateDefaultDeployment(ctx, deploymentsql.UpdateDefaultDeploymentParams{ProjectID: projectID, DefaultDeploymentID: dep.ID, ExpectedRevision: in.ExpectedRevision})
		}
		if err != nil {
			return ReleaseDefault{}, err
		}
		if err := audit.At(s.now()).RecordIn(ctx, tx, actor.Principal.Name, projectID, "deployment.default-setup", dep.Name, map[string]any{"deploymentId": dep.ID, "targetId": target.ID, "requirePromotion": dep.RequirePromotion, "revision": selected.Revision}); err != nil {
			return ReleaseDefault{}, err
		}
		return ReleaseDefault{ProjectID: projectID, DeploymentID: dep.ID, Revision: selected.Revision}, nil
	})
}
