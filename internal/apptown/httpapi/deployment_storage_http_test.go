package httpapi

import (
	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"net/http"
	"testing"
)

// Authority precedes even settled-state inspection; reading never grants adoption.
func TestDeploymentStorageAuthorityHTTP(t *testing.T) {
	t.Parallel()
	a := newDeploymentAuthorityWorld(t, nil)
	for _, name := range []string{"reader", "operator"} {
		if _, err := a.identity.EnsureUser(t.Context(), name); err != nil {
			t.Fatal(err)
		}
	}
	target, err := a.service.Targets.Put(t.Context(), &deployment.Target{Name: "local", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.service.Deployments.EnsureStanding(t.Context(), a.project, "main", target.ID, deployment.Attributes{}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	a.grant(t, "reader", access.CapProjectRead)
	a.grant(t, "reader", access.CapDeploymentRead)
	a.grant(t, "operator", access.CapProjectRead)
	a.grant(t, "operator", access.CapDeploymentOperate)
	h := NewDeployments(a.service, a.data, a.projects)
	path := "/api/v1/projects/game/deployments/main/storage-bindings"
	values := map[string]string{"slug": "game", "name": "main"}
	for _, tc := range []struct {
		who, method string
		handler     http.HandlerFunc
		want        int
	}{
		{"outsider", "GET", h.DeploymentStorageBindings, 403},
		{"reader", "GET", h.DeploymentStorageBindings, 409},
		{"reader", "POST", h.AdoptDeploymentStorageBindings, 403},
		{"operator", "POST", h.AdoptDeploymentStorageBindings, 409},
	} {
		w := deploymentRequest(tc.handler, tc.who, tc.method, path, values)
		if w.Code != tc.want {
			t.Errorf("%s %s: %d %s", tc.who, tc.method, w.Code, w.Body.String())
		}
	}
}
