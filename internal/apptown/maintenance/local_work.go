package maintenance

import (
	"context"

	"github.com/flokkit/apptown/internal/deploy/docker"
)

// WorkerBackend begins the backend recorded for a task run on a worker.
const WorkerBackend = "worker at "

// RemoveBuildImages removes the build images made for one container (a
// preview's), naming each removal that failed. An image a container still
// uses stays: the removal never forces.
func RemoveBuildImages(ctx context.Context, dk docker.Scope, container string) []string {
	refs, err := dk.BuildImages(ctx, container)
	if err != nil {
		return []string{err.Error()}
	}
	var failed []string
	for _, ref := range refs {
		if err := dk.RemoveUnusedImage(ctx, ref); err != nil {
			failed = append(failed, err.Error())
		}
	}
	return failed
}
