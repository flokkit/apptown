package docker_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
)

// A named service container is left running, started when stopped, created
// when missing, and a create that loses a race to another process uses the
// winner's container instead of failing, though Docker answers "no such
// container" for a moment after the name conflict.
func TestEnsureRunningStartsCreatesOrLeavesAlone(t *testing.T) {
	run := []string{"-d", "--name", "db", "postgres:16-alpine"}
	count := func(d *dockerfake.Daemon, prefix string) int { return len(d.Matching(prefix)) }

	fresh := dockerfake.New()
	if err := (docker.Scope{Client: fresh}).EnsureRunning(context.Background(), "db", run); err != nil || count(fresh, "run -d --name db") != 1 || count(fresh, "start") != 0 {
		t.Fatalf("missing container must be created once: %v %v", err, fresh.Commands())
	}

	running := dockerfake.New()
	running.SetRunning("db", true)
	if err := (docker.Scope{Client: running}).EnsureRunning(context.Background(), "db", run); err != nil || count(running, "run") != 0 || count(running, "start") != 0 {
		t.Fatalf("a running container is left alone: %v %v", err, running.Commands())
	}

	stopped := dockerfake.New()
	stopped.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) == 2 && args[0] == "inspect" {
			return []byte("[]"), nil, true // it exists
		}
		return nil, nil, false // State.Running answers false
	})
	if err := (docker.Scope{Client: stopped}).EnsureRunning(context.Background(), "db", run); err != nil || count(stopped, "start db") != 1 || count(stopped, "run") != 0 {
		t.Fatalf("a stopped container is started, not recreated: %v %v", err, stopped.Commands())
	}

	raced := dockerfake.New()
	inspections := 0
	raced.SetReply(func(args []string) ([]byte, error, bool) {
		switch {
		case args[0] == "inspect" && len(args) == 2:
			inspections++
			// Absent before the run and for the first looks after the
			// conflict: Docker reserves the name before the winner's
			// container can be inspected.
			if inspections <= 3 {
				return []byte("No such container"), errors.New("absent"), true
			}
			return []byte("[]"), nil, true
		case args[0] == "run":
			return []byte("Conflict. The container name \"/db\" is already in use"), errors.New("exit status 125"), true
		}
		return nil, nil, false
	})
	// The winner's container is running, so it is used, not run again.
	if err := (docker.Scope{Client: raced}).EnsureRunning(context.Background(), "db", run); err != nil || count(raced, "run") != 1 || count(raced, "inspect db") != 4 {
		t.Fatalf("a lost create race uses the winner's container: %v %v", err, raced.Commands())
	}

	broken := dockerfake.New()
	broken.SetReply(func(args []string) ([]byte, error, bool) {
		switch {
		case args[0] == "run":
			return []byte("no such image"), errors.New("exit status 125"), true
		case args[0] == "inspect" && len(args) == 2:
			return []byte("No such container"), errors.New("absent"), true
		}
		return nil, nil, false
	})
	if err := (docker.Scope{Client: broken}).EnsureRunning(context.Background(), "db", run); err == nil || !strings.Contains(err.Error(), "no such image") {
		t.Fatalf("a failed create reports Docker's words: %v", err)
	}
}
