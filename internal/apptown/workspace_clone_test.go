package apptown

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

// The clone workspace model (complaint #9): a workspace must be a REAL,
// self-contained repo — .git is a directory, no remote, git works from
// inside the tree alone. Publishing makes its committed work available to
// the landing merge without giving the workspace a remote.
func TestWorkspaceCloneSelfContained(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	p := engineProject(t, a, "p")
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	ch := engineLandingChange(t, a, p)
	chID := ch.ID
	if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", chID))
	if fi, err := os.Stat(filepath.Join(wt, ".git")); err != nil || !fi.IsDir() {
		t.Fatal("a workspace .git must be a real directory, not a pointer")
	}
	if remotes, _ := gitexec.Run(t.Context(), wt, "remote"); remotes != "" {
		t.Fatalf("a workspace must have no remote, got %q", remotes)
	}
	// Work committed in the workspace reaches the hub via publish, and the
	// merge machinery sees it.
	if err := os.WriteFile(filepath.Join(wt, "game.js"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, wt, "add", "game.js")
	gittest.Run(t, wt, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "the change's work")
	run := engineLandingRun(t, a, p, ch)
	repo := a.projectRepoPath("p")
	if _, err := gitexec.Run(t.Context(), repo, "rev-parse", "--verify", ch.Branch); err != nil {
		t.Fatal("sweep must publish the branch into the hub")
	}
	if merged, c, d := mergeRunForTest(t, a, run); !merged {
		t.Fatalf("merge failed: %v %v", c, d)
	}
	if _, err := os.Stat(filepath.Join(repo, "game.js")); err != nil {
		t.Fatal("published work did not land on main")
	}
}
