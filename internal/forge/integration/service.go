package integration

import (
	"context"
	"flokkit.io/forge/internal/forge/dependency"
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/integration/internal/integrationsql"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/secret"
)

// Service combines integration records with service-kind environment rendering.
// Provider execution and authorization remain with the callers of these operations.
type Service struct {
	kinds        KindCatalog // what a dependency kind gives an app, e.g. error reporting's variables
	projects     *project.Projects
	secrets      *secret.Service
	Bindings     *Bindings
	ServiceKinds *ServiceKinds
	Attachments  *Attachments
	clock        func() time.Time // the forge's clock; nil is the real one
}

// now reads the forge's clock: an SCM token's expiry is written and
// judged by it.
func (s *Service) now() time.Time { return clock.Or(s.clock)() }

// KindCatalog is the dependency catalog's answer for one capability.
type KindCatalog interface {
	ByCapability(context.Context, string) (*dependency.Kind, error)
}

type Dependencies struct {
	// Kinds is the dependency catalog: the stock definitions say what an
	// app with a dependency is given.
	Kinds    KindCatalog
	Projects *project.Projects
	Secrets  *secret.Service
	// Now is the forge's clock; nil is the real one.
	Now func() time.Time
}

func NewService(db *database.DB, deps Dependencies) *Service {
	bindings, kinds, attachments := New(db), NewServiceKinds(db), NewAttachments(db)
	bindings.clock, kinds.clock, attachments.clock = deps.Now, deps.Now, deps.Now
	return &Service{kinds: deps.Kinds, projects: deps.Projects, secrets: deps.Secrets, Bindings: bindings, ServiceKinds: kinds, Attachments: attachments, clock: deps.Now}
}

// MarkScannerManaged marks an existing stock scanner before its version sync.
// A missing binding remains missing; publication is a separate operation.
func (s *Service) MarkScannerManaged(ctx context.Context, name string) error {
	return integrationsql.New(s.Bindings.db.SQL()).MarkScannerManaged(ctx, name)
}
