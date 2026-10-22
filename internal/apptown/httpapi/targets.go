package httpapi

import (
	"time"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

type Targets struct {
	deployment   *deployment.Service
	hosts        *deployment.Hosts
	providers    *deployment.Providers
	projects     *project.Projects
	access       *identity.Service
	integrations *integration.Service
	needs        *dependency.Catalog
	db           *database.DB
	now          func() time.Time // AppTown's clock, which times a target check
}

func NewTargets(deployment *deployment.Service, hosts *deployment.Hosts, providers *deployment.Providers, projects *project.Projects, access *identity.Service, integrations *integration.Service, needs *dependency.Catalog, db *database.DB, now func() time.Time) Targets {
	return Targets{deployment: deployment, hosts: hosts, providers: providers, projects: projects, access: access, integrations: integrations, needs: needs, db: db, now: clock.Or(now)}
}
