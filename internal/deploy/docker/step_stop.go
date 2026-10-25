package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/ids"
)

// StopStep removes every container a step is running
// (apptown-step-<id>-… for an agent, apptown-review-<id>-… for a reviewer).
// On failure it returns only the removals already confirmed, plus the error.
func (dk Scope) StopStep(ctx context.Context, stepID ids.ID) ([]string, error) {
	// A clean-up: it runs though the caller stops.
	ctx = context.WithoutCancel(ctx)
	var stopped []string
	for _, prefix := range []string{fmt.Sprintf("apptown-step-%d-", stepID), fmt.Sprintf("apptown-review-%d-", stepID)} {
		out, err := dk.Run(ctx, "ps", "--filter", "name="+prefix, "--format", "{{.Names}}")
		if err != nil {
			return stopped, fmt.Errorf("listing step containers %s: %w", prefix, err)
		}
		for _, name := range strings.Fields(string(out)) {
			if strings.HasPrefix(name, prefix) {
				if _, err := dk.Run(ctx, "rm", "-f", "-v", name); err != nil {
					return stopped, fmt.Errorf("removing step container %s: %w", name, err)
				}
				stopped = append(stopped, name)
			}
		}
	}
	return stopped, nil
}
