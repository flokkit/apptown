//go:build integration

package dind_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/dind"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// A private daemon, started the common way, pushes to and pulls from a
// registry whose certificate the extra authority signed, and the builder
// made in it builds FROM that registry: dockerd and BuildKit each trust the
// extra certificates.
// Not parallel: it installs the process's trust.
func TestIntegrationAPrivateDaemonAndItsBuilderTrustATLSRegistry(t *testing.T) {
	ctx := context.Background()
	company := trusttest.New(t, "Company Inspecting CA")
	trusttest.Install(t, company.Bundle(t), trust.Bundle{})
	dk := docker.Scope{Client: docker.CLI{}}
	must := func(args ...string) string {
		t.Helper()
		out, err := dk.Run(ctx, args...)
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	id := strings.ToLower(ids.New(time.Now()).String())

	// The private daemon's network, which the registry joins: the daemon
	// and its builder (network=host, in the daemon's namespace) reach it by
	// its container name, with no port published on the host.
	network, name := "apptown-trust-net-"+id, "apptown-trust-dind-"+id
	must("network", "create", network)
	t.Cleanup(func() { _, _ = dk.Run(ctx, "network", "rm", network) })

	// The registry, over TLS from the company's authority.
	registry := "apptown-trust-registry-" + id
	certs := t.TempDir()
	certPEM, keyPEM, _ := company.Server(t, registry)
	for file, body := range map[string][]byte{"cert.pem": certPEM, "key.pem": keyPEM} {
		if err := os.WriteFile(filepath.Join(certs, file), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = dk.Run(ctx, "rm", "-f", registry) })
	must("run", "-d", "--name", registry, "--network", network, "-v", certs+":/certs:ro",
		"-e", "REGISTRY_HTTP_TLS_CERTIFICATE=/certs/cert.pem", "-e", "REGISTRY_HTTP_TLS_KEY=/certs/key.pem", "registry:2")
	at := registry + ":5000"

	// The private daemon and its builder, as an owner makes them.
	t.Cleanup(func() { _, _ = dk.Run(ctx, "rm", "-f", "-v", name) })
	config := t.TempDir()
	if err := os.WriteFile(filepath.Join(config, "buildkitd.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	daemon := dind.Daemon{Name: name, Network: network, Image: "docker:27-dind", OwnerLabel: "apptown.test", Owner: id,
		Args: []string{"-v", config + ":/forge-dind:ro"}}
	if err := daemon.Create(ctx, dk.Run); err != nil {
		t.Fatal(err)
	}
	if err := dind.Ready(ctx, dk.Run, name); err != nil {
		t.Fatal(err)
	}
	inner := func(script string) (string, error) {
		out, err := dk.Run(ctx, "exec", "-e", "DOCKER_HOST=tcp://localhost:2375", "-e", "DOCKER_CONFIG=/tmp/client", name, "sh", "-c", script)
		return string(out), err
	}

	// An image in the daemon, pushed to the registry and pulled back.
	saved := filepath.Join(t.TempDir(), "busybox.tar")
	must("save", "-o", saved, "busybox:latest")
	must("cp", saved, name+":/busybox.tar")
	if out, err := inner("docker load -i /busybox.tar && docker tag busybox:latest " + at + "/busybox:t && docker push " + at + "/busybox:t && docker rmi " + at + "/busybox:t && docker pull " + at + "/busybox:t"); err != nil {
		t.Fatalf("the private daemon does not trust the registry: %v\n%s", err, out)
	}

	if err := dind.Builder(ctx, dk.Run, name, "/tmp/client", "/forge-dind/buildkitd.toml"); err != nil {
		t.Fatal(err)
	}
	if out, err := inner(fmt.Sprintf(`mkdir -p /b && printf 'FROM %s/busybox:t\nRUN true\n' > /b/Dockerfile && docker buildx build --builder apptown /b`, at)); err != nil {
		t.Fatalf("the private builder does not trust the registry: %v\n%s", err, out)
	}
}
