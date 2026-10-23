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

	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/stock"
)

// The stock dotnet Dockerfile builds a .NET app on the pack's SDK line and
// boots it on its Postgres need: the init step (the same image with --init)
// brings the schema up, then the app answers /healthz and serves a row the
// init wrote. The fixture is the app the selection guidance has the agent
// generate, reduced to what the recipe must carry; it targets the toolchain's
// line, so a seed base left on another line fails the build.
func TestIntegrationDotnetSeedBootsOnPostgres(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/dotnet-seed-app")); err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(stock.FS, "seeds/dotnet/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("forge-dotnet-seed-it-%d-%d", os.Getpid(), time.Now().UnixNano())
	pg, app := name+"-pg", name+"-app"
	t.Cleanup(func() {
		_ = dockertest.Run("rm", "-f", "-v", app, pg) // -v: the postgres image declares a data volume
		_ = dockertest.Run("network", "rm", name)
		_ = dockertest.Run("image", "rm", "-f", name+":test")
	})
	if out, err := dockertest.CombinedOutput("network", "create", name); err != nil {
		t.Fatalf("network: %v\n%s", err, out)
	}
	if out, err := dockertest.CombinedOutput("run", "-d", "--name", pg, "--network", name,
		"-e", "POSTGRES_DB=app", "-e", "POSTGRES_USER=app", "-e", "POSTGRES_PASSWORD=app", "postgres:16-alpine"); err != nil {
		t.Fatalf("postgres: %v\n%s", err, out)
	}
	if out, err := dockertest.CombinedOutput("build", "-t", name+":test", dir); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	env := []string{"--network", name, "-e", "PGHOST=" + pg, "-e", "PGPORT=5432",
		"-e", "PGDATABASE=app", "-e", "PGUSER=app", "-e", "PGPASSWORD=app"}
	// Postgres is the image the postgres need runs (postgres:{version}-alpine).
	// It started while the image built, so it is usually ready; the init step
	// is retried until it is, as AppTown waits on the need.
	var initOut []byte
	if !poll.Until(t.Context(), 60*time.Second, time.Second, func() bool {
		var err error
		initOut, err = dockertest.CombinedOutput(append(append([]string{"run", "--rm"}, env...), name+":test", "dotnet", "App.dll", "--init")...)
		return err == nil
	}) {
		t.Fatalf("the init step never brought the schema up: %s", initOut)
	}
	if out, err := dockertest.CombinedOutput(append(append([]string{"run", "-d", "--name", app}, env...), name+":test")...); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	get := func(path string) (string, bool) {
		out, err := dockertest.Output("exec", app, "curl", "-fsS", "http://localhost:8080"+path)
		return string(out), err == nil
	}
	var body string
	if !poll.Until(t.Context(), 60*time.Second, 250*time.Millisecond, func() bool {
		var ok bool
		body, ok = get("/healthz")
		return ok && strings.HasPrefix(body, "ok")
	}) {
		logs, _ := dockertest.CombinedOutput("logs", app)
		t.Fatalf("/healthz never answered (last %q): %s", body, logs)
	}
	if body, ok := get("/"); !ok || !strings.Contains(body, "seeded by init") {
		logs, _ := dockertest.CombinedOutput("logs", app)
		t.Fatalf("the app does not serve the row the init step wrote (got %q): %s", body, logs)
	}
}
