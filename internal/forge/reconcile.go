package forge

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"flokkit.io/forge/internal/forge/scm"
)

// InstallNoPushHooks backfills the pre-receive hook onto every existing project
// hub at boot (new hubs get it during repository initialization). It returns
// one error per hub left without its hook, each naming the hub, and an
// error when the hubs cannot be listed at all.
func (a *App) InstallNoPushHooks() error {
	root := filepath.Join(a.cfg.dataDir, "workspaces")
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // no project yet
	}
	if err != nil {
		return fmt.Errorf("listing the project hubs: %w", err)
	}
	var failed []error
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "project-") {
			if err := scm.InstallNoPushHook(filepath.Join(root, e.Name())); err != nil {
				failed = append(failed, err)
			}
		}
	}
	return errors.Join(failed...)
}
