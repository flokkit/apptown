package scm

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	"github.com/flokkit/apptown/internal/gitexec"
)

// Origin returns (originURL, projectPathNS, ok) for a repo dir.
func Origin(ctx context.Context, repoDir string) (string, string, bool) {
	// The answer has no way to say "unknown", so a stop must not shape it:
	// this bounded local read runs to its end though the caller stops.
	ctx = context.WithoutCancel(ctx)
	origin, err := gitexec.Run(ctx, repoDir, "remote", "get-url", "origin")
	if err != nil || origin == "" {
		return "", "", false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return origin, "", true
	}
	p := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	return origin, p, true
}
func PublicRemoteURL(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		u.User = nil
		return u.String()
	}
	return raw
}

// readOrigin distinguishes an absent remote from an unreadable repository or
// ambiguous multiple fetch URLs. Provider decisions must not use Origin's
// display-only absence convention.
func readOrigin(ctx context.Context, repoDir string) (string, error) {
	raw, err := gitexec.HubOutput(ctx, repoDir, nil, "config", "--local", "--get-all", "remote.origin.url")
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && len(exit.Stderr) == 0 {
			return "", nil
		}
		return "", fmt.Errorf("reading the repository origin: %w", err)
	}
	values := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", fmt.Errorf("repository origin must have exactly one readable URL")
	}
	return values[0], nil
}
