package clocktest

import (
	"sync"
	"time"
)

// Manual moves only when its test advances it. Concurrent operations read
// the same clock without sharing an unguarded test variable.
type Manual struct {
	mu sync.Mutex
	at time.Time
}

func NewManual(at time.Time) *Manual       { return &Manual{at: at} }
func (c *Manual) Now() time.Time           { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *Manual) Advance(by time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.at = c.at.Add(by) }
