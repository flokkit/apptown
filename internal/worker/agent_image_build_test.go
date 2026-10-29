package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/execfile"
)

// The worker's agent image is built by the one build rule (dockerbuild):
// its daemon's own builder named. It used to be a plain `docker build`,
// which honours whatever builder the worker's Docker has selected.
func TestTheWorkersAgentImageIsBuiltByTheBuildRule(t *testing.T) {
	t.Parallel()
	dir, bin := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(bin, "log")
	script := `#!/bin/sh
case "$*" in
"image inspect "*) exit 1 ;;
"buildx version") echo "github.com/docker/buildx v0.38.1" ;;
"context show") echo worker ;;
"buildx inspect worker") printf 'Name: worker\nDriver: docker\n' ;;
*) echo "$*" >> "` + log + `" ;;
esac
`
	docker := filepath.Join(bin, "docker")
	if err := execfile.Write(docker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	image, err := BuildAgentImage(context.Background(), docker, dir)
	if err != nil {
		t.Fatal(err)
	}
	built, err := os.ReadFile(log)
	if err != nil || !strings.HasPrefix(string(built), "build ") || !strings.Contains(string(built), "--builder worker -t "+image+" "+dir) {
		t.Fatalf("the agent image's build: %q %v", built, err)
	}
}
