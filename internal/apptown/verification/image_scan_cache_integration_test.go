//go:build integration

package verification

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/agentenv"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/stock"
)

// A landing starts its image scans side by side, in containers that share
// the project's cache volume. Real stock trivy, with another process holding
// trivy's on-disk scan cache in that volume for the whole test: the stock
// licence scan still answers with its report. Before the scanners kept their
// scan cache in memory, it gave up after five seconds with "cache may be in
// use by another process" and errored the landing. Only the scans that need
// no download run here: the vulnerability scan would first fetch its
// database, minutes on a slow link, and it carries the same flag.
func TestIntegrationStockImageScansDoNotWaitOnAnotherScansCache(t *testing.T) {
	var scanners []check.Scanner
	for _, s := range loadScannerManifests(stock.FS) {
		if s.Looks() == "image" && strings.HasPrefix(s.Image, "aquasec/trivy:") && !s.Network {
			scanners = append(scanners, s)
		}
	}
	if len(scanners) == 0 {
		t.Fatal("expected the stock trivy licence image scan")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cli := docker.CLI{}
	run := func(args ...string) string {
		t.Helper()
		out, err := cli.Run(ctx, "", docker.Command{Args: args})
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	slug := fmt.Sprintf("scan-cache-%d", time.Now().UnixNano())
	volume := agentenv.ProjectCache(slug)
	run("volume", "create", volume)
	t.Cleanup(func() {
		_, _ = cli.Run(context.Background(), "", docker.Command{Args: []string{"volume", "rm", "-f", volume}})
	})

	// The other scan: a process holding the lock trivy takes on its scan
	// cache, as a scan of a large image does for as long as it reads it.
	run("pull", "-q", check.ShImage)
	holder := run("run", "-d", "--rm", "--entrypoint", "sh", "-v", volume+":/apptown-cache", check.ShImage, "-c",
		"mkdir -p /apptown-cache/trivy/fanal && flock -x /apptown-cache/trivy/fanal/fanal.db sh -c 'echo held; sleep 600'")
	t.Cleanup(func() { _, _ = cli.Run(context.Background(), "", docker.Command{Args: []string{"rm", "-f", holder}}) })
	held := poll.Until(ctx, 30*time.Second, 200*time.Millisecond, func() bool {
		out, _ := cli.Run(ctx, "", docker.Command{Args: []string{"logs", holder}})
		return strings.Contains(string(out), "held")
	})
	if !held {
		t.Fatal("the lock holder never took the scan cache's lock")
	}

	image := run("image", "inspect", "-f", "{{.Id}}", check.ShImage)
	scoped := agentenv.WithCacheScope(ctx, slug)
	for _, s := range scanners {
		t.Run(s.Name, func(t *testing.T) {
			ws := t.TempDir()
			if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0o700); err != nil {
				t.Fatal(err)
			}
			argv := append([]string{}, s.Command...)
			for i := range argv {
				argv[i] = strings.ReplaceAll(argv[i], "${IMAGE_TAR}", "/work/.apptown/image.tar")
			}
			out := agentenv.Execute(execution.Runner{}, scoped, execution.Docker, execution.Request{
				Workspace: ws, Argv: argv, Image: s.Image, Network: "none", Report: s.Report,
				ExportImage: image, Timeout: 2 * time.Minute, Name: execution.UniqueName("apptown-scan-cache-test"),
			}, time.Now())
			if !out.Answered() || (out.ExitCode != 0 && out.ExitCode != 1) || out.ReportErr != nil {
				t.Fatalf("%s did not answer beside a held scan cache: exit %d, %v, report %v: %s", s.Name, out.ExitCode, out.RunErr, out.ReportErr, out.Output)
			}
		})
	}
}
