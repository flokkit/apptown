package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// Ignoring the runtime directory must not prevent collecting project source,
// including the explicitly supported native contract inside that directory.
func TestACollectionWorksWhenTheProjectIgnoresAppTown(t *testing.T) {
	for _, change := range []string{"ordinary output", "add contract", "edit contract", "delete contract"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			ca, state := workertest.NewCA(t), t.TempDir()
			driver := workertest.Driver(t, ca, workertest.Start(t, ca, state, &executionfake.Tasks{}))
			hub := sourceRepo(t)
			write := func(path, body string) {
				t.Helper()
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
			write(filepath.Join(hub, ".gitignore"), ".apptown/\n")
			git("add", ".gitignore")
			if change == "edit contract" || change == "delete contract" {
				write(filepath.Join(hub, ".apptown/app.yaml"), "name: before\n")
				git("add", "-f", ".apptown/app.yaml")
			}
			git("commit", "-qm", "ignore runtime directory")
			base := git("rev-parse", "HEAD")
			spec := fakeSpec("review-ignored-forge", "", 0, false)
			spec.Source = &execution.TaskSource{Repository: hub, Commit: base}
			if _, err := driver.Ensure(ctx, spec); err != nil {
				t.Fatal(err)
			}
			work := filepath.Join(state, "workspaces", spec.Key)
			write(filepath.Join(work, ".apptown/runtime.json"), "{}\n")
			write(filepath.Join(work, "made.txt"), "ordinary output\n")
			if change == "add contract" || change == "edit contract" {
				write(filepath.Join(work, ".apptown/app.yaml"), "name: after\n")
			} else if change == "delete contract" {
				if err := os.Remove(filepath.Join(work, ".apptown/app.yaml")); err != nil {
					t.Fatal(err)
				}
			}
			bundle := filepath.Join(t.TempDir(), "result.bundle")
			tip, err := driver.Collect(ctx, spec.Key, base, "task result", bundle)
			if err != nil || tip == "" {
				t.Fatalf("collecting source with ignored runtime directory: tip=%q err=%v", tip, err)
			}
			dir, _, cleanup, err := gitexec.Quarantine(ctx, hub, bundle, "work", "work", base)
			defer cleanup()
			if err != nil {
				t.Fatal(err)
			}
			if got, err := gitexec.Output(ctx, dir, "show", tip+":made.txt"); err != nil || got != "ordinary output" {
				t.Fatalf("ordinary output: %q %v", got, err)
			}
			got, contractErr := gitexec.Output(ctx, dir, "show", tip+":.apptown/app.yaml")
			if change == "add contract" || change == "edit contract" {
				if contractErr != nil || got != "name: after" {
					t.Fatalf("contract: %q %v", got, contractErr)
				}
			} else if contractErr == nil {
				t.Fatal("result unexpectedly contains app contract")
			}
			if _, err := gitexec.Output(ctx, dir, "show", tip+":.apptown/runtime.json"); err == nil {
				t.Fatal("result contains runtime file")
			}
			if again, err := driver.Collect(ctx, spec.Key, base, "task result", bundle); err != nil || again != tip {
				t.Fatalf("repeat collection: %q %v", again, err)
			}
		})
	}
}

// A staging failure fails the collection instead of returning a result
// without what git could not take: with .apptown ignored and a source
// directory git cannot add (a repository with no commit), there is no
// result.
func TestAStagingFailureFailsTheCollection(t *testing.T) {
	ctx := context.Background()
	ca, state := workertest.NewCA(t), t.TempDir()
	driver := workertest.Driver(t, ca, workertest.Start(t, ca, state, &executionfake.Tasks{}))
	hub := sourceRepo(t)
	if err := os.WriteFile(filepath.Join(hub, ".gitignore"), []byte(".apptown/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", ".gitignore"}, {"commit", "-qm", "ignore runtime directory"}} {
		if out, err := gitexec.Run(ctx, hub, args...); err != nil {
			t.Fatal(err, out)
		}
	}
	base, _ := gitexec.Output(ctx, hub, "rev-parse", "HEAD")
	spec := fakeSpec("real-staging-failure", "", 0, false)
	spec.Source = &execution.TaskSource{Repository: hub, Commit: base}
	if _, err := driver.Ensure(ctx, spec); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(state, "workspaces", spec.Key)
	for _, dir := range []string{".apptown", "src/scratch"} {
		if err := os.MkdirAll(filepath.Join(work, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(work, ".apptown/runtime.json"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := gitexec.Run(ctx, filepath.Join(work, "src/scratch"), "init", "-q"); err != nil {
		t.Fatal(err, out)
	}
	tip, err := driver.Collect(ctx, spec.Key, base, "task result", filepath.Join(t.TempDir(), "result.bundle"))
	if err == nil || tip != "" {
		t.Fatalf("a staging failure returned a result: tip=%q err=%v", tip, err)
	}
}
