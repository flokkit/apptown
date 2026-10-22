package docker

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// blockingClient is a Docker whose named commands run until they are
// stopped; the rest answer as the recording client does.
type blockingClient struct {
	blocks  string // the command that blocks: "build", "run", …
	entered chan struct{}
	calls   atomic.Int32
}

func (c *blockingClient) Run(ctx context.Context, host string, req Command) ([]byte, error) {
	c.calls.Add(1)
	args := req.Args
	if len(args) > 1 && args[0] == "--config" {
		args = args[2:]
	}
	if len(args) > 0 && args[0] == c.blocks {
		select {
		case c.entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return (&recordingClient{}).Run(ctx, host, req)
}

// A command through a scope with a lifetime is stopped when the lifetime
// ends, and one asked for after it ended is refused without reaching
// Docker.
func TestACommandEndsWithItsScopesLifetime(t *testing.T) {
	t.Parallel()
	client := &blockingClient{blocks: "build", entered: make(chan struct{}, 1)}
	life, cancel := context.WithCancel(context.Background())
	dk := Scope{Client: client, Host: "unix:///lifetime"}.WithLifetime(life)
	done := make(chan error, 1)
	go func() {
		_, err := dk.Command(context.Background(), Command{Args: []string{"build", "."}})
		done <- err
	}()
	<-client.entered
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a command stopped by its lifetime answered success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a command outlived its scope's lifetime")
	}
	before := client.calls.Load()
	if _, err := dk.Command(context.Background(), Command{Args: []string{"ps"}}); err == nil {
		t.Fatal("a command after the lifetime ended was run")
	}
	if client.calls.Load() != before {
		t.Fatal("a command after the lifetime ended reached Docker")
	}
}

// A boot whose build does not finish is ended by its lifetime: the boot
// answers an error soon after, and its own Docker configuration is gone.
func TestABootIsEndedByItsLifetime(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("version: 1\napp:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	prepared := Prepared{Version: PreparedVersion, Contract: c}
	client := &blockingClient{blocks: "build", entered: make(chan struct{}, 1)}
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	scratch := t.TempDir()
	finished := make(chan error, 1)
	go func() {
		_, err := NewHost(prepared.Dependencies(upProbe{}, "", "")).Boot(context.Background(), BootRequest{
			Docker: Scope{Client: client, Host: "unix:///lifetime"}.WithLifetime(life), Name: "apptown-preview-change-43",
			StackID: "pv43", Workspace: t.TempDir(), Scratch: scratch, Probe: time.Second,
		}, c)
		finished <- err
	}()
	<-client.entered
	if left, _ := filepath.Glob(filepath.Join(scratch, bootConfigPattern)); len(left) != 1 {
		t.Fatalf("the boot's configuration while it builds: %q", left)
	}
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("a boot ended by its lifetime answered success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a boot outlived its lifetime")
	}
	left, _ := filepath.Glob(filepath.Join(scratch, bootConfigPattern))
	for _, dir := range left {
		if _, err := os.Stat(dir); err == nil {
			t.Fatalf("an ended boot left its Docker configuration: %s", dir)
		}
	}
}
