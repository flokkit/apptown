package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
func BuildAgentImage(ctx context.Context, docker, dir string) (string, error) {
	image, err := AgentImage(dir)
	if err != nil {
		return "", err
	}
	if exec.CommandContext(ctx, docker, "image", "inspect", image).Run() == nil {
		return image, nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	out, err := exec.CommandContext(ctx, docker, "build", "-t", image, dir).CombinedOutput()
	if err != nil {
		tail := strings.TrimSpace(string(out))
		if len(tail) > 2000 {
			tail = tail[len(tail)-2000:]
		}
		return "", fmt.Errorf("building %s: %w\n%s", image, err, tail)
	}
	return image, nil
}

// DockerImages answers whether the worker's Docker has an image.
type DockerImages struct{ Binary string }

func (d DockerImages) Has(ctx context.Context, image string) (bool, error) {
	out, err := exec.CommandContext(ctx, d.Binary, "image", "inspect", "--format", "{{.Id}}", image).CombinedOutput()
	if err == nil {
		return true, nil
	}
	if strings.Contains(strings.ToLower(string(out)), "no such image") {
		return false, nil
	}
	return false, fmt.Errorf("docker image inspect: %v: %s", err, strings.TrimSpace(string(out)))
}
