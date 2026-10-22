package scm

import (
	"context"
	"strings"

	"github.com/flokkit/apptown/internal/gitexec"
)

// PredatesRename says whether a commit keeps AppTown's files in .forge/
// and not in .apptown/: work made before the 20 Oct 2026 rename, which
// AppTown does not convert on the fly (apptown upgrade renamed each
// project's branches once).
func PredatesRename(ctx context.Context, repo, commit string) (bool, error) {
	listing, err := gitexec.HubRun(ctx, repo, "ls-tree", "--name-only", commit, "--", ".forge", ".apptown")
	if err != nil {
		return false, err
	}
	old, renamed := false, false
	for _, name := range strings.Split(listing, "\n") {
		old = old || strings.TrimSpace(name) == ".forge"
		renamed = renamed || strings.TrimSpace(name) == ".apptown"
	}
	return old && !renamed, nil
}
