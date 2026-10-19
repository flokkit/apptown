package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// A folder is a working copy the forge cloned for its own use (a change's or
// a run's), which a local writer (an agent, a person's chat) may since have
// changed, its Git metadata included. The forge still runs a few Git
// commands in it on its own account: bringing it forward to a published
// branch, resetting it for a restart. Those commands run here, confined:
//
//   - before any Git runs, the folder's metadata is checked by reading files
//     only: a real .git directory inside the folder, nothing under it a
//     symbolic link, no shared or borrowed metadata (commondir, gitdir,
//     alternates), a local config holding exactly what the forge's own
//     clone writes, and no hook but Git's inert samples. Anything else is
//     refused (ErrFolderRefused) and the folder is left as it is;
//   - Git then runs with an environment of only PATH, an empty HOME and no
//     system or global config, so no override from the forge's own
//     environment (GIT_DIR, GIT_OBJECT_DIRECTORY, ...) reaches it, with the
//     metadata and work tree named explicitly, hooks and fsmonitor off and
//     no submodule recursion.
//
// A folder's writers take its lock (LockFolder), and the commands that
// change a folder are methods of the held lock, so the check stays true
// while they run.

// FolderUserName and FolderUserEmail are the identity the forge's clone of a
// folder is given (workflow's EnsureClone), and the only identity a folder's
// config may hold.
const (
	FolderUserName  = "forge helper"
	FolderUserEmail = "helper@forge.local"
)

// ErrFolderRefused: the folder's Git metadata is not what the forge's own
// clone makes, so the forge runs no Git in it. A FolderRefusal says why.
var ErrFolderRefused = errors.New("the folder's Git metadata is not AppTown's own clone")

// FolderRefusal is a refused folder and the reason.
type FolderRefusal struct{ Folder, Reason string }

func (e *FolderRefusal) Error() string {
	return fmt.Sprintf("%s: %s: %s", e.Folder, ErrFolderRefused, e.Reason)
}
func (e *FolderRefusal) Is(target error) bool { return target == ErrFolderRefused }

// folderLocks holds one lock per folder path. The forge runs one process per
// data directory, so a process-wide lock is the whole of it.
var folderLocks = struct {
	sync.Mutex
	held map[string]*sync.Mutex
}{held: map[string]*sync.Mutex{}}

// Folder is a folder's lock, held: the commands that change the folder are
// its methods, so none runs without it.
type Folder struct {
	path string
	mu   *sync.Mutex
	once sync.Once
}

// LockFolder waits for path's lock and returns it held. A step that works
// in the folder holds it from before its checks until it has finished, so a
// step that overlaps another in the same folder waits for it instead of
// racing its checks; a chat save that brings the folder forward takes the
// same lock. Across a restart, the records the checks read carry over. A
// lock is kept per folder used; there are few, and each is small.
func LockFolder(path string) *Folder {
	path = filepath.Clean(path)
	folderLocks.Lock()
	m := folderLocks.held[path]
	if m == nil {
		m = &sync.Mutex{}
		folderLocks.held[path] = m
	}
	folderLocks.Unlock()
	m.Lock()
	return &Folder{path: path, mu: m}
}

// Path is the locked folder.
func (f *Folder) Path() string { return f.path }

// Release gives the lock back. Releasing twice is harmless.
func (f *Folder) Release() { f.once.Do(f.mu.Unlock) }

// FastForward brings the folder's checked-out branch to branch in hub when
// the folder has nothing of its own: a fetch from hub, then a fast-forward
// merge. A folder that cannot fast-forward returns Git's error and is left
// as it is; a refused folder returns a FolderRefusal and nothing ran.
func (f *Folder) FastForward(ctx context.Context, hub, branch string) error {
	if hub == "" || branch == "" {
		return fmt.Errorf("bringing %s forward needs a repository and a branch", f.path)
	}
	return f.run(ctx,
		[]string{"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--end-of-options", hub, "refs/heads/" + branch},
		[]string{"merge", "--ff-only", "--quiet", "--no-verify", "FETCH_HEAD"})
}

// Reset moves the folder's checked-out branch, index and files to commit
// and removes untracked files and directories, except the paths in keep.
func (f *Folder) Reset(ctx context.Context, commit string, keep ...string) error {
	if !IsObjectID(commit) {
		return fmt.Errorf("resetting %s needs a full commit ID, not %q", f.path, commit)
	}
	clean := []string{"clean", "-fd"}
	for _, k := range keep {
		clean = append(clean, "-e", k)
	}
	return f.run(ctx, []string{"reset", "--quiet", "--hard", commit}, clean)
}

// run checks the folder, then runs each command confined, stopping at the
// first that fails.
func (f *Folder) run(ctx context.Context, commands ...[]string) error {
	if err := CheckFolder(ctx, f.path); err != nil {
		return err
	}
	for _, args := range commands {
		if out, err := confined(ctx, f.path, args...); err != nil {
			return fmt.Errorf("git %s in %s: %w: %s", args[0], f.path, err, out)
		}
	}
	return nil
}

// FolderHolds reports whether the folder's branch contains commit, running
// only read-only Git in it, confined, after the same check. It does not
// take the folder's lock: a step holds that for as long as it runs, and a
// view must not wait for it. A writer racing it can change its answer, not
// what it runs. A commit or branch the folder lacks is an answer (no); Git
// failing to answer is an error, never either; a refused folder returns a
// FolderRefusal.
func FolderHolds(ctx context.Context, folder, branch, commit string) (bool, error) {
	if folder == "" || branch == "" || commit == "" {
		return false, fmt.Errorf("asking a folder for a commit needs the folder, a branch and the commit")
	}
	if err := CheckFolder(ctx, folder); err != nil {
		return false, err
	}
	return branchHolds(ctx, confined, folder, branch, commit)
}

// FolderTip is the commit the folder's branch is at, "" when the folder,
// its Git metadata or the branch does not exist. Like FolderHolds it runs
// only read-only Git in the folder, confined, after the check, and does not
// take the lock; a refused folder returns a FolderRefusal.
func FolderTip(ctx context.Context, folder, branch string) (string, error) {
	if folder == "" || branch == "" {
		return "", fmt.Errorf("reading a folder's branch needs the folder and the branch")
	}
	if _, err := os.Lstat(filepath.Join(folder, ".git")); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err := CheckFolder(ctx, folder); err != nil {
		return "", err
	}
	out, err := confined(ctx, folder, "rev-parse", "--verify", "--quiet", "--end-of-options", "refs/heads/"+branch)
	switch ok, err := answer(err); {
	case ok:
		return out, nil
	case err == nil:
		return "", nil
	}
	return "", fmt.Errorf("reading %s's branch %s: %v: %s", folder, branch, err, out)
}

// confined runs git in folder hardened as in a hub (hubCommand), with the
// metadata and work tree named.
func confined(ctx context.Context, folder string, args ...string) (string, error) {
	return HubRun(ctx, folder, append([]string{"--git-dir=" + filepath.Join(folder, ".git"), "--work-tree=" + folder}, args...)...)
}

// CheckFolder refuses a folder whose Git metadata is not what the forge's
// own clone makes, reading files only; no Git runs in the folder. A folder
// that cannot be read is an error, not a refusal.
func CheckFolder(ctx context.Context, folder string) error {
	refuse := func(format string, args ...any) error {
		return &FolderRefusal{Folder: folder, Reason: fmt.Sprintf(format, args...)}
	}
	if info, err := os.Lstat(folder); err != nil {
		return fmt.Errorf("reading the folder %s: %w", folder, err)
	} else if !info.IsDir() {
		return refuse("the folder is not a directory")
	}
	gitdir := filepath.Join(folder, ".git")
	if info, err := os.Lstat(gitdir); errors.Is(err, fs.ErrNotExist) {
		return refuse("it has no .git directory")
	} else if err != nil {
		return fmt.Errorf("reading %s: %w", gitdir, err)
	} else if !info.IsDir() {
		return refuse(".git is not a directory of its own")
	}
	// Nothing under .git may point elsewhere or be anything but a file or a
	// directory: objects, refs, logs, info, hooks and the config then all
	// live in the folder.
	if err := filepath.WalkDir(gitdir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() && !d.IsDir() {
			rel, _ := filepath.Rel(folder, path)
			return refuse("%s is a link or special file", rel)
		}
		return nil
	}); err != nil {
		var refusal *FolderRefusal
		if errors.As(err, &refusal) {
			return err
		}
		return fmt.Errorf("reading %s: %w", gitdir, err)
	}
	// Shared or borrowed metadata: another repository's refs, objects or
	// config would be read and written.
	for _, name := range []string{"commondir", "gitdir", filepath.Join("objects", "info", "alternates"), filepath.Join("objects", "info", "http-alternates")} {
		if _, err := os.Lstat(filepath.Join(gitdir, name)); err == nil {
			return refuse(".git/%s borrows another repository's metadata", filepath.ToSlash(name))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("reading %s: %w", gitdir, err)
		}
	}
	if hooks, err := os.ReadDir(filepath.Join(gitdir, "hooks")); err == nil {
		for _, h := range hooks {
			if !strings.HasSuffix(h.Name(), ".sample") || !h.Type().IsRegular() {
				return refuse("it has a hook, .git/hooks/%s", h.Name())
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reading %s's hooks: %w", folder, err)
	}
	return checkConfig(ctx, folder, gitdir, refuse)
}

// checkConfig refuses any local config entry the forge's clone does not
// write: the core values Git's init sets on this filesystem, the forge's
// identity and the checked-out branch's tracking. Includes are entries too,
// so they refuse.
func checkConfig(ctx context.Context, folder, gitdir string, refuse func(string, ...any) error) error {
	entries, err := readConfig(ctx, filepath.Join(gitdir, "config"))
	if err != nil && ctx.Err() != nil {
		// A read its caller stopped says nothing about the folder: never a
		// refusal, which reads as a folder left behind.
		return fmt.Errorf("reading the folder's config: %w", ctx.Err())
	}
	if err != nil {
		return refuse("its config cannot be read: %v", err)
	}
	core, err := initCore(ctx, filepath.Dir(folder))
	if err != nil {
		return fmt.Errorf("learning what Git's clone writes beside %s: %w", folder, err)
	}
	allowed := map[string]string{"user.name": FolderUserName, "user.email": FolderUserEmail}
	for k, v := range core {
		allowed[k] = v
	}
	head, err := os.ReadFile(filepath.Join(gitdir, "HEAD"))
	if err != nil {
		return refuse("its HEAD cannot be read: %v", err)
	}
	if branch, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/"); ok && branch != "" {
		allowed["branch."+branch+".remote"] = "origin"
		allowed["branch."+branch+".merge"] = "refs/heads/" + branch
	}
	for _, e := range entries {
		if want, ok := allowed[e.key]; !ok || want != e.value {
			return refuse("its config has %s=%q, which AppTown's clone does not write", e.key, e.value)
		}
	}
	return nil
}

type configEntry struct{ key, value string }

// readConfig lists a config file's entries as data: git config reading a
// named file runs nothing and follows no include.
func readConfig(ctx context.Context, path string) ([]configEntry, error) {
	cmd, done, err := homeCommand(ctx, []string{"config", "--file", path, "--no-includes", "--null", "--list"})
	defer done()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var entries []configEntry
	for _, item := range strings.Split(string(out), "\x00") {
		if item == "" {
			continue
		}
		key, value, _ := strings.Cut(item, "\n")
		entries = append(entries, configEntry{key, value})
	}
	return entries, nil
}

// initCores caches, per directory, the core values Git's init writes there:
// a clone writes the same, and some (filemode, ignorecase, symlinks) depend
// on the filesystem, so they are learned where the folders are.
var initCores sync.Map

func initCore(ctx context.Context, dir string) (map[string]string, error) {
	if v, ok := initCores.Load(dir); ok {
		return v.(map[string]string), nil
	}
	probe, err := os.MkdirTemp(dir, ".forge-probe-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(probe)
	cmd, done, err := homeCommand(ctx, []string{"init", "--quiet", "--template=", probe})
	defer done()
	if err != nil {
		return nil, err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	entries, err := readConfig(ctx, filepath.Join(probe, ".git", "config"))
	if err != nil {
		return nil, err
	}
	core := map[string]string{}
	for _, e := range entries {
		if strings.HasPrefix(e.key, "core.") {
			core[e.key] = e.value
		}
	}
	initCores.Store(dir, core)
	return core, nil
}
