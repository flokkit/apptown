package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/deploy"
)

type listingHTTPDriver struct {
	deploy.Driver
	calls int
	err   error
}

func (d *listingHTTPDriver) Observe(_ context.Context, p deploy.Placement) (deploy.Observation, error) {
	d.calls++
	return deploy.Observation{Generation: p.Generation, Rollout: "absent"}, d.err
}

func TestDeploymentListingChecksInternalAuthorityAndHTTPUnknown(t *testing.T) {
	t.Parallel()
	driver := &listingHTTPDriver{err: errors.New("private provider detail")}
	a := newDeploymentAuthorityWorld(t, map[string]deployment.DriverFactory{"listing-test": func(context.Context, *deployment.Target, *deployment.Deployment) (deploy.Driver, error) {
		return driver, nil
	}})
	a.standing(t, "listing-test")
	a.grant(t, "owner", access.CapDeploymentRead)
	ctx := deploymentCaller("owner")
	if _, err := a.service.List(context.Background(), a.project); !errors.Is(err, access.ErrPrincipalRequired) {
		t.Fatalf("missing principal: %v", err)
	}
	// Disabled accounts are denied before any provider observation.
	disabledOutsider := true
	if err := a.identity.SaveUser(context.Background(), identity.AccountChange{Username: "outsider", Disabled: &disabledOutsider}, access.Principal{Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	h := NewDeploymentListing(a.projects, a.service, t.TempDir())
	path := "/api/v1/projects/game/deployments"
	values := map[string]string{"slug": "game"}
	if w := deploymentRequest(h.Deployments, "outsider", http.MethodGet, path, values); (w.Code != 403 && w.Code != 401) || driver.calls != 0 {
		t.Fatalf("outsider: %d %s calls=%d", w.Code, w.Body.String(), driver.calls)
	}
	t.Cleanup(a.db.FailNextStatement("-- name: UserByName :one"))
	if _, err := a.service.List(ctx, a.project); err == nil || driver.calls != 0 {
		t.Fatalf("unreadable authority reached provider: %v calls=%d", err, driver.calls)
	}
	w := deploymentRequest(h.Deployments, "owner", http.MethodGet, path, values)
	var rows []struct {
		Running     *bool
		Observation string
		Target      *struct{ Name string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || w.Code != 200 || len(rows) != 1 {
		t.Fatalf("listing: %d %s %v", w.Code, w.Body.String(), err)
	}
	if rows[0].Running != nil || rows[0].Observation != "unknown" || rows[0].Target == nil || rows[0].Target.Name != "host" || strings.Contains(w.Body.String(), "private provider detail") || !strings.Contains(w.Body.String(), `"running":null`) {
		t.Fatalf("unknown response: %s", w.Body.String())
	}
	if driver.calls != 0 {
		t.Fatalf("cold listing called provider %d times", driver.calls)
	}
	if err := a.service.ObserveRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	driver.err = nil
	if err := a.service.ObserveRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	w = deploymentRequest(h.Deployments, "owner", http.MethodGet, path, values)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"running":false`) || !strings.Contains(w.Body.String(), `"observation":"stopped"`) {
		t.Fatalf("stopped response: %d %s", w.Code, w.Body.String())
	}
	t.Cleanup(a.db.FailNextStatement("-- name: NeedInstances"))
	if w := deploymentRequest(h.Deployments, "owner", http.MethodGet, path, values); w.Code != 500 {
		t.Fatalf("missing stored facts: %d %s", w.Code, w.Body.String())
	}
	disabled := true
	if err := a.identity.SaveUser(context.Background(), identity.AccountChange{Username: "owner", Disabled: &disabled}, access.Principal{Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.service.List(ctx, a.project); err == nil {
		t.Fatal("disabled caller read deployments")
	}
}
