package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
	"flokkit.io/forge/internal/forge/jobs"
)

// A checkpoint is evidence needed to resume observation, separate from a
// progress log. A response's identifiers must be committed together.
func (s *Outcomes) Checkpoint(ctx context.Context, at Attempt) deploy.Checkpoint {
	ctx = context.WithoutCancel(ctx) // what the effect reached is recorded though ctx has ended
	return func(values map[string]any) error {
		data, err := json.Marshal(values)
		if err != nil {
			return fmt.Errorf("encoding deployment checkpoint: %w", err)
		}
		if len(values) == 0 {
			return errors.New("empty deployment checkpoint")
		}
		if _, reserved := values["_recovery"]; reserved {
			return errors.New("checkpoint uses reserved recovery key")
		}
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		dep, status, previous, err := lockDeploymentIn(ctx, tx, at)
		if err != nil {
			return err
		}
		if dep.DesiredGeneration != at.Generation || status != AttemptRunning {
			return fmt.Errorf("attempt %d no longer owns provider execution (%s)", at.ID, status)
		}
		if value, ok := values["docker"]; ok {
			encoded, err := json.Marshal(value)
			if err != nil {
				return err
			}
			var identity deploy.DockerIdentity
			if err := json.Unmarshal(encoded, &identity); err != nil {
				return err
			}
			inputs, err := deploymentInputsIn(ctx, tx, at)
			if err != nil {
				return err
			}
			if identity.Deployment != dep.Label() || identity.Generation != at.Generation || identity.Artifact == "" || identity.Artifact != inputs.ArtifactHash || inputs.DockerSource == "" {
				return errors.New("Docker checkpoint differs from admitted source identity")
			}
		}
		var old, incoming map[string]any
		if err := json.Unmarshal([]byte(previous), &old); err != nil {
			return err
		}
		if err := json.Unmarshal(data, &incoming); err != nil {
			return err
		}
		for key, value := range incoming {
			if existing, ok := old[key]; ok && jsonText(existing) != jsonText(value) {
				return fmt.Errorf("attempt %d checkpoint %s already names different evidence", at.ID, key)
			}
		}
		if _, err := deploymentsql.New(tx.SQL()).CheckpointAttempt(ctx, deploymentsql.CheckpointAttemptParams{ObservedJson: json.RawMessage(string(data)), ID: at.ID}); err != nil {
			return err
		}
		return tx.Commit()
	}
}

func lockDeploymentIn(ctx context.Context, tx *database.Tx, at Attempt) (*Deployment, AttemptState, string, error) {
	if at.ID.IsZero() || at.DeploymentID.IsZero() || at.Generation == 0 {
		return nil, 0, "", errors.New("deployment requires an admitted attempt and generation")
	}
	dep, err := LockDeploymentIn(ctx, tx, at.DeploymentID)
	if err != nil {
		return nil, 0, "", err
	}
	r, err := deploymentsql.New(tx.SQL()).AttemptForUpdate(ctx, deploymentsql.AttemptForUpdateParams{ID: at.ID, DeploymentID: at.DeploymentID, Generation: at.Generation})
	if err != nil {
		return dep, 0, "", err
	}
	var state AttemptState
	if err := state.Scan(r.Status); err != nil {
		return dep, 0, "", err
	}
	return dep, state, string(r.ObservedJson), nil
}

// Uncertainty is durable, nonterminal, and owns the deployment until resolved.
// Queue the observation in the same transaction so a crash cannot lose its retry.
func (s *Outcomes) Uncertain(ctx context.Context, at Attempt, cause error) (Outcome, error) {
	ctx = context.WithoutCancel(ctx) // an uncertain outcome is recorded though ctx has ended
	now := s.now()
	if cause == nil {
		return "", errors.New("uncertain deployment requires a reason")
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (Outcome, error) {
		dep, status, _, err := lockDeploymentIn(ctx, tx, at)
		if err != nil {
			return "", err
		}
		verdict, err := at.Unsettled(dep, status)
		if err != nil {
			return "", err
		}
		if !verdict.Changed {
			return verdict.Outcome, nil
		}
		// Uncertainty is recorded once: an attempt already uncertain keeps its
		// state and gains the new observation note.
		next := verdict.Next
		if _, err := deploymentsql.New(tx.SQL()).UncertainAttempt(ctx, deploymentsql.UncertainAttemptParams{
			Status:       next.String(),
			Terminal:     next.Terminal(),
			ObservedJson: json.RawMessage(jsonText(map[string]any{"_recovery": map[string]any{"reason": cause.Error(), "at": now}})),
			ID:           at.ID,
		}); err != nil {
			return "", err
		}
		lost := verdict.Readiness
		if _, err := deploymentsql.New(tx.SQL()).SetReadiness(ctx, deploymentsql.SetReadinessParams{Readiness: lost.String(), UpdatedAt: now, ID: dep.ID}); err != nil {
			return "", err
		}
		if err := s.followups.EnqueueIn(ctx, tx, jobs.Request{Operation: RecoveryJob, Key: fmt.Sprint(at.ID)}); err != nil {
			return "", err
		}
		return Uncertain, nil
	})
}

func (s *Outcomes) InterruptedAttempts(ctx context.Context) ([]Attempt, error) {
	rows, err := deploymentsql.New(s.db.SQL()).InterruptedAttempts(ctx, deploymentsql.InterruptedAttemptsParams{Status: AttemptRunning.String(), Status_2: AttemptUncertain.String()})
	if err != nil {
		return nil, fmt.Errorf("discovering interrupted deployments: %w", err)
	}
	var out []Attempt
	for _, r := range rows {
		out = append(out, Attempt{ID: r.ID, DeploymentID: r.DeploymentID, Generation: r.Generation})
	}
	return out, nil
}

func (s *Outcomes) RecoveryAttempt(ctx context.Context, id ids.ID) (Attempt, AttemptState, map[string]any, error) {
	r, err := deploymentsql.New(s.db.SQL()).AttemptByID(ctx, id)
	if err != nil {
		return Attempt{}, 0, nil, err
	}
	at := Attempt{ID: r.ID, DeploymentID: r.DeploymentID, Generation: r.Generation}
	var status AttemptState
	if err := status.Scan(r.Status); err != nil {
		return at, 0, nil, err
	}
	var checkpoint map[string]any
	if err := json.Unmarshal(r.ObservedJson, &checkpoint); err != nil {
		return at, status, nil, fmt.Errorf("attempt %d checkpoint: %w", id, err)
	}
	return at, status, checkpoint, nil
}

func (s *Outcomes) DeploymentByID(ctx context.Context, id ids.ID) (*Deployment, error) {
	return NewDeployments(s.db).ByID(ctx, id)
}

func (s *Outcomes) AttemptByKey(ctx context.Context, deploymentID ids.ID, key string) (Attempt, AttemptState, error) {
	at := Attempt{DeploymentID: deploymentID}
	r, err := deploymentsql.New(s.db.SQL()).AttemptByKey(ctx, deploymentsql.AttemptByKeyParams{DeploymentID: deploymentID, IdempotencyKey: key})
	if err != nil {
		return at, 0, err
	}
	at.ID, at.Generation = r.ID, r.Generation
	var status AttemptState
	if err := status.Scan(r.Status); err != nil {
		return at, 0, err
	}
	return at, status, nil
}

func (s *Outcomes) InputsAtGeneration(ctx context.Context, deploymentID ids.ID, generation int64) (Inputs, error) {
	r, err := deploymentsql.New(s.db.SQL()).CompletedAttempt(ctx, deploymentsql.CompletedAttemptParams{DeploymentID: deploymentID, Generation: generation, Status: AttemptDone.String()})
	if err != nil {
		return Inputs{}, err
	}
	return s.InputsFor(ctx, Attempt{ID: r.ID, DeploymentID: r.DeploymentID, Generation: r.Generation})
}

// HasOpenAttempt reports whether a deploy or an observation currently owns
// the deployment: an attempt is running or uncertain.
func (s *Outcomes) HasOpenAttempt(ctx context.Context, deploymentID ids.ID) (bool, error) {
	return deploymentsql.New(s.db.SQL()).HasOpenAttempt(ctx, deploymentsql.HasOpenAttemptParams{DeploymentID: deploymentID, RunningState: AttemptRunning.String(), UncertainState: AttemptUncertain.String()})
}
