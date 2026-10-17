package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PreviewSource keeps the current worktree when it exists, including its edits.
// Otherwise History is materialized under Snapshots, in a folder of the
// commit's own: preparing another commit never changes a tree a running
// preview serves (Retain removes the others once none does). Neither source
// reads database contract snapshots or borrows files from the repository's
// current checkout.
type PreviewSource struct {
	Worktree  string
	History   SelectedSource
	Snapshots string
}

func (s PreviewSource) Prepare(ctx context.Context) (SourceTree, error) {
	if err := ctx.Err(); err != nil {
		return SourceTree{}, err
	}
	if s.Worktree != "" {
		info, err := os.Stat(s.Worktree)
		if err == nil {
			if !info.IsDir() {
				return SourceTree{}, fmt.Errorf("preview worktree is not a directory")
			}
			out := SourceTree{Directory: s.Worktree}
			// A new worktree may not be a Git checkout yet. Its edits remain the source.
			if _, err := os.Stat(filepath.Join(s.Worktree, ".git")); err == nil {
				out.Revision, _ = (SelectedSource{Repository: s.Worktree, Revision: "HEAD"}).Resolve(ctx)
			}
			if err := ctx.Err(); err != nil {
				return SourceTree{}, err
			}
			return out, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return SourceTree{}, err
		}
	}
	if s.History.Revision == "" {
		return SourceTree{}, fmt.Errorf("this change has no selected historical source to show")
	}
	if s.Snapshots == "" {
		return SourceTree{}, errors.New("a historical source needs a folder for its snapshots")
	}
	commit, err := s.History.Resolve(ctx)
	if err != nil {
		return SourceTree{}, err
	}
	return s.History.Snapshot(ctx, filepath.Join(s.Snapshots, commit))
}

// Retain removes every snapshot but tree's, once tree is what the preview
// serves and no container mounts the others.
func (s PreviewSource) Retain(tree SourceTree) error {
	if s.Snapshots == "" {
		return nil
	}
	entries, err := os.ReadDir(s.Snapshots)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var failed []error
	for _, e := range entries {
		path := filepath.Join(s.Snapshots, e.Name())
		if path != filepath.Clean(tree.Directory) {
			failed = append(failed, os.RemoveAll(path))
		}
	}
	return errors.Join(failed...)
}

// Snapshot publishes a selected tree at a caller-owned cache path. The caller
// serializes use of that path (preview boot ownership does so). Resolve before
// consulting the marker: invalid or absent selections cannot revive old bytes.
func (s SelectedSource) Snapshot(ctx context.Context, destination string) (SourceTree, error) {
	if destination == "" || filepath.Base(filepath.Clean(destination)) == "." || filepath.Dir(filepath.Clean(destination)) == filepath.Clean(destination) {
		return SourceTree{}, errors.New("snapshot requires a destination directory")
	}
	commit, err := s.Resolve(ctx)
	if err != nil {
		return SourceTree{}, err
	}
	forgeDir := filepath.Join(destination, ".forge")
	marker := filepath.Join(forgeDir, "snapshot-sha")
	// A marker copied from an untrusted tree must never redirect a cache read.
	if root, err := os.Lstat(destination); err == nil && root.IsDir() {
		if dir, err := os.Lstat(forgeDir); err == nil && dir.IsDir() {
			if file, err := os.Lstat(marker); err == nil && file.Mode().IsRegular() {
				if b, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(b)) == commit {
					if err := ctx.Err(); err != nil {
						return SourceTree{}, err
					}
					return SourceTree{Directory: destination, Revision: commit}, nil
				}
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return SourceTree{}, err
	}
	tree, cleanup, err := stageResolved(ctx, s.Repository, commit, filepath.Dir(destination))
	if err != nil {
		return SourceTree{}, err
	}
	defer cleanup()
	stagedForge := filepath.Join(tree.Directory, ".forge")
	if info, err := os.Lstat(stagedForge); err == nil && !info.IsDir() {
		return SourceTree{}, errors.New("selected source .forge must be a directory")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return SourceTree{}, err
	}
	if err := os.MkdirAll(stagedForge, 0o755); err != nil {
		return SourceTree{}, err
	}
	stagedMarker := filepath.Join(stagedForge, "snapshot-sha")
	if err := os.Remove(stagedMarker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return SourceTree{}, err
	}
	if err := os.WriteFile(stagedMarker, []byte(commit+"\n"), 0o644); err != nil {
		return SourceTree{}, err
	}
	if err := ctx.Err(); err != nil {
		return SourceTree{}, err
	}
	if err := os.RemoveAll(destination); err != nil {
		return SourceTree{}, err
	}
	if err := os.Rename(tree.Directory, destination); err != nil {
		return SourceTree{}, err
	}
	return SourceTree{Directory: destination, Revision: commit}, nil
}
