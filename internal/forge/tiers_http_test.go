package forge

import (
	"flokkit.io/forge/internal/forge/deployment"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTiersHTTP(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	h := a.Handler()
	get := func(path string) (int, string) {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-AppTown-Actor", "bls")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	pid := createReadTestProject(t, a, "policy-app")
	target, err := a.Deployment.Targets.Put(t.Context(), &deployment.Target{Name: "policy-local", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Deployment.Deployments.EnsureStanding(t.Context(), pid, "main", target.ID, (deployment.Attributes{}).WithDefaults()); err != nil {
		t.Fatal(err)
	}
	if c, _ := get("/api/v1/tiers"); c != 200 {
		t.Fatalf("tiers: %d", c)
	}
	if c, body := get("/api/v1/tiers?project=policy-app"); c != 200 || !strings.Contains(body, `"deploymentPolicy":"compatible-update"`) {
		t.Fatalf("fleet procedure: %d %s", c, body)
	}
}
