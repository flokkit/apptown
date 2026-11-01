package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/clock"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// explicitColdReadiness witnesses each declared success against the same
// provider deployment; native rollout success is not a substitute for a sample.
func (d *Driver) explicitColdReadiness(ctx context.Context, c *Client, appID, deployment, front string, q readinessSnapshot, release string) error {
	policy := q.Policy
	if policy == nil {
		return errors.New("explicit readiness has no policy")
	}
	budget := max(90*time.Second, policy.StartupGrace)
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	passes := 0
	for ctx.Err() == nil {
		if deadline, ok := ctx.Deadline(); ok && deadline.Sub(clock.Or(nil)()) < policy.Timeout {
			break
		}
		start := clock.Or(nil)()
		sample, cancelSample := context.WithTimeout(ctx, policy.Timeout+5*time.Second)
		current := func() error {
			var got struct {
				App doApp `json:"app"`
			}
			if err := c.Call(sample, http.MethodGet, "/v2/apps/"+appID, nil, &got); err != nil {
				return err
			}
			a := got.App
			if a.ID != appID || a.ActiveDeployment == nil || a.ActiveDeployment.ID != deployment || a.ActiveDeployment.Phase != "ACTIVE" || (a.PendingDeployment != nil && a.PendingDeployment.ID != deployment) || (a.InProgressDeployment != nil && a.InProgressDeployment.ID != deployment) {
				return errors.New("App Platform changed deployment during cold readiness")
			}
			url := a.LiveURL
			if url == "" {
				url = a.DefaultIngress
			}
			if url != front {
				return errors.New("App Platform changed front door during cold readiness")
			}
			return nil
		}
		err := current()
		healthy := false
		if err == nil {
			probe, stopProbe := context.WithTimeout(sample, policy.Timeout)
			healthy, err = d.sampleFrontDoor(probe, strings.TrimRight(front, "/")+q.Path, q.Expect, "", func(context.Context) error { return nil })
			if err == nil && healthy && release != "" {
				healthy, err = d.sampleFrontDoor(probe, strings.TrimRight(front, "/")+appcontract.ReleasePath, 0, release, func(context.Context) error { return nil })
			}
			if probe.Err() != nil {
				healthy = false
			}
			stopProbe()
			if identityErr := current(); identityErr != nil {
				cancelSample()
				return identityErr
			}
		}
		if sample.Err() != nil || ctx.Err() != nil {
			healthy = false
		}
		cancelSample()
		if healthy && err == nil {
			passes++
		} else {
			passes = 0
		}
		if passes >= policy.SuccessThreshold {
			return nil
		}
		if err := waitDeploymentPoll(ctx, max(0, policy.Period-clock.Or(nil)().Sub(start))); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("App Platform readiness did not reach its declared success threshold within the boot budget")
}
