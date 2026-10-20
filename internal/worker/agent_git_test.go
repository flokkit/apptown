package worker_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/execution/executionfake"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/worker/workertest"
)

// agentSource is a hub whose main is at base and whose change branch adds
// one commit on it, the source a step is given.
func agentSource(t *testing.T) (hub, base, source string) {
	t.Helper()
	hub = sourceRepo(t)
	base, _ = gitexec.Output(t.Context(), hub, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(hub, "change.txt"), []byte("from the change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"checkout", "-q", "-b", "change/1"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-q", "-m", "the change so far"}} {
		if out, err := gitexec.Run(t.Context(), hub, args...); err != nil {
			t.Fatal(err, out)
		}
	}
	source, _ = gitexec.Output(t.Context(), hub, "rev-parse", "HEAD")
	return hub, strings.TrimSpace(base), strings.TrimSpace(source)
}

// hostileGit is what a task may write into its own repository to make the
// worker's git run something: a work tree elsewhere, a file-system monitor,
// hooks and a filter driver its .gitattributes names. Each would leave a
// file in sentinel, a folder on the worker's side that the task cannot see.
func hostileGit(sentinel string) string {
	return `set -e
cd /work 2>/dev/null || true
cat >> .git/config <<EOF
[core]
	worktree = ` + sentinel + `
	fsmonitor = touch ` + sentinel + `/fsmonitor
	hooksPath = .git/hooks
[filter "evil"]
	clean = touch ` + sentinel + `/clean
	smudge = touch ` + sentinel + `/smudge
	process = touch ` + sentinel + `/process
	required = true
EOF
mkdir -p .git/hooks
for h in pre-commit commit-msg post-commit pre-auto-gc reference-transaction post-index-change; do
	printf '#!/bin/sh\ntouch %s/%s\n' '` + sentinel + `' "$h" > ".git/hooks/$h"
	chmod +x ".git/hooks/$h"
done
echo '* filter=evil' > .gitattributes
echo after > after.txt
`
}

// collectedTree fetches a collected bundle into hub and lists its paths.
func collectedTree(t *testing.T, hub, bundle string) (tip string, paths []string) {
	t.Helper()
	if out, err := gitexec.Run(t.Context(), hub, "fetch", "-q", bundle, "refs/heads/work:refs/test/collected"); err != nil {
		t.Fatal(err, out)
	}
	tip, _ = gitexec.Output(t.Context(), hub, "rev-parse", "refs/test/collected")
	files, err := gitexec.Output(t.Context(), hub, "ls-tree", "-r", "--name-only", "refs/test/collected")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(tip), strings.Fields(files)
}

// A workspace made with the agent's repository has an ordinary one at
// /work/.git: the source's history on the change's branch, the base branch
// at the change's base, no remote, AppTown as committer and a clean
// index. What the task then writes into it (hostileGit) is never run by the
// worker's collection, which still collects the tree and no .git content.
// A base not in the source's history is refused.
func TestAnAgentsOwnRepositoryIsNeverTheWorkers(t *testing.T) {
	ca := workertest.NewCA(t)
	state := t.TempDir()
	driver := workertest.Driver(t, ca, workertest.Start(t, ca, state, &executionfake.Tasks{}))
	ctx := context.Background()
	hub, base, source := agentSource(t)
	spec := fakeSpec("step-3-a1-agent", "", 0, false)
	spec.Source, spec.SharedWorkspace = &execution.TaskSource{Repository: hub, Commit: source}, "step-3-a1"
	spec.AgentGit = &execution.AgentGit{Branch: "change/1", Base: "main", BaseCommit: base}
	if _, err := driver.Ensure(ctx, spec); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(state, "workspaces", "step-3-a1")
	agentGit := func(args ...string) string {
		t.Helper()
		cmd := gittest.Command(append([]string{"-c", "safe.directory=*"}, args...)...)
		cmd.Dir, cmd.Env = work, append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in the agent's repository: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got := agentGit("rev-parse", "--abbrev-ref", "HEAD"); got != "change/1" {
		t.Fatalf("the agent's branch is %q", got)
	}
	if got := agentGit("rev-parse", "HEAD", "main"); got != source+"\n"+base {
		t.Fatalf("the agent's HEAD and main: %q", got)
	}
	if got := agentGit("status", "--porcelain"); got != "" {
		t.Fatalf("the agent's repository starts dirty: %q", got)
	}
	if got := agentGit("diff", "--name-only", "main"); got != "change.txt" {
		t.Fatalf("the agent's diff against main: %q", got)
	}
	if got := agentGit("remote"); got != "" {
		t.Fatalf("the agent's repository has remotes: %q", got)
	}
	if err := os.WriteFile(filepath.Join(work, "agent.txt"), []byte("by the agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agentGit("add", "agent.txt")
	agentGit("commit", "-q", "-m", "the agent's own message")
	if got := agentGit("log", "-1", "--format=%an <%ae>"); got != "apptown <apptown@localhost>" {
		t.Fatalf("the agent commits as %q", got)
	}

	// The task turns its repository against the worker.
	sentinel := t.TempDir()
	hostile := exec.Command("sh", "-c", hostileGit(sentinel))
	hostile.Dir = work
	if out, err := hostile.CombinedOutput(); err != nil {
		t.Fatalf("writing the hostile repository: %v: %s", err, out)
	}

	bundle := filepath.Join(t.TempDir(), "result.bundle")
	head, err := driver.Collect(ctx, "step-3-a1", source, "apptown: the agent's work", bundle)
	if err != nil || head == "" {
		t.Fatalf("collecting: %q %v", head, err)
	}
	if ran, _ := os.ReadDir(sentinel); len(ran) > 0 {
		t.Fatalf("the worker's git ran what the task wrote: %v", ran)
	}
	tip, paths := collectedTree(t, hub, bundle)
	if tip != head {
		t.Fatalf("the bundle's tip %s, the worker's %s", tip, head)
	}
	want := map[string]bool{"change.txt": true, "agent.txt": true, "after.txt": true, ".gitattributes": true}
	for _, p := range paths {
		if p == ".git" || strings.HasPrefix(p, ".git/") {
			t.Errorf("the collected commit holds %s", p)
		}
		delete(want, p)
	}
	if len(want) > 0 {
		t.Fatalf("the collected commit lacks %v: %v", want, paths)
	}
	// The agent's commits are squashed into the one the worker makes.
	if got, _ := gitexec.Output(ctx, hub, "log", "--format=%s", source+"..refs/test/collected"); strings.TrimSpace(got) != "apptown: the agent's work" {
		t.Fatalf("the collected commits: %q", got)
	}

	// A base the source does not descend from is refused, with nothing made.
	other := fakeSpec("step-4-a1-agent", "", 0, false)
	other.Source, other.SharedWorkspace = &execution.TaskSource{Repository: hub, Commit: base}, "step-4-a1"
	other.AgentGit = &execution.AgentGit{Branch: "change/1", Base: "main", BaseCommit: source}
	if _, err := driver.Ensure(ctx, other); err == nil || !strings.Contains(err.Error(), "the agent's repository") {
		t.Fatalf("a base not in the source's history: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "workspaces", "step-4-a1")); !os.IsNotExist(err) {
		t.Fatalf("a refused workspace was made: %v", err)
	}

	// A branch or base git would not take as a branch name is refused, with
	// nothing made: the plain ref rules, and the two --branch adds.
	for i, names := range [][2]string{{"change..1", "main"}, {"change/1", "-main"}, {"HEAD", "main"}} {
		named := fakeSpec(fmt.Sprintf("step-%d-a1-agent", 5+i), "", 0, false)
		named.Source, named.SharedWorkspace = &execution.TaskSource{Repository: hub, Commit: source}, fmt.Sprintf("step-%d-a1", 5+i)
		named.AgentGit = &execution.AgentGit{Branch: names[0], Base: names[1], BaseCommit: base}
		if _, err := driver.Ensure(ctx, named); err == nil || !strings.Contains(err.Error(), "is not a branch name") {
			t.Fatalf("the branch %q on %q: %v", names[0], names[1], err)
		}
		if _, err := os.Stat(filepath.Join(state, "workspaces", named.SharedWorkspace)); !os.IsNotExist(err) {
			t.Fatalf("a refused workspace was made: %v", err)
		}
	}
}
