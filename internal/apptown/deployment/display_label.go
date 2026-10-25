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
	"github.com/flokkit/apptown/internal/strs"
)

// EditTargetLabel changes presentation without changing target settings or scope.
func (s *Service) EditTargetLabel(ctx context.Context, id ids.ID, expected, value string) (*Target, error) {
	if s.authority == nil {
		return nil, access.ErrAuthorityUnavailable
	}
	actor, err := s.authority.Require(ctx, access.Permission{Capability: access.CapPlatformTargets, Resource: access.Resource{Type: "instance"}, Context: access.Context{Route: "deployment.target-label", Consequential: true}})
	if err != nil {
		return nil, err
	}
	label, err := strs.DisplayLabel(value)
	if err != nil {
		return nil, fault.New(fault.Invalid, err.Error())
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Target, error) {
		row, err := deploymentsql.New(tx.SQL()).EditTargetDisplayLabel(ctx, deploymentsql.EditTargetDisplayLabelParams{ID: id, ExpectedLabel: expected, DisplayLabel: label})
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fault.New(fault.Conflict, "target label changed or target no longer exists; reload before editing")
		}
		if err != nil {
			return nil, err
		}
		if err := audit.At(s.now()).RecordIn(ctx, tx, actor.Principal.Name, ids.Nil, "target.label-edited", row.Name, map[string]any{"id": id, "before": expected, "label": label}); err != nil {
			return nil, err
		}
		return targetRecord(row, nil)
	})
}

// EditDeploymentLabel leaves all machine identities, generations and addresses unchanged.
func (s *Service) EditDeploymentLabel(ctx context.Context, projectID, id ids.ID, expected, value string) (*Deployment, error) {
	if s.configuration.Authority == nil {
		return nil, access.ErrAuthorityUnavailable
	}
	actor, err := s.configuration.Authority.Require(ctx, access.Permission{Capability: access.CapDeploymentBind, Resource: access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}, Context: access.Context{Route: "deployment.label", Consequential: true}})
	if err != nil {
		return nil, err
	}
	label, err := strs.DisplayLabel(value)
	if err != nil {
		return nil, fault.New(fault.Invalid, err.Error())
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Deployment, error) {
		if _, err := releasesql.New(tx.SQL()).LockReleaseProject(ctx, projectID); err != nil {
			return nil, err
		}
		if err := project.RefuseUnlessActiveIn(ctx, tx, projectID); err != nil {
			return nil, err
		}
		row, err := deploymentsql.New(tx.SQL()).EditDeploymentDisplayLabel(ctx, deploymentsql.EditDeploymentDisplayLabelParams{ID: id, ProjectID: projectID, ExpectedLabel: expected, DisplayLabel: label})
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fault.New(fault.Conflict, "deployment label changed or deployment no longer exists; reload before editing")
		}
		if err != nil {
			return nil, err
		}
		if err := audit.At(s.now()).RecordIn(ctx, tx, actor.Principal.Name, projectID, "deployment.label-edited", row.Name, map[string]any{"id": id, "before": expected, "label": label}); err != nil {
			return nil, err
		}
		return deploymentRecord(row, nil)
	})
}
