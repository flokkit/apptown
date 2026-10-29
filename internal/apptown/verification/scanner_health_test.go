package verification

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/execution"
)

// A scanner's Test connection asks for its image where checks run: on this
// AppTown's Docker (present, or pulled), or, with no local Docker, pulled on
// the worker; an image that cannot be had is not working.
func TestAScannersTestConnectionAsksForItsImageWhereChecksRun(t *testing.T) {
	t.Parallel()
	never := func(context.Context, execution.ImageRequest) execution.ImageAnswer {
		t.Error("the worker was asked for an image while this AppTown has Docker")
		return execution.ImageAnswer{}
	}
	if h := TestScannerImage(t.Context(), "", &docker.Scope{Client: dockerfake.New()}, never); h.OK || h.Detail != "image is required" {
		t.Fatalf("no image: %+v", h)
	}
	local := dockerfake.New()
	if h := TestScannerImage(t.Context(), "scanner:1", &docker.Scope{Client: local}, never); !h.OK || h.Detail != "image present locally" {
		t.Fatalf("a local image: %+v", h)
	}
	local.SetReply(func(args []string) ([]byte, error, bool) {
		switch args[0] {
		case "image":
			return []byte("Error: No such image"), errors.New("exit status 1"), true
		case "pull":
			return []byte("pull access denied"), errors.New("exit status 1"), true
		}
		return nil, nil, false
	})
	if h := TestScannerImage(t.Context(), "scanner:1", &docker.Scope{Client: local}, never); h.OK || !strings.Contains(h.Detail, "pull access denied") {
		t.Fatalf("an image that will not pull: %+v", h)
	}
	// A daemon that cannot answer is not one that lacks the image: nothing
	// is pulled, and the answer says it is unknown.
	unknown := dockerfake.New()
	unknown.SetReply(func(args []string) ([]byte, error, bool) {
		switch args[0] {
		case "image":
			return []byte("Cannot connect to the Docker daemon"), errors.New("exit status 1"), true
		case "pull":
			t.Error("an image was pulled though whether it is present is unknown")
		}
		return nil, nil, false
	})
	if h := TestScannerImage(t.Context(), "scanner:1", &docker.Scope{Client: unknown}, never); h.OK || !strings.Contains(h.Detail, "could not be read") {
		t.Fatalf("an unreadable answer: %+v", h)
	}
	var asked []execution.ImageRequest
	worker := func(_ context.Context, req execution.ImageRequest) execution.ImageAnswer {
		asked = append(asked, req)
		if req.Pull == "missing:1" {
			return execution.ImageAnswer{ID: "sha256:stale", Err: "manifest unknown"} // an error is not a pull, whatever else it says
		}
		return execution.ImageAnswer{ID: "sha256:scanner"}
	}
	absent := &docker.Scope{Client: docker.Refusing{}} // no Docker of its own
	if h := TestScannerImage(t.Context(), "scanner:1", absent, worker); !h.OK || h.Detail != "image pulled on the worker" {
		t.Fatalf("pulled on the worker: %+v", h)
	}
	if h := TestScannerImage(t.Context(), "missing:1", absent, worker); h.OK || !strings.Contains(h.Detail, "manifest unknown") {
		t.Fatalf("not pulled on the worker: %+v", h)
	}
	if len(asked) != 2 || asked[0].Tag != "scanner:1" || asked[0].Pull != "scanner:1" {
		t.Fatalf("the worker was asked %+v", asked)
	}
}
