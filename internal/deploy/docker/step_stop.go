package docker

import (
	"context"
	"flokkit.io/forge/internal/ids"
	"fmt"
	"strings"
)

// StopStep removes every container a step is running
// (forge-step-<id>-… for an agent, forge-review-<id>-… for a reviewer).
func (dk Scope) StopStep(ctx context.Context, stepID ids.ID) []string {
	// A clean-up: it runs though the caller stops.
	ctx = context.WithoutCancel(ctx)
	var stopped []string
	for _, prefix := range []string{fmt.Sprintf("forge-step-%d-", stepID), fmt.Sprintf("forge-review-%d-", stepID)} {
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
