package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/forge/deployment"
)

// A deployment's resources are read with the deployment read grant; an
// override is set or cleared only by someone who may deploy the
// deployment, and a refusal records nothing.
func TestDeploymentResourcesHTTP(t *testing.T) {
	t.Parallel()
	driver := &listingHTTPDriver{}
	a := newDeploymentAuthorityWorld(t, map[string]deployment.DriverFactory{
		deployment.KindDockerHost: func(context.Context, *deployment.Target, *deployment.Deployment) (deploy.Driver, error) {
			return driver, nil
		},
	})
	source := configurationHTTPSource{projectID: a.project}
	checked := access.Checked{Authority: a.identity, Projects: a.projects}
	deps := a.shared
	deps.Authority = checked
	deps.Releases = deployment.ReleaseDependencies{Authority: checked, Sources: source, Secrets: unreachedReleaseSecrets{}, Queue: &admittedReleaseQueue{}}
	a.service = deployment.New(context.Background(), a.db, deps)
	for _, cap := range []access.Capability{access.CapDeploymentDeploy, access.CapDeploymentRead, access.CapProjectRead} {
		a.grant(t, "owner", cap)
	}
	builtin, err := a.service.Targets.Put(context.Background(), &deployment.Target{Name: "built-in-docker", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := a.service.Deployments.EnsureStanding(t.Context(), a.project, "main", builtin.ID, deployment.Attributes{Feed: "landing", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	h := NewDeployments(a.service, a.data, a.projects)
	values := map[string]string{"slug": "game", "name": "main"}
	path := "/api/v1/projects/game/deployments/main/resources"

	if w := deploymentRequest(h.DeploymentResources, "outsider", http.MethodGet, path, values); w.Code != 403 {
		t.Errorf("an outsider read the resources: %d %s", w.Code, w.Body.String())
	}
	if w := deploymentRequest(h.DeploymentResources, "owner", http.MethodGet, path, values); w.Code != 200 || !strings.Contains(w.Body.String(), `"recorded":false`) {
		t.Errorf("the owner's read: %d %s", w.Code, w.Body.String())
	}
	body := `{"part":"web","memory":"2G","reason":"out of memory"}`
	for _, req := range []struct {
		method  string
		handler http.HandlerFunc
	}{{http.MethodPut, h.SetResourceOverride}, {http.MethodDelete, h.ClearResourceOverride}} {
		if w := deploymentBodyRequest(req.handler, "outsider", req.method, path, body, values); w.Code != 403 {
			t.Errorf("an outsider's %s: %d %s", req.method, w.Code, w.Body.String())
		}
	}
	if o, err := a.service.Overrides(t.Context(), dep.ID); err != nil || len(o) != 0 {
		t.Errorf("a refused override recorded %v %v", o, err)
	}
	if w := deploymentBodyRequest(h.SetResourceOverride, "owner", http.MethodPut, path, `{"part":"web","memory":"lots","reason":"x"}`, values); w.Code != 400 {
		t.Errorf("an override with a memory that is not one: %d %s", w.Code, w.Body.String())
	}
	if w := deploymentBodyRequest(h.SetResourceOverride, "owner", http.MethodPut, path, `{"part":"web","memory":"2G","reason":" "}`, values); w.Code != 400 {
		t.Errorf("an override without a reason: %d %s", w.Code, w.Body.String())
	}
	if w := deploymentBodyRequest(h.SetResourceOverride, "owner", http.MethodPut, path, `{"part":"web","memory":"none","reason":"measured external supervisor"}`, values); w.Code != 422 {
		t.Errorf("explicit unlimited did not reach owner validation: %d %s", w.Code, w.Body.String())
	}
	if w := deploymentBodyRequest(h.SetResourceOverride, "owner", http.MethodPut, path, `{"part":"web","memory":"none","reason":" "}`, values); w.Code != 400 {
		t.Errorf("explicit unlimited without a reason: %d %s", w.Code, w.Body.String())
	}
	// Authorised and well formed: nothing has landed, so there is no app to
	// know the part from.
	if w := deploymentBodyRequest(h.SetResourceOverride, "owner", http.MethodPut, path, body, values); w.Code != 422 {
		t.Errorf("an override with no bundle: %d %s", w.Code, w.Body.String())
	}
}
