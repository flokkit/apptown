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

// Collecting a task that changed nothing returns no result, and collecting
// again after a lost reply returns the same result, not a second commit.
func TestACollectionCanBeRepeated(t *testing.T) {
	for _, withResult := range []bool{false, true} {
		name := "nothing changed"
		if withResult {
			name = "repeat collection after a lost reply"
		}
		t.Run(name, func(t *testing.T) {
			ca, state := workertest.NewCA(t), t.TempDir()
			driver := workertest.Driver(t, ca, workertest.Start(t, ca, state, &executionfake.Tasks{}))
			hub := sourceRepo(t)
			base, err := gitexec.Output(t.Context(), hub, "rev-parse", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			spec := fakeSpec("review-excluded", "", 0, false)
			spec.Source = &execution.TaskSource{Repository: hub, Commit: base}
			if _, err := driver.Ensure(context.Background(), spec); err != nil {
				t.Fatal(err)
			}
			work := filepath.Join(state, "workspaces", spec.Key)
			bundle := filepath.Join(t.TempDir(), "result.bundle")
			firstTip := ""
			if withResult {
				if err := os.WriteFile(filepath.Join(work, "made.txt"), []byte("task output\n"), 0600); err != nil {
					t.Fatal(err)
				}
				firstTip, err = driver.Collect(context.Background(), spec.Key, base, "task result", bundle)
				if err != nil || firstTip == "" {
					t.Fatalf("first collection: tip=%q err=%v", firstTip, err)
				}
			}
			tip, err := driver.Collect(context.Background(), spec.Key, base, "task result", bundle)
			if err != nil || tip != firstTip {
				t.Fatalf("collecting again: tip=%q want=%q err=%v", tip, firstTip, err)
			}
		})
	}
}

// Script edits to the native app contract are source, like edits to other
// project files; the worker must preserve the local publication contract.
func TestACollectionKeepsTheNativeAppContract(t *testing.T) {
	ca, state := workertest.NewCA(t), t.TempDir()
	driver := workertest.Driver(t, ca, workertest.Start(t, ca, state, &executionfake.Tasks{}))
	hub := sourceRepo(t)
	base, err := gitexec.Output(t.Context(), hub, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	spec := fakeSpec("review-contract", "", 0, false)
	spec.Source = &execution.TaskSource{Repository: hub, Commit: base}
	if _, err := driver.Ensure(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(state, "workspaces", spec.Key)
	if err := os.Mkdir(filepath.Join(work, ".forge"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{".forge/app.yaml": "name: example\n", "made.txt": "task output\n"} {
		if err := os.WriteFile(filepath.Join(work, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bundle := filepath.Join(t.TempDir(), "result.bundle")
	tip, err := driver.Collect(context.Background(), spec.Key, base, "task result", bundle)
	if err != nil || tip == "" {
		t.Fatalf("collecting: tip=%q err=%v", tip, err)
	}
	dir, _, cleanup, err := gitexec.Quarantine(context.Background(), hub, bundle, "work", "work", base)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := gitexec.Output(t.Context(), dir, "show", tip+":.forge/app.yaml"); err != nil || got != "name: example" {
		t.Fatalf("worker result lost the native app contract: %q %v", got, err)
	}
}
