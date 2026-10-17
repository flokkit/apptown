// Package clocktest holds a clock for tests that must see how often an
// operation reads its owner's clock.
package clocktest

import (
	"sync"
	"time"
)

// Stepping is a clock that moves on by its step at every reading. An
// operation that reads it twice sees two different times, so a record and
// its receipt stamped by separate readings disagree where one shared
// reading would make them equal.
type Stepping struct {
	mu   sync.Mutex
	next time.Time
	step time.Duration
}

// NewStepping is a clock whose first reading is start.
func NewStepping(start time.Time, step time.Duration) *Stepping {
	return &Stepping{next: start, step: step}
}

// Now takes a reading and moves the clock on.
func (c *Stepping) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.next
	c.next = c.next.Add(c.step)
	return t
}

// Next is the time the next reading will give, without taking it.
func (c *Stepping) Next() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.next
}
