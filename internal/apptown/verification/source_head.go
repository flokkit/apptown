package verification

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/gitexec"
)

// Bound a request's Git inspection independently of long-running evaluation.
// A missing tree is an explicit observation; caller cancellation is an error.
func readSourceHead(ctx context.Context, repo, branch string) (conformance.SourceHead, error) {
	if err := ctx.Err(); err != nil {
		return conformance.SourceHead{}, err
	}
	inspect, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	sha, err := gitexec.ResolveCommit(inspect, repo, branch)
	if ctx.Err() != nil {
		return conformance.SourceHead{}, ctx.Err()
	}
	if err != nil {
		return conformance.SourceHead{Unavailable: "The project's current committed source could not be read."}, nil
	}
	if sha == "" {
		return conformance.SourceHead{Unavailable: "The project has no current committed source."}, nil
	}
	return conformance.SourceHead{SHA: sha}, nil
}
