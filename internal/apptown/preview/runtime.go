package preview

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/clock"
)

// previewKey separates change previews from project-scoped release numbers.
// Its zero value is invalid. These are transient execution identities, not rows.
type previewKey struct {
	kind    uint8
	project ids.ID
	change  ids.ID // a change preview's change
	number  int64  // a release preview's release number, within project
}

func changeKey(id ids.ID) previewKey { return previewKey{kind: 1, change: id} }
func releaseKey(projectID ids.ID, number int64) previewKey {
	return previewKey{kind: 2, project: projectID, number: number}
}

type State string

const (
	none          State = "none"
	stateStarting State = "starting"
	live          State = "live"
	Broken        State = "broken"
	cold          State = "cold"
	// unavailable: where previews run could not be asked, so whether
	// one is running is not known.
	unavailable State = "unavailable"
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
	// Built is when the running preview was booted from what it serves
	// (a reuse of it is not a build), and Commit the commit it was booted
	// from: "" for a change's working files, which are not a commit
	// (their HEAD is not what they hold). Both zero when AppTown keeps no
	// record of the running preview.
	Built  time.Time `json:"built,omitzero"`
	Commit string    `json:"commit,omitempty"`
}

// observation supplies facts; the runtime owns their precedence. Workflow
// decides when an absent contract means there is nothing runnable yet.
type observation struct {
	URL        string
	HostOnly   bool
	NoContract bool
	// Unavailable is why the place previews run could not be asked.
	Unavailable string
	// Built and Commit are AppTown's record of the running preview
	// (Status.Built, Status.Commit).
	Built  time.Time
	Commit string
}
type observer interface {
	ObservePreview(ctx context.Context) observation
}

type entry struct {
	active  chan struct{}
	at      time.Time
	failure string
}

// previewRuntime owns boot serialization and transient diagnostics for one
// instance. The zero value is ready to use and must not be copied after use.
// It starts no work of its own; restart still leaves previews cold.
type previewRuntime struct {
	// now is AppTown's clock, which stamps a boot's start and end (the
	// real clock when nil).
	now     func() time.Time
	mu      sync.Mutex
	entries map[previewKey]*entry
}

// booting is exclusive ownership until Finish. It cannot settle a later boot,
// even if a caller repeats Finish after another request was admitted.
type booting struct {
	runtime *previewRuntime
	key     previewKey
	done    chan struct{}
}

// begin waits for this preview alone. Cancelling a waiter never cancels its owner
// or changes diagnostics. The admitted caller must defer Finish on every path.
func (p *previewRuntime) begin(ctx context.Context, key previewKey) (*booting, error) {
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
			p.entries = make(map[previewKey]*entry)
		}
		e := p.entries[key]
		if e == nil {
			e = &entry{}
			p.entries[key] = e
		}
		if e.active == nil {
			done := make(chan struct{})
			e.active, e.at, e.failure = done, clock.Or(p.now)().UTC(), ""
			p.mu.Unlock()
			return &booting{runtime: p, key: key, done: done}, nil
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

func (b *booting) finish(err error) {
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
	e.active, e.at, e.failure = nil, clock.Or(p.now)().UTC(), failure
	close(b.done)
}

func (p *previewRuntime) status(key previewKey, observed observation) Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := Status{State: cold}
	failure := ""
	if e := p.entries[key]; e != nil {
		out.At, failure = e.at, e.failure
		if e.active != nil {
			out.State = stateStarting
			return out
		}
	}
	switch {
	case observed.Unavailable != "":
		out.State, out.Reason = unavailable, observed.Unavailable
	case observed.URL != "":
		out.State, out.URL, out.HostOnly = live, observed.URL, observed.HostOnly
		out.Built, out.Commit = observed.Built, observed.Commit
	case observed.NoContract:
		out.State = none
	case failure != "":
		out.State, out.Reason = Broken, failure
	}
	return out
}

// inspect does no provider work during a boot. Otherwise observation runs outside
// the mutex, then state is reread so a boot admitted meanwhile takes precedence.
func (p *previewRuntime) inspect(ctx context.Context, key previewKey, observer observer) Status {
	current := p.status(key, observation{})
	if current.State == stateStarting {
		return current
	}
	return p.status(key, observer.ObservePreview(ctx))
}
