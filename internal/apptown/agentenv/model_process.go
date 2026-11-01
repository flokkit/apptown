package agentenv

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/dockerlabel"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/execution"
)

// modelProcess applies inference admission before running an attributed process.
type modelProcess struct {
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
}, enabled bool, now func() time.Time) modelProcess {
	return modelProcess{runner, credentials, enabled, now}
}
func (p modelProcess) Run(ctx context.Context, backend string, req execution.Request) execution.Outcome {
	if err := RequireInferenceRuntime(p.enabled, backend); err != nil {
		return execution.Outcome{RunErr: err}
	}
	if err := p.credentials.CheckEnvironment(ctx, req.Env); err != nil {
		return execution.Outcome{RunErr: err}
	}
	req.Labels = append(req.Labels, dockerlabel.Inference+"=1")
	return Execute(p.runner, ctx, backend, req, clock.Or(p.clock)())
}
