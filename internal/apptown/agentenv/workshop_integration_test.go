//go:build integration

package agentenv

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/dind"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

// Rehearsal containers use the owner's DNS from the private daemon's namespace.
func TestIntegrationPrivateDockerResolvesDeclaredNeeds(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	dk := docker.Scope{Client: docker.CLI{}}
	if _, err := dk.Run(ctx, "info"); err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	_, advice, ok := strings.Cut(DockerInstructions, "`--network ")
	if !ok {
		t.Fatal("Docker instructions do not say how to run containers that use declared needs")
	}
	flags, _, ok := strings.Cut(advice, "`")
	if !ok {
		t.Fatal("Docker network instruction has no closing code delimiter")
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := dk.Run(ctx, args...)
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	suffix := strings.ToLower(ids.New(time.Now()).String())
	network, daemon, need := "apptown-need-net-"+suffix, "apptown-need-dind-"+suffix, "apptown-need-"+suffix
	must("network", "create", network)
	t.Cleanup(func() { _, _ = dk.Run(context.Background(), "network", "rm", network) })
	t.Cleanup(func() { _, _ = dk.Run(context.Background(), "rm", "-f", "-v", need, daemon) })
	image := stock.Images().DF
	must("run", "-d", "--name", need, "--network", network, "--network-alias", "declared-need", image, "sh", "-c", "mkdir -p /www; echo declared-need >/www/index.html; exec httpd -f -p 8080 -h /www")
	d := dind.Daemon{Name: daemon, Network: network, Image: stock.Images().PrivateDocker, OwnerLabel: "apptown.test", Owner: suffix, Options: dind.DaemonOptions("")}
	if err := d.Create(ctx, dk.Run); err != nil {
		t.Fatal(err)
	}
	if err := dind.Ready(ctx, dk.Run, daemon); err != nil {
		t.Fatal(err)
	}
	must("exec", "-e", "DOCKER_HOST=tcp://localhost:2375", daemon, "docker", "pull", image)
	args := []string{"exec", "-e", "DOCKER_HOST=tcp://localhost:2375", daemon, "docker", "run", "--rm"}
	args = append(args, strings.Fields("--network "+flags)...)
	args = append(args, image, "wget", "-qO-", "http://declared-need:8080")
	out := must(args...)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[len(lines)-1] != "declared-need" {
		t.Fatalf("declared need unreachable: %s", out)
	}
}
