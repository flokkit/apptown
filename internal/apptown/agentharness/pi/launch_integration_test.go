//go:build integration

package pi

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
)

// The integration-rest CI tier builds deploy/apptown-agent before this test.
// Link its existing Node into mise: no toolchain download or host executable
// fixture is needed. Only Pi is replaced, inside the disposable container.
func TestIntegrationImageInterpreterPreservesMiseForChildCommands(t *testing.T) {
	image := os.Getenv("APPTOWN_TEST_AGENT_IMAGE")
	if image == "" {
		image = "apptown-agent"
	}
	const script = `set -eu
cd /work
version=$(/usr/local/bin/node -p process.versions.node)
printf '[tools]\nnode = "%s"\n' "$version" > mise.toml
mise link "node@$version" /usr/local >/dev/null
mise reshim
rm /usr/local/bin/pi
cat > /usr/local/bin/pi <<'JS'
#!/usr/bin/env node
const {execFileSync} = require('node:child_process');
process.stdout.write(execFileSync('sh', ['-c', 'printf "%s\n" "$PATH"; command -v node; node --version']));
JS
chmod +x /usr/local/bin/pi
exec /usr/local/bin/apptown-pi
`
	out := (execution.Runner{}).Run(t.Context(), execution.Docker, execution.Request{
		Image: image, Name: execution.UniqueName("apptown-pi-mise"),
		NoEntrypoint: true, Network: "none", Timeout: time.Minute,
		Argv: []string{"sh", "-c", script},
		Env: []string{
			"MISE_DATA_DIR=/tmp/mise", "MISE_TRUSTED_CONFIG_PATHS=/work", "MISE_YES=1", "MISE_QUIET=1",
			"PATH=/tmp/mise/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		},
	})
	if out.RunErr != nil || out.ExitCode != 0 {
		t.Fatalf("launch: exit %d, %v:\n%s", out.ExitCode, out.RunErr, out.Output)
	}
	lines := strings.Split(strings.TrimSpace(string(out.Output)), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "/tmp/mise/shims:") || strings.Contains(lines[0], "/installs/node") || lines[1] != "/tmp/mise/shims/node" || !strings.HasPrefix(lines[2], "v") {
		t.Fatalf("child command lost mise shims: %s", out.Output)
	}
}
