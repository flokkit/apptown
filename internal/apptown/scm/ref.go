package scm

import (
	"context"
	"strings"

	"github.com/flokkit/apptown/internal/gitexec"
)

// hubRef: the sha a ref points at in the hub, "" when it does not exist.
func Ref(ctx context.Context, hub, ref string) string {
	// The answer has no way to say "unknown", so a stop must not shape it:
	// this bounded local read runs to its end though the caller stops.
	ctx = context.WithoutCancel(ctx)
	out, err := gitexec.Run(ctx, hub, "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Contains reports whether branch in the hub already contains commit.
func Contains(ctx context.Context, hub, branch, commit string) bool {
	// The answer has no way to say "unknown", so a stop must not shape it:
	// this bounded local read runs to its end though the caller stops.
	ctx = context.WithoutCancel(ctx)
	_, err := gitexec.Run(ctx, hub, "merge-base", "--is-ancestor", commit, "refs/heads/"+branch)
	return err == nil
}
