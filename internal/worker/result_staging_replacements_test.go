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

// A script may replace a source file with a directory or the reverse.
// Collection must preserve the replacement and be repeatable after reply loss.
func TestACollectionTakesFileAndDirectoryReplacements(t *testing.T) {
	for _, tc := range []struct {
		name, before, after string
	}{
		{"file becomes directory", "entry", "entry/new.txt"},
		{"directory becomes file", "entry/old.txt", "entry"},
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
			write(hub, tc.before, "before\n")
			for _, args := range [][]string{{"add", "--", "entry"}, {"commit", "-qm", "source before replacement"}} {
				if out, err := gitexec.Run(ctx, hub, args...); err != nil {
					t.Fatal(err, out)
				}
			}
			base, err := gitexec.Output(ctx, hub, "rev-parse", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			spec := fakeSpec("review-round5-staging", "", 0, false)
			spec.Source = &execution.TaskSource{Repository: hub, Commit: base}
			if _, err := driver.Ensure(ctx, spec); err != nil {
				t.Fatal(err)
			}
			work := filepath.Join(state, "workspaces", spec.Key)
			if err := os.RemoveAll(filepath.Join(work, "entry")); err != nil {
				t.Fatal(err)
			}
			write(work, tc.after, "after\n")
			write(work, "made.txt", "ordinary output\n")
			bundle := filepath.Join(t.TempDir(), "result.bundle")
			tip, err := driver.Collect(ctx, spec.Key, base, "task result", bundle)
			if err != nil || tip == "" {
				t.Fatalf("collecting a file/directory replacement: tip=%q err=%v", tip, err)
			}
			dir, imported, cleanup, err := gitexec.Quarantine(ctx, hub, bundle, "work", "work", base)
			defer cleanup()
			if err != nil || imported != tip {
				t.Fatalf("import: tip=%q err=%v", imported, err)
			}
			if got, err := gitexec.Output(ctx, dir, "ls-tree", "-r", "--name-only", tip, "--", "entry"); err != nil || got != tc.after {
				t.Errorf("replacement tree: %q, want %q: %v", got, tc.after, err)
			}
			for name, want := range map[string]string{tc.after: "after", "made.txt": "ordinary output"} {
				if got, err := gitexec.Output(ctx, dir, "show", tip+":"+name); err != nil || got != want {
					t.Errorf("source %s: %q %v", name, got, err)
				}
			}
			if again, err := driver.Collect(ctx, spec.Key, base, "task result", bundle); err != nil || again != tip {
				t.Errorf("repeat collection: tip=%q err=%v", again, err)
			}
		})
	}
}
