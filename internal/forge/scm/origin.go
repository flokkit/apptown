package scm

import (
	"context"
	"net/url"
	"strings"

	"flokkit.io/forge/internal/gitexec"
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
