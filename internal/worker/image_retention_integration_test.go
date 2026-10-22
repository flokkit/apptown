//go:build integration

package worker_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dockercli "github.com/flokkit/apptown/internal/deploy/docker"

	"github.com/flokkit/apptown/internal/worker"
)

// Two commit tags can name the same cached build. Expiry must reclaim that
// unused image too. The wrapper exposes ONLY this test's image to the sweep.
func TestIntegrationAnExpiredImageWithTwoNamesIsRemoved(t *testing.T) {
	t.Setenv("BUILDX_CONFIG", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	cli := func(args ...string) string {
		t.Helper()
		b, err := exec.CommandContext(ctx, docker, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	nonce := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	first, second := "apptown-review-step4:"+nonce+"-a", "apptown-review-step4:"+nonce+"-b"
	t.Cleanup(func() { _ = exec.Command(docker, "rmi", first, second).Run() })
	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "Dockerfile"), []byte("FROM scratch\nLABEL forge.class=check review.test="+nonce+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cli("build", "-q", "-t", first, tree)
	cli("tag", first, second)
	id := cli("image", "inspect", "--format", "{{.Id}}", first)
	srv := worker.Server{State: t.TempDir()}
	uses := filepath.Join(srv.State, "check-images")
	if err := os.MkdirAll(uses, 0700); err != nil {
		t.Fatal(err)
	}
	used := filepath.Join(uses, strings.TrimPrefix(id, "sha256:"))
	if err := os.WriteFile(used, nil, 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(used, old, old); err != nil {
		t.Fatal(err)
	}
	// Only this image is listed; the sweep's other questions (who uses it,
	// what it is named) and its removals reach the real Docker.
	wrapper := onlyImage{real: dockercli.CLI{Binary: docker}, id: id}
	srv.SweepChecks(ctx, wrapper, false)
	if b, err := exec.CommandContext(ctx, docker, "image", "inspect", "--format", "{{json .RepoTags}}", id).CombinedOutput(); err == nil {
		t.Fatalf("expired unused image survived the sweep: %s: %s", id, b)
	}
}

// An expired check image a container still uses, even a stopped one, is kept,
// with its names: the sweep asks again next time.
func TestIntegrationAnExpiredImageInUseIsKept(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	cli := func(args ...string) string {
		t.Helper()
		b, err := exec.CommandContext(ctx, docker, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	nonce := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	tag := "apptown-review-step4:" + nonce + "-used"
	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "Dockerfile"), []byte("FROM alpine:3\nLABEL forge.class=check review.test="+nonce+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cli("build", "-q", "-t", tag, tree)
	id := cli("image", "inspect", "--format", "{{.Id}}", tag)
	container := cli("create", tag, "true")
	t.Cleanup(func() {
		_ = exec.Command(docker, "rm", "-f", container).Run()
		_ = exec.Command(docker, "rmi", tag).Run()
	})
	srv := worker.Server{State: t.TempDir()}
	uses := filepath.Join(srv.State, "check-images")
	if err := os.MkdirAll(uses, 0700); err != nil {
		t.Fatal(err)
	}
	used := filepath.Join(uses, strings.TrimPrefix(id, "sha256:"))
	if err := os.WriteFile(used, nil, 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(used, old, old); err != nil {
		t.Fatal(err)
	}
	// Only this image is listed; the sweep's other questions (who uses it,
	// what it is named) and its removals reach the real Docker.
	wrapper := onlyImage{real: dockercli.CLI{Binary: docker}, id: id}
	srv.SweepChecks(ctx, wrapper, false)
	if got := cli("image", "inspect", "--format", "{{json .RepoTags}}", id); !strings.Contains(got, tag) {
		t.Fatalf("an image a container uses lost its name or went: %s", got)
	}
}

// onlyImage lists one check image and passes the sweep's other questions
// and removals to the real Docker.
type onlyImage struct {
	real dockercli.Client
	id   string
}

func (o onlyImage) Run(ctx context.Context, host string, c dockercli.Command) ([]byte, error) {
	switch c.Args[0] {
	case "images":
		_, _ = c.Stdout.Write([]byte(o.id + "\n"))
		return nil, nil
	case "rmi", "ps", "image":
		return o.real.Run(ctx, host, c)
	}
	return nil, nil
}
