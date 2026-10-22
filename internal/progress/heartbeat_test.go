package progress

import (
	"testing"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A long, output-silent step gets a heartbeat feed: startHeartbeat opens a
// live feed (so the UI shows a ticking elapsed) and its stop func discards it
// WITHOUT filing a spurious agent-log artifact (complaint #3).
func TestHeartbeatFeedOpensAndDiscards(t *testing.T) {
	t.Parallel()
	feeds := &Feeds{}
	stepID := idstest.N(7)

	stop := feeds.Heartbeat(stepID, "deploying")
	if _, running := feeds.Read(stepID, 0); !running {
		t.Fatal("startHeartbeat must open a live feed")
	}

	stop()
	stop() // idempotent — must not panic

	if _, running := feeds.Read(stepID, 0); running {
		t.Fatal("stop must discard the feed")
	}
}
