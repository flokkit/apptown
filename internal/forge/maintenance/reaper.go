// Package maintenance owns the periodic host cleanup policy. It protects
// persistent resources, active executions and saved conversations; Docker
// supplies mechanisms and each domain supplies its own recorded facts.
package maintenance

import (
	"context"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/strs"
)

func shortOutput(s string) string {
	return strs.Truncate(s, 120)
}

type ConsoleRuntime interface {
	Status(context.Context, string) (console.Status, error)
}
type Dependencies struct {
	Docker   docker.Scope
	Sessions *console.Service
	Runtime  ConsoleRuntime
	// Now is the forge's clock, against which the reaper judges the ages
	// the forge stamped (the real clock when nil).
	Now func() time.Time
}
type Reaper struct {
	db       *database.DB
	docker   docker.Scope
	sessions *console.Service
	runtime  ConsoleRuntime
	dataDir  string
	clock    func() time.Time
}

func New(db *database.DB, deps Dependencies, dataDir string) *Reaper {
	return &Reaper{db: db, docker: deps.Docker, sessions: deps.Sessions, runtime: deps.Runtime, dataDir: dataDir, clock: deps.Now}
}

func (r *Reaper) now() time.Time { return clock.Or(r.clock)() }
