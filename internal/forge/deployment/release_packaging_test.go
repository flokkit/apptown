package deployment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/docker"

	"flokkit.io/forge/internal/deploy/appcontract"
)

func TestReleasePackagingFollowsDeclaredContext(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"Dockerfile", "app/Dockerfile"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte("FROM scratch\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir, file, cleanup, err := releaseBuildFiles(root, &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true, Build: "app"}}})
	defer cleanup()
	if err != nil || dir != filepath.Join(root, "app") || file != filepath.Join(root, "app/Dockerfile") {
		t.Fatalf("wrong build selection: %q %q %v", dir, file, err)
	}
	if _, _, cleanup, err := releaseBuildFiles(root, &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true, Image: "chosen:1"}}}); err == nil {
		cleanup()
		t.Fatal("image declaration was overridden by a Dockerfile")
	}
	if err := os.Remove(filepath.Join(root, "app/Dockerfile")); err != nil {
		t.Fatal(err)
	}
	if _, _, cleanup, err := releaseBuildFiles(root, &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true, Build: "app"}}}); err == nil {
		cleanup()
		t.Fatal("missing declared Dockerfile borrowed the root Dockerfile")
	}
}

func TestStaticReleaseUsesTheDeclaredFilesAndServer(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "site"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir, file, cleanup, err := releaseBuildFiles(root, &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true, Static: "site", Port: 8088}}})
	defer cleanup()
	if err != nil || dir != filepath.Join(root, "site") {
		t.Fatalf("static context %q: %v", dir, err)
	}
	raw, err := os.ReadFile(file)
	// The same server every path runs: the site, and the forge's /_forge/env.js.
	var cmd []string
	line := string(raw)[strings.Index(string(raw), "CMD ")+4:]
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(line)), &cmd); jerr != nil || !slices.Equal(cmd, docker.StaticServerCommand(8088)) || strings.Contains(line, `\u003e`) {
		t.Fatalf("static server command: %s %v", line, jerr)
	}
	if err != nil || !strings.Contains(string(raw), "FROM "+docker.StaticServerImage+"\nCOPY . /srv\n") {
		t.Fatalf("wrong static server recipe: %s %v", raw, err)
	}
	cleanup()
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("temporary Dockerfile survived cleanup: %v", err)
	}
}

// A static site with a declared build is packaged from its build's folder,
// whose Dockerfile must be there, into the same file server recipe; the
// check builds nothing (the build runs only when the release is built).
func TestABuiltStaticReleaseNeedsItsDockerfileAndPacksWithTheServer(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true, Port: 80,
		StaticBuild: &appcontract.StaticBuild{Context: "web", Output: "dist"}}}}
	if _, _, cleanup, err := releaseBuildFiles(root, c); err == nil || !strings.Contains(err.Error(), "needs its Dockerfile") {
		cleanup()
		t.Fatalf("a build with no Dockerfile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "web", "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir, file, cleanup, err := releaseBuildFiles(root, c)
	defer cleanup()
	if err != nil || dir != filepath.Join(root, "web") {
		t.Fatalf("context %q: %v", dir, err)
	}
	if raw, err := os.ReadFile(file); err != nil || string(raw) != docker.StaticSiteDockerfile(docker.StaticServerImage, 80) {
		t.Fatalf("the pack's recipe: %s %v", raw, err)
	}
}
