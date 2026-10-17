package docker

import (
	"os"
	"path/filepath"
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
