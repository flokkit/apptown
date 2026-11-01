package model

import (
	"context"
	"fmt"
	"sync"

	"github.com/flokkit/apptown/internal/ids"
)

// capacitySource returns a binding's current capacity: zero for a binding it
// does not have, an error when the capacity could not be read. Implementations
// must be safe for concurrent reads.
type capacitySource interface {
	Capacity(ctx context.Context, binding string) (int, error)
}

// Scheduler admits agent/review work independently for each model binding.
// Acquisition waits without cancellation. Owners defer release after execution;
// daemon shutdown uses its separate bounded deadline. Pools are replaced when
// capacity changes; existing holders release into their original pool. During
// resizing, total admitted work may exceed the new pool's capacity.
type Scheduler struct {
	source   capacitySource
	fallback int
	mu       sync.Mutex
	pools    map[string]*slotPool
}

func NewScheduler(source capacitySource, fallback int) *Scheduler {
	if fallback < 1 {
		fallback = 1
	}
	return &Scheduler{source: source, fallback: fallback, pools: map[string]*slotPool{}}
}

// Capacity is the binding's capacity, or the scheduler's configured default
// for a binding its source does not have. A failed read is an error: it never
// becomes the default.
func (s *Scheduler) Capacity(ctx context.Context, binding string) (int, error) {
	if s.source != nil {
		n, err := s.source.Capacity(ctx, binding)
		if err != nil {
			return 0, err
		}
		if n > 0 {
			return n, nil
		}
	}
	return s.fallback, nil
}

// slotPool is one binding's pool and the steps waiting on it, in order.
type slotPool struct {
	slots   chan struct{}
	waiting []ids.ID
}

func (s *Scheduler) poolFor(ctx context.Context, binding string) (*slotPool, error) {
	// caller holds s.mu
	want, err := s.Capacity(ctx, binding)
	if err != nil {
		return nil, err
	}
	p := s.pools[binding]
	if p == nil || cap(p.slots) != want {
		// A new or resized pool: steps holding slots in the old one release
		// into the old channel (the release closes over it), so nothing leaks.
		np := &slotPool{slots: make(chan struct{}, want)}
		if p != nil {
			np.waiting = p.waiting
		}
		s.pools[binding] = np
		p = np
	}
	return p, nil
}

// Acquire takes a slot on the binding's pool for stepID, blocking when full.
// It returns a release function, which the caller must invoke exactly once, and
// whether it had to wait. Acquisition is not cancellable and does not promise FIFO
// ordering. Binding should identify the provider binding, not an individual model.
// When the binding's capacity cannot be read nothing is reserved and the error
// is returned: the work is refused, not admitted under another limit.
func (s *Scheduler) Acquire(ctx context.Context, binding string, stepID ids.ID) (release func(), waited bool, err error) {
	s.mu.Lock()
	p, err := s.poolFor(ctx, binding)
	if err != nil {
		s.mu.Unlock()
		return nil, false, fmt.Errorf("reading the capacity of model binding %q: %w", binding, err)
	}
	slots := p.slots
	select {
	case slots <- struct{}{}:
		s.mu.Unlock()
		return func() { <-slots }, false, nil
	default:
	}
	p.waiting = append(p.waiting, stepID)
	s.mu.Unlock()
	slots <- struct{}{}
	s.mu.Lock()
	if p := s.pools[binding]; p != nil {
		for i, id := range p.waiting {
			if id == stepID {
				p.waiting = append(p.waiting[:i], p.waiting[i+1:]...)
				break
			}
		}
	}
	s.mu.Unlock()
	return func() { <-slots }, true, nil
}

// Waiting says whether stepID is waiting for a slot, on which binding,
// and how many steps were queued ahead of it. This is a display observation,
// not a reservation or a guarantee of acquisition order.
func (s *Scheduler) Waiting(stepID ids.ID) (binding string, ahead int, waiting bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, p := range s.pools {
		for i, id := range p.waiting {
			if id == stepID {
				return name, i, true
			}
		}
	}
	return "", 0, false
}
