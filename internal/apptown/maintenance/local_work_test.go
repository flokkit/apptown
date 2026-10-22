package maintenance

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
)

// A preview's build images are removed without force: one a container
// still uses stays and is no failure; a removal that fails otherwise, and
// a listing that fails, are named.
func TestRemoveBuildImagesNeverForcesAndNamesFailures(t *testing.T) {
	t.Parallel()
	const own, part, inUse, broken = "apptown-preview-build-apptown-preview-run-00000000-0001-7000-8000-000000000000:latest", "apptown-preview-build-apptown-preview-run-00000000-0001-7000-8000-000000000000-web:latest", "apptown-preview-build-apptown-preview-run-00000000-0001-7000-8000-000000000000-db:latest", "apptown-preview-build-apptown-preview-run-00000000-0001-7000-8000-000000000000-x:latest"
	daemon := dockerfake.New()
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		call := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(call, "image ls"):
			return []byte(strings.Join([]string{own, part, inUse, broken}, "\n")), nil, true
		case call == "rmi "+inUse:
			return []byte(`Error response from daemon: conflict: unable to remove repository reference "` + inUse + `" (must force) - container 0123 is using its referenced image 4567`), errors.New("exit status 1"), true
		case call == "rmi "+broken:
			return []byte("Error response from daemon: something else"), errors.New("exit status 1"), true
		}
		return nil, nil, false
	})
	failed := RemoveBuildImages(context.Background(), docker.Scope{Client: daemon}, "apptown-preview-run-00000000-0001-7000-8000-000000000000")
	if len(failed) != 1 || !strings.Contains(failed[0], broken) {
		t.Fatalf("failures: %q", failed)
	}
	var removed []string
	for _, call := range daemon.Matching("rmi") {
		if slices.Contains(call, "-f") || slices.Contains(call, "--force") {
			t.Fatalf("an image was forced: %v", call)
		}
		removed = append(removed, call[len(call)-1])
	}
	if !slices.Equal(removed, []string{own, part, inUse, broken}) {
		t.Fatalf("asked to remove %q", removed)
	}

	unlisted := dockerfake.New()
	unlisted.SetReply(func([]string) ([]byte, error, bool) { return []byte("daemon gone"), errors.New("exit status 1"), true })
	if failed := RemoveBuildImages(context.Background(), docker.Scope{Client: unlisted}, "apptown-preview-run-00000000-0001-7000-8000-000000000000"); len(failed) != 1 {
		t.Fatalf("a listing that failed: %q", failed)
	}
}
