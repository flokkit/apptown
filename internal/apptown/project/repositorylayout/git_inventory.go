package repositorylayout

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// LinkedWorkspace records the two directions of a legacy Git worktree link.
// The migration checks these while both paths still have their original names;
// it never lets a pointer choose a different project's metadata.
type LinkedWorkspace struct {
	Folder       string `json:"folder"`
	Hub          string `json:"hub"`
	OriginalRoot string `json:"originalRoot,omitempty"`
	CurrentRoot  string `json:"currentRoot,omitempty"`
	Admin        string `json:"admin"` // relative to the hub's .git directory
}

// InspectRestoredLinkedWorkspace uses only the archive manifest's root mapping.
// Absolute old pointers are translated before reading; the old tree is never read.
func InspectRestoredLinkedWorkspace(folder, hub, oldRoot, root string) (LinkedWorkspace, bool, error) {
	if !filepath.IsAbs(oldRoot) || !filepath.IsAbs(root) || filepath.Clean(oldRoot) != oldRoot || filepath.Clean(root) != root {
		return LinkedWorkspace{}, false, errors.New("invalid restored installation roots")
	}
	return inspectLinkedWorkspace(folder, hub, oldRoot, root)
}
func inspectLinkedWorkspace(folder, hub, oldRoot, root string) (LinkedWorkspace, bool, error) {
	if err := directory(folder); err != nil {
		return LinkedWorkspace{}, false, err
	}
	git := filepath.Join(folder, ".git")
	info, err := os.Lstat(git)
	if err != nil {
		return LinkedWorkspace{}, false, err
	}
	if info.IsDir() {
		return LinkedWorkspace{}, false, nil
	}
	if !info.Mode().IsRegular() {
		return LinkedWorkspace{}, false, fmt.Errorf("workspace Git pointer is not regular: %s", git)
	}
	raw, err := os.ReadFile(git)
	if err != nil {
		return LinkedWorkspace{}, false, err
	}
	value, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir: ")
	if !ok || value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return LinkedWorkspace{}, false, fmt.Errorf("invalid Git worktree pointer in %s", git)
	}
	admin := restoredGitPath(folder, value, oldRoot, root)
	common := filepath.Join(hub, ".git")
	canonicalCommon, err := filepath.EvalSymlinks(common)
	if err != nil {
		return LinkedWorkspace{}, false, err
	}
	if err := directory(admin); err != nil {
		return LinkedWorkspace{}, false, err
	}
	canonicalAdmin, err := filepath.EvalSymlinks(admin)
	if err != nil {
		return LinkedWorkspace{}, false, err
	}
	relative, err := filepath.Rel(canonicalCommon, canonicalAdmin)
	if err != nil || !strings.HasPrefix(relative, "worktrees"+string(filepath.Separator)) || strings.Count(relative, string(filepath.Separator)) != 1 {
		return LinkedWorkspace{}, false, fmt.Errorf("workspace %s does not belong to identified hub %s", folder, hub)
	}
	for _, path := range []string{hub, common, filepath.Join(common, "worktrees"), admin} {
		if err := directory(path); err != nil {
			return LinkedWorkspace{}, false, err
		}
	}
	back, err := regularText(filepath.Join(admin, "gitdir"))
	if err != nil {
		return LinkedWorkspace{}, false, err
	}
	canonicalGit, err := filepath.EvalSymlinks(git)
	if err != nil {
		return LinkedWorkspace{}, false, err
	}
	backlink, err := filepath.EvalSymlinks(restoredGitPath(admin, back, oldRoot, root))
	if err != nil {
		return LinkedWorkspace{}, false, err
	}
	if backlink != canonicalGit {
		return LinkedWorkspace{}, false, fmt.Errorf("Git worktree backlink does not identify %s", folder)
	}
	shared, err := regularText(filepath.Join(admin, "commondir"))
	if err != nil {
		return LinkedWorkspace{}, false, err
	}
	sharedPath, err := filepath.EvalSymlinks(restoredGitPath(admin, shared, oldRoot, root))
	if err != nil {
		return LinkedWorkspace{}, false, err
	}
	if sharedPath != canonicalCommon {
		return LinkedWorkspace{}, false, fmt.Errorf("workspace %s borrows another Git common directory", folder)
	}
	// Per-worktree configuration can change the meaning of shared metadata.
	// It is preserved for an explicit repair, not silently discarded by conversion.
	for _, path := range []string{filepath.Join(admin, "config.worktree"), filepath.Join(admin, "locked")} {
		if _, err := os.Lstat(path); err == nil {
			return LinkedWorkspace{}, false, fmt.Errorf("workspace %s has unsupported or explicitly locked metadata: %s", folder, path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return LinkedWorkspace{}, false, err
		}
	}
	return LinkedWorkspace{Folder: folder, Hub: hub, Admin: relative, OriginalRoot: oldRoot, CurrentRoot: root}, true, nil
}
func resolveGitPath(base, value string) string {
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	return filepath.Clean(value)
}
func regularText(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("Git metadata pointer is not regular: %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("invalid Git metadata pointer: %s", path)
	}
	return value, nil
}

func restoredGitPath(base, value, oldRoot, root string) string {
	if oldRoot != "" && filepath.IsAbs(value) && (value == oldRoot || strings.HasPrefix(value, oldRoot+string(filepath.Separator))) {
		relative, _ := filepath.Rel(oldRoot, value)
		return filepath.Join(root, relative)
	}
	return resolveGitPath(base, value)
}
