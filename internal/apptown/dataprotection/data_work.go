package dataprotection

import (
	"fmt"
	"sync"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/fault"
)

// dataWork names the deployments whose data a restore or a removal is
// changing now. One excludes the other on the same deployment, refused at
// once rather than waited for: a deletion's final backup never copies, nor
// its take-down deletes, a volume a restore is still writing, and a
// restore never writes a deployment that is being taken down. Both run in
// this AppTown's one daemon, so the claim lives in memory and excludes them
// for that daemon's lifetime. A restart releases it, but not necessarily
// the host's side of the work it guarded: a restore streams into a
// container on the Docker host, which may outlive a stopped AppTown
// (design/subsystems/daemon-lifecycle.md, "Shutdown"). After an
// interrupted restore, establish that the host-side work has ended before
// taking the deployment down.
type dataWork struct {
	mu   sync.Mutex
	busy map[ids.ID]string
}

// claim holds the deployment for what, or refuses naming what holds it.
func (w *dataWork) claim(deploymentID ids.ID, what string) (func(), error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if holder, ok := w.busy[deploymentID]; ok {
		return nil, fault.New(fault.Conflict, fmt.Sprintf("%s is under way on this deployment; try again once it has ended", holder))
	}
	if w.busy == nil {
		w.busy = map[ids.ID]string{}
	}
	w.busy[deploymentID] = what
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		delete(w.busy, deploymentID)
	}, nil
}
