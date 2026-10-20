package verification

import (
	"context"
	"io"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/deploy/docker"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/agentenv"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/modelcall"
)

// CheckRun runs one check container: the check executor
// (design/operations/workers.md, "Checks on the worker"), chosen
// once at composition. Every check path runs through it, and reads its
// answer from the outcome alone.
type CheckRun func(ctx context.Context, req execution.Request) execution.Outcome

// LocalChecks runs checks on this machine's Docker: an AppTown without a
// worker. A check on a tree gets AppTown's cache mounts and labels; a run
// inside an image gets nothing of this machine's, as the app sees itself.
// now is AppTown's clock, which stamps a check container; nil is the real one.
func LocalChecks(runner execution.Runner, now func() time.Time) CheckRun {
	return func(ctx context.Context, req execution.Request) execution.Outcome {
		if req.Workspace == "" {
			return runner.Run(ctx, execution.Docker, req)
		}
		return agentenv.Execute(runner, ctx, "docker", req, clock.Or(now)())
	}
}

// CheckImage prepares an application image for checks, on the machine that
// runs them.
type CheckImage func(ctx context.Context, req execution.ImageRequest) execution.ImageAnswer

// Executor is the check executor: where checks run and their images are
// prepared, chosen once at composition.
type Executor struct {
	Run   CheckRun
	Image CheckImage
	// Local says the executor is this machine's Docker. Only then can a
	// check reuse a release image, or read the serving deployment's image,
	// which are both here; a worker builds and reports "unavailable".
	Local bool
	// AgentImage is the agent image on the executor's machine, in which a
	// committed tree is compiled with the toolchain it declares.
	AgentImage string
}

// LocalExecutor is this machine's Docker as the check executor: checks run
// through runner, and their images are prepared through AppTown's own
// Docker access (dk).
func LocalExecutor(runner execution.Runner, dk *docker.Scope, agentImage string, now func() time.Time) Executor {
	cli := func(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
		return dk.Command(ctx, docker.Command{Args: args, Stdin: stdin})
	}
	return Executor{Run: LocalChecks(runner, now), Local: true, AgentImage: agentImage,
		Image: func(ctx context.Context, req execution.ImageRequest) execution.ImageAnswer {
			return execution.PrepareImage(ctx, cli, req)
		}}
}

type checkEffects struct {
	run      CheckRun
	observer *Runtime
	models   modelcall.Completer
}

// NewEffects binds what checks do; models answers their model calls (the
// inference gateway).
func NewEffects(run CheckRun, observer *Runtime, models modelcall.Completer) Effects {
	return checkEffects{run, observer, models}
}
func (e checkEffects) Run(ctx context.Context, req execution.Request) execution.Outcome {
	return e.run(ctx, req)
}
func (e checkEffects) Ask(ctx context.Context, call modelcall.Call) (string, *modelcall.Usage, error) {
	return e.models.Ask(ctx, call)
}
func (e checkEffects) Observe(ctx context.Context, id ids.ID, source, probe string) (conformance.Outcome, string) {
	return e.observer.Observe(ctx, id, source, probe)
}
