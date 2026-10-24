//go:build integration

package execution_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/diskspace/diskspacetest"
	"github.com/flokkit/apptown/stock"
)

// A disk read through Docker answers every time under parallel load and
// leaves none of its containers behind. The reads it replaced (the answer
// taken from docker run's attached stream, then from docker logs) came
// back empty with exit 0 a few times in CI; that loss was not reproduced
// in 13,400 runs on three daemons here, so this loop shows the file read
// holds under load rather than that the old one fails. A log that
// disagrees with its file is logged, not failed: it is the loss itself.
func TestIntegrationADiskReadAnswersUnderLoadAndLeavesNothing(t *testing.T) {
	const reads, readers, loaders = 300, 16, 8
	ctx := t.Context()
	var mu sync.Mutex
	var names []string
	dk := func(ctx context.Context, args ...string) (string, string, error) {
		if diskspacetest.Step(args) == "create" {
			mu.Lock()
			names = append(names, args[2]) // create --name <name>
			mu.Unlock()
		}
		var stdout, stderr strings.Builder
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}
	root, _, err := dk(ctx, "info", "--format", "{{.DockerRootDir}}")
	if err != nil {
		t.Fatal(err)
	}
	root = strings.TrimSpace(root)

	// The load: short containers that print, started and removed for as
	// long as the reads run.
	loadCtx, stopLoad := context.WithCancel(ctx)
	var load sync.WaitGroup
	for range loaders {
		load.Go(func() {
			for loadCtx.Err() == nil {
				_, _, _ = dk(loadCtx, "run", "--rm", "--network", "none", stock.Images().DF, "sh", "-c", "seq 1 2000")
			}
		})
	}

	var next atomic.Int32
	var failures sync.Map
	var readersDone sync.WaitGroup
	for range readers {
		readersDone.Go(func() {
			for next.Add(1) <= reads {
				space, err := diskspace.ReadThroughDocker(ctx, dk, stock.Images().DF, root, time.Now, func(r string) { t.Log(r) })
				if err == nil && space.TotalBytes <= 0 {
					err = errors.New("an empty filesystem")
				}
				if err != nil {
					failures.Store(err.Error(), true)
				}
			}
		})
	}
	readersDone.Wait()
	stopLoad()
	load.Wait()

	failures.Range(func(k, _ any) bool {
		t.Errorf("a read did not answer: %s", k)
		return true
	})
	if len(names) != reads {
		t.Fatalf("%d reads made %d containers", reads, len(names))
	}
	out, _, err := dk(ctx, "ps", "-a", "--filter", "label=apptown.class="+diskspace.ProbeClass, "--format", "{{.Names}}")
	if err != nil {
		t.Fatal(err)
	}
	standing := map[string]bool{}
	for _, n := range strings.Fields(out) {
		standing[n] = true
	}
	for _, n := range names {
		if standing[n] {
			t.Errorf("read's container %s was left behind", n)
		}
	}
}

// A real probe container's result file is the answer even when its log
// comes back empty (a stand-in that drops what docker logs prints): the
// copy out of the exited container carries it, and the disagreement is
// reported.
func TestIntegrationADiskReadAnswersFromItsFileWhenTheLogIsEmpty(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dk := func(ctx context.Context, args ...string) (string, string, error) {
		var stdout, stderr strings.Builder
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if diskspacetest.Step(args) == "logs" {
			return "", "", err
		}
		return stdout.String(), stderr.String(), err
	}
	root, _, err := dk(ctx, "info", "--format", "{{.DockerRootDir}}")
	if err != nil {
		t.Fatal(err)
	}
	var reports []string
	space, err := diskspace.ReadThroughDocker(ctx, dk, stock.Images().DF, strings.TrimSpace(root), time.Now, func(r string) { reports = append(reports, r) })
	if err != nil || space.TotalBytes <= 0 {
		t.Fatalf("read %+v, %v", space, err)
	}
	if len(reports) != 1 || !strings.Contains(reports[0], "not in what docker logs returned") || !strings.Contains(reports[0], "docker client ") {
		t.Fatalf("reports: %q", reports)
	}
}
