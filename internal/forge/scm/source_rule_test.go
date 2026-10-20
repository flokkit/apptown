package scm

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"flokkit.io/forge/internal/gitexec"
)

// What is not source is anything a result changes under .apptown (added,
// edited, deleted, or moved out) except the native app contract; changes
// elsewhere are source.
func TestNonSourceIsEverythingUnderForgeButTheContract(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := gitexec.Output(t.Context(), repo, append([]string{"-c", "user.name=t", "-c", "user.email=t@example.org"}, args...)...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return out
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	for _, name := range []string{"src.txt", ".apptown/app.yaml", ".apptown/old.json", ".apptown/moved.txt"} {
		write(name, name+"\n")
	}
	git("add", "-A", "-f")
	git("commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	write("src.txt", "changed\n")
	write(".apptown/app.yaml", "changed\n")
	write(".apptown/runtime.json", "{}\n")
	write(".apptown/cache/item", "x\n")
	git("rm", "-q", ".apptown/old.json")
	git("mv", ".apptown/moved.txt", "moved.txt")
	git("add", "-A", "-f")
	git("commit", "-qm", "result")
	got, err := nonSourceIn(context.Background(), repo, base+".."+git("rev-parse", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".apptown/cache/item", ".apptown/moved.txt", ".apptown/old.json", ".apptown/runtime.json"}
	if slices.Sort(got); !slices.Equal(got, want) {
		t.Fatalf("not source: %q, want %q", got, want)
	}
}

// The rule reads a result's whole history, not its last tree: a runtime file
// added in one commit and removed in the next is still not source, as is
// one brought in through a merge. The .apptown entry itself (a file or link in
// its place), and .apptown in another case, are not source either.
func TestNonSourceReadsEveryNewCommit(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := gitexec.Output(t.Context(), repo, append([]string{"-c", "user.name=t", "-c", "user.email=t@example.org"}, args...)...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return out
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("a.txt", "a\n")
	git("add", "-A")
	git("commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	write(".apptown/runtime.json", "{}\n")
	git("add", "-A", "-f")
	git("commit", "-qm", "adds a runtime file")
	git("rm", "-q", "-r", ".apptown")
	write(".FORGE/other.json", "{}\n")
	git("add", "-A", "-f")
	git("commit", "-qm", "removes it; another case")
	git("checkout", "-q", "-b", "side", base)
	write(".apptown", "a file in the directory's place\n")
	git("add", "-A", "-f")
	git("commit", "-qm", "replaces .apptown")
	git("checkout", "-q", "main")
	git("merge", "-q", "--no-edit", "side")
	got, err := nonSourceIn(context.Background(), repo, base+".."+git("rev-parse", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".FORGE/other.json", ".apptown", ".apptown/runtime.json"}
	if slices.Sort(got); !slices.Equal(got, want) {
		t.Fatalf("not source: %q, want %q", got, want)
	}
}
