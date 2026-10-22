package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
)

// A deployment's diagnostics are read with the deployment read grant; a
// part's log, which can hold the app's secrets, only with the grant to
// operate the deployment. Reading the deployment is not enough for its
// logs, and a refusal never reaches its host.
func TestDeploymentDiagnosticsHTTP(t *testing.T) {
	t.Parallel()
	a := newDeploymentAuthorityWorld(t, nil)
	builtin, err := a.service.Targets.Put(context.Background(), &deployment.Target{Name: "built-in-docker", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.service.Deployments.EnsureStanding(t.Context(), a.project, "main", builtin.ID, deployment.Attributes{Feed: "landing", Audience: "team", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	for _, cap := range []access.Capability{access.CapDeploymentRead, access.CapProjectRead} {
		a.grant(t, "owner", cap)
	}
	h := NewDeployments(a.service, a.data, a.projects)
	values := map[string]string{"slug": "game", "name": "main"}
	diagnostics, logs := "/api/v1/projects/game/deployments/main/diagnostics", "/api/v1/projects/game/deployments/main/logs?part=web"

	for _, who := range []string{"outsider"} {
		if w := deploymentRequest(h.DeploymentDiagnostics, who, http.MethodGet, diagnostics, values); w.Code != 403 {
			t.Errorf("%s read the diagnostics: %d %s", who, w.Code, w.Body.String())
		}
	}
	if w := deploymentRequest(h.DeploymentDiagnostics, "owner", http.MethodGet, diagnostics, values); w.Code != 200 || !strings.Contains(w.Body.String(), `"unsupported":"nothing has been deployed here yet"`) {
		t.Errorf("a reader's diagnostics: %d %s", w.Code, w.Body.String())
	}
	for _, who := range []string{"outsider", "owner"} {
		if w := deploymentRequest(h.DeploymentLog, who, http.MethodGet, logs, values); w.Code != 403 {
			t.Errorf("%s read a log without the grant to operate: %d %s", who, w.Code, w.Body.String())
		}
	}
	a.grant(t, "owner", access.CapDeploymentOperate)
	w := deploymentRequest(h.DeploymentLog, "owner", http.MethodGet, logs, values)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "nothing has been deployed") {
		t.Errorf("an operator's log of a deployment with nothing deployed: %d %s", w.Code, w.Body.String())
	}
	// Whatever it answers, no browser or proxy may keep a log response.
	if got := w.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("a log response may be stored: Cache-Control %q", got)
	}
}
