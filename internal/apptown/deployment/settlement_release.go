package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
)

type deploymentRelease struct {
	id, projectID, deploymentID, runID           ids.ID
	number, generation                           int64
	state                                        ReleaseState
	deployment, actor, revision, image, artifact string
}

func deploymentReleaseIn(ctx context.Context, tx *database.Tx, id ids.ID) (deploymentRelease, error) {
	r, err := releasesql.New(tx.SQL()).ReleaseForUpdate(ctx, id)
	if err != nil {
		return deploymentRelease{}, database.QueryError(err, "ReleaseForUpdate")
	}
	state, err := parseReleaseState(r.State)
	if err != nil {
		return deploymentRelease{}, fmt.Errorf("release %d: %w", id, err)
	}
	return deploymentRelease{id: r.ID, projectID: r.ProjectID, number: r.Number, deploymentID: r.DeploymentID,
		generation: r.Generation, runID: r.RunID, state: state, deployment: r.Deployment,
		actor: r.ApprovedBy, revision: r.CommitSha, image: r.ExternalRef, artifact: r.ArtifactHash}, nil
}

func (r deploymentRelease) belongsTo(dep *Deployment) error {
	if r.projectID != dep.ProjectID || (r.deploymentID != dep.ID && (!r.deploymentID.IsZero() || r.deployment != dep.Name)) {
		return fmt.Errorf("release %d does not belong to deployment %d", r.id, dep.ID)
	}
	return nil
}

func deploymentInputsIn(ctx context.Context, tx *database.Tx, at Attempt) (Inputs, error) {
	r, err := releasesql.New(tx.SQL()).DeploymentInputs(ctx, at.ID)
	if err != nil {
		return Inputs{}, database.QueryError(err, "DeploymentInputs")
	}
	var raw json.RawMessage
	if r.IngressJson != nil {
		raw = *r.IngressJson
	}
	address, err := addressSnapshotFrom(raw)
	if err != nil {
		return Inputs{}, err
	}
	applied, err := appliedFrom(r.Applied)
	return Inputs{Address: address, ReleaseID: r.ReleaseID, ReleaseRun: r.ReleaseRun, Revision: r.Revision,
		ImageDigest: r.ImageDigest, ArtifactHash: r.ArtifactHash, DockerSource: r.DockerSource, Rebuild: r.Rebuild, BundleID: r.BundleID, Applied: applied}, err
}

func insertDeploymentInputsIn(ctx context.Context, tx *database.Tx, at Attempt, in Inputs) error {
	applied, err := in.Applied.json()
	if err != nil {
		return err
	}
	address, err := addressSnapshotJSON(in.Address)
	if err != nil {
		return err
	}
	var addressInput *json.RawMessage
	if address != nil {
		addressInput = &address
	}
	n, err := releasesql.New(tx.SQL()).InsertDeploymentInputs(ctx, releasesql.InsertDeploymentInputsParams{
		AttemptID: at.ID, ReleaseID: in.ReleaseID, ReleaseRun: in.ReleaseRun, Revision: in.Revision,
		ImageDigest: in.ImageDigest, ArtifactHash: in.ArtifactHash, DockerSource: in.DockerSource, Rebuild: in.Rebuild, BundleID: in.BundleID,
		Applied: applied, IngressJson: addressInput,
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("deployment attempt %d inputs were not recorded", at.ID)
	}
	return nil
}

func appendReleaseEvent(ctx context.Context, db database.Queryer, releaseID ids.ID, text string, at time.Time) error {
	n, err := releasesql.New(db.SQL()).AppendReleaseEvent(ctx, releasesql.AppendReleaseEventParams{ID: ids.New(at),
		ReleaseID: releaseID, At: at, Text: text,
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("release %d event was not recorded", releaseID)
	}
	return nil
}

// Terminal receipts and the release state are part of the outcome transaction.
// A superseded attempt uses the existing failed release state, with an explicit
// reason in history, so it cannot remain releasing or fail the new deployment.
func settleDeploymentReleaseIn(ctx context.Context, tx *database.Tx, at Attempt, dep *Deployment, in Inputs, status AttemptState, o deploy.Observation, reason string, now time.Time) error {
	if in.ReleaseID.IsZero() {
		return nil
	}
	r, err := deploymentReleaseIn(ctx, tx, in.ReleaseID)
	if err != nil {
		return err
	}
	if err := r.belongsTo(dep); err != nil {
		return err
	}
	state, err := releaseOutcome(status)
	if err != nil {
		return err
	}
	kind := "release.failed"
	image := in.ImageDigest
	if state == Released {
		image, err = deploymentImage(in, o)
		if err != nil {
			return err
		}
		if o.Rollout != "active" || o.Generation != at.Generation {
			return fmt.Errorf("attempt %d has no confirmed observation of generation %d", at.ID, at.Generation)
		}
		kind = "release.deployed"
	}
	if r.state == state && (state == ReleaseFailed || r.generation == at.Generation) {
		return nil // the same outcome again: acknowledged, never rewritten
	}
	if _, err := r.state.settled(status); err != nil {
		return fmt.Errorf("%w: release %d is %s at generation %d", errOutcomeConflict, r.id, r.state, r.generation)
	}
	if r.revision != "" && in.Revision != "" && r.revision != in.Revision {
		return fmt.Errorf("release %d revision differs from attempt %d", r.id, at.ID)
	}
	text := fmt.Sprintf("deployment attempt %d, generation %d: %s", at.ID, at.Generation, state)
	if status == attemptSuperseded {
		reason = "superseded by a newer deployment generation"
	}
	if state == Released {
		text += " — " + o.URL
	} else {
		text += " — " + reason
	}
	n, err := releasesql.New(tx.SQL()).SettleReleaseAttempt(ctx, releasesql.SettleReleaseAttemptParams{
		State: state.String(), Released: state == Released, OpenState: Releasing.String(),
		At: now, Notes: o.URL, DeploymentID: dep.ID, Generation: at.Generation,
		ArtifactHash: in.ArtifactHash, ExternalRef: image, ID: r.id,
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return errOutcomeConflict
	}
	if err := appendReleaseEvent(ctx, tx, r.id, text, now); err != nil {
		return err
	}
	actor := r.actor
	if actor == "" {
		actor = "apptown-system"
	}
	return audit.At(now).RecordIn(ctx, tx, actor, dep.ProjectID, kind, fmt.Sprintf("r%d", r.number), map[string]any{
		"attempt": at.ID, "generation": at.Generation, "deployment": dep.Name, "targetId": dep.TargetID,
		"revision": in.Revision, "artifact": in.ArtifactHash, "image": image, "url": o.URL, "reason": reason,
	})
}

func deploymentImage(in Inputs, o deploy.Observation) (string, error) {
	if in.ImageDigest != "" && o.ImageDigest != "" && in.ImageDigest != o.ImageDigest {
		return "", errors.New("observed image differs from the admitted image")
	}
	if o.ImageDigest != "" {
		return o.ImageDigest, nil
	}
	return in.ImageDigest, nil
}

// Preparation can fail before an attempt exists. Once admission has committed,
// only the deployment outcome operation may settle the associated release.
func (s *Outcomes) FailUnstartedRelease(ctx context.Context, releaseID ids.ID, cause error) (deploymentOutcome, error) {
	ctx = context.WithoutCancel(ctx) // the release failed: settled though ctx has ended
	if cause == nil {
		return "", errors.New("release failure requires a cause")
	}
	now := s.now()
	return database.Transact(ctx, s.db, func(tx *database.Tx) (deploymentOutcome, error) {
		// Admission locks deployment before release. Respect that order even for
		// preparation failure, so concurrent admission cannot slip past the test.
		queries := releasesql.New(tx.SQL())
		envID, err := queries.ReleaseDeploymentID(ctx, releaseID)
		if err != nil {
			return "", err
		}
		if !envID.IsZero() {
			if _, err := queries.LockDeployment(ctx, envID); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return "", err
			}
		}
		r, err := deploymentReleaseIn(ctx, tx, releaseID)
		if err != nil {
			return "", err
		}
		if r.state == ReleaseFailed {
			return alreadyApplied, nil
		}
		if !r.state.open() {
			return "", errOutcomeConflict
		}
		admitted, err := queries.HasReleaseAttempt(ctx, releaseID)
		if err != nil {
			return "", err
		}
		if admitted {
			return "", fmt.Errorf("release %d has a durable deployment attempt; settle its outcome", releaseID)
		}
		changed, err := queries.FailUnstartedRelease(ctx, releasesql.FailUnstartedReleaseParams{State: ReleaseFailed.String(), ID: releaseID})
		if err != nil {
			return "", err
		}
		if changed != 1 {
			return "", errOutcomeConflict
		}
		if err := appendReleaseEvent(ctx, tx, releaseID, "FAILED before deployment admission: "+cause.Error(), now); err != nil {
			return "", err
		}
		if err := audit.At(now).RecordIn(ctx, tx, r.actor, r.projectID, "release.failed", fmt.Sprintf("r%d", r.number), map[string]any{"reason": cause.Error(), "deployment": r.deployment}); err != nil {
			return "", err
		}
		return Applied, nil
	})
}
