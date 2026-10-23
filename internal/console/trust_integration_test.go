//go:build integration

package console

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// An agent session on a project that declares Java: the agent image has no
// JVM, mise installs the declared one in the toolchain prelude, and the
// prelude's second run of the extra certificates gives that JVM a store with
// them, so its first HTTPS request (Maven's, say) through the company's
// authority succeeds.
// Not parallel: it installs the process's trust.
func TestIntegrationAJVMMiseInstallsTrustsTheExtraCertificates(t *testing.T) {
	image := os.Getenv("APPTOWN_TEST_AGENT_IMAGE")
	if image == "" {
		image = "apptown-agent"
	}
	company := trusttest.New(t, "Company Inspecting CA")
	server := fmt.Sprintf("https://host.docker.internal:%d/", company.Serve(t, trusttest.OK))
	trusttest.Install(t, company.Bundle(t), trust.Bundle{})
	ws := t.TempDir()
	java, err := os.ReadFile(filepath.Join("..", "trust", "testdata", "Get.java"))
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"mise.toml": "[tools]\njava = \"temurin-21\"\n", "Get.java": string(java)} {
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := execution.Runner{}.Run(context.Background(), execution.Docker, execution.Request{
		Workspace: ws, Image: image, NoEntrypoint: true,
		Argv:       ToolchainArgs([]string{"sh", "-c", "java /work/Get.java " + server, "the agent's prompt"}),
		Env:        []string{"MISE_DATA_DIR=/tmp/mise", "MISE_TRUSTED_CONFIG_PATHS=/work", "MISE_YES=1", "PATH=/tmp/mise/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		ExtraHosts: []string{"host.docker.internal:host-gateway"}, Timeout: 10 * time.Minute, Name: execution.UniqueName("apptown-trust-jvm"),
	})
	if out.RunErr != nil || out.ExitCode != 0 || !strings.Contains(string(out.Output), server+" 200") {
		t.Fatalf("exit %d %v:\n%s", out.ExitCode, out.RunErr, out.Output)
	}
}
