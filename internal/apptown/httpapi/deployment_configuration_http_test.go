package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids"
)

type configurationHTTPSource struct {
	projectID ids.ID
	before    func()
}

func (s configurationHTTPSource) Project(context.Context, ids.ID) (deployment.ReleaseProject, error) {
	return deployment.ReleaseProject{Application: deployment.Application{ID: s.projectID, Slug: "game"}}, nil
}
func (s configurationHTTPSource) CurrentContract(context.Context, string) (*appcontract.Contract, error) {
	if s.before != nil {
		s.before()
	}
	return &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true, DataDir: "/data"}}}, nil
}
func (s configurationHTTPSource) Contract(ctx context.Context, _ deployment.ReleaseProject, _ string) (*appcontract.Contract, string, error) {
	c, err := s.CurrentContract(ctx, "game")
	return c, "selected", err
}

// The feed check rejects a landing deployment before these release effects.
type admittedReleaseQueue struct{ ids []ids.ID }

func (q *admittedReleaseQueue) EnqueueReleaseIn(_ context.Context, _ *database.Tx, id ids.ID) error {
	q.ids = append(q.ids, id)
	return nil
}

type unreachedReleaseSecrets struct{ deployment.ReleaseSecrets }

func TestDeploymentConfigurationHTTP(t *testing.T) {
	t.Parallel()
	driver := &listingHTTPDriver{}
	drivers := map[string]deployment.DriverFactory{
		deployment.KindDockerHost: func(context.Context, *deployment.Target, *deployment.Deployment) (deploy.Driver, error) {
			return driver, nil
		},
		deployment.KindDOApp: func(context.Context, *deployment.Target, *deployment.Deployment) (deploy.Driver, error) {
			return driver, nil
		},
	}
	a := newDeploymentAuthorityWorld(t, drivers)
	sourceReads := 0
	source := configurationHTTPSource{projectID: a.project, before: func() { sourceReads++ }}
	checked := access.Checked{Authority: a.identity, Projects: a.projects}
	queue := &admittedReleaseQueue{}
	deps := a.shared
	deps.Authority = checked
	deps.Configuration = deployment.ConfigurationDependencies{Authority: checked, Projects: source, Contracts: source}
	deps.Releases = deployment.ReleaseDependencies{Authority: checked, Sources: source, Secrets: unreachedReleaseSecrets{}, Queue: queue}
	a.service = deployment.New(context.Background(), a.db, deps)
	for _, cap := range []access.Capability{access.CapDeploymentBind, access.CapDeploymentDeploy, access.CapDeploymentRead, access.CapProjectRead} {
		a.grant(t, "owner", cap)
	}
	builtin, err := a.service.Targets.Put(context.Background(), &deployment.Target{Name: "built-in-docker", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady, Capabilities: deployment.TargetCapabilities{PersistentVolumes: true}})
	if err != nil {
		t.Fatal(err)
	}
	cloud, err := a.service.Targets.Put(context.Background(), &deployment.Target{Name: "cloud", Kind: deployment.KindDOApp, Origin: deployment.OriginAttached, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	deployments := NewDeployments(a.service, a.data, a.projects)
	targets := NewTargets(a.service, a.shared.Hosts, a.shared.Providers, a.projects, a.identity, nil, nil, a.db, nil)
	listing := NewDeploymentListing(a.projects, a.service, t.TempDir())
	projectValues := map[string]string{"slug": "game"}
	createPath := "/api/v1/projects/game/deployments"
	body := `{"name":"prod","target":"cloud","audience":"public","data":"production"}`
	denied := deploymentBodyRequest(deployments.CreateDeployment, "outsider", http.MethodPost, createPath, body, projectValues)
	if denied.Code != 403 {
		t.Fatalf("outsider configured deployment: %d %s", denied.Code, denied.Body.String())
	}
	rows, err := a.service.Deployments.ForProject(t.Context(), a.project)
	if err != nil || len(rows) != 0 {
		t.Fatalf("denial persisted configuration: %+v %v", rows, err)
	}
	w := deploymentBodyRequest(deployments.CreateDeployment, "owner", http.MethodPost, createPath, body, projectValues)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "not offered") {
		t.Fatalf("domain refusal: %d %s", w.Code, w.Body.String())
	}
	// A recognized unavailable procedure is refused before contract lookup or
	// settings, even before the target is offered. Unknown values are invalid.
	beforePolicyReads := sourceReads
	for _, tc := range []struct {
		value  string
		status int
		code   string
	}{
		{`"protected-maintenance"`, 422, "deployment_policy_not_implemented"},
		{`"unknown"`, 400, ""}, {`"Compatible-Update"`, 400, ""}, {`17`, 400, ""}, {`true`, 400, ""}, {`{}`, 400, ""},
	} {
		request := fmt.Sprintf(`{"name":"prod","target":"cloud","deploymentPolicy":%s}`, tc.value)
		refused := deploymentBodyRequest(deployments.CreateDeployment, "owner", http.MethodPost, createPath, request, projectValues)
		if sourceReads != beforePolicyReads {
			t.Fatalf("refused policy %s reached source preparation: %d -> %d", tc.value, beforePolicyReads, sourceReads)
		}
		if refused.Code != tc.status || refused.Header().Get("X-AppTown-Deny-Code") != tc.code {
			t.Fatalf("policy %s: %d %s %v", tc.value, refused.Code, refused.Body.String(), refused.Header())
		}
		if tc.code != "" && (!strings.Contains(refused.Body.String(), "protected-maintenance") || !strings.Contains(refused.Body.String(), "use compatible-update")) {
			t.Fatalf("unreadable reserved refusal: %s", refused.Body.String())
		}
		outsider := deploymentBodyRequest(deployments.CreateDeployment, "outsider", http.MethodPost, createPath, request, projectValues)
		if tc.value[0] == '"' && (outsider.Code != 403 || strings.Contains(outsider.Body.String(), "not implemented")) {
			t.Fatalf("procedure leaked through authority: %d %s", outsider.Code, outsider.Body.String())
		}
	}
	misspelled := deploymentBodyRequest(deployments.CreateDeployment, "owner", http.MethodPost, createPath, `{"name":"prod","target":"cloud","deploymentPolcy":"protected-maintenance"}`, projectValues)
	if misspelled.Code != 400 || !strings.Contains(misspelled.Body.String(), "deploymentPolcy") {
		t.Fatalf("misspelled safety selection ignored: %d %s", misspelled.Code, misspelled.Body.String())
	}
	if sourceReads != beforePolicyReads {
		t.Fatalf("refused policy reached source preparation: %d -> %d", beforePolicyReads, sourceReads)
	}
	rows, err = a.service.Deployments.ForProject(t.Context(), a.project)
	if err != nil || len(rows) != 0 {
		t.Fatalf("policy refusal saved settings: %+v %v", rows, err)
	}
	targetsPath := "/api/v1/projects/game/targets"
	w = deploymentRequest(targets.ProjectTargets, "owner", http.MethodGet, targetsPath, projectValues)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"name":"cloud"`) || !strings.Contains(w.Body.String(), `"name":"built-in-docker"`) {
		t.Fatalf("offered target list: %d %s", w.Code, w.Body.String())
	}
	if err := a.service.Targets.Offer(context.Background(), cloud, "*", deployment.OfferCaps{}, "fixture"); err != nil {
		t.Fatal(err)
	}
	w = deploymentRequest(targets.ProjectTargets, "owner", http.MethodGet, targetsPath, projectValues)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"cloud"`) {
		t.Fatalf("offered target: %d %s", w.Code, w.Body.String())
	}
	w = deploymentBodyRequest(deployments.CreateDeployment, "owner", http.MethodPost, createPath, body, projectValues)
	if w.Code != 422 || !strings.Contains(w.Body.String(), "persistent volumes") {
		t.Fatalf("saved incompatible configuration: %d %s", w.Code, w.Body.String())
	}
	env, err := a.service.Deployments.ByName(t.Context(), a.project, "prod")
	if err != nil || !env.Protected() {
		t.Fatalf("settings must be saved despite 422: %+v %v", env, err)
	}
	cloud.Capabilities.PersistentVolumes = true
	if _, err := a.service.Targets.Put(context.Background(), cloud); err != nil {
		t.Fatal(err)
	}
	w = deploymentBodyRequest(deployments.CreateDeployment, "owner", http.MethodPost, createPath, body, projectValues)
	var saved deployment.Deployment
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.ID != env.ID {
		t.Fatalf("configuration response: %d %s", w.Code, w.Body.String())
	}
	for _, suffix := range []string{"", `,"deploymentPolicy":null`, `,"deploymentPolicy":""`, `,"deploymentPolicy":"compatible-update"`} {
		request := `{"name":"prod","target":"cloud","audience":"public","data":"production"` + suffix + `}`
		defaulted := deploymentBodyRequest(deployments.CreateDeployment, "owner", http.MethodPost, createPath, request, projectValues)
		var view map[string]any
		if defaulted.Code != 200 || json.Unmarshal(defaulted.Body.Bytes(), &view) != nil || view["deploymentPolicy"] != "compatible-update" {
			t.Fatalf("default %s: %d %s", suffix, defaulted.Code, defaulted.Body.String())
		}
	}
	if _, err := a.service.Deployments.EnsureStanding(t.Context(), a.project, "main", builtin.ID, deployment.Attributes{Feed: "landing", Audience: "team", DataClass: "non-production"}); err != nil {
		t.Fatal(err)
	}
	denied = deploymentRequest(deployments.DeployDeployment, "outsider", http.MethodPost, "/api/v1/projects/game/deployments/main/deploy", map[string]string{"slug": "game", "name": "main"})
	if denied.Code != 403 {
		t.Fatalf("outsider reached release admission: %d %s", denied.Code, denied.Body.String())
	}
	w = deploymentRequest(deployments.DeployDeployment, "owner", http.MethodPost, "/api/v1/projects/game/deployments/main/deploy", map[string]string{"slug": "game", "name": "main"})
	if w.Code != 409 {
		t.Fatalf("landing-fed refusal: %d %s", w.Code, w.Body.String())
	}
	w = deploymentRequest(listing.Deployments, "owner", http.MethodGet, createPath, projectValues)
	var list []map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list) != 2 {
		t.Fatalf("deployment list: %d %s", w.Code, w.Body.String())
	}
	for _, e := range list {
		if e["deploymentPolicy"] != "compatible-update" {
			t.Fatalf("listed procedure: %+v", e)
		}
		if e["name"] == "prod" && (e["protected"] != true || e["target"].(map[string]any)["kind"] != deployment.KindDOApp) {
			t.Fatalf("list representation: %v", e)
		}
	}
	// Manual release admission has a real source/bundle and a counted queue.
	if _, err := a.service.Deployments.EnsureStanding(t.Context(), a.project, "manual", builtin.ID, deployment.Attributes{Feed: "manual", Audience: "team", DataClass: "non-production"}); err != nil {
		t.Fatal(err)
	}
	bundle, err := a.service.Bundles.Cut(t.Context(), a.project, "selected", map[string]string{"web": "image@sha256:fixture"}, nil, []byte("parts: []"), deployment.BundleOrigin{})
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"slug": "game", "name": "manual"}
	for _, value := range []string{`"protected-maintenance"`, `"compatible-update"`, `null`} {
		override := deploymentBodyRequest(deployments.DeployDeployment, "owner", http.MethodPost, "/api/v1/projects/game/deployments/manual/deploy", `{"deploymentPolicy":`+value+`}`, values)
		if override.Code != 400 || !strings.Contains(override.Body.String(), "deploymentPolicy") || len(queue.ids) != 0 {
			t.Fatalf("per-release policy override silently deployed: %d %s queue%v", override.Code, override.Body.String(), queue.ids)
		}
	}
	body = fmt.Sprintf(`{"bundleId":%q}`, bundle)
	w = deploymentBodyRequest(deployments.DeployDeployment, "outsider", "POST", "/", body, values)
	releases, err := a.service.Releases.ForProject(context.Background(), a.project, 100)
	if w.Code != 403 || err != nil || len(releases) != 0 || len(queue.ids) != 0 {
		t.Fatalf("denied release admitted: %d %s, %+v %v", w.Code, w.Body.String(), releases, err)
	}
	w = deploymentBodyRequest(deployments.DeployDeployment, "owner", "POST", "/", body, values)
	releases, err = a.service.Releases.ForProject(context.Background(), a.project, 100)
	if w.Code != 202 || err != nil || len(releases) != 1 || len(queue.ids) != 1 || releases[0].ID != queue.ids[0] || releases[0].State != deployment.Releasing {
		t.Fatalf("release admission: %d %s, %+v %v", w.Code, w.Body.String(), releases, err)
	}

}
