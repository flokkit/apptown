//go:build integration

package apptown

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// In the no-local-Docker mode an attached target's connection check reaches
// a real daemon through the Docker CLI with that target's --host, while the
// AppTown's own Docker refuses, and neither DOCKER_HOST nor DOCKER_CONTEXT
// chooses a daemon. This host's selected daemon plays the attached target, by
// its explicit address; APPTOWN_TEST_ATTACHED_HOST names another (for example
// ssh://root@lab.example).
func TestIntegrationAnAttachedTargetAnswersInTheNoLocalDockerMode(t *testing.T) {
	requireDocker(t)
	target := os.Getenv("APPTOWN_TEST_ATTACHED_HOST")
	if target == "" {
		target = execution.SelectDocker().Host
	}
	db := testDB(t) // its container is started before the environment changes
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:9")
	t.Setenv("DOCKER_CONTEXT", "no-such-context")
	// The environment is in force: a call naming no host fails under it.
	if _, err := (docker.CLI{}).Run(context.Background(), "", docker.Command{Args: []string{"info"}, Timeout: 30 * time.Second}); err == nil {
		t.Fatal("the ambient Docker still answered under DOCKER_HOST and DOCKER_CONTEXT")
	}
	a := newApp(t, db, Config{NoLocalDocker: true, ConsoleCLI: fixtureCLI(t),
		Worker: workerClientConfig(t, workertest.NewCA(t), "https://worker.invalid")})
	if _, err := a.docker.Run(context.Background(), "info"); !errors.Is(err, docker.ErrNoLocalBackend) {
		t.Fatalf("AppTown's own Docker: %v", err)
	}
	// The connection check an attachment makes (ProbeHostTarget), on the
	// target's scope.
	out, err := a.docker.WithHost(target, "", "").Run(context.Background(), "info", "--format", "{{.ServerVersion}} on {{.OperatingSystem}}, {{.ContainersRunning}}/{{.Containers}} containers")
	if err != nil || !strings.Contains(string(out), " on ") {
		t.Fatalf("the attached target %s: %s %v", target, out, err)
	}
	t.Logf("the attached target %s answered: docker %s", target, strings.TrimSpace(string(out)))
}
