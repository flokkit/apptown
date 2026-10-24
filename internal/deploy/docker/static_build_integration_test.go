//go:build integration

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/runtimeimages"
)

func staticBuildTree(t *testing.T, dockerfile string) string {
	t.Helper()
	dir := t.TempDir()
	for path, body := range map[string]string{"Dockerfile": dockerfile, "page.html": "built page"} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// With real Docker: a declared static build is exported, checked and
// packed on the selected daemon's own builder, even while AppTown's
// environment names another Buildx builder; an export whose site holds a
// link is refused before it is packed.
func TestIntegrationAStaticBuildIsPackedOnTheSelectedDaemon(t *testing.T) {
	t.Setenv("BUILDX_BUILDER", "forge-test-no-such-builder")
	dk := Scope{Client: CLI{Binary: "docker"}}
	tag := Current.BuildTag("forge-static-build-test-" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", ""))
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })
	tree := staticBuildTree(t, "FROM scratch AS site\nCOPY page.html /dist/index.html\nFROM scratch\nCOPY page.html /other\n")
	if err := PackStaticBuild(context.Background(), dk, tree, appcontract.StaticBuild{Context: ".", Target: "site", Output: "dist"}, runtimeimages.StaticServer(), 8080, tag); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "run", "--rm", "--entrypoint", "cat", tag, "/srv/index.html").CombinedOutput()
	if err != nil || string(out) != "built page" {
		t.Fatalf("the packed image's site: %q %v", out, err)
	}
	cmd, err := exec.Command("docker", "image", "inspect", "--format", "{{json .Config.Cmd}}", tag).Output()
	if err != nil || !strings.Contains(string(cmd), "/_apptown/env.js") {
		t.Fatalf("the packed image does not serve itself: %s %v", cmd, err)
	}

	linked := staticBuildTree(t, "FROM "+runtimeimages.StaticServer()+" AS b\nRUN mkdir -p /out && cp /etc/hostname /out/index.html 2>/dev/null || echo x > /out/index.html; ln -s /etc/passwd /out/leak\nFROM scratch\nCOPY --from=b /out /dist\n")
	err = PackStaticBuild(context.Background(), dk, linked, appcontract.StaticBuild{Context: ".", Output: "dist"}, runtimeimages.StaticServer(), 8080, tag+"-linked")
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag+"-linked").Run() })
	if err == nil || !strings.Contains(err.Error(), "dist/leak") {
		t.Fatalf("a site with a link was packed: %v", err)
	}
}

// With real Docker: a Docker configuration that makes `docker build` an
// alias for buildx and has a remote builder selected (what `docker buildx
// install` and `docker buildx use --global` leave) does not take the build
// away from the selected daemon: AppTown names the daemon's own builder.
func TestIntegrationABuilderAliasDoesNotTakeTheBuildElsewhere(t *testing.T) {
	config := t.TempDir()
	settings := map[string]any{"aliases": map[string]string{"builder": "buildx"}, "auths": map[string]any{"forge-no-login.invalid": map[string]any{}}}
	home, _ := os.UserHomeDir()
	if dir := filepath.Join(home, ".docker", "cli-plugins"); dirExists(dir) {
		settings["cliPluginsExtraDirs"] = []string{dir}
	}
	body, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(config, "config.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	// A socket nothing listens on (a short path: a socket's name is
	// limited); nothing is created there.
	elsewhere := fmt.Sprintf("unix:///tmp/forge-test-elsewhere-%d.sock", os.Getpid())
	for _, args := range [][]string{
		{"buildx", "create", "--name", "forge-test-elsewhere", "--driver", "remote", "--use", elsewhere},
		{"buildx", "use", "--global", "forge-test-elsewhere"},
	} {
		if out, err := exec.Command("docker", append([]string{"--config", config}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", args, out)
		}
	}
	dk := Scope{Client: CLI{Binary: "docker"}, Config: config}
	tag := Current.BuildTag("forge-static-alias-test-" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", ""))
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })
	tree := staticBuildTree(t, "FROM scratch\nCOPY page.html /dist/index.html\n")
	if err := PackStaticBuild(context.Background(), dk, tree, appcontract.StaticBuild{Context: ".", Output: "dist"}, runtimeimages.StaticServer(), 8080, tag); err != nil {
		t.Fatalf("the build left the selected daemon: %v", err)
	}
	if out, err := exec.Command("docker", "image", "inspect", tag).CombinedOutput(); err != nil {
		t.Fatalf("the packed image is not on the daemon: %s", out)
	}
}

func dirExists(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}
