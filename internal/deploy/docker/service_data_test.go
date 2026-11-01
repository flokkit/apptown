package docker_test

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// A preview's or an agent step's need that keeps data gets a disposable
// folder there: an anonymous volume (-v <path>) on its --rm container, so it
// goes with the container. S3's emulator exits at once without /data. A
// service with no data path gets no volume.
func TestAThrowawayNeedThatKeepsDataGetsADisposableFolderThere(t *testing.T) {
	t.Parallel()
	daemon := newDockerFake()
	dk := docker.Scope{Client: daemon}
	svcs := []deploy.ServiceSpec{{Name: "s3", Image: "versity/versitygw:v1.8.0", Data: "/data"}, {Name: "cache", Image: "redis:7"}}
	if _, cleanup, err := docker.StartServices(t.Context(), dk, "preview-9", svcs); err != nil {
		t.Fatal(err)
	} else {
		cleanup()
	}
	var s3, cache string
	for _, args := range daemon.Commands() {
		cmd := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(cmd, "run ") && strings.Contains(cmd, "--network-alias s3 "):
			s3 = cmd
		case strings.HasPrefix(cmd, "run ") && strings.Contains(cmd, "--network-alias cache "):
			cache = cmd
		}
	}
	if !strings.Contains(s3, " --rm ") || !strings.Contains(s3, " -v /data versity/versitygw:v1.8.0") {
		t.Fatalf("s3 started without a disposable folder at /data: %q", s3)
	}
	if cache == "" || strings.Contains(cache, " -v ") {
		t.Fatalf("a service with no data path got a volume: %q", cache)
	}
}
