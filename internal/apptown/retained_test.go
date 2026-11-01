package apptown

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/deploy"
)

func TestRetainedSettlementChecksAuthorityAndHTTPOutcome(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	driver := &removalHTTPDriver{}
	deps := deploymentDependenciesForTest(a)
	providers := providerDependenciesFor(a, digitalocean.Options{})
	providers.Drivers = map[string]deployment.DriverFactory{"retained-test": func(context.Context, *deployment.Target, *deployment.Deployment) (deploy.Driver, error) {
		return driver, nil
	}}
	deps.Providers = deployment.NewProviders(context.Background(), a.DB, deployment.ProviderRecords{Targets: deps.Targets, Deployments: deps.Deployments}, deps.Hosts, deps.Environment, providers)
	// Deployment and data protection's take-down share the one Providers,
	// as the composition root builds them.
	a.targetProviders = deps.Providers
	starter := dataprotection.NewStarter(a.DB, starterDependenciesFor(a))
	deps.CopyStarter = starter
	a.Deployment = deployment.New(context.Background(), a.DB, deps)
	// The app's data protection registered the operation kinds with its
	// ledger; this removal is a direct take-down, which needs none.
	dataDeps := dataProtectionDependencies(a)
	dataDeps.Operations = nil
	a.DataProtection = dataprotection.New(starter, dataDeps)
	rebuildConsoleOwners(a)
	a.health = a.Deployment.HealthController()
	target, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: "host", Kind: "retained-test", Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	for _, cap := range []access.Capability{access.CapDeploymentBind, access.CapDeploymentTeardown} {
		a.addGrant("user", "owner", "", string(cap), access.ScopeProject, idstest.N(1).String(), "boss", "")
	}
	ctx := verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "owner", Via: "session"})
	configured, err := a.Deployment.Configure(ctx, deployment.ConfigureDeployment{ProjectID: idstest.N(1), Name: "staging", Target: target.Name})
	if err != nil {
		t.Fatal(err)
	}
	if err := deployment.NewNeedStore(a.DB, a.cfg.clock.Now).RecordIntent(ctx, configured.Deployment.ID, "postgres", map[string]any{"name": "demo-db"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DataProtection.Remove(ctx, dataprotection.RemoveDeployment{ProjectID: idstest.N(1), Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	rows, err := a.Deployment.Retained.ForProject(ctx, idstest.N(1))
	if err != nil || len(rows) != 1 {
		t.Fatalf("retained fixture: %+v %v", rows, err)
	}
	in := deployment.SettleRetained{ProjectID: idstest.N(1), ResourceID: rows[0].ID, Disposition: deployment.RetainedReleased}
	if err := a.Deployment.SettleRetained(context.Background(), in); !errors.Is(err, access.ErrPrincipalRequired) {
		t.Fatalf("missing principal: %v", err)
	}
	outsider := verifiedContext(context.Background(), access.Principal{Kind: "user", Name: "outsider", Via: "session"})
	if err := a.Deployment.SettleRetained(outsider, in); err == nil {
		t.Fatal("direct unauthorized call admitted")
	}
	h := a.Handler()
	path := fmt.Sprintf("/api/v1/projects/game/retained/%d", in.ResourceID)
	body := `{"disposition":"released","note":"demo database","settledBy":"boss"}`
	for _, tc := range []struct {
		who, path, body string
		code            int
	}{
		{"outsider", path, body, 403},
		{"outsider", path, `{"disposition":"invalid"}`, 403},
		{"owner", path, `{"disposition":"invalid"}`, 422},
		{"owner", path, `{"disposition":"retained"}`, 422},
		{"owner", path, `{"disposition":"kept"}`, 422},
		{"owner", path, `{`, 400},
		{"owner", "/api/v1/projects/game/retained/bad", body, 400},
		{"boss", fmt.Sprintf("/api/v1/projects/open/retained/%d", in.ResourceID), body, 404},
	} {
		if w := authzCall(h, tc.who, http.MethodPost, tc.path, tc.body); w.Code != tc.code {
			t.Fatalf("%s %s %s: %d %s, want %d", tc.who, tc.path, tc.body, w.Code, w.Body.String(), tc.code)
		}
	}
	dbtest.FailNextStatement(t, a.DB, "-- name: ActiveGrantsOfPrincipal")
	if w := authzCall(h, "owner", http.MethodPost, path, body); w.Code != 500 {
		t.Fatalf("unread authority: %d %s", w.Code, w.Body.String())
	}
	requireAudits(t, a.DB, 0, "retained.settled")
	rows, err = a.Deployment.Retained.ForProject(ctx, idstest.N(1))
	if err != nil || len(rows) != 1 || rows[0].Disposition != deployment.RetainedOpen {
		t.Fatalf("refused request mutated: %+v %v", rows, err)
	}
	if w := authzCall(h, "owner", http.MethodPost, path, body); w.Code != 200 || w.Body.String() != "{\"ok\":true}\n" {
		t.Fatalf("settlement: %d %s", w.Code, w.Body.String())
	}
	rows, err = a.Deployment.Retained.ForProject(ctx, idstest.N(1))
	if err != nil || len(rows) != 1 || rows[0].SettledBy != "owner" || rows[0].Disposition != deployment.RetainedReleased || rows[0].Note != "demo database" || !rows[0].SettledAt.Valid {
		t.Fatalf("settled record: %+v %v", rows, err)
	}
	if n := auditsWhere(t, a.DB, "retained.settled", func(e audit.Entry) bool { return e.Actor == "owner" }); n != 1 {
		t.Fatalf("settlement receipts by owner: %d", n)
	}
	if w := authzCall(h, "owner", http.MethodPost, path, body); w.Code != 404 {
		t.Fatalf("repeat: %d %s", w.Code, w.Body.String())
	}
	disabled := true
	if err := a.Access.SaveUser(context.Background(), identity.AccountChange{Username: "owner", Disabled: &disabled}, access.Principal{ID: testPrincipalOf(t, a.DB, "boss"), Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Deployment.SettleRetained(ctx, in); err == nil || errors.Is(err, deployment.ErrRetainedMissing) {
		t.Fatalf("disabled caller admitted: %v", err)
	}
	if driver.calls != 1 {
		t.Fatalf("settlement reached the provider: %d calls including removal", driver.calls)
	}
}
