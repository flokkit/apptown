package worker_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

func TestAnImageExportStaysInsideTheRequestTree(t *testing.T) {
	binary, _, _ := fakeImageDocker(t)
	ca := workertest.NewCA(t)
	hs := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Checks: execution.Runner{DockerBinary: binary}})
	checks := worker.Checks{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	tree, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "image.tar")
	if err := os.WriteFile(target, []byte("must survive"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tree, ".apptown")); err != nil {
		t.Fatal(err)
	}
	out := checks.Run(context.Background(), execution.Request{Workspace: tree, Image: "scanner:test", Argv: []string{"true"}, Network: "none", Timeout: time.Second, ExportImage: "sha256:app"})
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "must survive" {
		t.Fatalf("image export escaped the uploaded tree and changed a worker-host file: body=%q err=%v outcome=%+v", body, err, out)
	}
}

func TestABuildContextCannotFollowALinkOutOfTheTree(t *testing.T) {
	binary, _, _ := fakeImageDocker(t)
	ca := workertest.NewCA(t)
	hs := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Checks: execution.Runner{DockerBinary: binary}})
	checks := worker.Checks{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	tree, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "Dockerfile"), []byte("FROM scratch\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tree, "app")); err != nil {
		t.Fatal(err)
	}
	a := checks.Image(context.Background(), execution.ImageRequest{Tag: "review:escape", Tree: tree, Context: "app", AllowBuild: true})
	if a.Err == "" || a.ID != "" {
		t.Fatalf("an outside context was prepared: %+v", a)
	}
}

// The helper blocks after a dummy login is written, so killing it models a
// worker lost in the middle of an authenticated image preparation.
func TestImagePreparationHelper(t *testing.T) {
	marker := os.Getenv("APPTOWN_REVIEW_LOGIN_MARKER")
	if marker == "" {
		t.Skip("subprocess helper")
	}
	execution.PrepareImage(context.Background(), func(ctx context.Context, _ io.Reader, args ...string) ([]byte, error) {
		if len(args) > 2 && args[2] == "login" {
			if err := os.WriteFile(filepath.Join(args[1], "config.json"), []byte(`{"auths":{"example.test":{"auth":"dummy"}}}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, []byte(args[1]), 0600); err != nil {
				t.Fatal(err)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return nil, nil
	}, execution.ImageRequest{Pull: "example.test/app:1", Registry: &execution.RegistryLogin{Host: "example.test", Username: "test", Token: "dummy"}, Timeout: time.Minute,
		Scratch: os.Getenv("APPTOWN_REVIEW_SCRATCH")})
}

func TestARestartRemovesInterruptedRegistryCredentials(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "login-dir")
	// The request's folder, as the worker makes one: under its checks,
	// named by the request's deadline (an hour away).
	state := t.TempDir()
	scratch := filepath.Join(state, "checks", fmt.Sprintf("%d-request", time.Now().Add(time.Hour).Unix()))
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestImagePreparationHelper$")
	child.Env = append(os.Environ(), "APPTOWN_REVIEW_LOGIN_MARKER="+marker, "APPTOWN_REVIEW_SCRATCH="+scratch)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	var dir string
	poll.Until(context.Background(), 5*time.Second, 10*time.Millisecond, func() bool {
		if b, err := os.ReadFile(marker); err == nil {
			dir = strings.TrimSpace(string(b))
		}
		return dir != ""
	})
	if dir == "" {
		t.Fatal("helper did not create its login")
	}
	defer os.RemoveAll(dir)
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	binary, _ := sweepDocker(t)
	server := worker.Server{State: state}
	server.SweepChecks(context.Background(), binary, true)
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("request credentials survived worker death and startup sweep: %v", err)
	}
}
