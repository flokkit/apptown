package scm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/strs"
)

// StageTreeAt materializes a committed revision into a temporary directory.
func StageTreeAt(ctx context.Context, repo, sha string) (string, func(), error) {
	scratch, err := os.MkdirTemp("", "apptown-fleet-src-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(scratch) }
	staged := filepath.Join(scratch, "tree")
	if err := os.Mkdir(staged, 0o755); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := gitexec.ExtractCommit(ctx, repo, sha, staged); err != nil {
		cleanup()
		var ex *gitexec.ExtractError
		if errors.As(err, &ex) {
			return "", func() {}, fmt.Errorf("%s %s: %w: %s", ex.Step, strs.ShortSHA(sha), ex.Err, strs.Truncate(ex.Output, 200))
		}
		return "", func() {}, err
	}
	return staged, cleanup, nil
}
