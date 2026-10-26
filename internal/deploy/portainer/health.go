package portainer

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/poll"
	"gopkg.in/yaml.v3"
)

func artifactReadiness(compose string) (*Readiness, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(compose), &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 {
		return nil, errors.New("Portainer artifact is not one document")
	}
	node := mapValue(doc.Content[0], readinessKey)
	if node == nil {
		return nil, nil
	}
	var ready Readiness
	if err := node.Decode(&ready); err != nil {
		return nil, err
	}
	if err := ready.validate(); err != nil {
		return nil, err
	}
	return &ready, nil
}

func (d *Driver) diagnostic(ctx context.Context, cfg *Target) (Diagnostic, func() error, error) {
	if d.Allowed == nil || d.Diagnostics == nil || d.Config == nil || cfg.Scope == nil {
		return Diagnostic{}, nil, errors.New("Portainer health has no current diagnostic authority")
	}
	if err := d.Allowed(ctx); err != nil {
		return Diagnostic{}, nil, err
	}
	scope := *cfg.Scope
	diagnostic, close, err := d.Diagnostics(ctx, *cfg)
	if err != nil {
		return Diagnostic{}, nil, err
	}
	if close == nil {
		return Diagnostic{}, nil, errors.New("Portainer diagnostic transport has no cleanup owner")
	}
	if diagnostic.Command == nil {
		return Diagnostic{}, nil, errors.Join(errors.New("Portainer diagnostic transport is unavailable"), close())
	}
	diagnostic.Allowed = func(ctx context.Context) error {
		if err := d.Allowed(ctx); err != nil {
			return err
		}
		current, err := d.Config(ctx, d.TargetID)
		if err != nil {
			return err
		}
		if current.Scope == nil || *current.Scope != scope {
			return errors.New("Portainer target scope changed before diagnostic effect")
		}
		return nil
	}
	return diagnostic, close, nil
}

// Sample observes one retained generation using an explicitly bound diagnostic
// authority. Read-only Observe never calls this operation.
func (d *Driver) Sample(ctx context.Context, p deploy.Placement) (out deploy.Observation, err error) {
	expected, err := savedRelease(p.Handle)
	if err != nil {
		return out, err
	}
	if expected == nil {
		return out, errors.New("Portainer generation has no retained readiness expectation")
	}
	if d.ExpectedArtifact == "" || d.ExpectedArtifact != expected.ArtifactHash {
		return out, errors.New("Portainer sample artifact differs from admitted inputs")
	}

	cfg, err := d.Config(ctx, d.TargetID)
	if err != nil {
		return out, err
	}
	diagnostic, close, err := d.diagnostic(ctx, cfg)
	if err != nil {
		return out, err
	}
	defer func() {
		if cleanupErr := close(); cleanupErr != nil {
			invalidateHealth(&out)
			err = errors.Join(err, fmt.Errorf("closing Portainer diagnostic transport: %w", cleanupErr))
		}
	}()
	return d.sample(ctx, cfg, p, expected, diagnostic)
}

func (d *Driver) sample(ctx context.Context, cfg *Target, p deploy.Placement, expected *expectedRelease, diagnostic Diagnostic) (deploy.Observation, error) {
	var out deploy.Observation
	if expected.Readiness.Target != d.TargetID || d.ExpectedArtifact == "" || expected.ArtifactHash != d.ExpectedArtifact {
		return out, errors.New("Portainer readiness target or artifact differs from admitted owner inputs")
	}
	if err := diagnostic.Allowed(ctx); err != nil {
		return out, err
	}
	stacks, err := ListStacks(ctx, cfg)
	if err != nil {
		return out, err
	}
	found, err := associatedStack(ctx, cfg, p, stacks)
	if err != nil {
		return out, err
	}
	if !found {
		return out, errors.New("Portainer readiness stack is absent")
	}
	stack, _ := p.Handle["stack"].(string)
	before, id, err := inspectRelease(ctx, cfg, p, stack, expected)
	if err != nil {
		return before, err
	}
	if err := diagnostic.sample(ctx, id, expected.Readiness.Public); err != nil {
		return before, err
	}
	after, again, err := inspectRelease(ctx, cfg, p, stack, expected)
	if err != nil {
		return after, err
	}
	if again != id {
		return after, errors.New("Portainer public container changed during readiness sampling")
	}
	if err := diagnostic.Allowed(ctx); err != nil {
		return after, err
	}
	after.Health = "healthy"
	after.URL = expected.Readiness.URL
	after.HealthObservedAt = clock.Or(d.Now)()
	after.ArtifactHash = expected.ArtifactHash
	handle := maps.Clone(p.Handle)
	handle["readinessObservedAt"] = after.HealthObservedAt
	after.Handle = handle
	return after, nil
}

func (d *Driver) awaitReadiness(ctx context.Context, cfg *Target, p deploy.Placement, expected *expectedRelease, diagnostic Diagnostic) (deploy.Observation, error) {
	wait := d.ReadinessTimeout
	if wait <= 0 {
		wait = 90 * time.Second
	}
	if grace := time.Duration(expected.Readiness.Public.GraceNanos); grace > wait {
		wait = grace
	}
	bounded, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	var out deploy.Observation
	var last error
	done := poll.Until(bounded, wait, time.Second, func() bool {
		if bounded.Err() != nil {
			return false
		}
		observed, sampleErr := d.sample(bounded, cfg, p, expected, diagnostic)
		out = observed
		if !(errors.Is(sampleErr, context.DeadlineExceeded) && last != nil) {
			last = sampleErr
		}
		if last == nil {
			return true
		}
		var predicate *predicateFailure
		return errors.As(last, &predicate) && predicate.final
	})
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if done && last == nil {
		return out, nil
	}
	var predicate *predicateFailure
	if errors.As(last, &predicate) {
		return out, deploy.Terminal(fmt.Errorf("Portainer stack was applied but readiness failed: %w", last))
	}
	if last == nil {
		last = errors.New("no exact release readiness sample")
	}
	return out, fmt.Errorf("Portainer stack readiness remains unknown: %w", last)
}

func invalidateHealth(out *deploy.Observation) {
	out.Health = "unknown"
	out.HealthObservedAt = time.Time{}
	out.ArtifactHash = ""
	if h, ok := out.Handle.(map[string]any); ok {
		copy := maps.Clone(h)
		delete(copy, "readinessObservedAt")
		out.Handle = copy
	}
}
