package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// CheckRelease rechecks the accepted requester and approval immediately before
// data protection's destructive effects. It never admits or starts a release.
func (s *Service) CheckRelease(ctx context.Context, id ids.ID) error {
	row, err := deploymentsql.New(s.db.SQL()).RequestedReleaseForUpdate(ctx, id)
	if err != nil {
		return err
	}
	rel, err := s.Releases.record(row.Release, nil)
	if err != nil {
		return err
	}
	if rel.State != Releasing {
		return fault.New(fault.Conflict, "the release is no longer executing")
	}
	var raw json.RawMessage
	if row.ReleaseRequest.AuthorityJson != nil {
		raw = *row.ReleaseRequest.AuthorityJson
	}
	principal, err := releasePrincipal(raw)
	if err != nil {
		return err
	}
	return releaseAllowed(ctx, s, rel, row.ReleaseRequest, principal)
}
func releaseAllowed(ctx context.Context, s *Service, rel *Release, placement deploymentsql.ReleaseRequest, captured access.Principal) error {
	if err := s.checkReleaseApproval(ctx, rel); err != nil {
		return err
	}
	if placement.FeedRevision.Valid {
		current, err := s.Deployments.ByID(ctx, rel.DeploymentID)
		if err != nil {
			return err
		}
		if current.Feed != "main" || current.FeedRevision != placement.FeedRevision.Int64 {
			return errFeedMoved
		}
	}
	if err := s.requireReleaseAuthority(ctx, captured, rel.DeploymentID, placement.RequirePromotion); err != nil {
		if placement.FeedRevision.Valid {
			return fmt.Errorf("%w; confirm follows main again with current deploy authority", err)
		}
		return err
	}
	return nil
}
