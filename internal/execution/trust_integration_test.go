//go:build integration

package execution_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// A step's container, started by the runner as every step is, trusts a
// server the extra authority signed once its command runs the script, and
// not before it is configured.
// Not parallel: it installs the process's trust.
func TestIntegrationAStepContainerTrustsTheExtraCertificates(t *testing.T) {
	company := trusttest.New(t, "Company Inspecting CA")
	server := fmt.Sprintf("https://host.docker.internal:%d/", company.Serve(t, trusttest.OK))
	step := func() execution.Outcome {
		return execution.Runner{}.Run(context.Background(), execution.Docker, execution.Request{
			Image: "curlimages/curl:8.22.0", NoEntrypoint: true, Argv: []string{"sh", "-c", trust.Run + "; curl -fsS " + server},
			ExtraHosts: []string{"host.docker.internal:host-gateway"}, Timeout: time.Minute, Name: execution.UniqueName("apptown-trust-step"),
		})
	}
	if o := step(); o.ExitCode == 0 {
		t.Fatalf("reached the server with nothing configured: %s", o.Output)
	}
	trusttest.Install(t, company.Bundle(t), trust.Bundle{})
	if o := step(); o.RunErr != nil || o.ExitCode != 0 || strings.TrimSpace(string(o.Output)) != "ok" {
		t.Fatalf("exit %d %v: %s", o.ExitCode, o.RunErr, o.Output)
	}
}
