package preview

import (
	"context"
	"errors"
	"sync"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
)

// Key separates change previews from project-scoped release numbers.
// Its zero value is invalid. These are transient execution identities, not rows.
type Key struct {
	kind    uint8
	project ids.ID
	change  ids.ID // a change preview's change
	number  int64  // a release preview's release number, within project
}

func ChangeKey(id ids.ID) Key { return Key{kind: 1, change: id} }
func ReleaseKey(projectID ids.ID, number int64) Key {
	return Key{kind: 2, project: projectID, number: number}
}

type State string

const (
	None     State = "none"
	Starting State = "starting"
	Live     State = "live"
	Broken   State = "broken"
	Cold     State = "cold"
	// Unavailable: where previews run could not be asked, so whether
	// one is running is not known.
	Unavailable State = "unavailable"
)

type Status struct {
	State State  `json:"state"`
	URL   string `json:"url,omitempty"`
	// HostOnly: the URL answers on the AppTown host only
	// (Service.PreviewsHostOnly), so it is not a link for anyone
	// elsewhere.
	HostOnly bool      `json:"hostOnly,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	At       time.Time `json:"at,omitzero"`
}

// Observation supplies facts; the runtime owns their precedence. Workflow
// decides when an absent contract means there is nothing runnable yet.
type Observation struct {
	URL        string
	HostOnly   bool
	NoContract bool
	// Unavailable is why the place previews run could not be asked.
	Unavailable string
}
type Observer interface {
	ObservePreview(ctx context.Context) Observation
}

type entry struct {
	active  chan struct{}
	at      time.Time
	failure string
}

// Runtime owns boot serialization and transient diagnostics for one
// instance. The zero value is ready to use and must not be copied after use.
// It starts no work of its own; restart still leaves previews cold.
type Runtime struct {
	// Now is AppTown's clock, which stamps a boot's start and end (the
	// real clock when nil).
	Now     func() time.Time
	mu      sync.Mutex
	entries map[Key]*entry
}

// Booting is exclusive ownership until Finish. It cannot settle a later boot,
// even if a caller repeats Finish after another request was admitted.
type Booting struct {
	runtime *Runtime
	key     Key
	done    chan struct{}
}

// Begin waits for this preview alone. Cancelling a waiter never cancels its owner
// or changes diagnostics. The admitted caller must defer Finish on every path.
func (p *Runtime) Begin(ctx context.Context, key Key) (*Booting, error) {
	if (key.kind == 1 && key.change.IsZero()) || (key.kind == 2 && (key.project.IsZero() || key.number <= 0)) || (key.kind != 1 && key.kind != 2) {
		return nil, errors.New("invalid preview identity")
	}
	for {
		p.mu.Lock()
		if err := ctx.Err(); err != nil {
			p.mu.Unlock()
			return nil, err
		}
		if p.entries == nil {
			p.entries = make(map[Key]*entry)
		}
		e := p.entries[key]
		if e == nil {
			e = &entry{}
			p.entries[key] = e
		}
		if e.active == nil {
			done := make(chan struct{})
			e.active, e.at, e.failure = done, clock.Or(p.Now)().UTC(), ""
			p.mu.Unlock()
			return &Booting{runtime: p, key: key, done: done}, nil
		}
		wait := e.active
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait:
		}
	}
}

func (b *Booting) Finish(err error) {
	failure := ""
	if err != nil {
		failure = err.Error()
	}
	p := b.runtime
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[b.key]
	if e.active != b.done {
		return
	}
	e.active, e.at, e.failure = nil, clock.Or(p.Now)().UTC(), failure
	close(b.done)
}

func (p *Runtime) status(key Key, observed Observation) Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := Status{State: Cold}
	failure := ""
	if e := p.entries[key]; e != nil {
		out.At, failure = e.at, e.failure
		if e.active != nil {
			out.State = Starting
			return out
		}
	}
	switch {
	case observed.Unavailable != "":
		out.State, out.Reason = Unavailable, observed.Unavailable
	case observed.URL != "":
		out.State, out.URL, out.HostOnly = Live, observed.URL, observed.HostOnly
	case observed.NoContract:
		out.State = None
	case failure != "":
		out.State, out.Reason = Broken, failure
	}
	return out
}

// Inspect does no provider work during a boot. Otherwise observation runs outside
// the mutex, then state is reread so a boot admitted meanwhile takes precedence.
func (p *Runtime) Inspect(ctx context.Context, key Key, observer Observer) Status {
	current := p.status(key, Observation{})
	if current.State == Starting {
		return current
	}
	return p.status(key, observer.ObservePreview(ctx))
}
