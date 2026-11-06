package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// RequestLandingReleases runs after completion committed. Each request selects
// that run's bundle and its destination's standing authorization. No provider
// preparation or landing approver supplies deployment authority here.
func (s *Service) RequestLandingReleases(ctx context.Context, projectID, runID, changeID ids.ID, approvers string) error {
	bundle, err := s.Bundles.OfRun(ctx, projectID, runID)
	if err != nil {
		return err
	}
	if bundle == nil {
		return nil
	}
	deps, err := deploymentsql.New(s.db.SQL()).MainDeployments(ctx, projectID)
	if err != nil {
		return err
	}
	for _, row := range deps {
		err := s.db.InTx(ctx, func(tx *database.Tx) error {
			if _, err := releasesql.New(tx.SQL()).LockReleaseProject(ctx, projectID); err != nil {
				return err
			}
			q := deploymentsql.New(tx.SQL())
			exists, err := q.LandingReleaseExists(ctx, deploymentsql.LandingReleaseExistsParams{RunID: runID, DeploymentID: row.ID})
			if err != nil || exists {
				return err
			}
			dep, err := lockDeploymentRecordIn(ctx, tx, row.ID)
			if err != nil {
				return err
			}
			if dep.Feed != "main" {
				return nil
			}
			setter := "unconfirmed"
			if pr, e := releasePrincipal(dep.feedAuthority); e == nil {
				setter = pr.Name
			}
			rel, err := createReleaseIn(ctx, tx, ReleaseDraft{Automatic: true, Source: ReleaseFromBundle, ProjectID: projectID, DeploymentID: dep.ID, BundleID: bundle.ID, RunID: runID, ChangeID: changeID, Deployment: dep.Name, Revision: bundle.ContractRevision, Actor: "follows main, set by " + setter, At: s.now()})
			if err != nil {
				return err
			}
			attrs, err := json.Marshal(dep.releaseAttributes())
			if err != nil {
				return err
			}
			target, err := s.Targets.ByID(ctx, dep.TargetID)
			if err != nil {
				return err
			}
			lockedTarget, err := targetRecord(q.TargetByNameForUpdate(ctx, target.Name))
			if err != nil {
				return err
			}
			if lockedTarget.ID != dep.TargetID {
				return errTargetSettingsMoved
			}
			binding, err := providerBindingOf(lockedTarget)
			if err != nil {
				return err
			}
			address, err := s.admitAddressIn(ctx, tx, dep, lockedTarget, nil, nil, setter)
			if err != nil {
				return err
			}
			var ingress *json.RawMessage
			if address != nil {
				raw, err := addressSnapshotJSON(address)
				if err != nil {
					return err
				}
				ingress = &raw
			}
			var authority *json.RawMessage
			if len(dep.feedAuthority) > 0 {
				raw := json.RawMessage(dep.feedAuthority)
				authority = &raw
			}
			if _, err := q.InsertReleaseRequest(ctx, deploymentsql.InsertReleaseRequestParams{ReleaseID: rel.ID, TargetID: dep.TargetID, BaseGeneration: dep.DesiredGeneration, AttributesJson: attrs, ProviderBindingJson: binding, IngressJson: ingress, AuthorityJson: authority, RequirePromotion: dep.Protected()}); err != nil {
				return err
			}
			if _, err := q.LinkLandingRequest(ctx, deploymentsql.LinkLandingRequestParams{ReleaseID: rel.ID, FeedRevision: sql.NullInt64{Int64: dep.FeedRevision, Valid: true}, LandingApprovers: approvers}); err != nil {
				return err
			}
			if err := appendReleaseEvent(ctx, tx, rel.ID, fmt.Sprintf("follows main, set by %s; landing approvers: %s", setter, approvers), s.now()); err != nil {
				return err
			}
			state, err := s.admitApprovalIn(ctx, tx, dep, rel)
			if err != nil {
				return err
			}
			if state == releaseWaiting {
				return nil
			}
			return s.release.Queue.EnqueueReleaseIn(ctx, tx, rel.ID)
		})
		if err != nil {
			return err
		}
	}
	return nil
}
