package docker

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

// explicitLiveness keeps declared policy counters on one observed process. An
// unreadable contract cannot turn a previously explicit policy into defaults.
func (c *HealthController) explicitLiveness(ctx context.Context, e Watched, dk Scope, ph *partHealth, pub *appcontract.Part) bool {
	if pub == nil && ph.policy.Explicit {
		ph.fails, ph.passes = 0, 0
		return true
	}
	policy, err := livenessPolicy(pub)
	if err != nil {
		ph.fails, ph.passes = 0, 0
		return true
	}
	if !policy.Explicit {
		return false
	}
	if !ph.lastProbe.IsZero() && policy == ph.policy && c.now().Sub(ph.lastProbe) < policy.Period {
		return true
	}
	previousPolicy := ph.policy
	ph.policy = policy // remember explicit intent even when runtime inspection fails
	ph.lastProbe = c.now()
	rt, err := Inspect(ctx, dk, e.Container)
	if err != nil || rt.ID == "" || rt.State == nil || rt.State.StartedAt.IsZero() || !rt.Running || rt.Labels[dockerlabel.Deployment] != string(e.Identity.Deployment) || rt.Labels[dockerlabel.Generation] != strconv.FormatInt(e.Identity.Generation, 10) {
		ph.fails, ph.passes = 0, 0
		return true
	}
	token := rt.ID + "/" + rt.State.StartedAt.UTC().Format(time.RFC3339Nano)
	if token != ph.runtimeToken || policy != previousPolicy {
		ph.fails, ph.passes = 0, 0
		if !strings.HasPrefix(ph.runtimeToken, rt.ID+"/") || policy != previousPolicy {
			ph.restarts = 0
			ph.lastRestart = time.Time{}
		}
		ph.runtimeToken = token
	}
	grace := policy.StartupGrace
	if grace < healthGraceFloor {
		grace = healthGraceFloor
	}
	if c.now().Sub(rt.State.StartedAt) < grace {
		return true
	}
	probe := renderLivenessProbe(pub)
	ok := probeLiveness(ctx, dk, rt.ID, e.HostPort, probe, c.deps.FrontDoor())
	after, err := Inspect(ctx, dk, e.Container)
	if ctx.Err() != nil || err != nil || after.ID != rt.ID || after.State == nil || !after.State.StartedAt.Equal(rt.State.StartedAt) || !after.Running {
		ph.fails, ph.passes = 0, 0
		return true
	}
	if ok {
		ph.fails = 0
		ph.passes++
		if ph.passes >= policy.SuccessThreshold {
			ph.restarts, ph.passes = 0, 0
		}
		return true
	}
	ph.passes = 0
	ph.fails++
	if ph.fails < policy.FailureThreshold {
		return true
	}
	backoff := healthBackoffBase << ph.restarts
	if backoff <= 0 || backoff > healthBackoffCap {
		backoff = healthBackoffCap
	}
	if !ph.lastRestart.IsZero() && c.now().Sub(ph.lastRestart) < backoff {
		return true
	}
	// Use the witnessed immutable container ID, never a newly reused name.
	e.Container = rt.ID
	c.restart(ctx, e, dk, ph)
	ph.fails = 0
	ph.restarts++
	ph.lastRestart = c.now()
	return true
}
