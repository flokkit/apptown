package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/deploy"
)

type removalHTTPDriver struct {
	deploy.Driver
	calls int
	err   error
}

func (d *removalHTTPDriver) Teardown(context.Context, deploy.Placement, int64, deploy.TakeDown) (deploy.TakenDown, error) {
	d.calls++
	return deploy.TakenDown{}, d.err
}

func TestDeploymentRemovalChecksAuthorityAndHTTPOutcome(t *testing.T) {
	t.Parallel()
	driver := &removalHTTPDriver{}
	a := newDeploymentAuthorityWorld(t, map[string]deployment.DriverFactory{"removal-test": func(context.Context, *deployment.Target, *deployment.Deployment) (deploy.Driver, error) {
		return driver, nil
	}})
	a.standing(t, "removal-test")
	in := dataprotection.RemoveDeployment{ProjectID: a.project, Name: "staging"}
	if _, err := a.data.Remove(context.Background(), in); !errors.Is(err, access.ErrPrincipalRequired) {
		t.Fatalf("missing principal: %v", err)
	}
	h := NewDeployments(a.service, a.data, a.projects)
	path := "/api/v1/projects/game/deployments/staging"
	values := map[string]string{"slug": "game", "name": "staging"}
	if w := deploymentRequest(h.DeleteDeployment, "outsider", http.MethodDelete, path, values); w.Code != 403 {
		t.Fatalf("outsider: %d %s", w.Code, w.Body.String())
	}
	if driver.calls != 0 {
		t.Fatal("denied caller reached provider")
	}
	a.grant(t, "owner", access.CapDeploymentTeardown)
	t.Cleanup(a.db.FailNextStatement("-- name: InsertDecision"))
	if w := deploymentRequest(h.DeleteDeployment, "owner", http.MethodDelete, path, values); w.Code != 500 {
		t.Fatalf("authority receipt: %d %s", w.Code, w.Body.String())
	}
	if driver.calls != 0 {
		t.Fatal("failed authority receipt reached provider")
	}
	driver.err = errors.New("provider response lost")
	if w := deploymentRequest(h.DeleteDeployment, "owner", http.MethodDelete, path, values); w.Code != 409 {
		t.Fatalf("incomplete removal: %d %s", w.Code, w.Body.String())
	}
	if _, err := a.service.Deployments.ByName(t.Context(), a.project, "staging"); err != nil {
		t.Fatalf("incomplete removal lost the deployment: %v", err)
	}
	driver.err = nil
	if w := deploymentRequest(h.DeleteDeployment, "owner", http.MethodDelete, path, values); w.Code != 200 || w.Body.String() != "{\"ok\":true,\"deleted\":[],\"orphans\":[]}\n" {
		t.Fatalf("removal response: %d %s", w.Code, w.Body.String())
	}
	if _, err := a.service.Deployments.ByName(t.Context(), a.project, "staging"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("removed deployment still recorded: %v", err)
	}
	if receipts := receiptsOfKind(t, a.db, "deployment.torn-down"); len(receipts) != 1 || receipts[0].Actor != "owner" {
		t.Fatalf("teardown receipt: %+v", receipts)
	}
	if w := deploymentRequest(h.DeleteDeployment, "owner", http.MethodDelete, path, values); w.Code != 404 {
		t.Fatalf("missing deployment: %d %s", w.Code, w.Body.String())
	}
	disabled := true
	if err := a.identity.SaveUser(context.Background(), identity.AccountChange{Username: "owner", Disabled: &disabled}, access.Principal{Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.data.Remove(deploymentCaller("owner"), in); err == nil || errors.Is(err, deployment.ErrDeploymentMissing) {
		t.Fatalf("disabled caller admitted: %v", err)
	}
}
