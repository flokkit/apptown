package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	dockercli "github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/dockerbuild"
)

// The agent image a worker's chats run in is built by the worker from the
// same build context AppTown uses (deploy/apptown-agent), and named by that
// context's content: apptown-agent:<hash>. An AppTown asks for the name its own
// context gives, so a worker whose context differs does not have the image
// and refuses the chat, saying so, rather than running another version.
// Later, installations may use standard published images instead.

// AgentImage is the name of the agent image built from the build context
// dir.
func AgentImage(dir string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(body))
		h.Write(body)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("reading the agent image's build context: %w", err)
	}
	return "apptown-agent:" + hex.EncodeToString(h.Sum(nil))[:12], nil
}

// BuildAgentImage builds the agent image from dir with docker, unless the
// worker's Docker has it. It answers the image's name.
func BuildAgentImage(ctx context.Context, dk dockercli.Scope, dir string) (string, error) {
	image, err := AgentImage(dir)
	if err != nil {
		return "", err
	}
	// An answer that is not "absent" goes on to the build, which reports
	// its own failure: a daemon that cannot inspect cannot build either.
	if _, found, err := dk.Image(ctx, image); err == nil && found {
		return image, nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	// The one build rule (internal/dockerbuild): the worker's daemon's own
	// builder named, and the worker's extra certificates as the build's
	// secret, which the agent image's RUN lines download with.
	out, err := dockerbuild.Daemon{Run: dk.Run, Local: true}.Build(ctx, "-t", image, dir)
	if err != nil {
		tail := strings.TrimSpace(string(out))
		if len(tail) > 2000 {
			tail = tail[len(tail)-2000:]
		}
		return "", fmt.Errorf("building %s: %w\n%s", image, err, tail)
	}
	return image, nil
}

// DockerImages answers whether the worker's Docker has an image, and its
// labels.
type DockerImages struct{ Docker dockercli.Scope }

// Has: an image the daemon does not know is false; a daemon that cannot be
// asked is an error, never "absent".
func (d DockerImages) Has(ctx context.Context, image string) (bool, error) {
	_, found, err := d.Docker.Image(ctx, image)
	return found, err
}
