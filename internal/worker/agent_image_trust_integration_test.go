//go:build integration

package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// The worker's agent-image build at first start gives its Dockerfile's RUN
// lines the worker's extra certificates as the build secret: a RUN that
// downloads through the company's authority builds.
// Not parallel: it installs the process's trust.
func TestIntegrationTheAgentImageBuildTrustsTheWorkersExtraCertificates(t *testing.T) {
	company := trusttest.New(t, "Company Inspecting CA")
	server := fmt.Sprintf("https://host.docker.internal:%d/", company.Serve(t, trusttest.OK))
	dir := t.TempDir()
	dockerfile := "FROM curlimages/curl:8.22.0\nUSER root\n" +
		`RUN --mount=type=secret,id=apptown-trust eval "$(cat /run/secrets/apptown-trust 2>/dev/null)"; curl -fsS ` + server + "\n" +
		// A context of its own: its name is its content's.
		fmt.Sprintf("LABEL test=%q\n", t.Name()+server)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildAgentImage(context.Background(), "docker", dir); err == nil {
		t.Fatal("the build reached the company's server with nothing configured")
	}
	trusttest.Install(t, company.Bundle(t), trust.Bundle{})
	image, err := BuildAgentImage(context.Background(), "docker", dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = (docker.Scope{Client: docker.CLI{}}).Run(context.Background(), "rmi", "-f", image) })
}
