//go:build integration

package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// The worker's agent-image build at first start gives its Dockerfile's RUN
// lines the worker's extra certificates as the build secret: a RUN that
// downloads through the company's authority builds.
// Not parallel: it installs the process's trust.
func TestIntegrationTheAgentImageBuildTrustsTheWorkersExtraCertificates(t *testing.T) {
	company := trusttest.New(t, "Company Inspecting CA")
	port := company.Serve(t, trusttest.OK)
	server := fmt.Sprintf("https://host.docker.internal:%d/", port)
	dir := t.TempDir()
	// The RUN line reaches this machine as host.docker.internal. Docker
	// Desktop resolves that name in a build; on Linux a plain build (as
	// BuildAgentImage runs) has no such name, and this machine is the build
	// network's gateway, so curl is told that address for the name.
	reach := fmt.Sprintf(`a=$(getent hosts host.docker.internal | cut -d" " -f1); [ -n "$a" ] || a=$(ip route | awk "/^default/{print \$3}"); `+
		`curl -fsS --resolve host.docker.internal:%d:$a `, port)
	dockerfile := "FROM curlimages/curl:8.22.0\nUSER root\n" +
		`RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; ` + reach + server + "\n" +
		// A context of its own: its name is its content's.
		fmt.Sprintf("LABEL test=%q\n", t.Name()+server)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	// Without the certificates the build fails on the server's certificate
	// (curl exits 60), not on reaching it.
	if _, err := BuildAgentImage(context.Background(), "docker", dir); err == nil || !strings.Contains(err.Error(), "exit code: 60") {
		t.Fatalf("with nothing configured the build did not fail on the company's certificate: %v", err)
	}
	trusttest.Install(t, company.Bundle(t), trust.Bundle{})
	image, err := BuildAgentImage(context.Background(), "docker", dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = (docker.Scope{Client: docker.CLI{}}).Run(context.Background(), "rmi", "-f", image) })
	// The image says AppTown built it, for the maintenance sweep's prune.
	label, err := (docker.Scope{Client: docker.CLI{}}).Run(context.Background(), "image", "inspect", "--format", `{{index .Config.Labels "`+dockerlabel.Built+`"}}`, image)
	if err != nil || strings.TrimSpace(string(label)) != "1" {
		t.Fatalf("the agent image carries no build label: %q %v", label, err)
	}
}
