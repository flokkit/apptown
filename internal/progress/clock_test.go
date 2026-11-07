package progress

import (
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/flokkit/apptown/internal/ids/idstest"
)

// skewed is a clock a test sets: far from the real one, moved by hand.
type skewed struct {
	mu sync.Mutex
	at time.Time
}

func (c *skewed) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *skewed) move(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// A feed's elapsed time is read on the feeds' clock.
func TestAFeedsElapsedTimeIsTheFeedsClocks(t *testing.T) {
	t.Parallel()
	c := &skewed{at: time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)}
	feeds := &Feeds{Now: c.now}
	feeds.Start(idstest.N(1))
	c.move(90 * time.Second)
	if snap, _ := feeds.Read(idstest.N(1), 0); snap.ElapsedMs != 90_000 {
		t.Fatalf("elapsed %d ms on a clock moved 90 s", snap.ElapsedMs)
	}
}

// A heartbeat's line says the time elapsed on the feeds' clock.
func TestAHeartbeatCountsOnTheFeedsClock(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		c := &skewed{at: time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)}
		feeds := &Feeds{Now: c.now}
		stop := feeds.Heartbeat(idstest.N(3), "deploying")
		defer stop()
		synctest.Wait() // the heartbeat has read its start
		c.move(42 * time.Second)
		<-time.After(8 * time.Second) // the bubble's time passes to the first beat
		synctest.Wait()
		snap, _ := feeds.Read(idstest.N(3), 0)
		if len(snap.Lines) != 1 || !strings.Contains(snap.Lines[0], "42s elapsed") {
			t.Fatalf("the heartbeat said %q on a clock moved 42 s", snap.Lines)
		}
	})
}
