package workflow

import (
	"time"

	"flokkit.io/forge/internal/clock"
)

// clocked is AppTown's clock as a workflow store holds it. Every time a
// rule later compares is written by it: a step's finish, a change's close,
// an artifact's making, a vote, a send-back. The zero value is the real
// clock.
type clocked struct{ clock func() time.Time }

func (c clocked) now() time.Time { return clock.Or(c.clock)() }
