package docker

import (
	"context"
	"fmt"
	"github.com/flokkit/apptown/internal/ids"
	"strings"
)

// StopStep removes every container a step is running
// (apptown-step-<id>-… for an agent, apptown-review-<id>-… for a reviewer).
func (dk Scope) StopStep(ctx context.Context, stepID ids.ID) []string {
	// A clean-up: it runs though the caller stops.
	ctx = context.WithoutCancel(ctx)
	var stopped []string
	for _, prefix := range []string{fmt.Sprintf("apptown-step-%d-", stepID), fmt.Sprintf("apptown-review-%d-", stepID)} {
		out := dk.Text(ctx, "ps", "--filter", "name="+prefix, "--format", "{{.Names}}")
		for _, name := range strings.Fields(out) {
			if strings.HasPrefix(name, prefix) {
				_, _ = dk.Run(ctx, "rm", "-f", name)
				stopped = append(stopped, name)
			}
		}
	}
	return stopped
}
