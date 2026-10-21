package gitexec

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/execfile"
)

// gitAt runs git in dir for a fixture; an empty dir is refused, so a
// fixture never runs Git wherever the test happens to be.
func gitAt(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if dir == "" {
		t.Fatal("gitAt needs a directory")
	}
	out, err := Run(t.Context(), dir, append([]string{"-c", "user.name=t", "-c", "user.email=t@example.org"}, args...)...)
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return out
}

// folderWorld is a hub with a branch at B, a folder cloned as AppTown
// clones (workflow's EnsureClone) still at A, and a repository outside the
// folder that hostile metadata can point at.
type folderWorld struct {
	root, hub, folder, outside, fired string
	a, b                              string
}

// folderWorldTemplate holds newFolderWorld's three repositories under one
// root; folderWorldA and folderWorldB are the hub's commits in every copy.
var folderWorldA, folderWorldB string
var folderWorldTemplate = &repoTemplate{build: func(tb testing.TB, root string) {
	t := tb.(*testing.T)
	hub, folder, outside := filepath.Join(root, "hub"), filepath.Join(root, "workspaces", "run-1"), filepath.Join(root, "outside")
	gitAt(t, root, "init", "-q", "-b", "work", hub)
	if err := os.WriteFile(filepath.Join(hub, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, hub, "add", "a.txt")
	gitAt(t, hub, "commit", "-qm", "A")
	folderWorldA = gitAt(t, hub, "rev-parse", "HEAD")
	if err := os.MkdirAll(filepath.Dir(folder), 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, root, "clone", "-q", "--no-hardlinks", "--no-checkout", hub, folder)
	gitAt(t, folder, "checkout", "-q", "-B", "work", folderWorldA)
	gitAt(t, folder, "remote", "remove", "origin")
	gitAt(t, folder, "config", "user.name", FolderUserName)
	gitAt(t, folder, "config", "user.email", FolderUserEmail)
	if err := os.WriteFile(filepath.Join(hub, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, hub, "add", "b.txt")
	gitAt(t, hub, "commit", "-qm", "B")
	folderWorldB = gitAt(t, hub, "rev-parse", "HEAD")
	gitAt(t, root, "init", "-q", "-b", "main", outside)
	gitAt(t, outside, "commit", "-q", "--allow-empty", "-m", "outside")
}}

// newFolderWorld is the test's own copy of folderWorldTemplate: a hub on
// work at B, a folder cloned from it at A with no remote, and an unrelated
// repository outside.
func newFolderWorld(t *testing.T) folderWorld {
	t.Helper()
	root := filepath.Join(t.TempDir(), "world")
	folderWorldTemplate.copyTo(t, root)
	return folderWorld{root: root, hub: filepath.Join(root, "hub"), folder: filepath.Join(root, "workspaces", "run-1"),
		outside: filepath.Join(root, "outside"), fired: filepath.Join(root, "fired"), a: folderWorldA, b: folderWorldB}
}

// tree is every path under dir with its mode, and a file's content or a
// link's target: two equal trees had nothing written under them.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	if dir == "" {
		t.Fatal("tree needs a directory")
	}
	got := map[string]string{}
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := info.Mode().String() + " " + info.ModTime().String()
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entry += " -> " + target
		case info.Mode().IsRegular():
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry += " " + string(body)
		}
		got[path] = entry
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func sameTree(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	for path, entry := range before {
		if after[path] != entry {
			t.Fatalf("%s changed: %s", what, path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Fatalf("%s gained %s", what, path)
		}
	}
}

func headOf(t *testing.T, folder string) string {
	t.Helper()
	return gitAt(t, folder, "rev-parse", "HEAD")
}

// A folder cloned as AppTown clones it is brought forward, answers what
// it holds, and resets with its clean, keeping what the caller keeps.
func TestAForgeCloneIsBroughtForwardAndReset(t *testing.T) {
	t.Parallel()
	w := newFolderWorld(t)
	if err := CheckFolder(t.Context(), w.folder); err != nil {
		t.Fatalf("AppTown's own clone is refused: %v", err)
	}
	if held, err := FolderHolds(context.Background(), w.folder, "work", w.b); err != nil || held {
		t.Fatalf("a folder at A holds B: %v %v", held, err)
	}
	f := LockFolder(w.folder)
	defer f.Release()
	if err := f.FastForward(context.Background(), w.hub, "work"); err != nil {
		t.Fatal(err)
	}
	if got := headOf(t, w.folder); got != w.b {
		t.Fatalf("the folder is at %s, want B %s", got, w.b)
	}
	if held, err := FolderHolds(context.Background(), w.folder, "work", w.b); err != nil || !held {
		t.Fatalf("a folder brought to B does not hold it: %v %v", held, err)
	}
	for _, name := range []string{"untracked.txt", filepath.Join(".apptown", "kept.txt")} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(w.folder, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(w.folder, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Reset(context.Background(), w.a, ".apptown"); err != nil {
		t.Fatal(err)
	}
	if got := headOf(t, w.folder); got != w.a {
		t.Fatalf("the folder is at %s, want A %s", got, w.a)
	}
	for name, want := range map[string]bool{"b.txt": false, "untracked.txt": false, filepath.Join(".apptown", "kept.txt"): true} {
		if _, err := os.Stat(filepath.Join(w.folder, name)); (err == nil) != want {
			t.Errorf("%s present = %v after the reset, want %v", name, err == nil, want)
		}
	}
}

// Any config entry AppTown's clone does not write refuses the folder, even
// an inert one, and nothing is run or written.
func TestAnInertExtraKeyRefusesTheFolder(t *testing.T) {
	t.Parallel()
	w := newFolderWorld(t)
	gitAt(t, w.folder, "config", "review.note", "example")
	before := tree(t, w.folder)
	f := LockFolder(w.folder)
	defer f.Release()
	if err := f.FastForward(context.Background(), w.hub, "work"); !errors.Is(err, ErrFolderRefused) || !strings.Contains(err.Error(), "review.note") {
		t.Fatalf("an extra key: %v", err)
	}
	if _, err := FolderHolds(context.Background(), w.folder, "work", w.a); !errors.Is(err, ErrFolderRefused) {
		t.Fatalf("asking a refused folder: %v", err)
	}
	sameTree(t, "the refused folder", before, tree(t, w.folder))
}

// A folder cloned before the AppTown rename was given the email
// helper@forge.local; its config is history and the folder is still
// AppTown's own clone. Any other email is not.
func TestAFolderClonedBeforeTheRenameKeepsItsEmail(t *testing.T) {
	t.Parallel()
	w := newFolderWorld(t)
	gitAt(t, w.folder, "config", "user.email", "helper@forge.local")
	f := LockFolder(w.folder)
	if err := f.FastForward(context.Background(), w.hub, "work"); err != nil {
		t.Fatalf("a folder with the email from before the rename: %v", err)
	}
	f.Release()
	gitAt(t, w.folder, "config", "user.email", "someone@example.com")
	f = LockFolder(w.folder)
	defer f.Release()
	if err := f.FastForward(context.Background(), w.hub, "work"); !errors.Is(err, ErrFolderRefused) || !strings.Contains(err.Error(), "user.email") {
		t.Fatalf("a folder with another email: %v", err)
	}
}

// hostile is metadata a local writer can leave in a folder so that Git run
// there would start a command of its choosing or read and write outside the
// folder. Each is refused before any Git runs.
var hostile = []struct {
	name string
	make func(t *testing.T, w folderWorld)
}{
	{"a hook", func(t *testing.T, w folderWorld) {
		for _, hook := range []string{"post-merge", "post-checkout", "reference-transaction"} {
			writeFile(t, filepath.Join(w.folder, ".git", "hooks", hook), "#!/bin/sh\ntouch "+w.fired+"\n", 0o755)
		}
	}},
	{"a filter driver", func(t *testing.T, w folderWorld) {
		writeFile(t, filepath.Join(w.folder, ".git", "info", "attributes"), "* filter=x\n", 0o644)
		gitAt(t, w.folder, "config", "filter.x.smudge", "touch "+w.fired+"; cat")
		gitAt(t, w.folder, "config", "filter.x.clean", "touch "+w.fired+"; cat")
	}},
	{"a diff helper", func(t *testing.T, w folderWorld) {
		gitAt(t, w.folder, "config", "diff.external", "touch "+w.fired)
	}},
	{"core.fsmonitor", func(t *testing.T, w folderWorld) {
		script := filepath.Join(w.root, "fsmonitor")
		writeFile(t, script, "#!/bin/sh\ntouch "+w.fired+"\n", 0o755)
		gitAt(t, w.folder, "config", "core.fsmonitor", script)
	}},
	{"core.worktree", func(t *testing.T, w folderWorld) {
		gitAt(t, w.folder, "config", "core.worktree", w.outside)
	}},
	{"an include.path", func(t *testing.T, w folderWorld) {
		hooks := filepath.Join(w.root, "hooks")
		writeFile(t, filepath.Join(hooks, "post-merge"), "#!/bin/sh\ntouch "+w.fired+"\n", 0o755)
		writeFile(t, filepath.Join(hooks, "post-checkout"), "#!/bin/sh\ntouch "+w.fired+"\n", 0o755)
		writeFile(t, filepath.Join(w.root, "included"), "[core]\n\thooksPath = "+hooks+"\n\tworktree = "+w.outside+"\n", 0o644)
		gitAt(t, w.folder, "config", "include.path", filepath.Join(w.root, "included"))
	}},
	{"a .git link", func(t *testing.T, w folderWorld) {
		moved := filepath.Join(w.outside, "borrowed.git")
		if err := os.Rename(filepath.Join(w.folder, ".git"), moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(moved, filepath.Join(w.folder, ".git")); err != nil {
			t.Fatal(err)
		}
	}},
	{"a linked objects", linkedMetadata("objects")},
	{"a linked refs", linkedMetadata("refs")},
	{"a linked logs", linkedMetadata("logs")},
	{"a commondir", func(t *testing.T, w folderWorld) {
		writeFile(t, filepath.Join(w.folder, ".git", "commondir"), filepath.Join(w.outside, ".git")+"\n", 0o644)
	}},
	{"an alternates", func(t *testing.T, w folderWorld) {
		writeFile(t, filepath.Join(w.folder, ".git", "objects", "info", "alternates"), filepath.Join(w.outside, ".git", "objects")+"\n", 0o644)
	}},
}

// linkedMetadata moves one of the folder's metadata directories into the
// outside repository and links it back.
func linkedMetadata(name string) func(t *testing.T, w folderWorld) {
	return func(t *testing.T, w folderWorld) {
		moved := filepath.Join(w.outside, ".git", "borrowed-"+name)
		if err := os.Rename(filepath.Join(w.folder, ".git", name), moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(moved, filepath.Join(w.folder, ".git", name)); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := execfile.Write(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// Each hostile folder is refused for both the fast-forward and the reset:
// the command never fires, and neither the folder nor the repository
// outside it changes.
func TestAHostileFolderIsRefusedAndNothingRuns(t *testing.T) {
	t.Parallel()
	operations := map[string]func(f *Folder, w folderWorld) error{
		"fast-forward": func(f *Folder, w folderWorld) error { return f.FastForward(context.Background(), w.hub, "work") },
		"reset":        func(f *Folder, w folderWorld) error { return f.Reset(context.Background(), w.b) },
	}
	for _, h := range hostile {
		for op, do := range operations {
			t.Run(h.name+"/"+op, func(t *testing.T) {
				t.Parallel()
				w := newFolderWorld(t)
				h.make(t, w)
				folder, outside := tree(t, w.folder), tree(t, w.outside)
				f := LockFolder(w.folder)
				defer f.Release()
				if err := do(f, w); !errors.Is(err, ErrFolderRefused) {
					t.Fatalf("a hostile folder was not refused: %v", err)
				}
				if _, err := FolderHolds(context.Background(), w.folder, "work", w.a); !errors.Is(err, ErrFolderRefused) {
					t.Fatalf("a hostile folder was asked: %v", err)
				}
				if _, err := os.Stat(w.fired); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("the folder's command fired: %v", err)
				}
				sameTree(t, "the refused folder", folder, tree(t, w.folder))
				sameTree(t, "the repository outside the folder", outside, tree(t, w.outside))
			})
		}
	}
}

// GIT_DIR and GIT_OBJECT_DIRECTORY in AppTown's own environment do not
// reach the folder's Git: a clean folder is brought forward and reset in
// itself, and the repository they name does not change. The test sets the
// process's environment, so it does not run in parallel.
func TestTheForgesGitEnvironmentDoesNotReachTheFolder(t *testing.T) {
	for _, op := range []string{"fast-forward", "reset"} {
		t.Run(op, func(t *testing.T) {
			w := newFolderWorld(t)
			outside := tree(t, w.outside)
			t.Setenv("GIT_DIR", filepath.Join(w.outside, ".git"))
			t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(w.outside, ".git", "objects"))
			f := LockFolder(w.folder)
			defer f.Release()
			var err error
			want := w.b
			if op == "fast-forward" {
				err = f.FastForward(context.Background(), w.hub, "work")
			} else {
				if err = f.FastForward(context.Background(), w.hub, "work"); err == nil {
					err = f.Reset(context.Background(), w.a)
				}
				want = w.a
			}
			if err != nil {
				t.Fatal(err)
			}
			sameTree(t, "the repository the environment names", outside, tree(t, w.outside))
			os.Unsetenv("GIT_DIR")
			os.Unsetenv("GIT_OBJECT_DIRECTORY")
			if got := headOf(t, w.folder); got != want {
				t.Fatalf("the folder is at %s, want %s", got, want)
			}
		})
	}
}

// A folder check its caller stopped says nothing about the folder: it is
// never a refusal, which reads as a folder left behind.
//
// Kills: a stopped config read reported as a refused folder.
func TestAStoppedFolderCheckIsNotARefusal(t *testing.T) {
	t.Parallel()
	w := newFolderWorld(t)
	stopped, cancel := context.WithCancel(t.Context())
	cancel()
	if err := CheckFolder(stopped, w.folder); err == nil || errors.Is(err, ErrFolderRefused) {
		t.Fatalf("a stopped check of AppTown's own clone: %v, want an error that is not a refusal", err)
	}
}

// A stopped caller is told it got no answer, never "nothing staged" (which
// reads as nothing to commit).
//
// Kills: Staged reading a failed read as nothing staged.
func TestAStoppedCallerGetsNoAnswerAboutStagedWork(t *testing.T) {
	t.Parallel()
	w := newFolderWorld(t)
	if err := os.WriteFile(filepath.Join(w.folder, "uncommitted.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := Run(t.Context(), w.folder, "add", "uncommitted.txt"); err != nil {
		t.Fatal(out)
	}
	stopped, cancel := context.WithCancel(t.Context())
	cancel()
	if staged, err := Staged(stopped, w.folder); err == nil {
		t.Fatalf("a stopped caller got an answer (%v) instead of an error", staged)
	}
}
