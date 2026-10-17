//go:build integration

package execution_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/execution"
)

// An image with no labels at all is found, not an error: Docker 29 leaves
// Labels out of an unlabelled image's config, and a template naming
// .Config.Labels then fails ("map has no entry for key"), which broke a
// project's conformance image.
func TestIntegrationAnUnlabelledImageIsFound(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nCOPY x /x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tag := "forge-unlabelled-probe:" + filepath.Base(dir)
	if out, err := exec.Command("docker", "build", "-q", "-t", tag, dir).CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })
	got := execution.Runner{}.PrepareImage(context.Background(), execution.ImageRequest{Tag: tag})
	if got.Err != "" || got.ID == "" || len(got.Labels) != 0 {
		t.Fatalf("an unlabelled image: %+v", got)
	}
}
