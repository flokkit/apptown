package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"flokkit.io/forge/internal/gitexec"
)

// SelectedSource is an explicit repository revision, never an inferred HEAD.
type SelectedSource struct{ Repository, Revision string }

// SourceTree carries the directory and the resolved identity of its bytes.
// A live worktree may have uncommitted edits and no committed revision yet.
type SourceTree struct{ Directory, Revision string }

// Stage extracts selected bytes, without the repository's uncommitted changes.
// Parent is optional; publishing by rename requires the same filesystem.
// Cleanup is always callable, including on error.
func (s SelectedSource) Stage(ctx context.Context, parent string) (SourceTree, func(), error) {
	commit, err := s.Resolve(ctx)
	if err != nil {
		return SourceTree{}, func() {}, err
	}
	return stageResolved(ctx, s.Repository, commit, parent)
}

// StageCommit is Stage for a commit Resolve has already named in repo: the
// same extraction, without asking git to resolve it a second time.
func StageCommit(ctx context.Context, repo, commit, parent string) (SourceTree, func(), error) {
	return stageResolved(ctx, repo, commit, parent)
}

func stageResolved(ctx context.Context, repo, commit, parent string) (SourceTree, func(), error) {
	if err := ctx.Err(); err != nil {
		return SourceTree{}, func() {}, err
	}
	dir, err := os.MkdirTemp(parent, "forge-source-")
	if err != nil {
		return SourceTree{}, func() {}, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	tree := filepath.Join(dir, "tree")
	if err := os.Mkdir(tree, 0o755); err != nil {
		cleanup()
		return SourceTree{}, func() {}, err
	}
	if err := gitexec.ExtractCommit(ctx, repo, commit, tree); err != nil {
		cleanup()
		var ex *gitexec.ExtractError
		if errors.As(err, &ex) {
			verb := map[string]string{"archive": "archiving", "extract": "extracting"}[ex.Step]
			return SourceTree{}, func() {}, fmt.Errorf("%s selected source: %w: %s", verb, ex.Err, ex.Output)
		}
		return SourceTree{}, func() {}, err
	}
	return SourceTree{Directory: tree, Revision: commit}, cleanup, nil
}

func (s SelectedSource) Resolve(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.Revision == "" {
		return "", fmt.Errorf("deployment requires a selected revision")
	}
	commit, err := gitexec.ResolveCommit(ctx, s.Repository, s.Revision)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("resolving selected revision: %w", err)
	}
	return commit, nil
}
