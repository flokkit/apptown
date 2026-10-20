package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/execfile"

	"flokkit.io/forge/internal/gitexec"
)

// Refusing an existing Git worktree must preserve its metadata, committed and
// dirty work, and registration in the source repository. No database is involved.
func TestWorkspaceRejectsPointerWithoutMutatingIt(t *testing.T) {
	t.Parallel()
	// Keep the Git observations independent of GitWorkspace's command runner.
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := gitexec.Run(t.Context(), dir, args...)
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return out
	}
	g := GitWorkspace{DataDir: t.TempDir()}
	repo := t.TempDir()
	git(repo, "init", "-b", "main")
	git(repo, "config", "user.name", "test")
	git(repo, "config", "user.email", "test@example.invalid")
	git(repo, "commit", "--allow-empty", "-m", "initial")
	wt := filepath.Join(g.DataDir, "workspaces", "change-9")
	// The OLD scheme, built by hand: a worktree with a pointer .git file.
	git(repo, "worktree", "add", "-b", "apptown/change-9", wt)
	// One committed file and one dirty edit on top.
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(wt, "add", "a.txt")
	git(wt, "commit", "-m", "old-world commit")
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("dirty edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pointer, err := os.ReadFile(filepath.Join(wt, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.EnsureClone(context.Background(), CloneRequest{Repository: repo, Workspace: wt, Branch: "apptown/change-9"}); err == nil || !strings.Contains(err.Error(), "self-contained") {
		t.Fatalf("unsupported workspace must fail explicitly: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(wt, ".git"))
	if err != nil || string(after) != string(pointer) {
		t.Fatalf("workspace metadata changed: %q, %v", after, err)
	}
	if log := git(wt, "log", "-1", "--format=%s"); log != "old-world commit" {
		t.Fatalf("HEAD changed: %q", log)
	}
	b, _ := os.ReadFile(filepath.Join(wt, "a.txt"))
	if string(b) != "dirty edit\n" || git(wt, "status", "--porcelain") == "" {
		t.Fatalf("dirty edit changed: %q", b)
	}
	if out := git(repo, "worktree", "list"); !strings.Contains(out, "change-9") {
		t.Fatalf("worktree registration changed: %s", out)
	}
}

// A branch git would not take as a branch name is refused before any
// folder is made, even when the repository holds a ref of that name (git
// update-ref takes "-x" and "HEAD" under refs/heads, check-ref-format
// --branch does not).
func TestEnsureCloneRefusesANameThatIsNotABranchName(t *testing.T) {
	t.Parallel()
	g := GitWorkspace{DataDir: t.TempDir()}
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "initial"},
		{"update-ref", "refs/heads/-x", "HEAD"}, {"update-ref", "refs/heads/HEAD", "HEAD"}} {
		if out, err := gitexec.Run(t.Context(), repo, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	for _, branch := range []string{"-x", "HEAD", "change..1", "change.lock"} {
		wt := filepath.Join(g.DataDir, "workspaces", "change-1")
		if _, err := g.EnsureClone(context.Background(), CloneRequest{Repository: repo, Workspace: wt, Branch: branch}); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q is not a valid branch name", branch)) {
			t.Fatalf("branch %q: %v", branch, err)
		}
		if _, err := os.Stat(wt); !os.IsNotExist(err) {
			t.Fatalf("branch %q made a folder: %v", branch, err)
		}
	}
}

// An existing folder is taken only on its branch, and its tip is that
// branch's commit. A folder someone switched to another branch, or
// detached, is refused and left where it is: never switched back.
func TestAnExistingFolderIsTakenOnlyOnItsBranch(t *testing.T) {
	t.Parallel()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := gitexec.Run(t.Context(), dir, append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return out
	}
	g := GitWorkspace{DataDir: t.TempDir()}
	repo := t.TempDir()
	git(repo, "init", "-q", "-b", "main")
	git(repo, "commit", "-q", "--allow-empty", "-m", "initial")
	git(repo, "branch", "apptown/change-1")
	want := git(repo, "rev-parse", "refs/heads/apptown/change-1")
	wt := filepath.Join(g.DataDir, "workspaces", "change-1")
	in := CloneRequest{Repository: repo, Workspace: wt, Branch: "apptown/change-1"}
	for _, created := range []bool{true, false} {
		got, err := g.EnsureClone(context.Background(), in)
		if err != nil || got.Tip != want || got.Created != created {
			t.Fatalf("ensure (created %v) = %+v %v, want tip %s", created, got, err, want)
		}
	}
	git(wt, "commit", "-q", "--allow-empty", "-m", "the folder's own")
	if got, err := g.EnsureClone(context.Background(), in); err != nil || got.Tip != git(wt, "rev-parse", "HEAD") || got.Tip == want {
		t.Fatalf("a folder ahead of the repository = %+v %v", got, err)
	}
	for _, move := range []struct {
		args []string
		on   string
	}{{[]string{"checkout", "-q", "-b", "other"}, `"other"`}, {[]string{"checkout", "-q", "--detach"}, `"HEAD"`}} {
		git(wt, move.args...)
		before := git(wt, "rev-parse", "--symbolic-full-name", "HEAD")
		_, err := g.EnsureClone(context.Background(), in)
		if err == nil || !strings.Contains(err.Error(), "workspace is on "+move.on+`, expected "apptown/change-1"`) {
			t.Fatalf("after %v: %v", move.args, err)
		}
		if after := git(wt, "rev-parse", "--symbolic-full-name", "HEAD"); after != before {
			t.Fatalf("after %v the folder moved from %q to %q", move.args, before, after)
		}
	}
}

// A new folder is the clone AppTown always made: clone, then the branch
// checked out, origin removed and the folder identity configured, in four
// commands after the clone. Clone now writes the identity itself. Whether
// the branch exists yet or starts from main, the folder's config, refs,
// HEAD, index, files, Git metadata and reflog messages match what the old
// sequence makes, apart from Git's template files, which the new clone
// does not take. (The reflog's committer is the folder identity from the
// first entry now, rather than the machine's.)
func TestANewFolderIsTheCloneTheForgeAlwaysMade(t *testing.T) {
	t.Parallel()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := gitexec.Run(t.Context(), dir, args...)
		if err != nil {
			t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
		}
		return out
	}
	repo := t.TempDir()
	git(repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", "a.txt")
	git(repo, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "first")
	git(repo, "tag", "v1")
	git(repo, "branch", "apptown/change-1")
	git(repo, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "second")
	git(repo, "branch", "other")
	describe := func(dir string) string {
		t.Helper()
		var files []string
		err := filepath.WalkDir(filepath.Join(dir, ".git"), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, path)
			// The old clone also copied Git's template (sample hooks,
			// info/exclude, description); AppTown's takes none.
			if d.IsDir() || strings.HasPrefix(rel, ".git/objects/") || rel == ".git/index" ||
				strings.HasPrefix(rel, ".git/hooks/") || strings.HasPrefix(rel, ".git/info/") || rel == ".git/description" {
				return nil
			}
			files = append(files, rel)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		config := strings.Split(git(dir, "config", "--local", "--list"), "\n")
		slices.Sort(config)
		return strings.Join([]string{
			"config: " + strings.Join(config, " | "),
			"refs: " + git(dir, "for-each-ref", "--format=%(refname) %(objectname)"),
			"HEAD: " + git(dir, "symbolic-ref", "HEAD"),
			"index: " + git(dir, "ls-files", "--stage"),
			"status: " + git(dir, "status", "--porcelain", "--ignored"),
			"reflog: " + git(dir, "reflog", "--format=%gs"),
			"metadata: " + strings.Join(files, " "),
		}, "\n")
	}
	for _, branch := range []string{"apptown/change-1", "apptown/change-2"} {
		t.Run(branch, func(t *testing.T) {
			g := GitWorkspace{DataDir: t.TempDir()}
			wt := filepath.Join(g.DataDir, "workspaces", "change")
			got, err := g.EnsureClone(context.Background(), CloneRequest{Repository: repo, Workspace: wt, Branch: branch, DefaultBranch: "main"})
			if err != nil {
				t.Fatal(err)
			}
			old := filepath.Join(t.TempDir(), "old")
			git(filepath.Dir(old), "clone", "--quiet", "--no-hardlinks", "--no-checkout", repo, old)
			git(old, "checkout", "--quiet", "-B", branch, got.Tip)
			git(old, "remote", "remove", "origin")
			git(old, "config", "user.name", gitexec.FolderUserName)
			git(old, "config", "user.email", gitexec.FolderUserEmail)
			if now, then := describe(wt), describe(old); now != then {
				t.Fatalf("the folder is\n%s\nthe old sequence made\n%s", now, then)
			}
		})
	}
}

// A personal Git template (GIT_TEMPLATE_DIR, or init.templateDir in the
// global config) that sets an identity and a hook gives a new folder
// neither: its config has exactly the folder identity, one name and one
// email, and AppTown accepts it as its own clone (CheckFolder). Not
// parallel: it sets the process's environment.
func TestANewFolderTakesNothingFromAPersonalGitTemplate(t *testing.T) {
	base := t.TempDir()
	template := filepath.Join(base, "template")
	for name, body := range map[string]string{"config": "[user]\n\tname = Personal Name\n\temail = personal@example.invalid\n", "hooks/pre-commit": "#!/bin/sh\nexit 99\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(template, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := execfile.Write(filepath.Join(template, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	global := filepath.Join(base, "gitconfig")
	if err := os.WriteFile(global, []byte("[init]\n\ttemplateDir = "+template+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, env := range map[string][2]string{"GIT_TEMPLATE_DIR": {"GIT_TEMPLATE_DIR", template}, "init.templateDir": {"GIT_CONFIG_GLOBAL", global}} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(env[0], env[1])
			repo := filepath.Join(t.TempDir(), "repo")
			if out, err := gitexec.Run(t.Context(), filepath.Dir(repo), "init", "-q", "--template=", "-b", "main", repo); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if out, err := gitexec.Run(t.Context(), repo, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "base"); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			folder := filepath.Join(t.TempDir(), "change-1")
			if _, err := (GitWorkspace{}).EnsureClone(context.Background(), CloneRequest{Repository: repo, Workspace: folder, Branch: "work", DefaultBranch: "main"}); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{"user.name": gitexec.FolderUserName, "user.email": gitexec.FolderUserEmail} {
				if got, err := gitexec.Output(t.Context(), folder, "config", "--local", "--get-all", key); err != nil || got != want {
					t.Fatalf("%s is %q %v, want only %q", key, got, err, want)
				}
			}
			if err := gitexec.CheckFolder(t.Context(), folder); err != nil {
				t.Fatalf("AppTown refuses its own new folder: %v", err)
			}
		})
	}
}

// A writer's clone shares no object file with AppTown's repository and
// borrows none from it: overwriting every object file in the clone (as a
// process writing it could) leaves the repository whole. A clone made with
// hard links or alternates would corrupt it.
func TestAWritersCloneCannotChangeTheForgesRepository(t *testing.T) {
	t.Parallel()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := gitexec.Run(t.Context(), dir, args...)
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return out
	}
	g := GitWorkspace{DataDir: t.TempDir()}
	repo := t.TempDir()
	git(repo, "init", "-b", "main")
	git(repo, "config", "user.name", "test")
	git(repo, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("AppTown's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", "a.txt")
	git(repo, "commit", "-m", "initial")
	git(repo, "branch", "apptown/change-1")
	wt := filepath.Join(g.DataDir, "workspaces", "run-1")
	if _, err := g.EnsureClone(context.Background(), CloneRequest{Repository: repo, Workspace: wt, Branch: "apptown/change-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".git", "objects", "info", "alternates")); err == nil {
		t.Fatal("the clone borrows the repository's objects")
	}
	overwritten := 0
	err := filepath.WalkDir(filepath.Join(wt, ".git", "objects"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if err := os.Chmod(path, 0o644); err != nil {
			return err
		}
		overwritten++
		return os.WriteFile(path, []byte("written through the clone"), 0o644)
	})
	if err != nil || overwritten == 0 {
		t.Fatalf("overwriting the clone's objects: %d files, %v", overwritten, err)
	}
	git(repo, "fsck", "--strict")
	if got := git(repo, "show", "apptown/change-1:a.txt"); got != "AppTown's" {
		t.Fatalf("the repository's file now reads %q", got)
	}
}

// The apptown runs its own Git in a folder only when the folder's metadata is
// what EnsureClone makes (gitexec.CheckFolder): every clone EnsureClone
// makes passes that check and is brought forward by the confined
// fast-forward, whichever branch it checks out.
func TestAFolderEnsureCloneMakesIsOneTheForgeRunsGitIn(t *testing.T) {
	t.Parallel()
	for _, branch := range []string{"main", "apptown/change-1", "forge/new"} {
		t.Run(branch, func(t *testing.T) {
			t.Parallel()
			git := func(dir string, args ...string) string {
				t.Helper()
				if dir == "" {
					t.Fatal("git needs a directory")
				}
				out, err := gitexec.Run(t.Context(), dir, args...)
				if err != nil {
					t.Fatalf("git %v: %s: %v", args, out, err)
				}
				return out
			}
			g := GitWorkspace{DataDir: t.TempDir()}
			repo := t.TempDir()
			git(repo, "init", "-q", "-b", "main")
			git(repo, "config", "user.name", "test")
			git(repo, "config", "user.email", "test@example.invalid")
			git(repo, "commit", "-q", "--allow-empty", "-m", "initial")
			git(repo, "branch", "apptown/change-1")
			wt := filepath.Join(g.DataDir, "workspaces", "run-1")
			if _, err := g.EnsureClone(context.Background(), CloneRequest{Repository: repo, Workspace: wt, Branch: branch, DefaultBranch: "main"}); err != nil {
				t.Fatal(err)
			}
			if err := gitexec.CheckFolder(t.Context(), wt); err != nil {
				t.Fatalf("AppTown's own clone is refused: %v", err)
			}
			// The branch as published moves on; the folder follows it.
			git(repo, "checkout", "-q", "-B", branch)
			git(repo, "commit", "-q", "--allow-empty", "-m", "published")
			tip := git(repo, "rev-parse", "HEAD")
			f := gitexec.LockFolder(wt)
			defer f.Release()
			if err := f.FastForward(context.Background(), repo, branch); err != nil {
				t.Fatal(err)
			}
			if got := git(wt, "rev-parse", "HEAD"); got != tip {
				t.Fatalf("the folder is at %s, want %s", got, tip)
			}
		})
	}
}

// A repository that still keeps AppTown's files in .forge/ takes no new
// workspace until the upgrade renames it; once .apptown/ is there, it does.
func TestANewWorkspaceIsRefusedUntilTheRepositoryIsUpgraded(t *testing.T) {
	t.Parallel()
	g := GitWorkspace{DataDir: t.TempDir()}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := gitexec.Run(t.Context(), repo, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	write := func(rel string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(repo, filepath.Dir(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, rel), []byte("app: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write(".forge/app.yaml")
	git("add", "-f", "-A")
	git("-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "before")
	wt := filepath.Join(g.DataDir, "workspaces", "change-1")
	if _, err := g.EnsureClone(t.Context(), CloneRequest{Repository: repo, Workspace: wt, Branch: "apptown/change-1"}); !errors.Is(err, ErrNotUpgraded) {
		t.Fatalf("before the upgrade: %v, want ErrNotUpgraded", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("a refused workspace made a folder: %v", err)
	}
	git("mv", ".forge", ".apptown")
	git("-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-qm", "upgrade")
	if _, err := g.EnsureClone(t.Context(), CloneRequest{Repository: repo, Workspace: wt, Branch: "apptown/change-1"}); err != nil {
		t.Fatalf("after the upgrade: %v", err)
	}
}
