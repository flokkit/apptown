package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// AppTown's own repositories (a project's hub, and the scratch
// repositories beside it that bundles are made in and imported through)
// have config AppTown writes; nothing project-controlled runs in them.
// The commands a worker's result passes through there still run hardened
// (design/operations/workers.md, "How work travels"): no system or global config, nothing from AppTown's
// own environment but PATH and an empty HOME (so no GIT_DIR,
// GIT_OBJECT_DIRECTORY or other override), hooks and fsmonitor off, no
// global attributes, no submodule recursion, and only local-path
// transports. The commands are plumbing (rev-parse, merge-base, update-ref,
// fetch, index-pack, rev-list, cat-file, bundle), which run no external
// diff, textconv or filter; the hub's config, which alone could name one,
// is AppTown's. Blobs are read with cat-file.

// hubFlags are the command-line settings every hardened command carries.
// Command-line config outranks the repository's own.
var hubFlags = []string{
	"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
	"-c", "core.attributesFile=/dev/null",
	"-c", "submodule.recurse=false", "-c", "protocol.allow=never", "-c", "protocol.file.allow=always",
}

// hubCommand is git in dir, hardened. done removes its empty HOME.
func hubCommand(ctx context.Context, dir string, args []string) (cmd *exec.Cmd, done func(), err error) {
	cmd, done, err = homeCommand(ctx, append(append([]string{}, hubFlags...), args...))
	if err == nil {
		cmd.Dir = dir
	}
	return cmd, done, err
}

// homeCommand is git with nothing from AppTown's environment but PATH, no
// system or global config, and an empty HOME of its own, which it runs in
// unless given another directory. done removes the HOME.
func homeCommand(ctx context.Context, args []string) (cmd *exec.Cmd, done func(), err error) {
	home, err := os.MkdirTemp("", "apptown-git-home-")
	if err != nil {
		return nil, func() {}, err
	}
	cmd = Command(ctx, home, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}, args...)
	return cmd, func() { _ = os.RemoveAll(home) }, nil // unchecked-error: temporary isolated HOME may remain; cleanup is not a Git outcome receipt.
}

// answer reads a query's exit: success is yes, exit status 1 with nothing
// on stderr (--verify --quiet naming nothing, --is-ancestor false) is no,
// and anything else is Git failing to answer.
func answer(err error) (bool, error) {
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1 && len(bytes.TrimSpace(exit.Stderr)) == 0:
		return false, nil
	}
	return false, err
}

// HubRun runs git in one of AppTown's own repositories, hardened, and
// returns stdout and stderr together, trimmed.
func HubRun(ctx context.Context, dir string, args ...string) (string, error) {
	cmd, done, err := hubCommand(ctx, dir, args)
	if err != nil {
		return "", err
	}
	defer done()
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err = cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

// HubOutput runs git in one of AppTown's own repositories, hardened, with
// optional stdin, and returns stdout untouched; a failure carries stderr.
func HubOutput(ctx context.Context, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	cmd, done, err := hubCommand(ctx, dir, args)
	if err != nil {
		return nil, err
	}
	defer done()
	cmd.Stdin = stdin
	return output(cmd)
}

// HubRef is the object a ref names in one of AppTown's own repositories,
// "" when it names none.
func HubRef(ctx context.Context, dir, ref string) string {
	out, err := HubOutput(ctx, dir, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// HubReadRef is the object a ref names in one of AppTown's own
// repositories, hardened. A ref that names nothing is "" with no error;
// Git failing to answer (no repository, an unreadable one, a killed
// command) is an error, never taken as a missing ref.
func HubReadRef(ctx context.Context, dir, ref string) (string, error) {
	out, err := HubOutput(ctx, dir, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", ref)
	switch ok, err := answer(err); {
	case ok:
		return strings.TrimSpace(string(out)), nil
	case err == nil:
		return "", nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return "", fmt.Errorf("reading %s in %s: %w: %s", ref, dir, err, strings.TrimSpace(string(exit.Stderr)))
	}
	return "", fmt.Errorf("reading %s in %s: %w", ref, dir, err)
}

// HubHolds reports whether branch in one of AppTown's own repositories
// contains commit. A commit or branch it lacks is an answer (no); Git
// failing to answer is an error, never either.
func HubHolds(ctx context.Context, dir, branch, commit string) (bool, error) {
	return branchHolds(ctx, HubRun, dir, branch, commit)
}

// branchHolds asks, through run (hardened in a hub, or confined in a
// folder), whether repo's branch contains commit.
func branchHolds(ctx context.Context, run func(context.Context, string, ...string) (string, error), repo, branch, commit string) (bool, error) {
	ask := func(args ...string) (bool, error) {
		out, err := run(ctx, repo, args...)
		if ok, err := answer(err); err == nil {
			return ok, nil
		}
		return false, fmt.Errorf("reading whether %s holds %s: %v: %s", repo, commit, err, out)
	}
	for _, ref := range []string{commit + "^{commit}", "refs/heads/" + branch} {
		if ok, err := ask("rev-parse", "--verify", "--quiet", "--end-of-options", ref); err != nil || !ok {
			return false, err
		}
	}
	return ask("merge-base", "--is-ancestor", "--end-of-options", commit, "refs/heads/"+branch)
}

// HubAncestor reports whether older is an ancestor of (or is) newer in one
// of AppTown's own repositories. Git failing to answer is false.
func HubAncestor(ctx context.Context, dir, older, newer string) bool {
	_, err := HubRun(ctx, dir, "merge-base", "--is-ancestor", "--end-of-options", older, newer)
	return err == nil
}

// HubIsAncestor is HubAncestor with Git's failure kept: an answer of no is
// false with no error, and Git failing to answer is the error, never no.
func HubIsAncestor(ctx context.Context, dir, older, newer string) (bool, error) {
	_, err := HubOutput(ctx, dir, nil, "merge-base", "--is-ancestor", "--end-of-options", older, newer)
	ok, err := answer(err)
	if err != nil {
		return false, fmt.Errorf("reading whether %s holds %s: %w", newer, older, err)
	}
	return ok, nil
}

// HubMergeBase is the best common ancestor of a and b in one of AppTown's
// own repositories. No common history is found false with no error; Git
// failing to answer is the error.
func HubMergeBase(ctx context.Context, dir, a, b string) (base string, found bool, err error) {
	out, err := HubOutput(ctx, dir, nil, "merge-base", "--end-of-options", a, b)
	if ok, err := answer(err); err != nil || !ok {
		return "", false, err
	}
	return strings.TrimSpace(string(out)), true, nil
}

// HubStream runs git in one of AppTown's own repositories, hardened,
// writing its stdout to w; a failure carries stderr.
func HubStream(ctx context.Context, dir string, w io.Writer, args ...string) error {
	cmd, done, err := hubCommand(ctx, dir, args)
	if err != nil {
		return err
	}
	defer done()
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = w, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
