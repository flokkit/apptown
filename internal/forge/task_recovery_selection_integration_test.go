//go:build integration

package forge

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/execution/executiontest"
)

// A Docker context that cannot be read when the forge starts refuses tasks
// for the forge's lifetime: restoring the context does not let the running
// forge create one under its unresolved backend. The production App driver
// and real Docker.
func TestIntegrationAnUnresolvedDockerContextAtStartupCreatesNothing(t *testing.T) {
	_ = testDB(t)
	endpoint := os.Getenv("DOCKER_HOST")
	if endpoint == "" {
		endpoint = dockerCLI(t, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	}
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "round5-restored")
	a := testApp(t)
	if !strings.Contains(a.tasks.Backend, "(unresolved)") {
		t.Fatalf("fixture did not start unresolved: %q", a.tasks.Backend)
	}
	dockerCLI(t, "context", "create", "round5-restored", "--docker", "host="+endpoint)
	b := newApp(t, a.DB, Config{DataDir: a.cfg.dataDir})
	key := executiontest.UniqueKeys("round5")("unresolved")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = b.tasks.Driver.Release(ctx, execution.TaskRef{Key: key})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	obs, err := a.tasks.Driver.Ensure(ctx, execution.TaskSpec{
		Key: key, Image: "alpine:3.21", Argv: []string{"sh", "-c", "echo must-not-create"},
		Admission: execution.Admission{Profile: "local-docker", Network: execution.NetworkNone},
	})
	if err == nil || obs.ID != "" {
		t.Fatalf("unresolved startup allowed real task %s: original backend=%q restarted backend=%q; recovery cannot match them", obs.ID, a.tasks.Backend, b.tasks.Backend)
	}
	// Refused, not unobservable: the forge knows nothing was created, so the
	// step fails with the reason at once instead of asking until its limit.
	if !errors.Is(err, execution.ErrUnsupported) {
		t.Fatalf("an unresolved backend must refuse to create: %v", err)
	}
}
