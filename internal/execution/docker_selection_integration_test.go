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

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executiontest"
)

// A context rewritten between AppTown's selection and the CLI's call does
// not move the call: the CLI wrapper changes a real context just before the
// real CLI runs, without faking replies. The selected daemon is unavailable,
// and the replacement is the test daemon, on which nothing may be created.
func TestIntegrationAContextRewrittenDuringACallDoesNotMoveIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	binary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	cli := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	endpoint := os.Getenv("DOCKER_HOST")
	if endpoint == "" {
		endpoint = cli("context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	}
	dir := t.TempDir()
	config := filepath.Join(dir, "config")
	t.Setenv("DOCKER_CONFIG", config)
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "round5-race")
	// Keep the unavailable Unix socket address within the platform length limit.
	missingDir, err := os.MkdirTemp("", "apptown-r5-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(missingDir) })
	original := "unix://" + filepath.Join(missingDir, "unavailable.sock")
	cli("context", "create", "round5-race", "--docker", "host="+original)
	selected := execution.SelectDocker()
	pinned := selected.Host
	if selected.Refused != nil || pinned != original {
		t.Fatalf("fixture endpoint: %+v", selected)
	}
	t.Setenv("REVIEW_R5_DOCKER", binary)
	t.Setenv("REVIEW_R5_ENDPOINT", endpoint)
	wrapper := filepath.Join(dir, "docker")
	if err := execfile.Write(wrapper, []byte(`#!/bin/sh
"$REVIEW_R5_DOCKER" context update round5-race --docker "host=$REVIEW_R5_ENDPOINT" >/dev/null || exit $?
exec "$REVIEW_R5_DOCKER" "$@"
`), 0755); err != nil {
		t.Fatal(err)
	}
	key := executiontest.UniqueKeys("round5")("endpoint-race")
	// Always clean up on the replacement, even though the admitted driver must
	// refuse to use it and therefore cannot be used for cleanup.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = exec.CommandContext(cleanupCtx, binary, "--host", endpoint, "rm", "-f", "forge-task-"+key).Run()
	})
	d := execution.DockerTasks{Binary: wrapper, Daemon: selected}
	obs, err := d.Ensure(ctx, dockerSpec(key, "must not run", 0, false))
	if err == nil || obs.ID != "" {
		t.Fatalf("admitted endpoint %s was replaced by %s during the call; created %s on the replacement: %v", pinned, endpoint, obs.ID, err)
	}
	if !errors.Is(err, execution.ErrTaskUnobservable) {
		t.Fatalf("lost endpoint must remain unobservable: %v", err)
	}
}
