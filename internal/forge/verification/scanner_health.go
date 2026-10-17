package verification

import (
	"context"
	"strings"
	"time"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/strs"
)

// TestScannerImage is a scanner binding's Test connection: its image is
// present on this forge's Docker or pulls there; with no local Docker, the
// check executor pulls it on the worker.
func TestScannerImage(ctx context.Context, image string, dk *docker.Scope, images CheckImage) integration.Health {
	if image == "" {
		return integration.Health{OK: false, Detail: "image is required"}
	}
	if dk.Absent() {
		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		if a := images(ctx, execution.ImageRequest{Tag: image, Pull: image}); a.Err != "" || a.ID == "" {
			return integration.Health{OK: false, Detail: "the worker could not pull the image: " + strs.Truncate(a.Err, 200)}
		}
		return integration.Health{OK: true, Detail: "image pulled on the worker"}
	}
	if dk.ImageExists(ctx, image) {
		return integration.Health{OK: true, Detail: "image present locally"}
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if out, err := dk.Command(ctx, docker.Command{Args: []string{"pull", "-q", image}}); err != nil {
		return integration.Health{OK: false, Detail: "image pull failed: " + strs.Truncate(strings.TrimSpace(string(out)), 200)}
	}
	return integration.Health{OK: true, Detail: "image pulled"}
}
