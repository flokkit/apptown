package httpapi

import (
	"context"
	"flokkit.io/forge/internal/forge/secret"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/dataprotection"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
)

type deploymentAuthorityWorld struct {
	db       *database.DB
	projects *project.Projects
	identity *identity.Service
	service  *deployment.Service
	data     *dataprotection.Service
	project  ids.ID
	// shared is the records and collaborators service was built over; a
	// test that builds the service again with other ports reuses them.
	shared deployment.Dependencies
}

func newDeploymentAuthorityWorld(t *testing.T, drivers map[string]deployment.DriverFactory) *deploymentAuthorityWorld {
	t.Helper()
	db := databaseSuite.New(t)
	projects := project.New(db, nil)
	projectID, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "game", Name: "Game"})
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := identity.SeedAdministrator(context.Background(), "boss"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"owner", "outsider"} {
		if _, err := identity.EnsureUser(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	checked := access.Checked{Authority: identity, Projects: projects}
	targets, deployments := deployment.NewTargets(db, nil), deployment.NewDeployments(db)
	hosts, environment := deployment.NewHosts(targets, nil, deployment.HostSettings{}), deployment.NewEnvironment(targets, deployment.EnvironmentDependencies{Projects: projects, Secrets: secret.NewService(db, secret.Config{DataDir: t.TempDir()})})
	providers := deployment.NewProviders(context.Background(), db, deployment.ProviderRecords{Targets: targets, Deployments: deployments}, hosts, environment, deployment.ProviderDependencies{Drivers: drivers})
	starter := dataprotection.NewStarter(db, dataprotection.StarterDependencies{Authority: checked, Targets: targets, Hosts: hosts, Environment: environment, Providers: providers})
	shared := deployment.Dependencies{Targets: targets, Deployments: deployments, Hosts: hosts, Environment: environment, Providers: providers, CopyStarter: starter}
	deps := shared
	deps.Authority = checked
	service := deployment.New(context.Background(), db, deps)
	data := dataprotection.New(starter, dataprotection.Dependencies{Deployments: service.Deployments, Releases: service.Releases, Bundles: service.Bundles, Retained: service.Retained, Outcomes: service.Outcomes, Projects: projects})
	return &deploymentAuthorityWorld{db: db, projects: projects, identity: identity, service: service, data: data, project: projectID, shared: shared}
}

func (w *deploymentAuthorityWorld) grant(t *testing.T, name string, cap access.Capability) {
	t.Helper()
	if _, err := w.identity.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: name, Capability: string(cap), Scope: access.ScopeProject, ScopeID: w.project.String(), By: "fixture"}, ""); err != nil {
		t.Fatal(err)
	}
}

func (w *deploymentAuthorityWorld) standing(t *testing.T, kind string) {
	t.Helper()
	target, err := w.service.Targets.Put(context.Background(), &deployment.Target{Name: "host", Kind: kind, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.service.Deployments.EnsureStanding(t.Context(), w.project, "staging", target.ID, deployment.Attributes{}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
}

func deploymentRequest(handler http.HandlerFunc, who, method, path string, values map[string]string) *httptest.ResponseRecorder {
	return deploymentBodyRequest(handler, who, method, path, "", values)
}

func deploymentBodyRequest(handler http.HandlerFunc, who, method, path, body string, values map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(access.WithCaller(r.Context(), access.Caller{Principal: access.Principal{Kind: "user", Name: who, Via: "session"}, Verified: true, Receipt: who}))
	for name, value := range values {
		r.SetPathValue(name, value)
	}
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func deploymentCaller(who string) context.Context {
	return access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: who, Via: "session"}, Verified: true, Receipt: who})
}
