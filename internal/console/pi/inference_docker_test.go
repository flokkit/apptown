//go:build inference_docker

package pi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"flokkit.io/forge/internal/poll"
)

func TestInferenceConsoleMountBoundary(t *testing.T) {
	home := t.TempDir()
	// Docker Desktop can return from --rm just before its host bind mounts
	// are released. Bound cleanup retries rather than leaving flaky temp dirs.
	t.Cleanup(func() {
		var err error
		if !poll.Until(context.Background(), 3*time.Second, 50*time.Millisecond, func() bool { err = os.RemoveAll(home); return err == nil }) {
			t.Errorf("Docker mount cleanup: %v", err)
		}
	})
	if err := os.WriteFile(filepath.Join(home, "session.json"), []byte(`{"actor":"alice"}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "--rm", "--network", "none", "--user", "0", "--entrypoint", "sh", "-v", home + ":/console:ro"}
	for _, dir := range writableDirs() {
		source := filepath.Join(home, dir)
		if err := os.MkdirAll(source, 0755); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-v", source+":/console/"+dir)
	}
	args = append(args, "apptown-agent", "-c", `set -eu
if (echo attacker > /console/session.json) 2>/dev/null; then exit 1; fi
if (echo attacker > /console/session.json.tmp) 2>/dev/null; then exit 2; fi
if rm /console/session.json 2>/dev/null; then exit 3; fi
for dir in pi-home sessions scratch uploads docker-client; do
 echo writable > /console/$dir/probe
 test -s /console/$dir/probe
done
`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("console filesystem boundary: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(home, "session.json"))
	if err != nil || string(raw) != `{"actor":"alice"}` {
		t.Fatalf("identity changed: %s %v", raw, err)
	}
}
