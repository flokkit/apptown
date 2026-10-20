package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/execution/executionfake"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/worker/workertest"
)

// A scratch repository a task leaves under .apptown is runtime, not source:
// whatever the project's ignore rules say, collection leaves it out and
// returns the ordinary source and the native contract, including on a
// retry. (Unaddable content elsewhere still fails the collection with
// git's reason: TestAStagingFailureFailsTheCollection.)
func TestAScratchRepositoryUnderForgeIsLeftOut(t *testing.T) {
	for _, ignore := range []string{"", ".apptown/other-cache/\n", ".apptown/\n"} {
		t.Run("ignore="+ignore, func(t *testing.T) {
			ctx := context.Background()
			ca, state := workertest.NewCA(t), t.TempDir()
			driver := workertest.Driver(t, ca, workertest.Start(t, ca, state, &executionfake.Tasks{}))
			hub := sourceRepo(t)
			if err := os.WriteFile(filepath.Join(hub, ".gitignore"), []byte(ignore), 0600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"add", ".gitignore"}, {"commit", "-qm", "source ignore rules"}} {
				if out, err := gitexec.Run(ctx, hub, args...); err != nil {
					t.Fatal(err, out)
				}
			}
			base, err := gitexec.Output(ctx, hub, "rev-parse", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			spec := fakeSpec("review-round6-staging", "", 0, false)
			spec.Source = &execution.TaskSource{Repository: hub, Commit: base}
			if _, err := driver.Ensure(ctx, spec); err != nil {
				t.Fatal(err)
			}
			work := filepath.Join(state, "workspaces", spec.Key)
			if out, err := gitexec.Run(ctx, work, "init", "-q", ".apptown/cache/repo"); err != nil {
				t.Fatal(err, out)
			}
			for name, body := range map[string]string{"made.txt": "ordinary output\n", ".apptown/app.yaml": "name: after\n"} {
				if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			bundle := filepath.Join(t.TempDir(), "result.bundle")
			tip, err := driver.Collect(ctx, spec.Key, base, "task result", bundle)
			if err != nil || tip == "" {
				t.Fatalf("collecting source with an excluded scratch repository: tip=%q err=%v", tip, err)
			}
			dir, imported, cleanup, err := gitexec.Quarantine(ctx, hub, bundle, "work", "work", base)
			defer cleanup()
			if err != nil || imported != tip {
				t.Fatalf("import: tip=%q err=%v", imported, err)
			}
			for name, want := range map[string]string{"made.txt": "ordinary output", ".apptown/app.yaml": "name: after"} {
				if got, err := gitexec.Output(ctx, dir, "show", tip+":"+name); err != nil || got != want {
					t.Errorf("source %s: %q %v", name, got, err)
				}
			}
			if got, err := gitexec.Output(ctx, dir, "ls-tree", "-r", "--name-only", tip, "--", ".apptown/cache"); err != nil || got != "" {
				t.Errorf("excluded runtime tree: %q %v", got, err)
			}
			if again, err := driver.Collect(ctx, spec.Key, base, "task result", bundle); err != nil || again != tip {
				t.Errorf("repeat collection: tip=%q err=%v", again, err)
			}
		})
	}
}
