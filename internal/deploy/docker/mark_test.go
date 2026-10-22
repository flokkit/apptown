package docker_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
)

// The marker is read through a read-only --mount, so a daemon without the
// path refuses it instead of creating an empty folder there (as -v would).
// The mark is found as a line of the output, beside any lines the CLI
// writes itself (an image pull); another file's content is not it; a
// refusal is an error.
//
// Kills: -v in place of --mount; the whole output compared (the live CT
// read "a different marker" when the CLI added a line); a refused mount
// read as no error.
func TestAMarkerIsReadThroughARefusingMount(t *testing.T) {
	t.Parallel()
	d := dockerfake.New()
	var seen []string
	d.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "run" {
			seen = args
			if slices.Contains(args, "type=bind,src=/tmp/forge-host-mark-1,dst=/m,readonly") {
				return []byte("Unable to find image 'busybox@sha256:bdf5' locally\nStatus: Downloaded newer image\nabc123\n"), nil, true
			}
			if slices.Contains(args, "type=bind,src=/tmp/someone-else,dst=/m,readonly") {
				return []byte("zzz999\n"), nil, true
			}
			return []byte("bind source path does not exist"), errors.New("exit 125"), true
		}
		return nil, nil, false
	})
	if read, err := docker.ReadMark(context.Background(), docker.Scope{Client: d}, "/tmp/forge-host-mark-1", "abc123"); err != nil || !read {
		t.Fatalf("the mark beside the CLI's own lines: %v, %v", read, err)
	}
	if read, err := docker.ReadMark(context.Background(), docker.Scope{Client: d}, "/tmp/someone-else", "abc123"); err != nil || read {
		t.Errorf("another file read as the mark: %v, %v", read, err)
	}
	if slices.Contains(seen, "-v") || !slices.Contains(seen, "--network") || !slices.Contains(seen, "--read-only") {
		t.Errorf("run as %v", seen)
	}
	if read, err := docker.ReadMark(context.Background(), docker.Scope{Client: d}, "/elsewhere", "abc123"); err == nil || read || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("a refused mount read as %v, %v", read, err)
	}
}
