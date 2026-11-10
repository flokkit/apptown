package deployment

import (
	"context"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/deploy"
)

// Inputs are immutable receipt inputs, never resolved credentials or rendered
// secret data. admission persists them before deployment execution begins.
type Inputs struct {
	Address    *AddressSnapshot
	ReleaseID  ids.ID
	ReleaseRun ids.ID
	Backup     ids.ID
	// BundleID pins the release bundle a generation runs, ahead of its
	// release's or its landing run's: a recovery clears ReleaseID, which
	// settles one release once, and names its bundle here; a landing whose
	// bundle is committed before its deploy names it here, so a later
	// attempt of the same run committing another bundle does not change
	// what this generation ran. 0 for other attempts, whose bundle is their
	// release's or their landing run's newest.
	BundleID                            ids.ID
	Revision, ImageDigest, ArtifactHash string
	DockerSource                        string // Selected configuration, without resolved secrets.
	Rebuild                             bool
	// Applied is what the generation applied of its deployment's profile
	// and overrides (profiles.go); a rebuild reapplies it.
	Applied AppliedProfile
}

// same says whether two inputs name the same work: every field equal, and
// the same applied profile.
func (in Inputs) same(other Inputs) bool {
	if (in.Address == nil) != (other.Address == nil) || (in.Address != nil && !in.Address.same(*other.Address)) {
		return false
	}
	if in.Backup != other.Backup || in.ReleaseID != other.ReleaseID || in.ReleaseRun != other.ReleaseRun || in.BundleID != other.BundleID ||
		in.Revision != other.Revision || in.ImageDigest != other.ImageDigest || in.ArtifactHash != other.ArtifactHash ||
		in.DockerSource != other.DockerSource || in.Rebuild != other.Rebuild {
		return false
	}
	x, errX := in.Applied.json()
	y, errY := other.Applied.json()
	return errX == nil && errY == nil && string(x) == string(y)
}

type Start struct {
	DeploymentID   ids.ID
	TargetID       ids.ID // admission rejects a changed target binding.
	Key            string
	Inputs         Inputs
	BaseGeneration *int64 // Requests and rebuilds must still replace the generation they selected.
}

// executionStore retains transaction authority. Admit must bind the key to
// immutable inputs and recheck target/base generation atomically; a replay
// returns created=false and never authorizes another execution.
type executionStore interface {
	settlementStore
	Admit(context.Context, Start) (Attempt, bool, error)
	Checkpoint(context.Context, Attempt) deploy.Checkpoint
}

type execute func(context.Context, Attempt, deploy.Checkpoint) (deploy.Observation, error)

type ExecutionResult struct {
	Attempt  Attempt
	Created  bool
	Observed deploy.Observation
	Outcome  deploymentOutcome
}

type stage string

const (
	admission  stage = "admission"
	Execution  stage = "execution"
	settlement stage = "settlement"
)

// runError keeps execution failure separate from failure to persist its outcome.
// When persistence fails, retry classification follows that error: even a
// confirmed provider rejection needs a durable receipt before retries can stop.
type runError struct {
	Stage       stage
	Cause       error
	Persistence error
}

func (e *runError) Error() string {
	if e.Persistence != nil {
		return fmt.Sprintf("deployment %s: %v; recording outcome: %v", e.Stage, e.Cause, e.Persistence)
	}
	return fmt.Sprintf("deployment %s: %v", e.Stage, e.Cause)
}
func (e *runError) Unwrap() error {
	if e.Persistence != nil {
		return e.Persistence
	}
	return e.Cause
}

// Runner owns admission/replay and settlement. The caller prepares execution
// inputs, owns context lifetime and handles workflow-specific evidence. No new
// deadline is imposed on builds or provider rollouts.
type Runner struct {
	Store executionStore
	// Acted hears of every effect that ran, once it is settled, whatever its
	// outcome: a failed or uncertain deploy may have changed the target too.
	Acted     func(ids.ID)
	BeforeAct func(context.Context, ids.ID) (done func(), err error)
}

// Run admits the attempt, executes only a newly admitted identity, and records
// its outcome. A replay returns Created=false without calling execute or loading
// its previous outcome. After admission, cancellation observed before execute
// records failure; cancellation observed on its return records uncertainty for
// later observation. runError keeps execution and persistence failures separate.
// The caller supplies the deadline.
func (r Runner) Run(ctx context.Context, in Start, execute execute) (ExecutionResult, error) {
	var result ExecutionResult
	if err := ctx.Err(); err != nil {
		return result, &runError{Stage: admission, Cause: err}
	}
	at, created, err := r.Store.Admit(ctx, in)
	result.Attempt, result.Created = at, created
	if err != nil {
		return result, &runError{Stage: admission, Cause: err}
	}
	if !created {
		return result, nil
	}

	// A cancelled executor returning success must never publish that success.
	// Once execution has begun, recovery owns observation of its effects.
	uncertain := func(stage stage, cause error) (ExecutionResult, error) {
		out, recordErr := r.Store.Uncertain(ctx, at, cause)
		if recordErr == nil {
			result.Outcome = out
		}
		return result, &runError{Stage: stage, Cause: cause, Persistence: recordErr}
	}
	failure := func(cause error, confirmed bool) (ExecutionResult, error) {
		if !confirmed {
			return uncertain(Execution, cause)
		}
		out, recordErr := r.Store.Fail(ctx, at, cause)
		result.Outcome = out
		if recordErr != nil {
			var recoveryErr error
			result.Outcome, recoveryErr = r.Store.Uncertain(ctx, at, recordErr)
			if recoveryErr != nil {
				result.Outcome = ""
			}
			recordErr = errors.Join(recordErr, recoveryErr)
		}
		return result, &runError{Stage: Execution, Cause: cause, Persistence: recordErr}
	}
	if err := ctx.Err(); err != nil {
		// admission committed, but this executor has not run: no provider effect
		// needs guessing or observation. Settle the cancelled attempt as failed.
		return failure(err, true)
	}
	if r.BeforeAct != nil {
		done, err := r.BeforeAct(ctx, in.TargetID)
		if err != nil {
			return failure(err, true)
		}
		defer done()
	}
	if r.Acted != nil {
		defer r.Acted(in.TargetID)
	}
	o, err := execute(ctx, at, r.Store.Checkpoint(ctx, at))
	result.Observed = o
	if cancelled := ctx.Err(); cancelled != nil {
		return uncertain(Execution, cancelled)
	}
	if err != nil {
		return failure(err, deploy.IsTerminal(err))
	}

	out, err := r.Store.Complete(ctx, Completion{Attempt: at, Observed: o})
	if err != nil {
		return uncertain(settlement, err)
	}
	result.Outcome = out
	return result, nil
}
