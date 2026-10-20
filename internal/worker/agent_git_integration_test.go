//go:build integration

package worker_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// With real Docker behind the worker and the packaged agent image's git: a
// task given its own repository reads the source's history and its diff
// against the base branch and commits, as the forge's committer; then it
// turns the repository against the worker (hostileGit). The worker's
// collection runs none of that (the sentinel folder, on this machine and
// out of the task's sight, stays empty) and returns the tree the task left
// as one commit, the forge's, with no .git content.
func TestIntegrationAnAgentUsesGitInItsTaskAndTheWorkerCollectsOnlyTheTree(t *testing.T) {
	const image = "forge-agent:latest"
	if exec.Command("docker", "image", "inspect", image).Run() != nil {
		t.Skipf("pending: the packaged agent image (%s) is not on this machine's Docker; it is not pulled here", image)
	}
	ca := workertest.NewCA(t)
	state := t.TempDir()
	namespace := fmt.Sprintf("agentgit%d", time.Now().UnixNano()%1_000_000_000)
	removeNamespace(t, namespace)
	driver := workertest.Driver(t, ca, workertest.StartServer(t, ca, &worker.Server{State: state, Tasks: execution.DockerTasks{Namespace: namespace}}))
	ctx := context.Background()
	hub, base, source := agentSource(t)
	sentinel := t.TempDir()

	script := `set -e
cd /work
git rev-parse --abbrev-ref HEAD > branch.txt
git log --format=%s > log.txt
git diff --name-only main > diff.txt
echo "by the agent" > agent.txt
git add agent.txt
git commit -q -m "the agent's own message"
git log -1 --format='%an <%ae> %s' > committed.txt
git status --porcelain -- agent.txt change.txt > status.txt
` + hostileGit(sentinel) + `echo done`
	spec := execution.TaskSpec{Key: namespace + "-agent", Image: image, Argv: []string{"sh", "-c", script},
		// As the forge sends it (workflowexec.buildAgentOnWorker): the
		// repository's owner on the worker need not be the image's user.
		Env:       []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=/work"},
		Admission: execution.Admission{Profile: "worker", Network: execution.NetworkNone, Expires: time.Now().Add(time.Hour)},
		Source:    &execution.TaskSource{Repository: hub, Commit: source}, SharedWorkspace: namespace,
		AgentGit: &execution.AgentGit{Branch: "change/1", Base: "main", BaseCommit: base}}
	created, err := driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}
	t.Cleanup(func() {
		_ = driver.Release(context.Background(), ref)
		_ = driver.ReleaseWorkspace(context.Background(), namespace)
	})
	if err := driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	code, err := driver.Wait(ctx, ref)
	page, _ := driver.Output(ctx, ref, execution.OutputRead{})
	if err != nil || code != 0 {
		t.Fatalf("the task: exit %d %v:\n%s", code, err, page.Data)
	}

	bundle := filepath.Join(t.TempDir(), "result.bundle")
	head, err := driver.Collect(ctx, namespace, source, "apptown: the agent's work", bundle)
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
	for _, p := range paths {
		if p == ".git" || strings.HasPrefix(p, ".git/") {
			t.Errorf("the collected commit holds %s", p)
		}
	}
	show := func(name string) string {
		got, err := gitexec.Output(ctx, hub, "show", "refs/test/collected:"+name)
		if err != nil {
			t.Fatalf("the collected commit lacks %s: %v (%v)", name, err, paths)
		}
		return strings.TrimSpace(got)
	}
	for name, want := range map[string]string{
		"branch.txt":    "change/1",
		"log.txt":       "the change so far\nbase",
		"diff.txt":      "change.txt",
		"agent.txt":     "by the agent",
		"committed.txt": "apptown <apptown@localhost> the agent's own message",
		"status.txt":    "",
		"after.txt":     "after",
	} {
		if got := show(name); got != want {
			t.Errorf("%s in the collected commit is %q, want %q", name, got, want)
		}
	}
	// The agent's commit is squashed into the one the worker makes.
	if got, _ := gitexec.Output(ctx, hub, "log", "--format=%s", source+"..refs/test/collected"); strings.TrimSpace(got) != "apptown: the agent's work" {
		t.Fatalf("the collected commits: %q", got)
	}
}
