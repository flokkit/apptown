package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/access"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/apptown/project"
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
	return s.SetFeed(ctx, projectID, name, FeedSettings{Revision: revision, Feed: "main", ApprovalRole: dep.ApprovalRole})
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

// FeedSettings is one revision-guarded edit of a deployment's release feed.
// From names a standing deployment in the same project for promotion only.
type FeedSettings struct {
	ApprovalRole string `json:"approvalRole"`
	Revision     int64  `json:"revision"`
	Feed         string `json:"feed"`
	From         string `json:"from,omitempty"`
}

// SetFeed changes the feed and its standing authorization together. All feed
// edits in a project lock the graph in identity order, so concurrent edits
// cannot introduce a cycle.
func (s *Service) SetFeed(ctx context.Context, projectID ids.ID, name string, in FeedSettings) (*Deployment, error) {
	if in.ApprovalRole != "" && (access.RoleByName(in.ApprovalRole) == nil || access.RoleByName(in.ApprovalRole).Scope != access.ScopeProject) {
		return nil, fault.New(fault.Invalid, "choose a role from the access catalog, or none")
	}
	if in.Feed != "main" && in.Feed != "manual" && in.Feed != "promotion" {
		return nil, fault.New(fault.Invalid, "choose main, manual or promotion")
	}
	if (in.Feed == "promotion") != (in.From != "") {
		return nil, fault.New(fault.Invalid, "promotion needs a source deployment; other feeds have no source")
	}
	dep, err := s.Deployments.ByName(ctx, projectID, name)
	if err != nil {
		return nil, err
	}
	actor, err := s.requireDeploy(ctx, projectID, name, dep.Protected(), "deployment.set-feed")
	if err != nil {
		return nil, err
	}
	if !actor.Verified {
		return nil, errReleaseAuthority
	}
	var captured *json.RawMessage
	if in.Feed == "main" {
		raw, err := confirmedFeedAuthority(actor)
		if err != nil {
			return nil, err
		}
		value := json.RawMessage(raw)
		captured = &value
	}
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := releasesql.New(tx.SQL()).LockReleaseProject(ctx, projectID); err != nil {
			return err
		}
		if err := project.RefuseUnlessActiveIn(ctx, tx, projectID); err != nil {
			return err
		}
		q := deploymentsql.New(tx.SQL())
		rows, err := q.LockFeedSettings(ctx, projectID)
		if err != nil {
			return err
		}
		byID := make(map[ids.ID]*Deployment, len(rows))
		var current, source *Deployment
		for _, row := range rows {
			d, err := deploymentRecord(row, nil)
			if err != nil {
				return err
			}
			byID[d.ID] = d
			if d.ID == dep.ID {
				current = d
			}
			if d.Name == in.From {
				source = d
			}
		}
		if current == nil {
			return ErrDeploymentMissing
		}
		if !current.Standing() {
			return fault.New(fault.Invalid, "only standing deployments have feed settings")
		}
		if current.FeedRevision != in.Revision {
			return errFeedMoved
		}
		if _, err := s.requireDeploy(ctx, projectID, name, current.Protected(), "deployment.set-feed"); err != nil {
			return err
		}
		if in.Feed == "main" && !current.StartFrom.IsZero() && !current.StartedFrom.Valid {
			return fault.New(fault.Invalid, "release the deployment's starting copy before choosing follows main")
		}
		var from ids.ID
		if in.Feed == "promotion" {
			if source == nil || !source.Standing() {
				return fault.New(fault.Invalid, "choose a standing source deployment in this project")
			}
			from = source.ID
			seen := map[ids.ID]bool{current.ID: true}
			for d := source; d != nil; d = byID[d.PromotedFromID] {
				if seen[d.ID] {
					return fault.New(fault.Invalid, "promotion sources cannot form a cycle; choose another deployment")
				}
				seen[d.ID] = true
				if d.Feed != "promotion" {
					break
				}
			}
		}
		changed := *current
		changed.Feed = in.Feed
		if err := preserveDefaultConfiguration(ctx, tx, &changed); err != nil {
			return err
		}
		n, err := q.SetDeploymentFeed(ctx, deploymentsql.SetDeploymentFeedParams{ID: current.ID, Revision: in.Revision, Feed: in.Feed, ApprovalRole: in.ApprovalRole, PromotedFromID: from, Authority: captured})
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

var errPromotionSource = fault.New(fault.Unprocessable, "promotion has no source deployment; choose one in deployment Settings")

var errPromotionEmpty = fault.New(fault.Unprocessable, "source deployment has no known successful release to promote; deploy to the source first")

var errPromotionStopped = fault.New(fault.Unprocessable, "observed stopped or taken down; deploy to the source before promoting")

func (s *Service) promotionBundle(ctx context.Context, dep *Deployment, expected ids.ID) (ids.ID, string, error) {
	if dep.PromotedFromID.IsZero() {
		return ids.Nil, "", errPromotionSource
	}
	source, err := s.Deployments.ByID(ctx, dep.PromotedFromID)
	if err != nil {
		return ids.Nil, "", err
	}
	if source.ProjectID != dep.ProjectID || !source.Standing() {
		return ids.Nil, "", errPromotionEmpty
	}
	target, err := s.Targets.ByID(ctx, source.TargetID)
	if err != nil {
		return ids.Nil, "", err
	}
	observed := s.observations.read(listingRecord{deployment: source, target: target}, s.now())
	if !observed.Stale && observed.State == RuntimeStopped {
		return ids.Nil, "", fmt.Errorf("source deployment %s: %w", source.Name, errPromotionStopped)
	}
	bundle, err := s.runningBundle(ctx, source)
	if err != nil {
		return ids.Nil, "", err
	}
	if bundle.IsZero() {
		return ids.Nil, "", errPromotionEmpty
	}
	number, err := s.Releases.LatestReleasedNumber(ctx, source.ProjectID, source.Name)
	if err != nil {
		return ids.Nil, "", err
	}
	if !expected.IsZero() && expected != bundle {
		return ids.Nil, "", fault.New(fault.Conflict, fmt.Sprintf("%s now runs release %d; promote again", source.Name, number))
	}
	return bundle, fmt.Sprintf("promoted release %d from %s", number, source.Name), nil
}
