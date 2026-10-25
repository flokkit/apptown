package gitexec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CheckLinkedConfig refuses worktree-specific behavior that cannot be carried
// into AppTown's hardened standalone clone. Hub remotes remain in the hub;
// they are not executed or copied into the writing workspace.
func CheckLinkedConfig(ctx context.Context, hub string) error {
	path := filepath.Join(hub, ".git", "config")
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("linked workspace common config is not regular")
	}
	entries, err := readConfig(ctx, path)
	if err != nil {
		return err
	}
	core, err := initCore(ctx, filepath.Dir(hub))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if value, ok := core[entry.key]; ok && value == entry.value {
			continue
		}
		if entry.key == "user.name" && (entry.value == FolderUserName || entry.value == folderUserNameBefore) {
			continue
		}
		if entry.key == "user.email" && (entry.value == FolderUserEmail || entry.value == folderUserEmailBefore) {
			continue
		}
		if strings.HasPrefix(entry.key, "remote.") && (strings.HasSuffix(entry.key, ".url") || strings.HasSuffix(entry.key, ".fetch") || strings.HasSuffix(entry.key, ".pushurl")) {
			continue
		}
		if strings.HasPrefix(entry.key, "branch.") && strings.HasSuffix(entry.key, ".remote") && (entry.value == "origin" || entry.value == ".") {
			continue
		}
		if strings.HasPrefix(entry.key, "branch.") && strings.HasSuffix(entry.key, ".merge") && strings.HasPrefix(entry.value, "refs/heads/") {
			continue
		}
		return fmt.Errorf("linked workspace config %s requires explicit repair before conversion", entry.key)
	}
	return nil
}
