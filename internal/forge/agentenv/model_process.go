package agentenv

import (
	"context"
	"flokkit.io/forge/internal/dockerlabel"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/execution"
)

// ModelProcess applies inference admission before running an attributed process.
type ModelProcess struct {
	runner      execution.Runner
	credentials interface {
		CheckEnvironment(context.Context, []string) error
	}
	enabled bool
	clock   func() time.Time // AppTown's clock; nil is the real one
}

// now is AppTown's clock, which stamps the process's container.
func NewModelProcess(runner execution.Runner, credentials interface {
	CheckEnvironment(context.Context, []string) error
}, enabled bool, now func() time.Time) ModelProcess {
	return ModelProcess{runner, credentials, enabled, now}
}
func (p ModelProcess) Run(ctx context.Context, backend string, req execution.Request) execution.Outcome {
	if err := RequireInferenceRuntime(p.enabled, backend); err != nil {
		return execution.Outcome{RunErr: err}
	}
	if err := p.credentials.CheckEnvironment(ctx, req.Env); err != nil {
		return execution.Outcome{RunErr: err}
	}
	req.Labels = append(req.Labels, dockerlabel.Inference+"=1")
	return Execute(p.runner, ctx, backend, req, clock.Or(p.clock)())
}
