//go:build integration

package execution_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execfile"
	"github.com/flokkit/apptown/stock"

	"github.com/flokkit/apptown/internal/execution/executiontest"
	"github.com/flokkit/apptown/internal/poll"

	"github.com/flokkit/apptown/internal/execution"
)

// Exercise actual Docker CLI initialization failure, without contacting a daemon.
// All these failures must enter TaskRunner's unanswered retry path.
func TestIntegrationDockerClientFailuresAreUnobservable(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2376")
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DOCKER_TLS_VERIFY", "1")
	t.Setenv("DOCKER_CERT_PATH", t.TempDir())
	d, ctx := execution.DockerTasks{ToolVersions: stock.Versions()}, context.Background()
	ref := execution.TaskRef{Key: "client-unavailable-all"}
	for _, op := range []string{"ensure", "wait", "output"} {
		t.Run(op, func(t *testing.T) {
			var err error
			switch op {
			case "ensure":
				_, err = d.Ensure(ctx, dockerSpec(ref.Key, "hi", 0, false))
			case "wait":
				_, err = d.Wait(ctx, ref)
			case "output":
				_, err = d.Output(ctx, ref, execution.OutputRead{})
			}
			if !errors.Is(err, execution.ErrTaskUnobservable) {
				t.Fatalf("%s bypasses the unobservable retry path: %v", op, err)
			}
		})
	}
}

// Pause one real inspect reply while another Start runs the short task to
// completion. This deterministically exercises the inspect/start race.
func TestIntegrationOverlappingDockerStartsRunATaskOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	barrier := t.TempDir()
	t.Setenv("TEST_DOCKER_BIN", binary)
	t.Setenv("TEST_BARRIER", barrier)
	wrapper := filepath.Join(barrier, "docker")
	body := `#!/bin/sh
if [ "$1" = inspect ]; then
 "$TEST_DOCKER_BIN" "$@" > "$TEST_BARRIER/observed" || exit $?
 touch "$TEST_BARRIER/ready"
 while [ ! -e "$TEST_BARRIER/release" ]; do sleep 0.01; done
 cat "$TEST_BARRIER/observed"
 exit 0
fi
exec "$TEST_DOCKER_BIN" "$@"
`
	if err := execfile.Write(wrapper, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	key := executiontest.UniqueKeys("race")("once")
	d := execution.DockerTasks{ToolVersions: stock.Versions()}
	t.Cleanup(func() { _ = d.Release(context.Background(), execution.TaskRef{Key: key}) })
	obs, err := d.Ensure(ctx, dockerSpec(key, "executed\n", 0, false))
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: key, ProviderID: obs.ID}
	delayed := execution.DockerTasks{ToolVersions: stock.Versions(), Binary: wrapper}
	done := make(chan error, 1)
	go func() { done <- delayed.Start(ctx, ref) }()
	defer func() { _ = os.WriteFile(filepath.Join(barrier, "release"), nil, 0600) }()
	for {
		if _, err := os.Stat(filepath.Join(barrier, "ready")); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Starts of one task are serialized: the second waits for the delayed
	// one instead of running beside it, so it is started alongside and the
	// delayed reply released.
	second := make(chan error, 1)
	go func() { second <- d.Start(ctx, ref) }()
	// The second Start is given its chance before the delayed reply is
	// released: until it returns, or for a bounded time if it waits on the
	// delayed one. Either way the command must run once (below).
	_ = poll.Until(ctx, 200*time.Millisecond, 10*time.Millisecond, func() bool { return len(second) > 0 })
	if err := os.WriteFile(filepath.Join(barrier, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if _, err := d.Wait(ctx, ref); err != nil {
		t.Fatal(err)
	}
	page, err := d.Output(ctx, ref, execution.OutputRead{})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(page.Data), "executed"); n != 1 {
		t.Fatalf("overlapping Starts executed the command %d times: %q", n, page.Data)
	}
}
