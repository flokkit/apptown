package integration

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dependency"

	"github.com/flokkit/apptown/internal/apptown/integration/internal/integrationsql"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

// Service combines integration records with service-kind environment rendering.
// Provider execution and authorization remain with the callers of these operations.
type Service struct {
	kinds           kindCatalog // what a dependency kind gives an app, e.g. error reporting's variables
	projects        *project.Projects
	secrets         *secret.Service
	scmRepositories scmRepositories
	Bindings        *Bindings
	ServiceKinds    *serviceKinds
	Attachments     *Attachments
	clock           func() time.Time // AppTown's clock; nil is the real one
}

// now reads AppTown's clock: an SCM token's expiry is written and
// judged by it.
func (s *Service) now() time.Time { return clock.Or(s.clock)() }

// kindCatalog is the dependency catalog's answer for one capability.
type kindCatalog interface {
	ByCapability(context.Context, string) (*dependency.Kind, error)
}

type Dependencies struct {
	// Kinds is the dependency catalog: the stock definitions say what an
	// app with a dependency is given.
	Kinds           kindCatalog
	Projects        *project.Projects
	Secrets         *secret.Service
	SCMRepositories scmRepositories
	// Now is AppTown's clock; nil is the real one.
	Now func() time.Time
}

func NewService(db *database.DB, deps Dependencies) *Service {
	bindings, kinds, attachments := NewWithClock(db, deps.Now), newServiceKinds(db), NewAttachments(db)
	kinds.clock, attachments.clock = deps.Now, deps.Now
	return &Service{kinds: deps.Kinds, projects: deps.Projects, secrets: deps.Secrets, scmRepositories: deps.SCMRepositories, Bindings: bindings, ServiceKinds: kinds, Attachments: attachments, clock: deps.Now}
}

// MarkScannerManaged marks an existing stock scanner before its version sync.
// A missing binding remains missing; publication is a separate operation.
func (s *Service) MarkScannerManaged(ctx context.Context, name string) error {
	return integrationsql.New(s.Bindings.db.SQL()).MarkScannerManaged(ctx, name)
}
