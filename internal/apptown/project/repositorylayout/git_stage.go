package repositorylayout

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/durable"
	"github.com/flokkit/apptown/internal/gitexec"
)

// StageStandalone prepares independent metadata without changing a workspace's
// working files or pointer. The caller journals the later metadata exchange and
// preserves the original pointer/admin directory until final verification.
func (w LinkedWorkspace) StageStandalone(ctx context.Context, destination string) error {
	observed, linked, err := inspectLinkedWorkspace(w.Folder, w.Hub, w.OriginalRoot, w.CurrentRoot)
	if err != nil {
		return err
	}
	if !linked || observed != w {
		return errors.New("linked workspace ownership changed before staging")
	}
	if err := gitexec.CheckLinkedConfig(ctx, w.Hub); err != nil {
		return err
	}
	common := filepath.Join(w.Hub, ".git")
	admin := filepath.Join(common, w.Admin)
	for _, path := range []string{filepath.Join(common, "objects", "info", "alternates"), filepath.Join(common, "objects", "info", "http-alternates"), filepath.Join(common, "config.worktree")} {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("linked workspace has an unsupported borrowed object/config source: %s", path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	if _, err := gitexec.HubRun(ctx, destination, "init", "--quiet", "--template=", "."); err != nil {
		return err
	}
	target := filepath.Join(destination, ".git")
	// Copy shared history, including unreachable objects and reflogs. No clone,
	// fetch or prune decides which of the writer's objects are worth retaining.
	for _, name := range []string{"objects", "refs", "packed-refs", "logs", "info", "shallow"} {
		source := filepath.Join(common, name)
		if _, err := os.Lstat(source); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := copyMetadata(source, filepath.Join(target, name)); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(admin)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "gitdir", "commondir":
			continue
		}
		if err := copyMetadata(filepath.Join(admin, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
			return err
		}
	}
	// A standalone workspace uses the same safe configuration as a current
	// AppTown clone. The upstream origin stays in the hub; none is invented here.
	if _, err := gitexec.HubRun(ctx, destination, "config", "user.name", gitexec.FolderUserName); err != nil {
		return err
	}
	if _, err := gitexec.HubRun(ctx, destination, "config", "user.email", gitexec.FolderUserEmail); err != nil {
		return err
	}
	if err := syncMetadata(target); err != nil {
		return err
	}
	if err := gitexec.CheckFolder(ctx, destination); err != nil {
		return err
	}
	// Read the staged commit through the hardened adapter before it can replace
	// the pointer. An unreadable or unborn linked HEAD is preserved for repair.
	head, err := regularText(filepath.Join(admin, "HEAD"))
	if err != nil {
		return err
	}
	head = strings.TrimPrefix(head, "ref: ")
	before, beforeErr := gitexec.HubRun(ctx, w.Hub, "rev-parse", "--verify", "--end-of-options", head+"^{commit}")
	after, afterErr := gitexec.HubRun(ctx, destination, "rev-parse", "--verify", "HEAD")
	if beforeErr != nil || afterErr != nil || before != after {
		return fmt.Errorf("staged workspace HEAD differs or cannot be read: %v / %v", beforeErr, afterErr)
	}
	return durable.SyncDir(destination)
}

// copyMetadata never follows links, borrows hardlinks, or runs repository code.
// All target entries are inside a newly created staging directory owned by this
// migration. Overlaying worktree metadata preserves its index and HEAD log.
func copyMetadata(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if strings.HasSuffix(filepath.Base(source), ".lock") {
		return fmt.Errorf("Git metadata is locked: %s", source)
	}
	switch {
	case info.IsDir():
		if err := os.Mkdir(target, info.Mode().Perm()); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if err := directory(target); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyMetadata(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
				return err
			}
		}
		return durable.SyncDir(target)
	case info.Mode().IsRegular():
		in, err := os.Open(source)
		if err != nil {
			return err
		}
		defer in.Close()
		if old, err := os.Lstat(target); err == nil && !old.Mode().IsRegular() {
			return fmt.Errorf("staged Git metadata has conflicting type: %s", target)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		if err := out.Chmod(info.Mode().Perm()); err != nil {
			return errors.Join(err, out.Close())
		}
		_, copyErr := io.Copy(out, in)
		syncErr := out.Sync()
		return errors.Join(copyErr, syncErr, out.Close())
	default:
		return fmt.Errorf("Git metadata contains a nonregular entry: %s", source)
	}
}

// Git init/config generate files outside copyMetadata. Flush the complete final
// tree before its digest may become durable exchange intent.
func syncMetadata(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("staged metadata is not regular: %s", path)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		return errors.Join(file.Sync(), file.Close())
	})
	if err != nil {
		return err
	}
	for index := len(dirs) - 1; index >= 0; index-- {
		if err := durable.SyncDir(dirs[index]); err != nil {
			return err
		}
	}
	return nil
}
