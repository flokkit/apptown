// Package progress owns bounded, ephemeral progress feeds. Persistence and HTTP
// presentation belong to callers; feeds know nothing about Forge or its database.
package progress

import (
	"sync"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
)

const MaxLines = 500

type feed struct {
	lines   []string
	total   int
	started time.Time
}

// Feeds is ready to use at its zero value. Do not copy it after first use.
type Feeds struct {
	// Now is the feeds' clock, for a feed's elapsed time; nil is the real
	// clock.
	Now func() time.Time

	mu    sync.Mutex
	feeds map[ids.ID]*feed
}

func (f *Feeds) now() time.Time { return clock.Or(f.Now)() }

// Snapshot is a detached copy; callers may retain or edit Lines safely.
type Snapshot struct {
	Lines     []string
	Total     int
	ElapsedMs int64
}

func (f *Feeds) Start(id ids.ID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.feeds == nil {
		f.feeds = make(map[ids.ID]*feed)
	}
	f.feeds[id] = &feed{started: f.now()}
}

func (f *Feeds) Append(id ids.ID, lines ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.feeds[id]
	if b == nil {
		return
	}
	b.total += len(lines)
	// Retain only the tail even when a single append exceeds the limit.
	if len(lines) >= MaxLines {
		b.lines = append([]string(nil), lines[len(lines)-MaxLines:]...)
		return
	}
	if extra := len(b.lines) + len(lines) - MaxLines; extra > 0 {
		copy(b.lines, b.lines[extra:])
		b.lines = b.lines[:len(b.lines)-extra]
	}
	b.lines = append(b.lines, lines...)
}

// Read returns lines after an absolute offset, clamped to the retained tail.
func (f *Feeds) Read(id ids.ID, from int) (Snapshot, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.feeds[id]
	if b == nil {
		return Snapshot{}, false
	}
	return snapshot(b, from, f.now()), true
}

// Finish removes the feed and returns its final tail for optional persistence.
// Appends after removal are ignored until another Start for the same ID.
func (f *Feeds) Finish(id ids.ID) (Snapshot, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.feeds[id]
	if b == nil {
		return Snapshot{}, false
	}
	delete(f.feeds, id)
	return snapshot(b, 0, f.now()), true
}

func (f *Feeds) Discard(id ids.ID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.feeds, id)
}

func snapshot(b *feed, from int, now time.Time) Snapshot {
	start := max(0, from-(b.total-len(b.lines)))
	var lines []string
	if start < len(b.lines) {
		lines = append(lines, b.lines[start:]...)
	}
	return Snapshot{Lines: lines, Total: b.total, ElapsedMs: now.Sub(b.started).Milliseconds()}
}
