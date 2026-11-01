package docker

import (
	"context"

	"github.com/flokkit/apptown/internal/containerresult"
)

// Result runs s.Script in a throwaway container of s.Image on this scope's
// daemon and returns its answer, read from a file in the container once it
// has exited, never from its output (containerresult.Run).
func (dk Scope) Result(ctx context.Context, s containerresult.Spec) ([]byte, error) {
	ctx, dk, done := dk.outlived(ctx)
	defer done()
	if s.Now == nil {
		s.Now = dk.Now
	}
	return containerresult.Run(ctx, dk.resultCLI, s)
}

// ExecResult runs s.Script in the running container and returns its
// answer, read from a file it wrote there (containerresult.RunIn).
func (dk Scope) ExecResult(ctx context.Context, container string, s containerresult.Spec) ([]byte, error) {
	ctx, dk, done := dk.outlived(ctx)
	defer done()
	return containerresult.RunIn(ctx, dk.resultCLI, container, s)
}

func (dk Scope) resultCLI(ctx context.Context, env []string, args ...string) ([]byte, error) {
	return dk.Command(ctx, Command{Args: args, Env: env})
}

// outlived moves the scope's lifetime onto ctx: a read's removal of what
// it made runs past ctx (and so past the lifetime), which it could not if
// every command were still bound by the lifetime.
func (dk Scope) outlived(ctx context.Context) (context.Context, Scope, func()) {
	if dk.ownerContext == nil {
		return ctx, dk, func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(dk.ownerContext, cancel)
	if dk.ownerContext.Err() != nil {
		cancel()
	}
	dk.ownerContext = nil
	return ctx, dk, func() { stop(); cancel() }
}
