package instancebackup

import (
	"context"
	"sync"
)

// Hold is AppTown holding new work for a moment: while it is held, a job
// waits before its work starts and the workflow dispatcher waits before it
// starts a step. Work already running goes on. It lives in memory only, so
// nothing of it is in a database dump and a stop releases it. A
// whole-AppTown backup holds it across the database dump and the copy of
// the data directory (backup.go). The zero value holds nothing.
type Hold struct {
	mu   sync.Mutex
	held chan struct{} // closed on release; nil when nothing is held
}

// Hold holds new work until release is called; false when it is already
// held (one backup at a time holds it).
func (h *Hold) Hold() (release func(), ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.held != nil {
		return nil, false
	}
	c := make(chan struct{})
	h.held = c
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			h.held = nil
			h.mu.Unlock()
			close(c)
		})
	}, true
}

// Wait returns at once when nothing is held, else once it is released, or
// with ctx's error when ctx ends first.
func (h *Hold) Wait(ctx context.Context) error {
	h.mu.Lock()
	c := h.held
	h.mu.Unlock()
	if c == nil {
		return nil
	}
	select {
	case <-c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
