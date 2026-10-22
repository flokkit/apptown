package remediation

import (
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/strs"
)

// cooldown is the pool's hold after a provider refuses: set by
// reconciliation, read by the scheduler and the Occupancy view. It lives in
// this process only; a restart forgets it.
type cooldown struct {
	mu    sync.Mutex
	until time.Time
	why   string
	clock func() time.Time // AppTown's clock; nil is the real one
}

func (c *cooldown) now() time.Time { return clock.Or(c.clock)() }

func (c *cooldown) hold(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.until = c.now().Add(Cooldown)
	c.why = strs.Truncate(reason, 160)
}

func (c *cooldown) state() (time.Time, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.until, c.why
}

func (c *cooldown) cooling() bool {
	until, _ := c.state()
	return c.now().Before(until)
}
