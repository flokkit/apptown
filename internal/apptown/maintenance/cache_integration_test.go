//go:build integration

package maintenance

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/stock"
)

type countingDocker struct {
	volume string
	starts int
}

// Run sends every call to the real daemon, with the shared cache volume
// renamed to this test's own and no other cache volume listed, so the sweep
// touches nothing of the machine's.
func (c *countingDocker) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
	if len(req.Args) > 1 && req.Args[0] == "volume" && req.Args[1] == "ls" {
		return nil, nil
	}
	if i := slices.Index(req.Args, "apptown-cache:/apptown-cache"); i >= 0 {
		req.Args = slices.Clone(req.Args)
		req.Args[i] = c.volume + ":/apptown-cache"
	}
	if len(req.Args) > 0 && req.Args[0] == "start" {
		c.starts++
	}
	return (docker.CLI{}).Run(ctx, host, req)
}

// A cache volume no toolchain was installed in lists as empty: the sweep is
// complete, and the next one, with nothing settled, starts no container.
//
// Kills: a listing of a missing toolchain folder that fails, which kept an
// idle AppTown reading every volume every sweep.
func TestIntegrationAnUnusedCacheVolumeCompletesTheSweep(t *testing.T) {
	c := &countingDocker{volume: fmt.Sprintf("apptown-cache-sweep-test-%d", time.Now().UnixNano())}
	t.Cleanup(func() {
		_, _ = (docker.CLI{}).Run(context.Background(), "", docker.Command{Args: []string{"volume", "rm", "-f", c.volume}})
	})
	r := New(databaseSuite.New(t), Dependencies{Docker: docker.Scope{Client: c}, ToolVersions: stock.Versions()}, t.TempDir())
	r.Cache(t.Context(), true)
	if c.starts != 1 {
		t.Fatalf("the first sweep started %d containers, want one", c.starts)
	}
	r.Cache(t.Context(), true)
	if c.starts != 1 {
		t.Fatalf("the idle sweep after an unused volume's empty listing started %d more", c.starts-1)
	}
}
