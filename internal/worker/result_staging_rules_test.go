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

// A result is what the task changed as git takes it: an untracked file
// under .apptown is left out even where the project's rules do not ignore it
// (a project started from existing code need not, and a step's report is
// written there), but one the rules re-include, or one already tracked, is
// in it (whether it may enter the change's branch is AppTown's check on
// import); the native contract is, even when ignored; and collecting again
// returns the same result.
func TestACollectionReturnsWhatGitTakes(t *testing.T) {
	for _, tc := range []struct {
		name            string
		ignore          string
		trackedContract bool
		trackedRuntime  bool
		ignoredSibling  bool
		writeRuntime    bool
		runtimeReturned bool
	}{
		{"partially ignored directory", ".apptown/cache/\n", false, false, true, true, false},
		{"rules that do not mention .apptown", "vendor/\n", false, false, false, true, false},
		{"negated runtime ignore", ".apptown/*\n!.apptown/runtime.json\n", false, false, true, true, true},
		{"tracked runtime in ignored directory", ".apptown/\n", false, true, true, true, true},
		{"untracked runtime in ignored directory", ".apptown/\n", false, false, true, true, false},
		{"only tracked contract in ignored directory", ".apptown/\n", true, false, false, false, false},
		{"only new contract then repeat collection", ".apptown/\n", false, false, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ca, state := workertest.NewCA(t), t.TempDir()
			driver := workertest.Driver(t, ca, workertest.Start(t, ca, state, &executionfake.Tasks{}))
			hub := sourceRepo(t)
			write := func(root, name, body string) {
				t.Helper()
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git := func(args ...string) string {
				t.Helper()
				out, err := gitexec.Output(ctx, hub, args...)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			write(hub, ".gitignore", tc.ignore)
			git("add", ".gitignore")
			if tc.trackedContract {
				write(hub, ".apptown/app.yaml", "name: before\n")
				git("add", "-f", ".apptown/app.yaml")
			}
			if tc.trackedRuntime {
				write(hub, ".apptown/runtime.json", "before\n")
				git("add", "-f", ".apptown/runtime.json")
			}
			git("commit", "-qm", "source ignore rules")
			base := git("rev-parse", "HEAD")
			spec := fakeSpec("review-round3-staging", "", 0, false)
			spec.Source = &execution.TaskSource{Repository: hub, Commit: base}
			if _, err := driver.Ensure(ctx, spec); err != nil {
				t.Fatal(err)
			}
			work := filepath.Join(state, "workspaces", spec.Key)
			write(work, "made.txt", "ordinary output\n")
			write(work, ".apptown/app.yaml", "name: after\n")
			if tc.ignoredSibling {
				write(work, ".apptown/cache/item", "ignored cache\n")
			}
			if tc.writeRuntime {
				write(work, ".apptown/runtime.json", "runtime must not travel\n")
			}
			bundle := filepath.Join(t.TempDir(), "result.bundle")
			tip, err := driver.Collect(ctx, spec.Key, base, "task result", bundle)
			if err != nil || tip == "" {
				t.Fatalf("collecting source: tip=%q err=%v", tip, err)
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
			got, runtimeErr := gitexec.Output(ctx, dir, "show", tip+":.apptown/runtime.json")
			if tc.runtimeReturned {
				if runtimeErr != nil || got != "runtime must not travel" {
					t.Errorf("the runtime file git takes is not in the result: %q %v", got, runtimeErr)
				}
			} else if runtimeErr == nil {
				t.Errorf("the result holds a runtime file git should leave out: %q", got)
			}
			if _, err := gitexec.Output(ctx, dir, "show", tip+":.apptown/cache/item"); err == nil {
				t.Error("result contains ignored cache")
			}
			if again, err := driver.Collect(ctx, spec.Key, base, "task result", bundle); err != nil || again != tip {
				t.Errorf("repeat collection: tip=%q err=%v", again, err)
			}
		})
	}
}
