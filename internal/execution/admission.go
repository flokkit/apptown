package execution

import (
	"context"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/clock"
)

// AdmissionLine orders new work and indexes holders until their owner removes
// them. Unused grants and absent waiters expire; live holders never do.
type AdmissionLine struct {
	mu      sync.Mutex
	limit   int
	now     func() time.Time
	lease   time.Duration
	held    map[string]time.Time
	waiting []admissionWaiter
	changed chan struct{}
}
type admissionWaiter struct {
	key string
	at  time.Time
}

func NewAdmissionLine(limit int, lease time.Duration, now func() time.Time, holders []string) *AdmissionLine {
	l := &AdmissionLine{limit: limit, lease: lease, now: clock.Or(now), held: map[string]time.Time{}, changed: make(chan struct{})}
	for _, key := range holders {
		l.held[key] = time.Time{}
	}
	return l
}
func (l *AdmissionLine) signal() { close(l.changed); l.changed = make(chan struct{}) }
func (l *AdmissionLine) expire() {
	now := l.now()
	for key, at := range l.held {
		if !at.IsZero() && !now.Before(at.Add(l.lease)) {
			delete(l.held, key)
		}
	}
	kept := l.waiting[:0]
	for _, w := range l.waiting {
		if now.Before(w.at.Add(l.lease)) {
			kept = append(kept, w)
		}
	}
	l.waiting = kept
}

// Admit waits for the key's turn until ctx ends. The disk check runs under
// the line's lock immediately before granting capacity. A waiting reply is
// an observation; the caller asks again within the lease to keep its place.
func (l *AdmissionLine) Admit(ctx context.Context, key string, check func() error) (int, bool, error) {
	for {
		l.mu.Lock()
		l.expire()
		if _, ok := l.held[key]; ok {
			l.mu.Unlock()
			return 0, true, nil
		}
		ahead := -1
		for i := range l.waiting {
			if l.waiting[i].key == key {
				ahead = i
				l.waiting[i].at = l.now()
				break
			}
		}
		if ahead < 0 {
			ahead = len(l.waiting)
			l.waiting = append(l.waiting, admissionWaiter{key, l.now()})
		}
		if ahead == 0 && len(l.held) < l.limit {
			err := check()
			l.waiting = l.waiting[1:]
			if err == nil {
				l.held[key] = l.now()
			}
			l.signal()
			l.mu.Unlock()
			return 0, err == nil, err
		}
		changed := l.changed
		l.mu.Unlock()
		timer := time.NewTimer(l.lease)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ahead, false, nil
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// Consume atomically converts one unused grant to a live holder. First
// writes cannot acquire capacity here.
func (l *AdmissionLine) Consume(key string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expire()
	at, ok := l.held[key]
	if !ok || at.IsZero() {
		return ErrBusy
	}
	l.held[key] = time.Time{}
	return nil
}

// Release is called only after the holder's resources have been removed.
func (l *AdmissionLine) Release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.held, key)
	l.signal()
}
