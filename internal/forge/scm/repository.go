package scm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/trailer"
)

// StageAll stages everything EXCEPT .apptown runtime — by pathspec, not by
// trusting .gitignore. Change 99's build agent replaced the project's
// .gitignore with its own; the engine's add -A then committed pi-home
// (docker credentials, pi auth), sessions and ambient config into branch
// history and pushed it. The contract file is the one .apptown path that
// belongs in the tree, re-added explicitly.
//
// A failure is returned: source git could not stage is still source, and a
// caller reading an unchanged index after a failed add would take it for
// none (and a desktop sync would then reset it away).
func StageAll(ctx context.Context, wt string) error {
	// .forge is the runtime folder's name before the AppTown rename: a
	// workspace from then can still hold its credentials and sessions, so
	// it stays excluded for good (a retired secret path, not a reader).
	if out, err := gitexec.Run(ctx, wt, "add", "-A", "--", ":(exclude).apptown", ":(exclude).forge"); err != nil {
		return fmt.Errorf("staging the folder's source: %w: %s", err, out)
	}
	// Only the native app contract belongs in source; generated runtime files
	// and retired contract names are never force-added. The first add cannot
	// carry it (-f would force every ignored file in, and an exclude has no
	// exception). A contract on disk is force-added; with none, any the index
	// still holds has its deletion staged (git add would refuse a path that
	// matches nothing).
	//
	// A folder made before the AppTown rename can still hold its contract at
	// the old path, .forge/app.yaml, until it catches up with the upgrade's
	// commit; that file is source too, so an edit to it is committed rather
	// than lost.
	for _, contract := range []string{".apptown/app.yaml", ".forge/app.yaml"} {
		args := []string{"add", "-f", "--", contract}
		if _, err := os.Lstat(filepath.Join(wt, filepath.FromSlash(contract))); errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			args = []string{"rm", "--cached", "--ignore-unmatch", "--quiet", "--", contract}
		} else if err != nil {
			return fmt.Errorf("reading the folder's app contract: %w", err)
		}
		if out, err := gitexec.Run(ctx, wt, args...); err != nil {
			return fmt.Errorf("staging the folder's app contract: %w: %s", err, out)
		}
	}
	return nil
}

// nonSourceIn lists what the commits revs selects change that is not
// source: the .apptown entry itself (replaced by a file, a link or a
// submodule) and anything under it except the native app contract, the
// same rule StageAll applies when AppTown commits locally. Each commit is
// read against each of its parents, not only the last tree: a commit that
// adds a runtime file puts it in the branch's history even when a later one
// removes it. .apptown is matched whatever its case, as a case-insensitive
// disk would.
func nonSourceIn(ctx context.Context, repo string, revs ...string) ([]string, error) {
	out, err := gitexec.HubOutput(ctx, repo, nil, append([]string{"log", "--no-ext-diff", "--no-textconv", "--format=", "--name-only", "--no-renames", "-m", "--root", "-z"}, revs...)...)
	if err != nil {
		return nil, fmt.Errorf("listing the new commits' changes: %w", err)
	}
	var paths []string
	for _, path := range strings.Split(string(out), "\x00") {
		if RuntimeOnly(path) && !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// NonSourceIn lists AppTown's runtime paths (RuntimeOnly) any commit
// the revisions select adds or changes, read commit by commit: what a move
// must never carry, by the same rule a publication applies.
func NonSourceIn(ctx context.Context, repo string, revs ...string) ([]string, error) {
	if len(revs) == 0 {
		return nil, nil
	}
	return nonSourceIn(ctx, repo, revs...)
}

// RuntimeOnly reports whether path is AppTown's runtime state in a working
// copy (.apptown, whatever its case), never source. The one exception is
// the application's native contract, .apptown/app.yaml, which is source.
// .forge, the folder's name before the AppTown rename, is held to the same
// rule for good: an old runtime file anywhere in history stays non-source,
// while the historical contract .forge/app.yaml stays source, so history
// from before the upgrade still publishes and exports.
func RuntimeOnly(path string) bool {
	top, _, _ := strings.Cut(path, "/")
	switch {
	case strings.EqualFold(top, ".apptown"):
		return path != ".apptown/app.yaml"
	case strings.EqualFold(top, ".forge"):
		return path != ".forge/app.yaml"
	}
	return false
}

// InstallNoPushHook makes the hub refuse every push but a Change ticket's
// (to its holding ref) and a step's scratch push (to its own ScratchRef,
// for checking only), each named by the git endpoint. The hub is the authority
// for main and is written ONLY by the landing contract (apptownd merges locally,
// it never pushes to the hub); workspaces may fetch (read) but never push
// (write). Client-side "fetch-only" config is a convention an agent could
// undo — this enforces it on the hub, so the only path to main stays the
// gated one. Idempotent. A folder that is not a repository has nothing to
// protect; any other failure is returned, since a hub without the hook
// accepts every push.
func InstallNoPushHook(hubDir string) error {
	if !gitexec.IsRepo(hubDir) {
		return nil
	}
	gitDir := filepath.Join(hubDir, ".git")
	if fi, err := os.Stat(gitDir); err != nil {
		return fmt.Errorf("installing the no-push hook in %s: %w", hubDir, err)
	} else if !fi.IsDir() {
		return fmt.Errorf("installing the no-push hook in %s: its .git is not a folder", hubDir)
	}
	hooks := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		return fmt.Errorf("installing the no-push hook in %s: %w", hubDir, err)
	}
	body := "#!/bin/sh\n" +
		"# AppTown: the hub is written only by the landing contract, never by a push —\n" +
		"# with ONE exception: a Change Git ticket (internal/forge/githttp.go) lets a\n" +
		"# local checkout push to the ticket's holding ref. AppTown then publishes\n" +
		"# it to the Change's branch if the ticket still holds the branch. This hook\n" +
		"# refuses early, with facts, a push that publication would refuse: one from\n" +
		"# a tip that has moved, or one that does not build on it. Workspaces fetch\n" +
		"# (read) but never push; the only path to main is the gated landing flow.\n" +
		"# A second exception: a step's runtime token may push its work in progress\n" +
		"# to its own scratch ref (scm.ScratchRef), for checking only. Nothing else.\n" +
		"zero=0000000000000000000000000000000000000000\n" +
		"if [ -n \"$APPTOWN_SCRATCH_REF\" ]; then\n" +
		"  while read old new ref; do\n" +
		"    if [ \"$ref\" != \"$APPTOWN_SCRATCH_REF\" ]; then\n" +
		"      echo \"apptown: this step may push only $APPTOWN_SCRATCH_REF, not $ref\" >&2; exit 1\n" +
		"    fi\n" +
		"    case \"$new\" in $zero) echo 'apptown: AppTown removes a scratch ref; deleting it is refused' >&2; exit 1;; esac\n" +
		"  done\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ -n \"$APPTOWN_PUSH_REF\" ]; then\n" +
		"  while read old new ref; do\n" +
		"    if [ \"$ref\" != \"$APPTOWN_PUSH_REF\" ]; then\n" +
		"      echo \"apptown: this ticket may push only $APPTOWN_PUSH_REF, not $ref\" >&2; exit 1\n" +
		"    fi\n" +
		"    case \"$new\" in $zero) echo 'apptown: deleting the Change ref is refused' >&2; exit 1;; esac\n" +
		"    if [ \"$old\" != \"$zero\" ]; then\n" +
		"      echo 'apptown: this ticket has already pushed; ask for a new ticket' >&2; exit 1\n" +
		"    fi\n" +
		"    current=$(git rev-parse --verify -q \"$APPTOWN_PUSH_BRANCH\" || echo $zero)\n" +
		"    if [ \"$current\" != \"$APPTOWN_PUSH_EXPECT\" ]; then\n" +
		"      echo \"apptown: the Change moved since your ticket was minted (expected $APPTOWN_PUSH_EXPECT, found $current): fetch, merge, and ask for a new ticket\" >&2; exit 1\n" +
		"    fi\n" +
		"    if [ \"$APPTOWN_PUSH_EXPECT\" != \"$zero\" ] && ! git merge-base --is-ancestor \"$APPTOWN_PUSH_EXPECT\" \"$new\"; then\n" +
		"      echo \"apptown: your push does not build on the Change's tip $APPTOWN_PUSH_EXPECT (non-fast-forward): fetch, merge, and push again\" >&2; exit 1\n" +
		"    fi\n" +
		"  done\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo 'apptown: pushing to the hub is refused — land through the change, do not git push to main' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(hooks, "pre-receive"), []byte(body), 0o755); err != nil {
		return fmt.Errorf("installing the no-push hook in %s: %w", hubDir, err)
	}
	return nil
}

// InitializeRepository adopts existing files without staging Forge runtime state.
func InitializeRepository(ctx context.Context, dir, defaultBranch string) error {
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if !gitexec.IsRepo(dir) {
		if err := initWithIdentity(ctx, dir, defaultBranch); err != nil {
			return err
		}
	} else {
		// The identity the hub's own commits carry: AppTown's git reads no
		// machine-wide config, so without it a commit here has none.
		for _, kv := range [][2]string{{"user.name", "apptown"}, {"user.email", "apptown@localhost"}} {
			if out, err := gitexec.Run(ctx, dir, "config", kv[0], kv[1]); err != nil {
				return fmt.Errorf("setting the repository's %s: %s", kv[0], out)
			}
		}
	}
	// .apptown/* is runtime state (steering, sessions, pi-home) EXCEPT the
	// durable app contract, which is versioned with the code it describes.
	// An existing .gitignore, even a link, is the repository's own: it is
	// kept and never followed.
	if _, err := CreateInTree(dir, ".gitignore", []byte(".apptown/*\n!.apptown/app.yaml\nnode_modules/\n"), 0o644); err != nil {
		return err
	}
	if err := StageAll(ctx, dir); err != nil {
		return err
	}
	staged, err := gitexec.Staged(ctx, dir)
	if err != nil {
		return err
	}
	if staged {
		if out, err := gitexec.Run(ctx, dir, "commit", "-m",
			trailer.Message("apptown: adopt existing workspace", "", trailer.Line{Key: trailer.Actor, Value: "apptown-system"})); err != nil {
			return fmt.Errorf("adopt commit: %s", out)
		}
	} else if _, err := gitexec.Run(ctx, dir, "rev-parse", "HEAD"); err != nil {
		if out, err := gitexec.Run(ctx, dir, "commit", "--allow-empty", "-m",
			trailer.Message("apptown: repository initialized", "", trailer.Line{Key: trailer.Actor, Value: "apptown-system"})); err != nil {
			return fmt.Errorf("init commit: %s", out)
		}
	}
	// The hub is written only by the landing contract; refuse every push so a
	// workspace can fetch (read) but never push (write) to main.
	return InstallNoPushHook(dir)
}

// repositoryIdentity is the config a new project repository starts with:
// the identity its own commits carry.
const repositoryIdentity = "[user]\n\tname = apptown\n\temail = apptown@localhost\n"

// initWithIdentity makes a new repository whose config already holds
// repositoryIdentity, in one git start: git init copies a template's files,
// config included, into the new .git. The template holds only that config,
// so git's sample hooks, info/exclude and description are not copied; the
// AppTown reads none of them.
func initWithIdentity(ctx context.Context, dir, branch string) error {
	template, err := os.MkdirTemp("", "apptown-git-template-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(template)
	if err := os.WriteFile(filepath.Join(template, "config"), []byte(repositoryIdentity), 0o644); err != nil {
		return err
	}
	if out, err := gitexec.Run(ctx, dir, "init", "--template="+template, "-b", branch); err != nil {
		return fmt.Errorf("git init: %s", out)
	}
	return nil
}

// ErrNoWorkspace means an operation requires a repository workspace that is absent.
var ErrNoWorkspace = fault.New(fault.Invalid, "repository workspace is unavailable")

// CommitWorkspace checkpoints changed source, excluding generated runtime state.
// An absent repository, or a folder whose source matches its last commit,
// produces no commit and an empty SHA. Whether to commit is read from what
// StageAll staged, not the working tree's status: an agent that committed
// its own work leaves only untracked runtime files under .apptown, which the
// status shows but no commit can record.
func CommitWorkspace(ctx context.Context, dir, message string) (sha string, committed bool, err error) {
	if !gitexec.IsRepo(dir) {
		return "", false, nil
	}
	if err := StageAll(ctx, dir); err != nil {
		return "", false, err
	}
	staged, err := gitexec.Staged(ctx, dir)
	if err != nil || !staged {
		return "", false, err
	}
	if out, err := gitexec.Run(ctx, dir, "commit", "-m", message); err != nil {
		return "", false, fmt.Errorf("%w: %s", err, out)
	}
	sha, _ = gitexec.Run(ctx, dir, "rev-parse", "--short", "HEAD")
	return sha, true, nil
}
