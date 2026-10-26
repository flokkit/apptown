package docker

import (
	"context"
	"github.com/flokkit/apptown/internal/clock"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/poll"
)

// coldPolicyReady collects a fresh streak within the boot budget. A runtime
// replacement, restart, unavailable observation or failed probe resets it.
func coldPolicyReady(ctx context.Context, policy *appcontract.HealthPolicy, budget time.Duration, sample func(context.Context) (string, bool)) bool {
	if policy == nil || !policy.Explicit || policy.CheckBootFit() != nil {
		return false
	}
	budget = max(budget, 90*time.Second, policy.StartupGrace)
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	passes := 0
	token := ""
	for ctx.Err() == nil {
		started := clock.Or(nil)()
		if deadline, ok := ctx.Deadline(); ok && deadline.Sub(started) < policy.Timeout {
			return false
		}
		one, stop := context.WithTimeout(ctx, policy.Timeout+5*time.Second)
		current, ok := sample(one)
		expired := one.Err() != nil
		stop()
		if ctx.Err() != nil {
			return false
		}
		if ok && !expired && current != "" {
			if current != token {
				passes = 0
				token = current
			}
			passes++
			if passes >= policy.SuccessThreshold {
				return true
			}
		} else {
			passes = 0
			token = ""
		}
		if poll.Wait(ctx, max(0, policy.Period-clock.Or(nil)().Sub(started))) != nil {
			return false
		}
	}
	return false
}

func coldRuntime(ctx context.Context, dk Scope, container string) string {
	r, err := Inspect(ctx, dk, container)
	if err != nil || !r.Running || r.ID == "" || r.State == nil || r.State.StartedAt.IsZero() {
		return ""
	}
	return r.ID + "/" + r.State.StartedAt.UTC().Format(time.RFC3339Nano)
}

func coldSample(ctx context.Context, dk Scope, container string, probe func(context.Context) bool) (string, bool) {
	before := coldRuntime(ctx, dk, container)
	if before == "" || !probe(ctx) {
		return "", false
	}
	after := coldRuntime(ctx, dk, container)
	return after, ctx.Err() == nil && before == after
}
