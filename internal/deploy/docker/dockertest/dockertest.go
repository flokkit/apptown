// Package dockertest runs the docker CLI against this machine's own daemon
// for tests that set up, inspect or clean up containers, images and networks
// the code under test does not. It goes through docker.CLI, so a test's
// docker starts as AppTown's does (no ambient Buildx builder or
// configuration), and a test needs no process of its own. A test that
// gives docker an environment or stdin uses docker.CLI with a
// docker.Command directly.
package dockertest

import (
	"bytes"
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/deploy/docker"
)

// Run runs docker with args and reports only whether it succeeded.
func Run(args ...string) error {
	_, err := CombinedOutput(args...)
	return err
}

// CombinedOutput runs docker with args and returns stdout and stderr together.
func CombinedOutput(args ...string) ([]byte, error) {
	return docker.CLI{}.Run(context.Background(), "", docker.Command{Args: args})
}

// Output runs docker with args and returns stdout alone: ids or a format's
// answer, unpolluted by warnings. A failure carries stderr.
func Output(args ...string) ([]byte, error) {
	var stdout bytes.Buffer
	stderr, err := docker.CLI{}.Run(context.Background(), "", docker.Command{Args: args, Stdout: &stdout})
	if err != nil {
		err = fmt.Errorf("docker %v: %w: %s", args, err, bytes.TrimSpace(stderr))
	}
	return stdout.Bytes(), err
}
