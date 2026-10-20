package httpapi

import (
	"time"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/project"
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
