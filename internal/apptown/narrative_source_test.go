package apptown

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestNarrativeDiffReadsWorkspaceWithoutPublishingAndFallsBackAfterRemoval(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo := filepath.Join(dir, "workspaces", "project-game")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repo, "init", "-q", "-b", "main")
	gittest.Run(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "base")
	base := gittest.Run(t, repo, "rev-parse", "HEAD")
	workspace := filepath.Join(dir, "workspaces", "change-00000000-0001-7000-8000-000000000000")
	gittest.Run(t, dir, "clone", "-q", repo, workspace)
	gittest.Run(t, workspace, "switch", "-c", "change")
	if err := os.WriteFile(filepath.Join(workspace, "game.txt"), []byte("historical game\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, workspace, "add", "game.txt")
	gittest.Run(t, workspace, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "playable")
	source := workflow.NarrativeSource{Change: &workflow.Change{ID: idstest.N(1), Branch: "change", BaseSHA: base}, Slug: "game", DefaultBranch: "main"}
	refs := gittest.Run(t, repo, "show-ref")
	before, err := narrativeDiff(context.Background(), dir, source)
	if err != nil || !strings.Contains(before.Text, "DIFF STAT:") || !strings.Contains(before.Text, "+historical game") {
		t.Fatalf("workspace evidence: %+v %v", before, err)
	}
	if got := gittest.Run(t, repo, "show-ref"); got != refs {
		t.Fatalf("narrative changed project refs: %s", got)
	}
	if err := mirrorBranchForTest(repo, workspace, "change"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(workspace); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repo, "merge", "--ff-only", "change")
	after, err := narrativeDiff(context.Background(), dir, source)
	if err != nil || before != after {
		t.Fatalf("landed historical diff changed: %+v %v", after, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := narrativeDiff(ctx, dir, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled source: %v", err)
	}
	source.Change.Branch = "absent"
	if got, err := narrativeDiff(context.Background(), dir, source); err == nil || got != (narrativeEvidence{}) {
		t.Fatalf("missing source became empty evidence: %+v %v", got, err)
	}
	source.Change.Branch = ""
	if got, err := narrativeDiff(context.Background(), dir, source); err == nil || got != (narrativeEvidence{}) {
		t.Fatalf("repository-free Change: %+v %v", got, err)
	}
}

func TestEmptyNarrativeComparisonIgnoresDirtyWorkAndLaterMainCommits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo := filepath.Join(dir, "workspaces", "project-game")
	base := gittest.Repository(t, repo, "main", gittest.Commit{Message: "starting point"})[0]
	ws := filepath.Join(dir, "workspaces", "change-00000000-0001-7000-8000-000000000000")
	gittest.Run(t, dir, "clone", "-q", repo, ws)
	gittest.ConfigureTester(t, ws)
	gittest.Run(t, ws, "switch", "-c", "change")
	// Empty commits are history, but do not make the net committed diff nonempty.
	gittest.Run(t, ws, "commit", "--allow-empty", "-qm", "thought about it")
	if err := os.WriteFile(filepath.Join(ws, "uncommitted.txt"), []byte("still working"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "other.txt"), []byte("other work"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, repo, "add", ".")
	gittest.Run(t, repo, "commit", "-qm", "main moved")
	source := workflow.NarrativeSource{Change: &workflow.Change{ID: idstest.N(1), Branch: "change", BaseSHA: base}, Slug: "game", DefaultBranch: "main"}
	got, err := narrativeDiff(context.Background(), dir, source)
	if err != nil || !got.Empty || got.Text != "" {
		t.Fatalf("empty committed comparison: %+v %v", got, err)
	}
	for _, bad := range []string{"missing-base", ""} {
		source.Change.BaseSHA = bad
		got, err := narrativeDiff(context.Background(), dir, source)
		if err == nil || got.Empty {
			t.Fatalf("unknown base became empty: %+v %v", got, err)
		}
	}
}
