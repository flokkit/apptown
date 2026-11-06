package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/flokkit/apptown/internal/access"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

var errFeedMoved = fault.New(fault.Conflict, "deployment feed changed; read its settings again")
var errFeedUnconfirmed = errors.New("follows main is not confirmed for this deployment")

// ConfirmFeed is the feed settings write for follows-main standing authority.
// The revision is the one the person saw; confirmation never reuses a past vote.
func (s *Service) ConfirmFeed(ctx context.Context, projectID ids.ID, name string, revision int64) (*Deployment, error) {
	dep, err := s.Deployments.ByName(ctx, projectID, name)
	if err != nil {
		return nil, err
	}
	if dep.Feed != "main" || !dep.Standing() {
		return nil, errFeedMoved
	}
	actor, err := s.requireDeploy(ctx, projectID, name, dep.Protected(), "deployment.confirm-feed")
	if err != nil {
		return nil, err
	}
	raw, err := confirmedFeedAuthority(actor)
	if err != nil {
		return nil, err
	}
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		current, err := lockDeploymentRecordIn(ctx, tx, dep.ID)
		if err != nil {
			return err
		}
		if _, err := s.requireDeploy(ctx, projectID, name, current.Protected(), "deployment.confirm-feed"); err != nil {
			return err
		}
		captured := json.RawMessage(raw)
		n, err := deploymentsql.New(tx.SQL()).ConfirmMainFeed(ctx, deploymentsql.ConfirmMainFeedParams{ID: dep.ID, Revision: revision, Authority: &captured})
		if err != nil {
			return err
		}
		if n != 1 {
			return errFeedMoved
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Deployments.ByID(ctx, dep.ID)
}

// ConfirmAllFeeds confirms only unconfirmed follows-main settings this caller
// may authorize. Deployments outside that authority are omitted.
func (s *Service) ConfirmAllFeeds(ctx context.Context) (confirmed, refused []string, err error) {
	deps, err := s.Deployments.All(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, dep := range deps {
		if dep.Feed != "main" || dep.FeedConfirmed {
			continue
		}
		if _, e := s.requireDeploy(ctx, dep.ProjectID, dep.Name, dep.Protected(), "deployment.confirm-feed"); e != nil {
			var denied *access.Denied
			if errors.As(e, &denied) {
				continue
			}
			return confirmed, refused, e
		}
		_, e := s.ConfirmFeed(ctx, dep.ProjectID, dep.Name, dep.FeedRevision)
		if e != nil {
			refused = append(refused, dep.Project+"/"+dep.Name+": "+e.Error())
		} else {
			confirmed = append(confirmed, dep.Project+"/"+dep.Name)
		}
	}
	return confirmed, refused, nil
}
