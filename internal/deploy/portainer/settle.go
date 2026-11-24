package portainer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/strs"
)

func stackHTTPError(name string, code int, raw []byte) error {
	var answer struct{ Message string }
	if code >= 400 && json.Unmarshal(raw, &answer) == nil && strings.HasPrefix(answer.Message, "failed to deploy a stack") {
		return deploy.Terminal(fmt.Errorf("Portainer could not start the stack %s: %s", name, answer.Message))
	}
	return fmt.Errorf("applying stack %s: HTTP %d %s", name, code, strs.Truncate(string(raw), 300))
}

// settleStack uses Portainer's compose result, independently of container health.
// Reads share the wait's deadline so an in-flight request cannot extend it.
func settleStack(ctx context.Context, cfg *Target, name string, stack stackApplied, timeout time.Duration) error {
	unknown := func(reason any) error {
		return fmt.Errorf("Portainer has not said whether the stack %s started: %v", name, reason)
	}
	if stack.Status == 3 {
		if stack.ID <= 0 {
			return unknown("deploying answer has no stack ID")
		}
		if timeout <= 0 {
			timeout = 90 * time.Second
		}
		bounded, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		last := unknown("still deploying at the deadline")
		id := stack.ID
		done := poll.Until(bounded, timeout, time.Second, func() bool {
			if bounded.Err() != nil {
				return false
			}
			code, raw, err := JSON(bounded, cfg, "GET", fmt.Sprintf("/api/stacks/%d", id), nil)
			if err != nil {
				last = unknown(err)
				return false
			}
			if code != 200 {
				last = unknown(fmt.Sprintf("reading status: HTTP %d", code))
				return false
			}
			var read stackApplied
			if err := json.Unmarshal(raw, &read); err != nil {
				last = unknown(err)
				return false
			}
			if read.ID != id {
				last = unknown("status answered with a different stack ID")
				return false
			}
			stack = read
			last = unknown("still deploying at the deadline")
			return stack.Status != 3
		})
		if !done {
			return last
		}
	}
	switch stack.Status {
	case 1:
		return nil
	case 4:
		message := "compose deployment failed"
		if n := len(stack.DeploymentStatus); n > 0 && stack.DeploymentStatus[n-1].Message != "" {
			message = stack.DeploymentStatus[n-1].Message
		}
		return deploy.Terminal(fmt.Errorf("Portainer could not start the stack %s: %s", name, message))
	default:
		return unknown(fmt.Sprintf("unexpected status %d", stack.Status))
	}
}
