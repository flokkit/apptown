package scm

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/gitexec/gittest"
)

func TestRepositoryDriftPreservesPathsAndReadFailures(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	gittest.RunAsTester(t, repo, "init", "-q")
	gittest.RunAsTester(t, repo, "commit", "--allow-empty", "-qm", "initial")
	paths := []string{"a", " spaced ", "line\nbreak", "quote\"file"}
	for _, path := range paths {
		if err := os.WriteFile(filepath.Join(repo, path), []byte("edit"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Drift(context.Background(), execution.Runner{}, repo)
	slices.Sort(paths)
	slices.Sort(got)
	if err != nil || !slices.Equal(got, paths) {
		t.Fatalf("drift paths: %q want %q: %v", got, paths, err)
	}
	if got, err := Drift(context.Background(), execution.Runner{}, t.TempDir()); err == nil || len(got) != 0 {
		t.Fatalf("unreadable repository looked clean: %q %v", got, err)
	}
	gittest.RunAsTester(t, repo, "add", "--", "a")
	gittest.RunAsTester(t, repo, "commit", "-qm", "a")
	gittest.RunAsTester(t, repo, "mv", "a", "renamed")
	got, err = Drift(context.Background(), execution.Runner{}, repo)
	if err != nil || !slices.Contains(got, "a") || !slices.Contains(got, "renamed") {
		t.Fatalf("rename lost paths: %q %v", got, err)
	}
}
