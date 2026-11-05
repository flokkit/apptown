package secret

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// Service combines stored values, deployment configuration and secret resolution.
// Application delivery and daemon/runtime lookup use distinct resolution rules.
type Service struct {
	projects    projectScope
	authority   access.Authorizer
	dataDir     string
	bootstrapMu sync.Mutex // serialises rewrites of the bootstrap file
	client      HTTPClient
	infisical   infisical
	Vault       *Vault
	Profiles    *profiles
	Bindings    *bindings
	clock       func() time.Time
}

// projectScope supplies current project identity without giving secret ownership of projects.
type projectScope interface {
	Slug(context.Context, ids.ID) (string, error)
}

type Config struct {
	Projects  projectScope
	Authority access.Authorizer
	DataDir   string
	Client    HTTPClient
	// Now is AppTown's clock, which stamps values, bindings and receipts
	// and ages the Infisical cache (the real clock when nil).
	Now func() time.Time
}

func NewService(db *database.DB, cfg Config) *Service {
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Service{projects: cfg.Projects, authority: cfg.Authority, dataDir: cfg.DataDir, client: cfg.Client, infisical: infisical{clock: cfg.Now}, clock: cfg.Now,
		Vault: &Vault{db: db, clock: cfg.Now}, Profiles: newProfiles(db, cfg.Now), Bindings: &bindings{db: db, clock: cfg.Now}}
}

func (a *Service) now() time.Time { return clock.Or(a.clock)().UTC() }
