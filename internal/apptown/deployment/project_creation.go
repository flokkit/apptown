package deployment

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

type creationAuthority interface {
	RequireIn(context.Context, *database.Tx, access.Principal, access.Permission) (access.Principal, error)
}

// InitializeProjectIn gives a newly created project its follows-main placement.
// It joins project creation, after ownership has been recorded. No default
// target or no deploy authority means no placement. Agent credentials may
// create an unconfirmed placement but never supply standing human authority.
// Read or write failures abort creation rather than silently losing the default.
func (s *Service) InitializeProjectIn(ctx context.Context, tx *database.Tx, projectID ids.ID) error {
	caller, ok := access.CallerFrom(ctx)
	if !ok || !caller.Verified || caller.Principal.Kind != "user" {
		return nil
	}
	if s.creationAuthority == nil {
		return access.ErrAuthorityUnavailable
	}
	p, err := project.ByIDIn(ctx, tx, projectID)
	if err != nil {
		return err
	}
	attrs := Attributes{Feed: "main", Audience: "team", DataClass: "synthetic"}
	target, err := s.landingTargetIn(ctx, tx, p, "main", attrs)
	if errors.Is(err, ErrNoTarget) || errors.Is(err, ErrReleaseRefused) {
		return nil
	}
	if err != nil {
		return err
	}
	var authority *json.RawMessage
	if !access.AgentCredential(caller.Principal) {
		resource, err := project.AuthorityResourceIn(ctx, tx, projectID)
		if err != nil {
			return err
		}
		setter, err := s.creationAuthority.RequireIn(ctx, tx, caller.Principal, access.Permission{Capability: access.CapDeploymentDeploy, Resource: resource, Context: access.Context{Consequential: true, Route: "deployment.confirm-feed"}})
		if err != nil {
			var denied *access.Denied
			if errors.As(err, &denied) {
				return nil
			}
			return err
		}
		raw, err := confirmedFeedAuthority(access.Caller{Principal: setter, Verified: true})
		if err != nil {
			return err
		}
		captured := json.RawMessage(raw)
		authority = &captured
	}
	dep, err := ensureStandingIn(ctx, tx, projectID, "main", target.ID, attrs, s.now())
	if err != nil {
		return err
	}
	if authority != nil {
		n, err := deploymentsql.New(tx.SQL()).ConfirmMainFeed(ctx, deploymentsql.ConfirmMainFeedParams{ID: dep.ID, Revision: dep.FeedRevision, Authority: authority})
		if err != nil {
			return err
		}
		if n != 1 {
			return errFeedMoved
		}
	}
	return audit.At(s.now()).RecordIn(ctx, tx, caller.Receipt, projectID, "deployment.bound", "main", map[string]any{"target": target.Name, "feed": "main", "audience": attrs.Audience, "data": attrs.DataClass})
}

func confirmedFeedAuthority(caller access.Caller) (json.RawMessage, error) {
	if !caller.Verified || caller.Principal.Kind != "user" || access.AgentCredential(caller.Principal) {
		return nil, errReleaseAuthority
	}
	raw, err := json.Marshal(operations.AuthorityOf(caller.Principal))
	if err != nil {
		return nil, err
	}
	if _, err := releasePrincipal(raw); err != nil {
		return nil, err
	}
	return raw, nil
}
