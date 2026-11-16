package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The file server's Dockerfile is alone in a folder of its own, which the
// builder sends instead of the shared temporary folder, and removing it
// leaves nothing behind.
func TestTheStaticSitesDockerfileIsAloneInItsFolder(t *testing.T) {
	t.Parallel()
	path, remove, err := staticDockerfile("nginx:1-alpine", 8080)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if dir == filepath.Clean(os.TempDir()) {
		t.Fatalf("the Dockerfile is in the shared temporary folder: %s", path)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("the Dockerfile's folder holds %v (%v)", entries, err)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != StaticSiteDockerfile("nginx:1-alpine", 8080) {
		t.Fatalf("the Dockerfile says %q (%v)", body, err)
	}
	remove()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("removing left the folder: %v", err)
	}
}

// The shared static recipe downloads Alpine fixes using the build trust secret
// before it copies the site's output, so both packing callers receive fixes.
func TestStaticImageTakesCurrentPackagesBeforeCopyingTheSite(t *testing.T) {
	t.Parallel()
	body := StaticSiteDockerfile("caddy:alpine", 8080)
	want := "FROM caddy:alpine\nRUN --mount=type=secret,id=apptown-trust eval \"$(cat /run/secrets/apptown-trust 2>/dev/null)\"; apk upgrade --no-cache\nCOPY . /srv\n"
	if !strings.HasPrefix(body, want) {
		t.Fatalf("static recipe must install trust then upgrade before COPY:\n%s", body)
	}
}
