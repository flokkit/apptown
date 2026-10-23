package console

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/trust"
)

// Every agent session (a step's pi, a chat's launcher) runs the extra
// certificates (internal/trust) before mise downloads the declared
// toolchain, and again after it, for what it installed (a JVM's store).
func TestTheToolchainPreludeRunsTheExtraCertificatesBeforeAndAfterTheInstall(t *testing.T) {
	t.Parallel()
	for name, prelude := range map[string]string{"a chat": ToolchainPrelude, "a step": ToolchainArgs([]string{"pi", "prompt"})[2]} {
		install := strings.Index(prelude, "mise install")
		if !strings.HasPrefix(prelude, trust.Run) || install < 0 || !strings.Contains(prelude[install:], trust.Run) {
			t.Fatalf("%s: %s", name, prelude)
		}
	}
}
