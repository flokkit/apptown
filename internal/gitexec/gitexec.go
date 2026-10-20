// Package gitexec runs the git binary for one local repository. Every AppTown
// package that shells out to git goes through here, so the rules for how git
// is started, what output comes back, how a commit's tree is extracted and
// what counts as a branch name (CheckBranchName, answered without git) live
// in one place.
//
// Command is the one place the repository starts git by name, AppTown's
// own commands and tests' alike (gittest builds on it). Callers that need a
// timeout, streamed output or a container for a long operation (merge,
// fetch, a lane's workspace) keep using execution.Runner with argv
// "git ...": that adds something real, and it starts processes with Environ
// too. Everything else uses this.
//
// No GIT_* variable AppTown inherited reaches the git it starts, and
// neither does the machine's system or global git config (Environ): the
// forge's git reads only the repository's own config and what the caller
// passes on purpose, so a commit, a merge or a checkout behaves the same on
// every host whatever the user the apptown runs as has configured (signing,
// hooks, templates, line endings, merge drivers). An identity AppTown
// commits with comes from the repository's config or -c, never from a
// person's ~/.gitconfig. AppTown CLI, acting in a person's own repository
// as them, is the one exception: it starts git with Command and the
// person's whole environment, so their GIT_* variables and their own git
// config apply.
package gitexec

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// waitDelay bounds how long a canceled git's pipes may stay open, so a child
// that inherited them cannot hold the caller past cancellation.
const waitDelay = time.Second

// Environ is the environment AppTown starts git (or any local process
// that may run git) with: the process's own without one GIT_* variable it
// inherited, with no system or global git config, then extra, the
// variables the caller sets on purpose. An inherited GIT_DIR,
// GIT_WORK_TREE, GIT_INDEX_FILE, GIT_OBJECT_DIRECTORY, GIT_CONFIG_* or any
// other GIT_* (an AppTown or a test started from a Git hook carries several)
// would point git at another repository, index, object store or
// configuration than the one AppTown named. The system config
// (/etc/gitconfig) and the global one (~/.gitconfig, ~/.config/git/config)
// belong to the machine and the user the apptown runs as: a commit.gpgsign,
// core.hooksPath, init.templateDir, merge.* or core.autocrlf there would
// change what AppTown's git does from one host to the next. HOME stays,
// for what else the process reads there (ssh, docker).
func Environ(extra ...string) []string {
	inherited := os.Environ()
	env := make([]string, 0, len(inherited)+len(RepositoryConfigOnly)+len(extra))
	for _, kv := range inherited {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	env = append(env, RepositoryConfigOnly...)
	return append(env, extra...)
}

// RepositoryConfigOnly are the variables that keep git to the repository's
// own config: no system config, and the global one (which also stands for
// the XDG location) empty. Environ includes them; a git started with an
// environment of its own (AppTown's Git HTTP backend) adds them.
var RepositoryConfigOnly = []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}

// Command is git with args, in dir, with env as its whole environment: the
// one constructor for every git the repository starts by name, so they all
// share how git is started. env is exactly what git gets; nil is an empty
// environment, never the process's own, which may carry GIT_* variables
// (build it with Environ). git and what it starts (a credential helper,
// ssh, a hook) form a process group of their own, and canceling ctx kills
// the whole group, as execution.Runner does: killing git alone would leave
// a helper running, holding git's pipes or still talking to a remote.
func Command(ctx context.Context, dir string, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	ownGroup(cmd)
	// Only a cancellable call bounds its pipes. An uncancellable one waits for
	// them as a plain exec would: a push can leave a helper holding a pipe
	// after git itself has finished, and that is not a failure.
	if ctx.Done() != nil {
		cmd.WaitDelay = waitDelay
	}
	cmd.Dir = dir
	cmd.Env = append([]string{}, env...)
	return cmd
}

// command is git in dir with base, the environment it starts from, plus
// env.
func command(ctx context.Context, dir string, base, env []string, args []string) *exec.Cmd {
	// AppTown's git takes no optional locks: a status read would otherwise
	// refresh the index under .git/index.lock, and the person or agent
	// committing in the same workspace at that moment would be refused.
	full := append(append(base, "GIT_OPTIONAL_LOCKS=0"), env...)
	// With an explicit environment, exec no longer sets PWD to Dir itself;
	// a git wrapper reading PWD must still see the folder.
	if abs, err := filepath.Abs(dir); dir != "" && err == nil {
		full = append(full, "PWD="+abs)
	}
	return Command(ctx, dir, full, args...)
}

// Run runs git in dir and returns stdout and stderr together, trimmed: the
// diagnostics a person needs when it fails.
func Run(ctx context.Context, dir string, args ...string) (string, error) {
	return RunEnv(ctx, dir, nil, args...)
}

// RunEnv is Run with a context and extra environment entries added to
// Environ's, GIT_* ones included.
func RunEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	return combined(command(ctx, dir, Environ(), env, args))
}

func combined(cmd *exec.Cmd) (string, error) {
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

// Output runs git in dir and returns stdout only, trimmed. Use it when the
// answer is data (a SHA, a URL, paths) that git's hints must not pollute.
func Output(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := OutputBytes(ctx, dir, nil, args...)
	return strings.TrimSpace(string(out)), err
}

// OutputBytes runs git with a context and optional stdin, and
// returns stdout untouched: file contents, a binary blob.
func OutputBytes(ctx context.Context, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := command(ctx, dir, Environ(), nil, args)
	cmd.Stdin = stdin
	return output(cmd)
}

// output is cmd.Output with stderr gathered in a buffer git's pipe can be
// read straight into: exec's own capture for Output makes a 32 KB copy
// buffer for every command, which across AppTown's many short git runs
// is most of what starting them allocates. A failure still carries stderr
// on its *exec.ExitError, as Output's does.
func output(cmd *exec.Cmd) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		exit.Stderr = stderr.Bytes()
	}
	return stdout.Bytes(), err
}

// IsRepo reports whether dir is the top of a git working tree (it has .git).
func IsRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// Staged reports whether dir's index differs from HEAD (from the empty tree
// on a branch with no commit yet): whether a commit would record anything.
// The working tree's status is not the question: files nothing staged, such
// as AppTown's runtime state under .apptown, would make a commit with
// nothing to record. A directory git cannot read is an error, never "nothing
// staged".
func Staged(ctx context.Context, dir string) (bool, error) {
	_, err := OutputBytes(ctx, dir, nil, "diff", "--cached", "--quiet", "--no-ext-diff", "--no-textconv")
	same, err := answer(err)
	if err != nil {
		return false, fmt.Errorf("reading what is staged: %w", err)
	}
	return !same, nil
}

// IsObjectID reports whether s is a full object id as git prints it: 40
// (SHA-1) or 64 (SHA-256) lowercase hex digits. A caller that takes a commit
// from outside checks it with this before git sees it, so an abbreviation,
// a ref name or an option can never be read as the commit.
func IsObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ResolveCommit names the commit a revision points at, refusing a revision
// that is not a commit or that could be read as an option.
func ResolveCommit(ctx context.Context, repo, revision string) (string, error) {
	out, err := OutputBytes(ctx, repo, nil, "rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
	return strings.TrimSpace(string(out)), err
}

// ExtractError says which half of ExtractCommit failed and what the tool said.
type ExtractError struct {
	// Step is "archive" (git could not produce the tree) or "extract" (tar
	// could not unpack it).
	Step   string
	Err    error
	Output string
}

func (e *ExtractError) Error() string {
	if e.Output == "" {
		return e.Step + ": " + e.Err.Error()
	}
	return e.Step + ": " + e.Err.Error() + ": " + e.Output
}
func (e *ExtractError) Unwrap() error { return e.Err }

// ExtractCommit writes the tree of a committed revision into dst, which must
// exist. Only committed bytes arrive: no uncommitted edits, no ignored files.
// The archive is materialized to a file first, because a pipeline only reports
// the last command's status and git can fail after emitting a valid archive.
// A canceled ctx is returned as itself.
func ExtractCommit(ctx context.Context, repo, revision, dst string) error {
	scratch, err := os.MkdirTemp("", "forge-archive-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	archive := filepath.Join(scratch, "tree.tar")
	if out, err := command(ctx, repo, Environ(), nil, []string{"archive", "--format=tar", "--output", archive, "--end-of-options", revision}).CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &ExtractError{Step: "archive", Err: err, Output: strings.TrimSpace(string(out))}
	}
	tar := exec.CommandContext(ctx, "tar", "-x", "-f", archive, "-C", dst)
	tar.WaitDelay = waitDelay
	if out, err := tar.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &ExtractError{Step: "extract", Err: err, Output: strings.TrimSpace(string(out))}
	}
	return ctx.Err()
}
