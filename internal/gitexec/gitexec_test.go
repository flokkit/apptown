package gitexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mainRepoTemplate is a repository on main with a.txt committed and a local
// identity; repo gives each test its own copy.
var mainRepoTemplate = &repoTemplate{build: func(t testing.TB, dir string) {
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.org"},
		{"config", "user.name", "t"},
	} {
		if out, err := Run(context.Background(), dir, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := Run(context.Background(), dir, "add", "a.txt"); err != nil {
		t.Fatal(err, out)
	}
	if out, err := Run(context.Background(), dir, "commit", "-q", "-m", "first"); err != nil {
		t.Fatal(err, out)
	}
}}

func repo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	mainRepoTemplate.copyTo(t, dir)
	return dir
}

func TestRepositoryStateReadsWhatGitSays(t *testing.T) {
	dir := repo(t)
	if !IsRepo(dir) || IsRepo(t.TempDir()) {
		t.Fatal("IsRepo must see .git and only .git")
	}
	if staged, err := Staged(t.Context(), dir); err != nil || staged {
		t.Fatalf("a fresh commit has nothing staged: %v %v", staged, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if staged, err := Staged(t.Context(), dir); err != nil || staged {
		t.Fatalf("an edit nobody staged is not staged: %v %v", staged, err)
	}
	if out, err := Run(t.Context(), dir, "add", "a.txt"); err != nil {
		t.Fatal(out)
	}
	if staged, err := Staged(t.Context(), dir); err != nil || !staged {
		t.Fatalf("a staged edit reads as staged: %v %v", staged, err)
	}
	if _, err := Staged(t.Context(), t.TempDir()); err == nil {
		t.Fatal("a folder git cannot read must be an error, not nothing staged")
	}
	sha, err := ResolveCommit(context.Background(), dir, "main")
	if err != nil || len(sha) != 40 {
		t.Fatalf("resolve main: %q %v", sha, err)
	}
	if _, err := ResolveCommit(context.Background(), dir, "--help"); err == nil {
		t.Fatal("a revision that reads as an option must be refused")
	}
	if out, err := Output(t.Context(), dir, "rev-parse", "HEAD"); err != nil || out != sha {
		t.Fatalf("output is stdout only, trimmed: %q %v", out, err)
	}
	if out, err := RunEnv(context.Background(), dir, []string{"GIT_AUTHOR_NAME=someone"}, "var", "GIT_AUTHOR_IDENT"); err != nil || !strings.HasPrefix(out, "someone ") {
		t.Fatalf("extra environment reaches git: %q %v", out, err)
	}
	// "committed\n" hashes to the blob already in the repository.
	if got, err := OutputBytes(context.Background(), dir, strings.NewReader("committed\n"), "hash-object", "--stdin"); err != nil || strings.TrimSpace(string(got)) != mustOutput(t, dir, "rev-parse", "HEAD:a.txt") {
		t.Fatalf("stdin reaches git: %q %v", got, err)
	}
}

// Only committed bytes are extracted, and a failure says which half failed.
func TestExtractCommitWritesOnlyTheCommittedTree(t *testing.T) {
	dir := repo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := ExtractCommit(context.Background(), dir, "HEAD", dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "a.txt")); err != nil || string(b) != "committed\n" {
		t.Fatalf("extracted %q %v", b, err)
	}
	err := ExtractCommit(context.Background(), dir, "no-such-revision", t.TempDir())
	var ex *ExtractError
	if !errors.As(err, &ex) || ex.Step != "archive" || ex.Output == "" {
		t.Fatalf("a bad revision fails in archive with git's words: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ExtractCommit(ctx, dir, "HEAD", t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("a canceled extraction reports cancellation: %v", err)
	}
}

func mustOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := Output(t.Context(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A repository named by a relative path is found once: the process runs in it,
// so no argument may name it again (./app must not resolve as ./app/app).
func TestRelativeRepositoryPathsResolveOnce(t *testing.T) {
	dir := repo(t)
	t.Chdir(filepath.Dir(dir))
	rel := "./" + filepath.Base(dir)
	want := mustOutput(t, dir, "rev-parse", "HEAD")
	if got, err := ResolveCommit(context.Background(), rel, "HEAD"); err != nil || got != want {
		t.Fatalf("resolve through %s: %q %v", rel, got, err)
	}
	if err := ExtractCommit(context.Background(), rel, "HEAD", t.TempDir()); err != nil {
		t.Fatalf("extract through %s: %v", rel, err)
	}
}

// Only a full object id as git prints it is one: an abbreviation, a ref
// name, an option or uppercase hex is not, so none reaches git as a commit.
func TestIsObjectIDAcceptsOnlyFullLowercaseHex(t *testing.T) {
	sha1, sha256 := strings.Repeat("a1", 20), strings.Repeat("0f", 32)
	for _, s := range []string{sha1, sha256} {
		if !IsObjectID(s) {
			t.Fatalf("%q is a full object id", s)
		}
	}
	for _, s := range []string{"", "HEAD", "main", sha1[:39], sha1 + "0", sha256[:63], strings.ToUpper(sha1),
		"--output=" + sha1[:31], sha1[:39] + "g", sha1[:36] + "^{t}"} {
		if IsObjectID(s) {
			t.Fatalf("%q is not a full object id", s)
		}
	}
}

// The forge reads the status of a change's workspace while the person or
// agent working in it may be committing: the change page asks every few
// seconds. A status read that refreshes the index takes .git/index.lock and
// a commit landing at that moment fails with "index.lock exists". The
// forge's git takes no optional locks, so its reads leave the index alone.
func TestAStatusReadLeavesTheIndexAlone(t *testing.T) {
	dir := repo(t)
	// Stale stat information makes an ordinary status refresh and rewrite
	// the index under its lock.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "a.txt"), later, later); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(dir, ".git", "index")
	before, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if staged, err := Staged(t.Context(), dir); err != nil || staged {
		t.Fatalf("an unchanged file reads as staged: %v %v", staged, err)
	}
	after, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("a status read rewrote the index, so it took index.lock")
	}
}

// A git run that fails returns its stdout untouched and carries git's
// stderr on the exit error, as exec's Output does; a run that succeeds
// returns stdout alone.
func TestOutputBytesCarriesStderrOnFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := OutputBytes(context.Background(), dir, nil, "rev-parse", "--verify", "no-such-ref")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || !strings.Contains(string(exit.Stderr), "not a git repository") {
		t.Fatalf("a failure's stderr: %v", err)
	}
	out, err := OutputBytes(context.Background(), dir, nil, "--version")
	if err != nil || !strings.HasPrefix(string(out), "git version") {
		t.Fatalf("a success's stdout: %q %v", out, err)
	}
}
