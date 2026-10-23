package workflowexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/trust"
)

// AppTown's own commands in a container run the extra certificates
// (internal/trust) before anything downloads, and again after installing a
// declared toolchain, for what it installed.
func TestAppTownsStepCommandsRunTheExtraCertificatesFirst(t *testing.T) {
	t.Parallel()
	before := func(t *testing.T, what, cmd, install string) {
		t.Helper()
		first := strings.Index(cmd, trust.Run)
		if first != 0 {
			t.Fatalf("%s does not start with the certificates: %s", what, cmd)
		}
		if install == "" {
			return
		}
		at := strings.Index(cmd, install)
		if at < 0 || !strings.Contains(cmd[at:], trust.Run) {
			t.Fatalf("%s does not run the certificates again after %q: %s", what, install, cmd)
		}
	}
	before(t, "a script step", containerScript("make test", false), "")
	before(t, "a script step with a toolchain", containerScript("make test", true), "mise install")
	before(t, "a tests step", testsScript("go test ./..."), "mise install")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var argv []string
	checks := CompileCheck{Image: "agent:image", Run: func(_ context.Context, req execution.Request) execution.Outcome {
		argv = req.Argv
		return execution.Outcome{Termination: execution.Exited}
	}}
	cleanTreeBuilds(context.Background(), dir, checks, agentenv.Ambient{})
	if len(argv) != 3 {
		t.Fatalf("clean build argv %q", argv)
	}
	before(t, "the clean build", argv[2], "")
}

// Every build a release task runs (a static site's export and its packing,
// an app's image) is given the extra certificates as its secret, from the
// task's own variable: none when the task has none.
func TestEveryReleaseBuildIsGivenTheExtraCertificatesAsItsSecret(t *testing.T) {
	t.Parallel()
	tree := t.TempDir()
	for _, d := range []string{"web", "api"} {
		if err := os.Mkdir(filepath.Join(tree, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c, err := appcontract.Parse([]byte("app:\n  site:\n    public: true\n    static: { build: web, target: out, output: dist }\n  api:\n    build: api\n    ports: [\"8080\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	script, _, _, err := releaseBuildRequest(tree, deployment.PlanRelease(c), "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	secret := "${" + trust.EnvVar + ":+--secret id=" + trust.SecretID + ",env=" + trust.EnvVar + "}"
	builds := 0
	for _, line := range strings.Split(script, "\n") {
		if !strings.HasPrefix(line, "'docker' 'buildx' 'build'") {
			continue
		}
		builds++
		if !strings.Contains(line, secret) {
			t.Fatalf("a build without the certificates: %s", line)
		}
	}
	if builds != 3 {
		t.Fatalf("%d builds in:\n%s", builds, script)
	}
}
