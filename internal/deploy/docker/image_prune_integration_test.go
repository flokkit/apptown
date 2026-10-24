//go:build integration

package docker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/stock"
)

// On a real daemon the maintenance sweep's prune removes a dangling image
// AppTown built and leaves every other dangling image: one pulled by digest
// alone (the disk read's image, untagged on Docker's classic store) and one
// someone else built.
//
// Kills: an unfiltered `docker image prune -f`, and a build through
// Scope.Build that does not label its image.
func TestIntegrationThePruneRemovesOnlyImagesAppTownBuilt(t *testing.T) {
	ctx := context.Background()
	dk := Scope{Client: CLI{Binary: "docker"}}
	if out, err := dk.Run(ctx, "pull", stock.Images().DF); err != nil {
		t.Fatalf("pulling %s: %v %s", stock.Images().DF, err, out)
	}
	pulledID := dk.imageID(ctx, stock.Images().DF)
	// The bug needs the pulled image dangling: a tag naming it would keep it
	// from any prune, and the test would prove nothing.
	if tags := dk.Text(ctx, "image", "inspect", "--format", "{{len .RepoTags}}", pulledID); tags != "0" {
		t.Fatalf("a tag names %s on this daemon (%s tags), so it is not dangling here and the test cannot show the prune leaves it", stock.Images().DF, tags)
	}

	nonce := fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM "+stock.Images().DF+"\nARG NONCE\nLABEL nonce=$NONCE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Someone else's: a plain docker build, untagged.
	out, err := exec.Command("docker", "build", "-q", "--build-arg", "NONCE=other-"+nonce, dir).CombinedOutput()
	if err != nil {
		t.Fatalf("building an unrelated image: %v %s", err, out)
	}
	other := lastLine(out)
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", other).Run() })
	// AppTown's: through the one door its builds share, untagged.
	out, err = dk.Build(ctx, "-q", "--build-arg", "NONCE=apptown-"+nonce, dir)
	if err != nil {
		t.Fatalf("building AppTown's image: %v %s", err, out)
	}
	ours := lastLine(out)
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", ours).Run() })

	if err := PruneBuiltImages(ctx, dk); err != nil {
		t.Fatal(err)
	}
	for what, id := range map[string]string{"the image pulled by digest": pulledID, "the unrelated image": other} {
		if dk.imageID(ctx, id) == "" {
			t.Errorf("the prune removed %s (%s)", what, id)
		}
	}
	if dk.imageID(ctx, ours) != "" {
		t.Errorf("the prune left the dangling image AppTown built (%s)", ours)
	}
}

// lastLine is a quiet build's image ID: the last line it printed.
func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
