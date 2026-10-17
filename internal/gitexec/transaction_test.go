package gitexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func transactionRepo(t *testing.T) (dir, first, second string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "repo")
	run := func(args ...string) string {
		out, err := Run(t.Context(), dir, append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return out
	}
	if out, err := Run(t.Context(), filepath.Dir(dir), "init", "-q", dir); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	run("commit", "-q", "--allow-empty", "-m", "one")
	first = run("rev-parse", "HEAD")
	run("commit", "-q", "--allow-empty", "-m", "two")
	second = run("rev-parse", "HEAD")
	run("update-ref", "refs/heads/t", first)
	return dir, first, second
}

// Branch publication relies on this: if the process that prepared an update
// dies before sending commit, git aborts instead of finishing on its own.
func TestAPreparedUpdateAbortsWhenItsCallerGoesAway(t *testing.T) {
	dir, first, second := transactionRepo(t)
	txn, err := PrepareUpdate(context.Background(), dir, "refs/heads/t", second, first)
	if err != nil {
		t.Fatal(err)
	}
	_ = txn.stdin.Close() // the caller's end of the pipe closes when it dies
	_ = txn.cmd.Wait()
	if got, _ := Output(t.Context(), dir, "rev-parse", "refs/heads/t"); got != first {
		t.Fatalf("ref = %s after the caller went away, want it unchanged at %s", got, first)
	}
}

func TestAPreparedUpdateHoldsTheRefUntilCommitted(t *testing.T) {
	dir, first, second := transactionRepo(t)
	txn, err := PrepareUpdate(context.Background(), dir, "refs/heads/t", second, first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareUpdate(context.Background(), dir, "refs/heads/t", first, first); err == nil {
		t.Fatal("a second update prepared while the first holds the ref")
	}
	if err := txn.Commit(); err != nil {
		t.Fatal(err)
	}
	if got, _ := Output(t.Context(), dir, "rev-parse", "refs/heads/t"); got != second {
		t.Fatalf("ref = %s after commit, want %s", got, second)
	}
}

// A prepared update outlives its caller's cancellation: git holds the lock
// from prepare to commit, and a caller that goes away in between neither
// kills it nor strands the lock. The commit lands, and no lock is left.
func TestAPreparedUpdateCommitsThoughItsCallerWasCancelled(t *testing.T) {
	dir, first, second := transactionRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	txn, err := PrepareUpdate(ctx, dir, "refs/heads/t", second, first)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := txn.Commit(); err != nil {
		t.Fatalf("commit after the caller was cancelled: %v", err)
	}
	if got, _ := Output(t.Context(), dir, "rev-parse", "refs/heads/t"); got != second {
		t.Fatalf("ref = %s after commit, want %s", got, second)
	}
	gitDir, _ := Output(t.Context(), dir, "rev-parse", "--absolute-git-dir")
	if _, err := os.Stat(filepath.Join(gitDir, "refs", "heads", "t.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the ref's lock is left: %v", err)
	}
}

func TestAnUpdateFromTheWrongValueIsRefused(t *testing.T) {
	dir, first, second := transactionRepo(t)
	if _, err := PrepareUpdate(context.Background(), dir, "refs/heads/t", first, second); err == nil {
		t.Fatal("prepared an update whose expected value is wrong")
	}
	if got, _ := Output(t.Context(), dir, "rev-parse", "refs/heads/t"); got != first {
		t.Fatalf("ref = %s, want unchanged %s", got, first)
	}
}
