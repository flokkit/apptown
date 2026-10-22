package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

// Outcomes owns attempt admission, checkpoints and settlement. Its transactions
// lock the deployment before the attempt and perform no provider I/O. Release
// settlement and required receipts commit with their attempt outcome.
type Outcomes struct {
	db        *database.DB
	followups jobs.Enqueuer
	clock     func() time.Time // AppTown's clock, which stamps attempts and settlements; nil is the real one
}

func (s *Outcomes) now() time.Time { return clock.Or(s.clock)().UTC() }

func NewOutcomes(db *database.DB, followups jobs.Enqueuer) *Outcomes {
	return &Outcomes{db: db, followups: followups}
}

var ErrUncertain = errors.New("deployment outcome is uncertain")

var ErrOutcomeConflict = errors.New("deployment attempt already has a different outcome")

// Admit binds a deployment-scoped idempotency key to immutable inputs and a
// generation. A new attempt atomically supersedes running predecessors and their
// releases. An uncertain predecessor blocks admission with ErrUncertain.
// A matching key returns the recorded attempt and created=false, even if that
// attempt is no longer active. Changed inputs for the same key are an error.
// Only created=true authorizes the caller to begin provider execution.
func (s *Outcomes) Admit(ctx context.Context, in Start) (Attempt, bool, error) {
	now := s.now()
	deploymentID, key := in.DeploymentID, in.Key
	at := Attempt{DeploymentID: deploymentID}
	if deploymentID.IsZero() || key == "" {
		return at, false, errors.New("deployment admission requires a deployment and idempotency key")
	}
	if in.Inputs.DockerSource != "" {
		var selected dockerSourceIdentity
		if err := json.Unmarshal([]byte(in.Inputs.DockerSource), &selected); err != nil {
			return at, false, err
		}
		if selected.Version != 1 || selected.Revision != in.Inputs.Revision || in.Inputs.ArtifactHash != deploy.ConfigHash(in.Inputs.DockerSource) || in.Inputs.ImageDigest != "" {
			return at, false, errors.New("Docker admission requires matching selected source and artifact identity")
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return at, false, err
	}
	defer func() { _ = tx.Rollback() }()
	dep, err := LockDeploymentIn(ctx, tx, deploymentID)
	if err != nil {
		return Attempt{}, false, err
	}
	if err := dep.Bound(in.TargetID); err != nil {
		return Attempt{}, false, err
	}
	at.Generation = dep.DesiredGeneration
	storedAttempt, readErr := deploymentsql.New(tx.SQL()).AttemptByKey(ctx, deploymentsql.AttemptByKeyParams{DeploymentID: deploymentID, IdempotencyKey: key})
	found, err := database.Lookup(readErr)
	if err != nil {
		return Attempt{}, false, err
	}
	if found {
		at.ID, at.Generation = storedAttempt.ID, storedAttempt.Generation
		stored, err := deploymentInputsIn(ctx, tx, at)
		if err != nil {
			return Attempt{}, false, err
		}
		if !stored.Same(in.Inputs) {
			return Attempt{}, false, fmt.Errorf("deployment key %q already names different inputs", key)
		}
		if err := tx.Commit(); err != nil {
			return Attempt{}, false, err
		}
		return at, false, nil
	}
	nextGeneration, admitted, err := dep.Admit(in.BaseGeneration)
	if err != nil {
		return Attempt{}, false, err
	}
	uncertainID, err := deploymentsql.New(tx.SQL()).UncertainAttemptID(ctx, deploymentsql.UncertainAttemptIDParams{DeploymentID: deploymentID, Status: AttemptUncertain.String()})
	if err != nil {
		return Attempt{}, false, err
	}
	if !uncertainID.IsZero() {
		return Attempt{}, false, fmt.Errorf("%w: deployment %d attempt %d needs observation or operator resolution", ErrUncertain, deploymentID, uncertainID)
	}
	if !in.Inputs.ReleaseID.IsZero() {
		r, err := deploymentReleaseIn(ctx, tx, in.Inputs.ReleaseID)
		if err != nil {
			return Attempt{}, false, err
		}
		if err := r.belongsTo(dep); err != nil {
			return Attempt{}, false, err
		}
		if !r.state.Open() && !(r.state == Released && r.generation == 0) {
			return Attempt{}, false, ErrOutcomeConflict
		}
		if r.revision != "" && r.revision != in.Inputs.Revision {
			return Attempt{}, false, fmt.Errorf("release %d revision differs from requested deployment", r.id)
		}
	}
	// Capture superseded identities before updating them, then settle their
	// releases in this same transaction. An old runner need not return first.
	rows, err := deploymentsql.New(tx.SQL()).AttemptsForUpdate(ctx, deploymentsql.AttemptsForUpdateParams{DeploymentID: deploymentID, Status: AttemptRunning.String()})
	if err != nil {
		return Attempt{}, false, err
	}
	previous := make([]Attempt, 0, len(rows))
	for _, r := range rows {
		previous = append(previous, Attempt{ID: r.ID, DeploymentID: r.DeploymentID, Generation: r.Generation})
	}
	at.Generation = nextGeneration
	inserted, err := deploymentsql.New(tx.SQL()).InsertAttempt(ctx, deploymentsql.InsertAttemptParams{ID: ids.New(now),
		DeploymentID:   deploymentID,
		Generation:     at.Generation,
		IdempotencyKey: key,
		Status:         AttemptRunning.String(),
		StartedAt:      now,
	})
	if err != nil {
		return Attempt{}, false, err
	}
	at.ID = inserted.ID
	if err := insertDeploymentInputsIn(ctx, tx, at, in.Inputs); err != nil {
		return Attempt{}, false, err
	}
	if _, err := deploymentsql.New(tx.SQL()).SupersedeAttempts(ctx, deploymentsql.SupersedeAttemptsParams{
		Status:       AttemptSuperseded.String(),
		SupersededBy: at.ID,
		FinishedAt:   sql.NullTime{Time: now, Valid: true},
		DeploymentID: deploymentID,
		RunningState: AttemptRunning.String(),
		ID:           at.ID,
	}); err != nil {
		return Attempt{}, false, err
	}
	for _, p := range previous {
		inputs, err := deploymentInputsIn(ctx, tx, p)
		if err != nil {
			return Attempt{}, false, err
		}
		if err := settleDeploymentReleaseIn(ctx, tx, p, dep, inputs, AttemptSuperseded, deploy.Observation{}, "", now); err != nil {
			return Attempt{}, false, err
		}
	}
	if _, err := deploymentsql.New(tx.SQL()).AdmitGeneration(ctx, deploymentsql.AdmitGenerationParams{
		DesiredGeneration: at.Generation,
		Readiness:         admitted.String(),
		UpdatedAt:         now,
		ID:                deploymentID,
	}); err != nil {
		return Attempt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Attempt{}, false, err
	}
	return at, true, nil
}

// Complete settles an observed result and its associated release in one
// transaction. It rechecks attempt ownership and observation identity. Replay
// retains stored evidence; a superseded result cannot replace current placement.
func (s *Outcomes) Complete(ctx context.Context, in Completion) (Outcome, error) {
	if err := in.Attempt.Completes(in.Observed); err != nil {
		return "", err
	}
	return s.finish(ctx, in, nil)
}

// Fail records a confirmed failure and its associated release atomically.
// cause must be nonnil. Use Uncertain when the provider outcome is unknown.
func (s *Outcomes) Fail(ctx context.Context, at Attempt, cause error) (Outcome, error) {
	if cause == nil {
		return "", errors.New("deployment failure requires a cause")
	}
	return s.finish(ctx, Completion{Attempt: at}, cause)
}

func (s *Outcomes) finish(ctx context.Context, in Completion, cause error) (Outcome, error) {
	ctx = context.WithoutCancel(ctx) // the effect is done: its settlement is written though ctx has ended
	now := s.now()
	at := in.Attempt
	if at.ID.IsZero() || at.DeploymentID.IsZero() || at.Generation == 0 {
		return "", errors.New("deployment outcome requires an admitted attempt and generation")
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (Outcome, error) {
		dep, status, storedObservation, err := lockDeploymentIn(ctx, tx, at)
		if err != nil {
			return "", err
		}
		inputs, err := deploymentInputsIn(ctx, tx, at)
		if err != nil {
			return "", err
		}
		verdict, err := at.Settle(dep, status, cause != nil)
		if err != nil {
			return "", err
		}
		switch verdict.Outcome {
		case Superseded:
			if verdict.Changed {
				if _, err := deploymentsql.New(tx.SQL()).FinishSupersededAttempt(ctx, deploymentsql.FinishSupersededAttemptParams{
					Status:     verdict.Next.String(),
					Terminal:   verdict.Next.Terminal(),
					FinishedAt: sql.NullTime{Time: now, Valid: true},
					ID:         at.ID,
				}); err != nil {
					return "", err
				}
			}
			if err := settleStoredDeploymentReleaseIn(ctx, tx, at, dep, inputs, verdict.Next, storedObservation, now); err != nil {
				return "", err
			}
			return Superseded, nil
		case AlreadyApplied:
			// Replay acknowledges the stored outcome; it never substitutes the
			// replay's URL, artifact or failure message for the original evidence.
			if err := settleStoredDeploymentReleaseIn(ctx, tx, at, dep, inputs, status, storedObservation, now); err != nil {
				return "", err
			}
			return AlreadyApplied, nil
		}
		next, readiness := verdict.Next, verdict.Readiness
		data, err := json.Marshal(in.Observed)
		if err != nil {
			return "", fmt.Errorf("encoding deployment observation: %w", err)
		}
		evidence := string(data)
		if cause != nil {
			evidence = jsonText(map[string]any{"error": cause.Error()})
			if _, err := deploymentsql.New(tx.SQL()).SetReadiness(ctx, deploymentsql.SetReadinessParams{Readiness: readiness.String(), UpdatedAt: now, ID: dep.ID}); err != nil {
				return "", err
			}
		} else {
			handle := dep.Handle
			if h, ok := in.Observed.Handle.(map[string]any); ok && h != nil {
				handle = h
			}
			container, _ := handle["container"].(string)
			hostPort := dep.HostPort
			switch p := handle["hostPort"].(type) {
			case int:
				hostPort = p
			case float64:
				hostPort = int(p)
			}
			image, err := deploymentImage(inputs, in.Observed)
			if err != nil {
				return "", err
			}
			if _, err := deploymentsql.New(tx.SQL()).ObserveGeneration(ctx, deploymentsql.ObserveGenerationParams{
				ObservedGeneration: at.Generation,
				HandleJson:         json.RawMessage(jsonText(handle)),
				Url:                in.Observed.URL,
				Container:          container,
				HostPort:           int64(hostPort),
				ReleaseRun:         inputs.ReleaseRun,
				ReleaseSha:         inputs.Revision,
				ImageDigest:        image,
				Readiness:          readiness.String(),
				UpdatedAt:          now,
				ID:                 dep.ID,
			}); err != nil {
				return "", err
			}
			if inputs.Rebuild {
				if err := audit.At(now).RecordIn(ctx, tx, "apptown-system", dep.ProjectID, "deployment.rebuilt", dep.Name, map[string]any{
					"attempt":    at.ID,
					"generation": at.Generation,
					"revision":   inputs.Revision,
					"artifact":   inputs.ArtifactHash,
					"image":      image,
					"url":        in.Observed.URL,
				}); err != nil {
					return "", err
				}
			}
		}
		if _, err := deploymentsql.New(tx.SQL()).FinishAttempt(ctx, deploymentsql.FinishAttemptParams{
			Status:       next.String(),
			Terminal:     next.Terminal(),
			ObservedJson: json.RawMessage(evidence),
			FinishedAt:   sql.NullTime{Time: now, Valid: true},
			ID:           at.ID,
		}); err != nil {
			return "", err
		}
		reason := ""
		if cause != nil {
			reason = cause.Error()
		}
		if err := settleDeploymentReleaseIn(ctx, tx, at, dep, inputs, next, in.Observed, reason, now); err != nil {
			return "", err
		}
		return Applied, nil
	})
}

func settleStoredDeploymentReleaseIn(ctx context.Context, tx *database.Tx, at Attempt, dep *Deployment, inputs Inputs, status AttemptState, evidence string, now time.Time) error {
	if inputs.ReleaseID.IsZero() {
		return nil
	}
	var o deploy.Observation
	var failure struct {
		Error string `json:"error"`
	}
	switch status {
	case AttemptDone:
		if err := json.Unmarshal([]byte(evidence), &o); err != nil {
			return fmt.Errorf("attempt %d observation: %w", at.ID, err)
		}
	case AttemptFailed:
		if err := json.Unmarshal([]byte(evidence), &failure); err != nil {
			return fmt.Errorf("attempt %d failure: %w", at.ID, err)
		}
	case AttemptSuperseded:
	default:
		return fmt.Errorf("attempt %d is still %s", at.ID, status)
	}
	return settleDeploymentReleaseIn(ctx, tx, at, dep, inputs, status, o, failure.Error, now)
}
