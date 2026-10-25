package gitexec

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/strs"
)

// Bundles move commits between AppTown and a machine it shares no files
// with. Neither direction writes to the repository it reads from: a bundle is
// made in, and a returned one is imported into, a scratch repository that
// borrows the real one's objects through alternates. Git runs hardened
// there, as in the hub (HubRun).

// ErrBundleRefused means a returned bundle was not admitted: too large,
// malformed, failing the object checks, or not a descendant of its base.
var ErrBundleRefused = errors.New("the returned bundle was refused")

// scratch makes a bare repository under dir whose object lookups fall back
// to repo's. Objects written to it stay in it.
func scratch(ctx context.Context, repo, dir string) error {
	common, err := HubRun(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("finding %s's objects: %w", repo, err)
	}
	if out, err := HubRun(ctx, "", "init", "-q", "--bare", dir); err != nil {
		return fmt.Errorf("making a scratch repository: %w: %s", err, out)
	}
	return os.WriteFile(filepath.Join(dir, "objects", "info", "alternates"), []byte(filepath.Join(common, "objects")+"\n"), 0o644)
}

// BundleCommit writes to dest a bundle holding commit as refs/heads/<branch>
// with its full history, so it can be cloned on its own.
func BundleCommit(ctx context.Context, repo, commit, branch, dest string) error {
	dir, err := os.MkdirTemp("", "apptown-bundle-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir) // unchecked-error: temporary scratch may remain; bundle creation has its own checked result.
	if err := scratch(ctx, repo, dir); err != nil {
		return err
	}
	if out, err := HubRun(ctx, dir, "update-ref", "refs/heads/"+branch, commit); err != nil {
		return fmt.Errorf("naming %s for its bundle: %w: %s", commit, err, out)
	}
	if out, err := HubRun(ctx, dir, "bundle", "create", "-q", dest, "refs/heads/"+branch); err != nil {
		return fmt.Errorf("bundling %s: %w: %s", commit, err, out)
	}
	return nil
}

// BundleCommits writes to dest a bundle holding each of commits, with their
// full histories, as refs/apptown/commits/<n>: enough for a repository made
// from it to answer questions about those commits and the ranges between
// them.
func BundleCommits(ctx context.Context, repo string, commits []string, dest string) error {
	if len(commits) == 0 {
		return errors.New("a bundle needs a commit")
	}
	dir, err := os.MkdirTemp("", "apptown-bundle-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir) // unchecked-error: temporary scratch may remain; bundle creation has its own checked result.
	if err := scratch(ctx, repo, dir); err != nil {
		return err
	}
	args := []string{"bundle", "create", "-q", dest}
	for i, commit := range commits {
		ref := fmt.Sprintf("refs/apptown/commits/%d", i)
		if out, err := HubRun(ctx, dir, "update-ref", ref, commit); err != nil {
			return fmt.Errorf("naming %s for its bundle: %w: %s", commit, err, out)
		}
		args = append(args, ref)
	}
	if out, err := HubRun(ctx, dir, args...); err != nil {
		return fmt.Errorf("bundling %v: %w: %s", commits, err, out)
	}
	return nil
}

// Quarantine imports refs/heads/<branch> (or branch itself, when it is a
// full ref name under refs/) from a bundle made elsewhere into a
// new scratch repository beside repo's objects, as refs/heads/<as>, and
// returns that repository and the imported tip. The tip must descend from
// base. repo is not written; the caller publishes from the quarantine, then
// calls cleanup.
//
// Fetching from a bundle does not check its objects, whatever fsck settings
// are given (git 2.49), so the bundle's pack is indexed directly with
// index-pack --strict: a tree naming ".git" or "..", or any other malformed
// object, refuses the whole bundle. Only the bundle's own objects are
// checked, not the history it builds on.
func Quarantine(ctx context.Context, repo, bundle, branch, as, base string) (dir, tip string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "apptown-quarantine-*")
	if err != nil {
		return "", "", func() {}, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) } // unchecked-error: temporary quarantine may remain; validation and publication are separately checked.
	fail := func(err error) (string, string, func(), error) {
		cleanup()
		return "", "", func() {}, err
	}
	if err := scratch(ctx, repo, dir); err != nil {
		return fail(err)
	}
	ref := "refs/heads/" + branch
	if strings.HasPrefix(branch, "refs/") {
		ref = branch
	}
	if out, err := HubRun(ctx, dir, "bundle", "verify", "-q", bundle); err != nil {
		return fail(fmt.Errorf("%w: %v: %s", ErrBundleRefused, err, out))
	}
	listed, err := HubOutput(ctx, dir, nil, "bundle", "list-heads", bundle, ref)
	heads := strings.TrimSpace(string(listed))
	if err != nil {
		return fail(fmt.Errorf("%w: reading its refs: %w", ErrBundleRefused, err))
	}
	fields := strings.Fields(heads)
	if len(fields) != 2 || fields[1] != ref {
		return fail(fmt.Errorf("%w: it holds no single %s", ErrBundleRefused, ref))
	}
	tip = fields[0]
	if err := indexStrict(ctx, dir, bundle); err != nil {
		return fail(err)
	}
	if out, err := HubRun(ctx, dir, "update-ref", "refs/heads/"+as, tip); err != nil {
		return fail(fmt.Errorf("%w: naming its tip: %v: %s", ErrBundleRefused, err, out))
	}
	if _, err := HubOutput(ctx, dir, nil, "rev-parse", "--verify", tip+"^{commit}"); err != nil {
		return fail(fmt.Errorf("%w: its tip is not a commit", ErrBundleRefused))
	}
	if _, err := HubRun(ctx, dir, "merge-base", "--is-ancestor", base, tip); err != nil {
		return fail(fmt.Errorf("%w: %s does not descend from the task's source %s", ErrBundleRefused, strs.ShortSHA(tip), strs.ShortSHA(base)))
	}
	return dir, tip, cleanup, nil
}

// indexStrict indexes the pack that follows a bundle's header into dir,
// checking every object strictly.
func indexStrict(ctx context.Context, dir, bundle string) error {
	f, err := os.Open(bundle)
	if err != nil {
		return err
	}
	defer f.Close() // unchecked-error: read-only handle cleanup; reads and indexing establish the result, not output durability.
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("%w: its header is incomplete", ErrBundleRefused)
		}
		if line == "\n" {
			break
		}
	}
	var stderr bytes.Buffer
	// A result bundle is thin: its deltas may be against objects of the
	// source commit, which the quarantine reads through its alternates.
	// --fix-thin completes the pack from them; --strict still checks every
	// object.
	cmd, done, err := hubCommand(ctx, dir, []string{"index-pack", "--strict", "--stdin", "--fix-thin"})
	if err != nil {
		return err
	}
	defer done()
	cmd.Stdin, cmd.Stderr = r, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: its objects failed the checks: %s", ErrBundleRefused, strings.TrimSpace(stderr.String()))
	}
	return nil
}
