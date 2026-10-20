package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/docker/dockertest"
)

// apptown dev writes its compose file under .forge/, but the file's relative
// paths name the app's tree: compose must resolve them from the repo root.
// The layout is reading-list's (a Dockerfile at the root that copies a
// frontend/ subfolder), where compose run from .forge/ failed with
// "open Dockerfile: no such file or directory".
func TestDevComposeResolvesPathsFromTheRepoRoot(t *testing.T) {
	args := devComposeArgs("up", "-d")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--project-directory . -f "+devComposePath) || args[len(args)-1] != "-d" {
		t.Fatalf("compose args: %v", args)
	}
	// Where a compose client is available, ask it where a relative build
	// context lands.
	if dockertest.Run("compose", "version") != nil {
		t.Skip("no docker compose client here; the argument contract above still holds")
	}
	root := t.TempDir()
	for _, dir := range []string{".forge", "frontend"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		devComposePath:          "services:\n  web:\n    build: .\n",
		"Dockerfile":            "FROM scratch\nCOPY frontend/ /src/frontend/\n",
		"frontend/package.json": "{}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("docker", devComposeArgs("config", "--format", "json")...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("compose config: %v", err)
	}
	var cfg struct {
		Services map[string]struct {
			Build struct{ Context, Dockerfile string }
		}
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	// The temp root may be reached through a symlink (/var → /private/var).
	build := cfg.Services["web"].Build
	if filepath.Base(build.Context) != filepath.Base(root) {
		t.Fatalf("build context %q is not the repo root %s", build.Context, root)
	}
	// The build file compose will open is the repo's own Dockerfile.
	if _, err := os.Stat(filepath.Join(build.Context, build.Dockerfile)); err != nil {
		t.Fatalf("compose would build from %s/%s: %v", build.Context, build.Dockerfile, err)
	}
}

// In a session whose Docker is another machine, apptown dev up names that
// machine as where the app's ports answer: localhost never does.
func TestDevUpNamesWherePublishedPortsAnswer(t *testing.T) {
	for in, want := range map[string]string{
		"tcp://dind:2375":             "dind",
		"tcp://10.0.0.5:2376/":        "10.0.0.5",
		"unix:///var/run/docker.sock": "",
		"":                            "",
	} {
		if got := publishedPortsHost(in); got != want {
			t.Errorf("DOCKER_HOST=%q: got %q, want %q", in, got, want)
		}
	}
}
