package verification

import (
	"context"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
)

// An image scan compares only with its base commit's image. When that image
// cannot be built, on an AppTown with local Docker and a release running,
// the base is unavailable with the build's reason: the image main serves is
// another commit's, and is neither inspected nor scanned in its place.
//
// Kills: the serving release's image substituted for a base image that
// failed to build (fallback audit F18).
func TestAFailedBaseImageIsNeverReplacedByTheServingImage(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	repo, sha := baselineTree(t, "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n")
	deployments := deployment.NewDeployments(f.DB)
	servingMain(t, f, deployments, sha)
	f.Scanners.deployments = deployments
	inspected, scanned := false, false
	f.Daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "inspect" {
			inspected = true
			if strings.Contains(strings.Join(args, " "), "State.Running") {
				return []byte("true"), nil, true
			}
			return []byte("sha256:serving"), nil, true
		}
		return nil, nil, false
	})
	f.Scanners.exec = Executor{Local: true,
		Image: func(context.Context, execution.ImageRequest) execution.ImageAnswer {
			return execution.ImageAnswer{Err: "base build failed"}
		},
		Run: func(context.Context, execution.Request) execution.Outcome {
			scanned = true
			return execution.Outcome{Termination: execution.Exited}
		},
	}
	base := f.Scanners.baseImageFingerprints(f.Context, f.ProjectID, repo, sha, check.Scanner{}, "scan", t.TempDir(), nil, "scan")
	if base.OK || inspected || scanned || !strings.Contains(base.Note, "base build failed") {
		t.Fatalf("failed base: %+v, inspected serving image %v, scanned substitute %v", base, inspected, scanned)
	}
}
