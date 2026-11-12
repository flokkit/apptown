package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"net/http"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

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
	if _, err := a.data.TakeDown(context.Background(), in); !errors.Is(err, access.ErrPrincipalRequired) {
		t.Fatalf("missing principal: %v", err)
	}
	h := NewDeployments(a.service, a.data, a.projects, a.identity)
	path := "/api/v1/projects/game/deployments/staging"
	values := map[string]string{"slug": "game", "name": "staging"}
	if w := deploymentRequest(h.DeleteDeployment, "outsider", http.MethodDelete, path, values); w.Code != 403 {
		t.Fatalf("outsider: %d %s", w.Code, w.Body.String())
	}
	if driver.calls != 0 {
		t.Fatal("denied caller reached provider")
	}
	a.grant(t, "owner", access.CapDeploymentTeardown)
	t.Cleanup(dbtest.NextStatementFailure(a.db, "-- name: UserByName :one"))
	if w := deploymentRequest(h.DeleteDeployment, "owner", http.MethodDelete, path, values); w.Code != 500 {
		t.Fatalf("unread authority: %d %s", w.Code, w.Body.String())
	}
	if driver.calls != 0 {
		t.Fatal("unread authority reached provider")
	}
	w := deploymentRequest(h.DeleteDeployment, "owner", http.MethodDelete, path, values)
	var admitted OperationAdmitted
	if err := json.Unmarshal(w.Body.Bytes(), &admitted); err != nil {
		t.Fatal(err)
	}
	if w.Code != 202 || admitted.OperationID.IsZero() {
		t.Fatalf("admission: %d %s", w.Code, w.Body.String())
	}
	if driver.calls != 0 {
		t.Fatal("request ran provider teardown")
	}
	if _, err := a.service.Deployments.ByName(t.Context(), a.project, "staging"); err != nil {
		t.Fatalf("admission removed deployment: %v", err)
	}
	ops := operations.New(a.db, access.Checked{Authority: a.identity, Projects: a.projects}, a.identity, nil)
	// Use the registered worker from a separate data service over the same records.
	starter := dataprotection.NewStarter(a.db, dataprotection.StarterDependencies{Authority: access.Checked{Authority: a.identity, Projects: a.projects}, Targets: a.shared.Targets, Hosts: a.shared.Hosts, Environment: a.shared.Environment, Providers: a.shared.Providers})
	_ = dataprotection.New(starter, dataprotection.Dependencies{Operations: ops, Deployments: a.service.Deployments, Releases: a.service.Releases, Bundles: a.service.Bundles, Retained: a.service.Retained, Outcomes: a.service.Outcomes, Projects: a.projects})
	done, err := ops.RunHere(context.Background(), admitted.OperationID)
	if err != nil || done.State != operations.Succeeded {
		t.Fatalf("worker: %+v %v", done, err)
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
	if err := a.identity.SaveUser(context.Background(), identity.AccountChange{Username: "owner", Disabled: &disabled}, testAdmittedPrincipal(t, a.identity, "boss")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.data.TakeDown(deploymentCaller("owner"), in); err == nil || errors.Is(err, deployment.ErrDeploymentMissing) {
		t.Fatalf("disabled caller admitted: %v", err)
	}
}

func TestDockerTakeDownOnlyAdmitsWorkInTheRequest(t *testing.T) {
	t.Parallel()
	driver := &removalHTTPDriver{}
	a := newDeploymentAuthorityWorld(t, map[string]deployment.DriverFactory{deployment.KindDockerHost: func(context.Context, *deployment.Target, *deployment.Deployment) (deploy.Driver, error) {
		return driver, nil
	}})
	a.standing(t, deployment.KindDockerHost)
	a.grant(t, "owner", access.CapDeploymentTeardown)
	h := NewDeployments(a.service, a.data, a.projects, a.identity)
	w := deploymentRequest(h.DeleteDeployment, "owner", http.MethodDelete, "/api/v1/projects/game/deployments/staging", map[string]string{"slug": "game", "name": "staging"})
	var admitted OperationAdmitted
	if err := json.Unmarshal(w.Body.Bytes(), &admitted); err != nil {
		t.Fatal(err)
	}
	if w.Code != 202 || admitted.OperationID.IsZero() || driver.calls != 0 {
		t.Fatalf("request: %d %s calls=%d", w.Code, w.Body.String(), driver.calls)
	}
	if _, err := a.service.Deployments.ByName(t.Context(), a.project, "staging"); err != nil {
		t.Fatalf("admission removed deployment: %v", err)
	}
}
