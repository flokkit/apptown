package workflowexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/poll"
)

// An untrusted worker can return several valid commits. Removing runtime
// content in the last commit must not admit it into the branch's history.
// This deliberately edits the worker's private repository to model a bad
// worker, not a task's ability to access that repository through /work.
func TestAWorkerResultWithRuntimeInItsHistoryIsRefused(t *testing.T) {
	t.Parallel()
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
	reviewRound6Write(t, remote, ".forge/runtime.json", "runtime marker that must not enter history\n")
	git("add", "-f", "--", ".forge/runtime.json")
	git("commit", "-qm", "worker commits runtime")
	hidden := git("rev-parse", "HEAD")
	git("rm", "-q", "--", ".forge/runtime.json")
	reviewRound6Write(t, remote, "made.txt", "ordinary result\n")
	git("add", "--", "made.txt")
	git("commit", "-qm", "worker removes runtime from final tree")
	fake.Exit(taskKeyOf(step), 0)
	settled := reviewRound6Terminal(t, a, step.ID)
	tip := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
	if tip != given {
		t.Errorf("forbidden history published: branch moved from %s to %s; ancestor %s contains %q", given, tip, hidden, gittest.Run(t, hub, "show", hidden+":.forge/runtime.json"))
	}
	if got := gittest.Run(t, ws, "rev-parse", "HEAD"); got != before {
		t.Errorf("forbidden history reached AppTown workspace: %s", got)
	}
	if settled.Status != workflow.StatusError || !strings.Contains(string(settled.Evidence), ".forge/runtime.json") {
		t.Errorf("result was not refused with its runtime path: status=%s evidence=%s", settled.Status, settled.Evidence)
	}
}

// A task can replace the reserved runtime directory itself with a file or
// symlink. Neither spelling has the .forge/ prefix used by the new check.
// These cases use the normal worker collection, without editing its Git dir.
func TestAWorkerResultReplacingForgeIsRefused(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			a, fake, step, run, remote := startOnWorker(t)
			source := a.source(t, run)
			hub, ws := source.Repository(a.dir), source.Workspace(a.dir)
			given := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
			before := gittest.Run(t, ws, "rev-parse", "HEAD")
			if err := os.RemoveAll(filepath.Join(remote, ".forge")); err != nil {
				t.Fatal(err)
			}
			if kind == "file" {
				reviewRound6Write(t, remote, ".forge", "blocks the runtime directory\n")
			} else {
				reviewRound6Write(t, remote, "runtime-target/marker", "target\n")
				if err := os.Symlink("runtime-target", filepath.Join(remote, ".forge")); err != nil {
					t.Fatal(err)
				}
			}
			fake.Exit(taskKeyOf(step), 0)
			settled := reviewRound6Terminal(t, a, step.ID)
			if tip := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch); tip != given {
				t.Errorf("reserved path replacement published: %s", gittest.Run(t, hub, "ls-tree", tip, "--", ".forge"))
			}
			if got := gittest.Run(t, ws, "rev-parse", "HEAD"); got != before {
				t.Errorf("reserved path replacement reached AppTown workspace: %s", got)
			}
			if settled.Status != workflow.StatusError || !strings.Contains(string(settled.Evidence), ".forge") {
				t.Errorf("result was not refused with its reserved path: status=%s evidence=%s", settled.Status, settled.Evidence)
			}
		})
	}
}

func reviewRound6Write(t *testing.T, root, path, body string) {
	t.Helper()
	name := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func reviewRound6Terminal(t *testing.T, a *taskForge, id ids.ID) *workflow.Step {
	t.Helper()
	var step *workflow.Step
	var err error
	if !poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, func() bool {
		step, err = workflow.ReadStepIn(context.Background(), a.db, id)
		return err != nil || step.Status == workflow.StatusPass || step.Status == workflow.StatusError || step.Status == workflow.StatusFail
	}) {
		t.Fatal("step did not settle")
	}
	if err != nil {
		t.Fatal(err)
	}
	return step
}
