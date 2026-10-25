package httpapi

import (
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

type Targets struct {
	deployment *deployment.Service
	providers  *deployment.Providers
	projects   *project.Projects
	access     *identity.Service
	db         *database.DB
	now        func() time.Time // AppTown's clock, which times a target check
}

func NewTargets(deployment *deployment.Service, providers *deployment.Providers, projects *project.Projects, access *identity.Service, db *database.DB, now func() time.Time) Targets {
	return Targets{deployment: deployment, providers: providers, projects: projects, access: access, db: db, now: clock.Or(now)}
}
