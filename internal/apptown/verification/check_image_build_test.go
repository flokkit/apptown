package verification

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
)

// buildDaemon answers the build rule's questions and has no images.
type buildDaemon struct{ commands [][]string }

func (d *buildDaemon) Run(_ context.Context, _ string, c docker.Command) ([]byte, error) {
	d.commands = append(d.commands, c.Args)
	switch strings.Join(c.Args, " ") {
	case "buildx version":
		return []byte("github.com/docker/buildx v0.38.1"), nil
	case "context show":
		return []byte("default\n"), nil
	case "buildx inspect default":
		return []byte("Name: default\nDriver: docker\n"), nil
	}
	if c.Args[0] == "image" {
		return []byte("Error: No such image"), errors.New("exit status 1")
	}
	return nil, nil
}

// A check image AppTown builds on its own Docker goes through the one
// build rule (docker.Scope.Build): the daemon's own builder named. It used
// to be a plain `docker build`, which honours whatever builder is selected.
func TestALocalCheckImageIsBuiltByTheBuildRule(t *testing.T) {
	t.Parallel()
	daemon := &buildDaemon{}
	tree := t.TempDir()
	if err := os.Mkdir(filepath.Join(tree, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	exec := LocalExecutor(execution.Runner{}, &docker.Scope{Client: daemon}, "", time.Now)
	exec.Image(t.Context(), execution.ImageRequest{Tag: "apptown-conform:abc", Tree: tree, Context: "app", AllowBuild: true})
	for _, c := range daemon.commands {
		if c[0] == "build" {
			if i := slices.Index(c, "--builder"); i < 0 || c[i+1] != "default" {
				t.Fatalf("the check image's build names no builder: %q", c)
			}
			return
		}
	}
	t.Fatalf("no build: %q", daemon.commands)
}
