// Package maintenance owns the periodic host cleanup policy. It protects
// persistent resources, active executions and saved conversations; Docker
// supplies mechanisms and each domain supplies its own recorded facts.
package maintenance

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/toolversions"
)

type consoleRuntime interface {
	Status(context.Context, string) (console.Status, error)
}
type Dependencies struct {
	Docker   docker.Scope
	Sessions *console.Service
	Runtime  consoleRuntime
	// ToolVersions names the shell image a cache pass runs its reads and
	// removals in, one snapshot per pass.
	ToolVersions toolversions.Source
	// Now is AppTown's clock, against which the reaper judges the ages
	// AppTown stamped (the real clock when nil).
	Now func() time.Time
}
type Reaper struct {
	db       *database.DB
	docker   docker.Scope
	sessions *console.Service
	runtime  consoleRuntime
	dataDir  string
	clock    func() time.Time
	tools    toolversions.Source
	// cacheSweptAt is when the last complete toolchain-cache sweep began
	// (Cache); zero until one completes. Only the scheduler calls Cache.
	cacheSweptAt time.Time
}

func New(db *database.DB, deps Dependencies, dataDir string) *Reaper {
	return &Reaper{db: db, docker: deps.Docker, sessions: deps.Sessions, runtime: deps.Runtime, dataDir: dataDir, clock: deps.Now, tools: deps.ToolVersions}
}

func (r *Reaper) now() time.Time { return clock.Or(r.clock)() }
