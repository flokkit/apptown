package deployment

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/fault"
)

// Deploying (deployment.md §4.2; architecture.md §5.6): ship the selected
// software to a standing deployment through its target's driver. The
// request carries everything the deploy needs, resolved by the caller;
// admission records its identity immutably with the attempt, so recovery
// after a restart reads the record and resolves nothing twice. The
// release row and its log are the receipt, written as the work happens.

// Application is the thing deployed. The record keeps a project id for
// now; the request does not require one beyond it.
type Application struct {
	ID   ids.ID
	Slug string
}

// preparedRelease is one deploy, ready to admit.
type preparedRelease struct {
	Bundle *ReleaseBundle // the bundle the source was cut from, for the record; nil for a staged tree
	// Source.Contract is the effective contract: Committed under the
	// deployment's profile and overrides, as Applied records it.
	Source    deploy.Source
	Committed *appcontract.Contract
	Applied   AppliedProfile
	// Artifact is set when the caller prepared the target's artifact itself
	// (the built-in Docker host takes prepared selected source); nil means
	// the driver renders one from deploy.Source.
	Artifact *deploy.Artifact
	// Secrets is the app's secrets, resolved per consumer for this
	// deployment; nil when it declares none. Values reach the driver, never
	// the record. SecretNames is what the record says was delivered.
	Secrets        deploy.ConsumerSecrets
	SecretNames    []string
	Address        *AddressSnapshot
	Actor          string
	BaseGeneration *int64
	// Allowed rechecks the queued requester before each consequential phase.
	Allowed func(context.Context) error
}

var (
	ErrNoTarget        = fault.New(fault.Conflict, "no target")
	ErrRefusedContract = fault.New(fault.Unprocessable, "the target refuses the contract")
)

// execute is the work of an admitted release: fulfil the contract's needs
// on the target, render the artifact, reconcile the deployment through
// the driver as the next generation, record the outcome. Every step is a
// line in the release's log; a failure before admission of the attempt
// fails the release, a failure after it is the attempt's outcome.
func (s *Service) execute(ctx context.Context, rel NumberedRelease, dep *Deployment, t *Target, req preparedRelease) (ExecutionResult, error) {
	logf := func(format string, args ...any) { s.log(rel.ID, format, args...) }
	fail := func(format string, args ...any) error {
		cause := fmt.Errorf(format, args...)
		if _, err := s.Outcomes.FailUnstartedRelease(ctx, rel.ID, cause); err != nil {
			logf("could not record the preparation failure: %v (preparation: %v)", err, cause)
		}
		return cause
	}
	allowed := req.Allowed
	if allowed == nil {
		allowed = func(context.Context) error { return nil }
	}
	if err := allowed(ctx); err != nil {
		return ExecutionResult{}, fail("%v", err)
	}
	if err := s.checkAddressSnapshot(ctx, dep, t, req.Address); err != nil {
		return ExecutionResult{}, fail("%v", err)
	}
	d, err := s.providers.Driver(ctx, t, dep)
	if err != nil {
		return ExecutionResult{}, fail("%v", err)
	}
	if host, ok := d.(*docker.Driver); ok {
		host.Allowed = req.Allowed
	}
	if remote, ok := d.(*portainerRelease); ok {
		remote.Allowed = req.Allowed
	}

	// A target that can tell from its own limits that this release cannot
	// go through says so before anything is made on it.
	if pf, ok := d.(deploy.Preflighter); ok && req.Artifact == nil {
		if err := pf.Preflight(ctx, dep.Placement(), req.Source, req.Secrets, logf); err != nil {
			return ExecutionResult{}, fail("%v", err)
		}
	}
	// req.Source.Contract is the effective one, fitted before any effect
	// (ExecuteRelease); committed is what the change committed.
	committed, applied := req.Committed, req.Applied
	logf("%s", applied.Words())
	if len(req.SecretNames) > 0 {
		logf("delivering the app's secrets (%s) to the target", strings.Join(req.SecretNames, ", "))
	}
	if err := allowed(ctx); err != nil {
		return ExecutionResult{}, fail("%v", err)
	}
	bindings, err := s.providers.PrepareNeeds(ctx, d, t, dep, req.Source.Contract, logf)
	if err != nil {
		return ExecutionResult{}, fail("%v", err)
	}
	// A deployment that starts from a copy has its data filled and checked
	// before its app first starts.
	if err := s.startFromCopy(ctx, rel, dep, t, req.Actor, logf, req.Allowed); err != nil {
		return ExecutionResult{}, fail("starting from the copy: %v", err)
	}
	if err := allowed(ctx); err != nil {
		return ExecutionResult{}, fail("%v", err)
	}
	var art deploy.Artifact
	if req.Artifact != nil {
		art = *req.Artifact
	} else {
		r, ok := d.(deploy.Renderer)
		if !ok {
			return ExecutionResult{}, fail("%s renders no artifact of its own; prepare one for it", t.Kind)
		}
		if art, err = r.Render(ctx, dep.Placement(), req.Source, bindings, req.Secrets, logf); err != nil {
			return ExecutionResult{}, fail("%v", err)
		}
	}
	src := req.Source
	art.Source = &src
	logf("rendered the %s artifact (%s)", art.Kind, art.Hash)
	key := fmt.Sprintf("release-%d", rel.ID)
	inputs := Inputs{ReleaseID: rel.ID, Revision: req.Source.Revision, ArtifactHash: art.Hash, Applied: applied}
	if req.Address != nil {
		address := req.Address.clone()
		inputs.Address = &address
	}
	if req.Bundle != nil && req.Source.Contract != nil {
		inputs.ImageDigest = req.Bundle.Parts[req.Source.Contract.PublicPart().Name]
	}
	if art.Kind == "docker-source" {
		inputs.ImageDigest, inputs.DockerSource = "", string(art.Body)
	}
	result, err := (Runner{Store: s.Outcomes, Applied: s.applied}).Run(ctx,
		Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: key, Inputs: inputs, BaseGeneration: req.BaseGeneration},
		func(ctx context.Context, at Attempt, checkpoint deploy.Checkpoint) (deploy.Observation, error) {
			if err := allowed(ctx); err != nil {
				return deploy.Observation{}, deploy.Terminal(err)
			}
			logf("deploying as generation %d of %s", at.Generation, dep.Name)
			start := s.now()
			o, err := d.Reconcile(ctx, dep.Placement(), at.Generation, art, key, logf, checkpoint)
			if err == nil {
				logf("the target reports the deployment %s after %s", o.Rollout, s.now().Sub(start).Round(time.Second))
			}
			return o, err
		})
	if err != nil {
		var failure *RunError
		if errors.As(err, &failure) && failure.Stage == Admission {
			_ = fail("minting the deployment generation: %v", failure.Cause)
		} else {
			logf("deployment outcome %s: %v", result.Outcome, err)
		}
		return result, err
	}
	if !result.Created {
		logf("deployment attempt %d was already admitted; observation owns any unfinished outcome", result.Attempt.ID)
		return result, nil
	}
	switch result.Outcome {
	case Superseded:
		logf("deployment generation %d was superseded by a newer attempt", result.Attempt.Generation)
	case AlreadyApplied:
		logf("deployment generation %d was already recorded", result.Attempt.Generation)
	case Applied:
		logf("live — %s serves release #%d", result.Observed.URL, rel.Number)
		s.tagRelease(ctx, rel.Number, req.Bundle, logf)
		s.clearCommittedOverrides(ctx, dep, committed, applied, req.Source.Revision, logf)
	}
	return result, nil
}

// log appends a timestamped line to a release's record: what happened,
// as it happens, so a watching page or a failure shows how far it got.
func (s *Service) log(releaseID ids.ID, format string, args ...any) {
	if err := AppendReleaseEvent(s.records, s.db, releaseID, fmt.Sprintf(format, args...), s.now()); err != nil {
		log.Printf("release %d: writing its log line: %v", releaseID, err)
	}
}
