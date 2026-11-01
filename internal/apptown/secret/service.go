package secret

import (
	"net/http"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

// Service combines stored values, deployment configuration and secret resolution.
// Application delivery and daemon/runtime lookup use distinct resolution rules.
type Service struct {
	dataDir     string
	bootstrapMu sync.Mutex // serialises rewrites of the bootstrap file
	client      HTTPClient
	infisical   infisical
	Vault       *Vault
	Profiles    *profiles
	Bindings    *bindings
	clock       func() time.Time
}

type Config struct {
	DataDir string
	Client  HTTPClient
	// Now is AppTown's clock, which stamps values, bindings and receipts
	// and ages the Infisical cache (the real clock when nil).
	Now func() time.Time
}

func NewService(db *database.DB, cfg Config) *Service {
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Service{dataDir: cfg.DataDir, client: cfg.Client, infisical: infisical{clock: cfg.Now}, clock: cfg.Now,
		Vault: &Vault{db: db, clock: cfg.Now}, Profiles: newProfiles(db, cfg.Now), Bindings: &bindings{db: db, clock: cfg.Now}}
}

func (a *Service) now() time.Time { return clock.Or(a.clock)().UTC() }
