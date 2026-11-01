package apptown

import (
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/project"

	"github.com/flokkit/apptown/internal/apptown/scm"
)

// installNoPushHooks backfills the pre-receive hook onto every existing project
// hub at boot (new hubs get it during repository initialization). It returns
// one error per hub left without its hook, each naming the hub, and an
// error when the hubs cannot be listed at all.
func (a *App) installNoPushHooks() error {
	repositories, err := project.RepositoriesOnDisk(a.cfg.dataDir)
	if err != nil {
		return fmt.Errorf("listing the project hubs: %w", err)
	}
	var failed []error
	for _, repo := range repositories {
		if err := scm.InstallNoPushHook(repo); err != nil {
			failed = append(failed, err)
		}
	}

	return errors.Join(failed...)
}
