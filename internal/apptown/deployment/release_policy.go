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

// SetPromotionPolicy changes the release permission gate, never data semantics.
func (s *Service) SetPromotionPolicy(ctx context.Context, projectID, deploymentID ids.ID, expected int64, required bool) (*Deployment, error) {
	if s.configuration.Authority == nil {
		return nil, access.ErrAuthorityUnavailable
	}
	actor, err := s.configuration.Authority.Require(ctx, access.Permission{Capability: access.CapDeploymentBind, Resource: access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}, Context: access.Context{Route: "deployment.policy", Consequential: true}})
	if err != nil {
		return nil, err
	}
	if actor.Principal.Name == "" {
		return nil, access.ErrAuthorityUnavailable
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Deployment, error) {
		if _, err := releasesql.New(tx.SQL()).LockReleaseProject(ctx, projectID); err != nil {
			return nil, err
		}
		if err := project.RefuseUnlessActiveIn(ctx, tx, projectID); err != nil {
			return nil, err
		}
		q := deploymentsql.New(tx.SQL())
		dep, err := deploymentRecord(q.DeploymentForUpdate(ctx, deploymentID))
		if err != nil {
			return nil, err
		}
		if dep.ProjectID != projectID {
			return nil, fault.New(fault.Invalid, "choose a deployment in this project")
		}
		if _, err := s.configuration.Authority.Require(ctx, access.Permission{Capability: access.CapReleasePromote, Resource: access.Resource{Type: "release-target", ID: dep.Name, ProjectID: projectID}, Context: access.Context{Route: "deployment.policy", Consequential: true}}); err != nil {
			return nil, err
		}
		changed, err := deploymentRecord(q.EditPromotionPolicy(ctx, deploymentsql.EditPromotionPolicyParams{ID: deploymentID, ExpectedRevision: expected, RequirePromotion: required}))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fault.New(fault.Conflict, "release policy changed; reload before editing")
		}
		if err != nil {
			return nil, err
		}
		if err := audit.At(s.now()).RecordIn(ctx, tx, actor.Principal.Name, projectID, "deployment.promotion-policy", dep.Name, map[string]any{"requirePromotion": required, "revision": changed.PolicyRevision}); err != nil {
			return nil, err
		}
		return changed, nil
	})
}
