//go:build integration

package verification

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy/docker/dockertest"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/stock"
)

// The stock node-fullstack Dockerfile ships production dependencies only: a
// dev tool (TypeScript 7's compiler is a Go binary) must not reach the image
// the landing scans, and the app still starts on what remains. The fixture is
// the pack's recommended layout, a root package with server and web
// workspaces: the server loads a production dependency at start, and the web
// build needs a devDependency.
func TestIntegrationNodeSeedImageShipsNoDevDependencies(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/node-seed-app")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Dockerfile", ".dockerignore"} {
		raw, err := fs.ReadFile(stock.FS, "seeds/node-fullstack/"+name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	name := fmt.Sprintf("forge-node-seed-it-%d-%d", os.Getpid(), time.Now().UnixNano())
	t.Cleanup(func() {
		_ = dockertest.Run("rm", "-f", name)
		_ = dockertest.Run("image", "rm", "-f", name+":test")
	})
	if out, err := dockertest.CombinedOutput("build", "-t", name+":test", dir); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if out, err := dockertest.CombinedOutput("run", "--rm", "--entrypoint", "sh", name+":test", "-c",
		"test -d /app/node_modules/ms && test -f /app/web/dist/index.html && test ! -e /app/node_modules/is-number"); err != nil {
		t.Fatalf("the runtime image does not hold the build output and production dependencies alone: %v\n%s", err, out)
	}
	if out, err := dockertest.CombinedOutput("run", "-d", "--name", name, name+":test"); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	var body string
	if !poll.Until(t.Context(), 30*time.Second, 250*time.Millisecond, func() bool {
		out, err := dockertest.Output("exec", name, "node", "-e",
			`fetch("http://localhost:8080/healthz").then(r=>r.text()).then(t=>process.stdout.write(t))`)
		body = string(out)
		return err == nil && strings.HasPrefix(body, "ok")
	}) {
		logs, _ := dockertest.CombinedOutput("logs", name)
		t.Fatalf("/healthz never answered (last %q): %s", body, logs)
	}
}
