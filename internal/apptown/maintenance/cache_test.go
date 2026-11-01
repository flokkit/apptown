package maintenance

import (
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/stock"
)

// Unavailable volume discovery is unknown, not a completed inventory. Once
// Docker recovers, the next pass must discover and list the omitted cache.
//
// Kills: a failed volume listing taken as an empty one.
func TestUnavailableCacheVolumeDiscoveryIsNotACompleteSweep(t *testing.T) {
	t.Parallel()
	d := newDockerFake()
	unavailable := true
	d.SetReply(func(args []string) ([]byte, error, bool) {
		line := strings.Join(args, " ")
		if strings.HasPrefix(line, "volume ls") {
			if strings.Contains(line, "name=apptown-cache-") {
				if unavailable {
					return nil, errors.New("daemon temporarily unavailable"), true
				}
				return []byte("apptown-cache-game\n"), nil, true
			}
			return nil, nil, true
		}
		return nil, nil, false
	})
	d.SetResults(func(c dockerfake.ResultCommand) (dockerfake.ResultAnswer, bool) {
		return dockerfake.ResultAnswer{Answer: []byte{}}, true
	})
	r := New(databaseSuite.New(t), Dependencies{Docker: docker.Scope{Client: d}, ToolVersions: stock.Versions()}, t.TempDir())
	r.Cache(t.Context(), false)
	unavailable = false
	before := len(d.Matching("start "))
	r.Cache(t.Context(), false)
	if got := len(d.Matching("start ")) - before; got != 2 {
		t.Fatalf("recovered discovery started %d listings, want shared and recovered legacy cache (2)", got)
	}
}

func newDockerFake() *dockerfake.Daemon {
	return dockerfake.New()
}
