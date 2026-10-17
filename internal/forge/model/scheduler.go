package model

import (
	"context"
	"sync"

	"flokkit.io/forge/internal/ids"
)

// CapacitySource returns a binding's current capacity, or zero if unknown.
// Implementations must be safe for concurrent reads.
type CapacitySource interface {
	Capacity(ctx context.Context, binding string) int
}

// Scheduler admits agent/review work independently for each model binding.
// Acquisition waits without cancellation. Owners defer release after execution;
// daemon shutdown uses its separate bounded deadline. Pools are replaced when
// capacity changes; existing holders release into their original pool. During
// resizing, total admitted work may exceed the new pool's capacity.
type Scheduler struct {
	source   CapacitySource
	fallback int
	mu       sync.Mutex
	pools    map[string]*slotPool
}

func NewScheduler(source CapacitySource, fallback int) *Scheduler {
	if fallback < 1 {
		fallback = 1
	}
	return &Scheduler{source: source, fallback: fallback, pools: map[string]*slotPool{}}
}

func (s *Scheduler) Capacity(ctx context.Context, binding string) int {
	if s.source != nil {
		if n := s.source.Capacity(ctx, binding); n > 0 {
			return n
		}
	}
	return s.fallback
}

// slotPool is one binding's pool and the steps waiting on it, in order.
type slotPool struct {
	slots   chan struct{}
	waiting []ids.ID
}

func (s *Scheduler) poolFor(ctx context.Context, binding string) *slotPool {
	// caller holds s.mu
	want := s.Capacity(ctx, binding)
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
	return p
}

// Acquire takes a slot on the binding's pool for stepID, blocking when full.
// It returns a release function, which the caller must invoke exactly once, and
// whether it had to wait. Acquisition is not cancellable and does not promise FIFO
// ordering. Binding should identify the provider binding, not an individual model.
func (s *Scheduler) Acquire(ctx context.Context, binding string, stepID ids.ID) (release func(), waited bool) {
	s.mu.Lock()
	p := s.poolFor(ctx, binding)
	slots := p.slots
	select {
	case slots <- struct{}{}:
		s.mu.Unlock()
		return func() { <-slots }, false
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
	return func() { <-slots }, true
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
