package workflowexec

import (
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
)

// The forge's source check holds for every shape of returned history: these
// graphs model an untrusted worker by editing its private repository.
func TestTheSourceCheckCoversEveryCommitGraph(t *testing.T) {
	t.Parallel()
	for _, shape := range []string{"merge-only-runtime", "unrelated-root", "gitlink", "source-only-merge"} {
		t.Run(shape, func(t *testing.T) {
			a, fake, step, run, remote := startOnWorker(t)
			source := a.source(t, run)
			hub, ws := source.Repository(a.dir), source.Workspace(a.dir)
			given := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
			before := gittest.Run(t, ws, "rev-parse", "HEAD")
			workerGit := filepath.Join(filepath.Dir(filepath.Dir(remote)), "git", filepath.Base(remote)+".git")
			git := func(args ...string) string {
				t.Helper()
				prefix := []string{"--git-dir", workerGit, "--work-tree", remote, "-c", "core.bare=false", "-c", "user.name=review", "-c", "user.email=review@example.invalid"}
				return gittest.Run(t, "", append(prefix, args...)...)
			}
			tree := git("rev-parse", "HEAD^{tree}")
			switch shape {
			case "merge-only-runtime":
				side := git("commit-tree", tree, "-p", given, "-m", "clean side")
				reviewRound6Write(t, remote, ".apptown/runtime.json", "merge-only forbidden content\n")
				git("add", "-f", "--", ".apptown/runtime.json")
				changedTree := git("write-tree")
				merged := git("commit-tree", changedTree, "-p", given, "-p", side, "-m", "runtime appears only in merge")
				// Remove it again, leaving every ordinary commit's tree clean.
				tip := git("commit-tree", tree, "-p", merged, "-m", "remove runtime")
				git("reset", "--hard", tip)
			case "unrelated-root":
				reviewRound6Write(t, remote, ".apptown/runtime.json", "unrelated forbidden root\n")
				git("add", "-f", "--", ".apptown/runtime.json")
				root := git("commit-tree", git("write-tree"), "-m", "unrelated root")
				side := git("commit-tree", tree, "-p", root, "-m", "remove runtime on side")
				tip := git("commit-tree", tree, "-p", given, "-p", side, "-m", "merge unrelated history")
				git("reset", "--hard", tip)
			case "gitlink":
				git("rm", "-rf", "--ignore-unmatch", "--", ".apptown")
				git("update-index", "--add", "--cacheinfo", "160000,"+given+",.apptown")
				bad := git("commit-tree", git("write-tree"), "-p", given, "-m", "reserved gitlink")
				tip := git("commit-tree", tree, "-p", bad, "-m", "remove reserved gitlink")
				git("reset", "--hard", tip)
			case "source-only-merge":
				reviewRound6Write(t, remote, "made.txt", "normal result\n")
				git("add", "--", "made.txt")
				changedTree := git("write-tree")
				side := git("commit-tree", changedTree, "-m", "unrelated source root")
				tip := git("commit-tree", changedTree, "-p", given, "-p", side, "-m", "source-only merge")
				git("reset", "--hard", tip)
			}
			fake.Exit(taskKeyOf(step), 0)
			settled := reviewRound6Terminal(t, a, step.ID)
			tip := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
			workspace := gittest.Run(t, ws, "rev-parse", "HEAD")
			if shape == "source-only-merge" {
				if settled.Status != workflow.StatusPass || tip == given || workspace != tip {
					t.Fatalf("normal merge not published: status=%s branch=%s workspace=%s evidence=%s", settled.Status, tip, workspace, settled.Evidence)
				}
			} else if settled.Status != workflow.StatusError || tip != given || workspace != before || !strings.Contains(string(settled.Evidence), ".apptown") {
				t.Fatalf("forbidden graph accepted: status=%s branch=%s (base %s) workspace=%s (before %s) evidence=%s", settled.Status, tip, given, workspace, before, settled.Evidence)
			}
		})
	}
}
