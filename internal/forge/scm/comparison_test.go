package scm

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/execfile"

	"flokkit.io/forge/internal/gitexec"
)

func TestComparisonRepositoryFallsBackOnlyForAnAbsentWorkspace(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"absent", "empty", "file", "broken symlink", "unreadable metadata", "clone", "worktree"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			workspace := filepath.Join(root, "workspace")
			project := filepath.Join(root, "project")
			var err error
			switch kind {
			case "absent":
			case "file":
				err = os.WriteFile(workspace, []byte("broken"), 0600)
			case "broken symlink":
				err = os.Symlink(filepath.Join(root, "gone"), workspace)
			default:
				err = os.Mkdir(workspace, 0700)
				if err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "unreadable metadata":
					err = os.Symlink(".git", filepath.Join(workspace, ".git")) // ELOOP even when tests run as root
				case "clone":
					err = os.Mkdir(filepath.Join(workspace, ".git"), 0700)
				case "worktree":
					err = os.WriteFile(filepath.Join(workspace, ".git"), []byte("gitdir: elsewhere"), 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := ComparisonRepository(context.Background(), project, workspace)
			switch kind {
			case "absent":
				if err != nil || got != project {
					t.Fatalf("absent: %q %v", got, err)
				}
			case "clone", "worktree":
				if err != nil || got != workspace {
					t.Fatalf("workspace: %q %v", got, err)
				}
			default:
				if err == nil || got != "" {
					t.Fatalf("damaged workspace concealed: %q %v", got, err)
				}
			}
		})
	}
}

// Behind answers from Git: the project's branch has the saved commit and the
// folder does not. A commit or branch a repository lacks is an answer; Git
// failing to answer is an error, never read as either.
func TestBehindAnswersOnlyWhatGitAnswers(t *testing.T) {
	t.Parallel()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := gitexec.Run(t.Context(), dir, append([]string{"-c", "user.name=t", "-c", "user.email=t@x"}, args...)...)
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return out
	}
	setup := func(t *testing.T) (project, folder, first, second string) {
		root := t.TempDir()
		project, folder = filepath.Join(root, "project"), filepath.Join(root, "folder")
		git(root, "init", "-q", "-b", "change", project)
		git(project, "commit", "-q", "--allow-empty", "-m", "first")
		first = git(project, "rev-parse", "HEAD")
		forgeClone(t, project, folder, "change")
		git(project, "commit", "-q", "--allow-empty", "-m", "second")
		second = git(project, "rev-parse", "HEAD")
		return
	}
	mark := func(t *testing.T, project, folder, branch, commit string) {
		if err := MarkSaved(context.Background(), folder, project, branch, commit); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name   string
		mark   func(first, second string) (branch, commit string)
		behind bool
	}{
		{"a saved commit the folder lacks", func(_, second string) (string, string) { return "change", second }, true},
		{"a saved commit the folder has", func(first, _ string) (string, string) { return "change", first }, false},
		{"a commit no repository has", func(string, string) (string, string) {
			return "change", "1234567890123456789012345678901234567890"
		}, false},
		{"a branch the project lacks", func(_, second string) (string, string) { return "gone", second }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			project, folder, first, second := setup(t)
			branch, commit := c.mark(first, second)
			mark(t, project, folder, branch, commit)
			if behind, err := Behind(context.Background(), folder, project); err != nil || behind != c.behind {
				t.Fatalf("behind = %v, %v; want %v", behind, err, c.behind)
			}
		})
	}
	t.Run("Git failing to answer", func(t *testing.T) {
		project, folder, _, second := setup(t)
		mark(t, project, folder, "change", second)
		if err := os.WriteFile(filepath.Join(project, ".git", "config"), []byte("[broken\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if behind, err := Behind(context.Background(), folder, project); err == nil {
			t.Fatalf("a Git failure read as behind = %v", behind)
		}
	})
}

// forgeClone makes folder a clone of project's branch as AppTown's own
// clone is made (workflow's EnsureClone): no remote, AppTown's identity.
func forgeClone(t *testing.T, project, folder, branch string) {
	t.Helper()
	if project == "" || folder == "" {
		t.Fatal("forgeClone needs a project and a folder")
	}
	for _, args := range [][]string{
		{"clone", "-q", "--no-checkout", project, folder},
		{"-C", folder, "checkout", "-q", "-B", branch, "origin/" + branch},
		{"-C", folder, "remote", "remove", "origin"},
		{"-C", folder, "config", "user.name", gitexec.FolderUserName},
		{"-C", folder, "config", "user.email", gitexec.FolderUserEmail},
	} {
		if out, err := gitexec.Run(t.Context(), filepath.Dir(folder), args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

// snapshot is every path under dir with its mode and content: what a
// comparison needs to show nothing under dir changed.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	if dir == "" {
		t.Fatal("snapshot needs a directory")
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
		entry := info.Mode().String()
		if info.Mode().IsRegular() {
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

// The saved record is kept beside the folder, never in it: recording and
// clearing it writes nothing under the folder, whatever its .git holds.
func TestTheSavedRecordIsKeptOutsideTheFolder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	project, folder := filepath.Join(root, "project"), filepath.Join(root, "workspaces", "change-7")
	if out, err := gitexec.Run(t.Context(), root, "init", "-q", "-b", "change", project); err != nil {
		t.Fatal(err, out)
	}
	if out, err := gitexec.Run(t.Context(), project, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "first"); err != nil {
		t.Fatal(err, out)
	}
	if err := os.MkdirAll(filepath.Dir(folder), 0o755); err != nil {
		t.Fatal(err)
	}
	forgeClone(t, project, folder, "change")
	commit, _ := gitexec.Output(t.Context(), project, "rev-parse", "HEAD")
	before := snapshot(t, folder)
	if err := MarkSaved(context.Background(), folder, project, "change", commit); err != nil {
		t.Fatal(err)
	}
	record, err := os.ReadFile(filepath.Join(root, "workspaces", ".saved", "change-7"))
	if err != nil || string(record) != commit+" change\n" {
		t.Fatalf("the record beside the folder: %q %v", record, err)
	}
	if err := ClearSaved(folder); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "workspaces", ".saved", "change-7")); !os.IsNotExist(err) {
		t.Fatalf("a cleared record remains: %v", err)
	}
	after := snapshot(t, folder)
	if len(after) != len(before) {
		t.Fatalf("recording a save changed the folder: %d paths, then %d", len(before), len(after))
	}
	for path, entry := range before {
		if after[path] != entry {
			t.Fatalf("recording a save changed %s", path)
		}
	}
}

// Behind asks the project's repository first. A record it holds, for a
// folder the apptown runs no Git in, is behind, and no Git ran in the folder;
// a record it does not hold is not behind, again with no Git in the folder.
// Git is watched through PATH, so the test changes the process's
// environment and does not run in parallel.
func TestBehindRunsNoGitInAFolderItWillNotTrust(t *testing.T) {
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, held := range []bool{true, false} {
		t.Run(fmt.Sprintf("held=%t", held), func(t *testing.T) {
			root := t.TempDir()
			project, folder := filepath.Join(root, "project"), filepath.Join(root, "folder")
			if out, err := gitexec.Run(t.Context(), root, "init", "-q", "-b", "change", project); err != nil {
				t.Fatal(err, out)
			}
			if out, err := gitexec.Run(t.Context(), project, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "first"); err != nil {
				t.Fatal(err, out)
			}
			forgeClone(t, project, folder, "change")
			if out, err := gitexec.Run(t.Context(), project, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "second"); err != nil {
				t.Fatal(err, out)
			}
			saved, _ := gitexec.Output(t.Context(), project, "rev-parse", "HEAD")
			if !held {
				saved = "1234567890123456789012345678901234567890"
			}
			if err := MarkSaved(context.Background(), folder, project, "change", saved); err != nil {
				t.Fatal(err)
			}
			// An inert key AppTown's clone does not write.
			if out, err := gitexec.Run(t.Context(), folder, "config", "review.note", "example"); err != nil {
				t.Fatal(err, out)
			}
			bin, calls := filepath.Join(root, "bin"), filepath.Join(root, "calls")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			wrapper := "#!/bin/sh\necho \"$PWD $*\" >> " + calls + "\nexec " + real + " \"$@\"\n"
			if err := execfile.Write(filepath.Join(bin, "git"), []byte(wrapper), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			behind, err := Behind(context.Background(), folder, project)
			if err != nil || behind != held {
				t.Fatalf("behind = %v, %v; want %v", behind, err, held)
			}
			log, err := os.ReadFile(calls)
			if err != nil {
				t.Fatalf("Git was not watched: %v", err)
			}
			if !strings.Contains(string(log), project) {
				t.Fatalf("the project's repository was not asked: %s", log)
			}
			// Reading the folder's config file as data (git config --file,
			// run elsewhere) runs nothing in the folder; any other mention
			// of the folder, or a Git started inside it, would.
			inside, _ := filepath.EvalSymlinks(folder)
			for _, call := range strings.Split(strings.TrimSpace(string(log)), "\n") {
				pwd, args, _ := strings.Cut(call, " ")
				// AppTown's git starts with its own -c settings (gitexec);
				// what it was asked to do follows them.
				for strings.HasPrefix(args, "-c ") {
					_, rest, _ := strings.Cut(strings.TrimPrefix(args, "-c "), " ")
					args = rest
				}
				if at, _ := filepath.EvalSymlinks(pwd); strings.HasPrefix(at, inside) {
					t.Fatalf("Git ran in a folder AppTown does not trust: %s", call)
				}
				if strings.Contains(args, folder) && !strings.HasPrefix(args, "config --file "+filepath.Join(folder, ".git", "config")+" ") {
					t.Fatalf("Git ran on a folder AppTown does not trust: %s", call)
				}
			}
		})
	}
}
