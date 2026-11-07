package deployment

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
)

// claimRestartRetry consumes the attempt's one retry before preparation or
// provider effects. Another delivery or restart can only observe its outcome.
func (s *Outcomes) claimRestartRetry(ctx context.Context, at Attempt) (bool, error) {
	return database.Transact(ctx, s.db, func(tx *database.Tx) (bool, error) {
		dep, status, checkpoint, err := lockDeploymentIn(ctx, tx, at)
		if err != nil {
			return false, err
		}
		if !status.open() || dep.DesiredGeneration != at.Generation {
			return false, nil
		}
		var noted map[string]any
		if err := json.Unmarshal([]byte(checkpoint), &noted); err != nil {
			return false, err
		}
		if _, restarted := noted["_restart"]; !restarted {
			return false, nil
		}
		n, err := deploymentsql.New(tx.SQL()).ClaimRestartRetry(ctx, deploymentsql.ClaimRestartRetryParams{ID: at.ID, At: s.now()})
		return n == 1, err
	})
}

func (s *Service) retryInterrupted(ctx context.Context, at Attempt) (deploy.Observation, error) {
	var empty deploy.Observation
	in, err := s.Outcomes.InputsFor(ctx, at)
	if err != nil {
		return empty, err
	}
	if in.ReleaseID.IsZero() {
		return empty, errors.New("interrupted attempt has no release request to retry")
	}
	row, err := deploymentsql.New(s.db.SQL()).RequestedReleaseForUpdate(ctx, in.ReleaseID)
	if err != nil {
		return empty, err
	}
	rel, err := s.Releases.record(row.Release, nil)
	if err != nil {
		return empty, err
	}
	if rel.DeploymentID != at.DeploymentID || rel.State != Releasing {
		return empty, errors.New("interrupted release no longer owns this deployment")
	}
	var raw json.RawMessage
	if row.ReleaseRequest.AuthorityJson != nil {
		raw = *row.ReleaseRequest.AuthorityJson
	}
	captured, err := releasePrincipal(raw)
	if err != nil {
		return empty, err
	}
	if err := releaseAllowed(ctx, s, rel, row.ReleaseRequest, captured); err != nil {
		return empty, err
	}
	claimed, err := s.Outcomes.claimRestartRetry(ctx, at)
	if err != nil {
		return empty, err
	}
	if !claimed {
		return empty, errors.New("the restart retry was already used; checking its outcome")
	}
	s.log(rel.ID, "interrupted by a restart — retrying release %d as generation %d once", rel.Number, at.Generation)
	result, err := s.executeRequestedRelease(ctx, rel, row.ReleaseRequest, &at)
	return result.Observed, err
}
