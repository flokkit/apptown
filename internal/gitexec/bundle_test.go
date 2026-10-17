package gitexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := Output(t.Context(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

// sendAndWork plays the worker: it clones a source bundle, lets change add
// commits on branch work, and returns the bundle of what it added.
func sendAndWork(t *testing.T, hub, base string, change func(clone string)) string {
	t.Helper()
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source.bundle")
	if err := BundleCommit(ctx, hub, base, "work", source); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	if out, err := Run(ctx, "", "clone", "-q", "--branch", "work", source, clone); err != nil {
		t.Fatalf("cloning the source bundle: %v %s", err, out)
	}
	mustGit(t, clone, "config", "user.email", "w@example.org")
	mustGit(t, clone, "config", "user.name", "w")
	change(clone)
	result := filepath.Join(t.TempDir(), "result.bundle")
	if out, err := Run(ctx, clone, "bundle", "create", "-q", result, base+"..refs/heads/work"); err != nil {
		t.Fatalf("bundling the result: %v %s", err, out)
	}
	return result
}

// A task's source goes out as a bundle of one commit, and its work comes
// back into a quarantine beside the repository. The repository itself gains
// no ref and no object until the caller publishes.
func TestABundleRoundTripLeavesTheRepositoryUntouched(t *testing.T) {
	hub := repo(t)
	base := mustGit(t, hub, "rev-parse", "HEAD")
	refsBefore := mustGit(t, hub, "for-each-ref")
	result := sendAndWork(t, hub, base, func(clone string) {
		if got := mustGit(t, clone, "rev-parse", "HEAD"); got != base {
			t.Fatalf("the clone is at %s, want the source commit %s", got, base)
		}
		if err := os.WriteFile(filepath.Join(clone, "b.txt"), []byte("the task's work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustGit(t, clone, "add", "b.txt")
		mustGit(t, clone, "commit", "-q", "-m", "task")
	})
	dir, tip, cleanup, err := Quarantine(context.Background(), hub, result, "work", "work", base)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if got := mustGit(t, dir, "show", tip+":b.txt"); got != "the task's work" {
		t.Fatalf("quarantined work: %q", got)
	}
	if mustGit(t, hub, "for-each-ref") != refsBefore {
		t.Fatal("importing a result changed the repository's refs")
	}
	if _, err := Run(t.Context(), hub, "cat-file", "-e", tip); err == nil {
		t.Fatal("importing a result wrote its objects into the repository")
	}
}

// A result is refused whole when any object in it fails git's strict checks
// (a tree naming ".git" or ".."), when it is not a bundle, when it holds no
// commit for the branch, or when its tip does not build on the task's source.
func TestAHostileResultIsRefused(t *testing.T) {
	hub := repo(t)
	base := mustGit(t, hub, "rev-parse", "HEAD")
	// commitTree commits, on branch work, a tree whose one entry is the
	// entry format given, filled with a blob or, for a directory, a tree
	// holding a hook.
	commitTree := func(t *testing.T, clone, entry string) {
		t.Helper()
		object := strings.TrimSpace(gitIn(t, clone, "evil\n", "hash-object", "-w", "--stdin"))
		if strings.HasPrefix(entry, "040000") {
			object = strings.TrimSpace(gitIn(t, clone, fmt.Sprintf("100755 blob %s\tpost-checkout\n", object), "mktree"))
		}
		tree := strings.TrimSpace(gitIn(t, clone, fmt.Sprintf(entry, object), "mktree"))
		commit := mustGit(t, clone, "commit-tree", tree, "-p", "HEAD", "-m", "hostile")
		mustGit(t, clone, "update-ref", "refs/heads/work", commit)
	}
	for _, tc := range []struct {
		name   string
		result func(t *testing.T) string
		why    string
	}{
		{"a hook smuggled in a .git directory", func(t *testing.T) string {
			return sendAndWork(t, hub, base, func(clone string) { commitTree(t, clone, "040000 tree %s\t.git\n") })
		}, "hasDotgit"},
		{"a path escaping the checkout", func(t *testing.T) string {
			return sendAndWork(t, hub, base, func(clone string) { commitTree(t, clone, "100644 blob %s\t..\n") })
		}, "hasDotdot"},
		{"not a bundle", func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "junk.bundle")
			if err := os.WriteFile(path, []byte("# v2 git bundle\nnot really\n\nPACK"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}, "unrecognized header"},
		{"history unrelated to the source", func(t *testing.T) string {
			other := t.TempDir()
			mustGit(t, other, "init", "-q")
			mustGit(t, other, "-c", "user.name=o", "-c", "user.email=o@example.org", "commit", "-q", "--allow-empty", "-m", "another project")
			path := filepath.Join(t.TempDir(), "other.bundle")
			mustGit(t, other, "branch", "work")
			if out, err := Run(t.Context(), other, "bundle", "create", "-q", path, "refs/heads/work"); err != nil {
				t.Fatal(err, out)
			}
			return path
		}, "does not descend from"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _, cleanup, err := Quarantine(context.Background(), hub, tc.result(t), "work", "work", base)
			cleanup()
			if !errors.Is(err, ErrBundleRefused) || dir != "" || !strings.Contains(err.Error(), tc.why) {
				t.Fatalf("a hostile result was admitted: %q %v", dir, err)
			}
		})
	}
}

func gitIn(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	out, err := OutputBytes(context.Background(), dir, strings.NewReader(stdin), args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}
