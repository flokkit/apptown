package apptown

import (
	"slices"

	"context"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/deploy"
)

func TestDigitalOceanTargetIsOfferedByTheConnector(t *testing.T) {
	t.Parallel()
	a, _, _, _ := doWorld(t)
	tg, err := a.Deployment.Targets.ByName(t.Context(), "do-syd")
	if err != nil {
		t.Fatal(err)
	}
	if tg.Kind != deployment.KindDOApp || tg.NeedsMode != "managed" || tg.Capabilities.PersistentVolumes || tg.Ingress["binding"] != "provider-issued" {
		t.Fatalf("target: %+v", tg)
	}
	h := a.targetProviders.CheckCloudAccount(context.Background(), map[string]any{"tokenFrom": "DIGITALOCEAN_ACCESS_TOKEN"})
	if !h.OK || !strings.Contains(h.Detail, "operator@example.com") {
		t.Fatalf("health: %+v", h)
	}
}

func TestDigitalOceanReleaseEndToEnd(t *testing.T) {
	t.Parallel()
	a, f, p, b := doWorld(t)
	tg, _ := a.Deployment.Targets.ByName(t.Context(), "do-syd")
	env, err := a.Deployment.Deployments.EnsureStanding(t.Context(), p.ID, "cloud", tg.ID, deployment.Attributes{Feed: "promotion", Audience: "organisation", DataClass: "non-production"})
	if err != nil {
		t.Fatal(err)
	}
	out, code, err := requestBundleForTest(t, a, p, env, b, "bls")
	if err != nil || code != 202 {
		t.Fatalf("release: %d %v", code, err)
	}
	number := out["number"].(int64)
	release, err := a.Deployment.Releases.ByNumber(context.Background(), p.ID, number)
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	rows, err := a.Deployment.Releases.Events(context.Background(), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		events = append(events, row.Text)
	}
	if release.State != deployment.Released {
		t.Fatalf("release ended %q:\n%s", release.State, strings.Join(events, "\n"))
	}
	// The registry was created, the cluster made and waited for, the app created.
	state := f.Snapshot()
	if state.Registry != "forge-test" || len(state.Databases) != 1 || len(state.Apps) != 1 {
		t.Fatalf("registry %q dbs %d apps %d", state.Registry, len(state.Databases), len(state.Apps))
	}
	spec := state.Specs[0]
	if spec["name"] != "apptown-"+deploy.HostKey(env.ID) || spec["region"] != "syd" {
		t.Fatalf("spec: %v", spec)
	}
	svc := spec["services"].([]any)[0].(map[string]any)
	if svc["http_port"] != float64(8080) || svc["image"].(map[string]any)["registry_type"] != "DOCR" || svc["health_check"].(map[string]any)["http_path"] != "/healthz" {
		t.Fatalf("service: %v", svc)
	}
	job := spec["jobs"].([]any)[0].(map[string]any)
	if job["kind"] != "PRE_DEPLOY" || job["run_command"] != "dotnet App.dll --init" {
		t.Fatalf("job: %v", job)
	}
	db := spec["databases"].([]any)[0].(map[string]any)
	if db["cluster_name"] != "apptown-db-"+deploy.HostKey(env.ID) || db["engine"] != "PG" {
		t.Fatalf("database component: %v", db)
	}
	var dbURL string
	for _, e := range svc["envs"].([]any) {
		if e.(map[string]any)["key"] == "DATABASE_URL" {
			dbURL = e.(map[string]any)["value"].(string)
		}
	}
	if dbURL != "${postgres.DATABASE_URL}" {
		t.Fatalf("the app is handed the platform's binding: %q", dbURL)
	}
	// The environment's record: generation 1, the app as its handle, the URL.
	env, _ = a.Deployment.Deployments.ByName(t.Context(), p.ID, "cloud")
	if env.ObservedGeneration != 1 || env.Handle["appId"] != "app-1" || !strings.HasSuffix(env.URL, ".ondigitalocean.app") || env.Readiness != deployment.Reconciled {
		t.Fatalf("environment: %+v", env)
	}
	needs, err := a.Deployment.Needs.ForDeployment(context.Background(), env.ID)
	if err != nil || len(needs) != 1 || needs[0].Need != "postgres" || needs[0].Status != deployment.NeedReady {
		t.Fatalf("need instances: %+v %v", needs, err)
	}
	if len(*f.owned) != 1 || !strings.HasSuffix((*f.owned)[0], "defaultdb app<-app") {
		t.Fatalf("the app's user owns its database: %v", *f.owned)
	}
	if len(*f.probed) != 1 || !strings.HasSuffix((*f.probed)[0], ".ondigitalocean.app/healthz") {
		t.Fatalf("done means the front door answers the readiness path: %v", *f.probed)
	}
	if needs[0].Handle["id"] != "db-1" {
		t.Fatalf("the provider's id is recorded on the need instance: %+v", needs[0])
	}
	if release.BundleID != b.ID || release.DeploymentID != env.ID || release.Generation != 1 || release.ArtifactHash == "" || release.Notes != env.URL {
		t.Fatalf("release row: %+v", release)
	}
	// A second release updates the app (PUT) as generation 2; the cluster is reused.
	out, _, err = requestBundleForTest(t, a, p, env, b, "bls")
	if err != nil {
		t.Fatal(err)
	}
	number = out["number"].(int64)
	release, err = a.Deployment.Releases.ByNumber(context.Background(), p.ID, number)
	if err != nil {
		t.Fatal(err)
	}
	env, _ = a.Deployment.Deployments.ByName(t.Context(), p.ID, "cloud")
	state = f.Snapshot()
	if release.State != deployment.Released || env.ObservedGeneration != 2 || len(state.Databases) != 1 || len(state.Apps) != 1 || len(state.Specs) != 2 {
		t.Fatalf("second release: %s gen %d dbs %d apps %d specs %d", release.State, env.ObservedGeneration, len(state.Databases), len(state.Apps), len(state.Specs))
	}
	// Isolation contract (§15.5a, stock/dependencies/postgres.yaml): the
	// cluster's admin credential is used by AppTown to set the app's
	// database up and reaches neither the app's spec nor any record.
	for i, spec := range state.Specs {
		if strings.Contains(mustJSON(spec), "doadmin") {
			t.Fatalf("spec %d carries the admin credential: %s", i, mustJSON(spec))
		}
	}
	var leaks int
	// sql-guarantee: the admin credential must reach no stored record at all; the stored bytes are the guarantee, which no owner read can show.
	_ = a.DB.SQL().QueryRowContext(context.Background(), `SELECT (SELECT COUNT(*) FROM need_instances WHERE handle_json::text LIKE '%doadmin%' OR binding_json::text LIKE '%doadmin%')
		+ (SELECT COUNT(*) FROM release_events WHERE text LIKE '%doadmin%')
		+ (SELECT COUNT(*) FROM deployment_attempts WHERE observed_json::text LIKE '%doadmin%')`).Scan(&leaks)
	if leaks != 0 {
		t.Fatalf("the admin credential reached %d records", leaks)
	}
	// The cluster AppTown made carries this installation's tag. One made
	// before the tag is given it, in place, once.
	mine := digitalocean.InstallationTag(a.docker.Instance)
	if tags, _ := f.Snapshot().Databases[0]["tags"].([]any); !slices.Contains(tags, any(mine)) {
		t.Fatalf("the made cluster lacks the installation's tag: %v", f.Snapshot().Databases[0]["tags"])
	}
	f.SetDatabaseTags("db-1", "apptown")
	if marked, err := a.targetProviders.MarkRecorded(t.Context()); err != nil || marked != 1 {
		t.Fatalf("marking the recorded cluster: %d %v", marked, err)
	}
	if marked, err := a.targetProviders.MarkRecorded(t.Context()); err != nil || marked != 0 {
		t.Fatalf("a second marking pass: %d %v", marked, err)
	}
	// Infrastructure: what is at the provider and what it hosts. A cluster
	// this installation tagged that nobody records shows as unrecorded; one
	// without its tag (another installation's, or one a script made) is not
	// listed; AppTown's own shows its host.
	f.SeedDatabase(map[string]any{"id": "db-stray", "name": "someones-cluster", "engine": "pg", "version": "16", "region": "syd1", "size": "db-s-1vcpu-1gb", "status": "online", "tags": []any{mine}})
	f.SeedDatabase(map[string]any{"id": "db-script", "name": "forge-demo-pg", "engine": "pg", "version": "16", "region": "syd1", "size": "db-s-1vcpu-1gb", "status": "online"})
	w := tgReq(httpapi.NewInfrastructure(a.Deployment, a.hosts, a.targetProviders, a.Projects, a.DB, a.inventory, a.jobs, a.cfg.usingBuiltinDB, currentBuiltinDB.Container, currentBuiltinDB.Volume, nil).Infrastructure, "GET", "/api/v1/infrastructure", "", nil)
	var infra struct {
		Targets []infrastructureResponse `json:"targets"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &infra)
	// Every target is on the page now; this test reads the cloud one.
	var cloud *infrastructureResponse
	for i := range infra.Targets {
		if infra.Targets[i].Target == "do-syd" {
			cloud = &infra.Targets[i]
		}
	}
	if w.Code != 200 || cloud == nil || !cloud.Live {
		t.Fatalf("infrastructure: %d %s", w.Code, w.Body.String())
	}
	byName := map[string]deploy.Resource{}
	for _, r := range cloud.Resources {
		byName[r.Kind+"/"+r.Name] = r
	}
	if r := byName["app/apptown-"+deploy.HostKey(env.ID)]; r.Record != "recorded" || len(r.Hosts) != 1 || r.Hosts[0].Deployment != "cloud" || r.Status != "active" {
		t.Fatalf("the app hosts the environment: %+v", r)
	}
	if r := byName["database/apptown-db-"+deploy.HostKey(env.ID)]; r.Record != "recorded" || len(r.Hosts) != 1 || r.Hosts[0].Need != "postgres" || r.Version != "pg 16" {
		t.Fatalf("the cluster is the environment's postgres: %+v", r)
	}
	if r := byName["database/someones-cluster"]; r.Record != "unrecorded" {
		t.Fatalf("a stray cluster is unrecorded: %+v", r)
	}
	if r, listed := byName["database/forge-demo-pg"]; listed {
		t.Fatalf("a cluster without this installation's tag is listed: %+v", r)
	}
	if r := byName["registry/forge-test"]; r.Record != "recorded" {
		t.Fatalf("the registry is the target's own: %+v", r)
	}
	w = tgReq(httpapi.NewDeploymentListing(a.Projects, a.Deployment, a.cfg.dataDir, a.DataProtection.FillsOf).Deployments, "GET", "/api/v1/projects/expense/deployments", "", map[string]string{"slug": "expense"})
	var listed []httpapi.DeploymentView
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(listed) != 1 {
		t.Fatalf("deployment listing: %d %s", w.Code, w.Body.String())
	}
	cloudDeployment := listed[0]
	if cloudDeployment.Name != "cloud" || cloudDeployment.ReadinessLabel.String() != "deployed" || cloudDeployment.RestoreLabel.String() != "unknown" || cloudDeployment.TargetName != "do-syd" || len(cloudDeployment.Needs) != 1 {
		t.Fatalf("cloud deployment display: %+v", cloudDeployment)
	}
	if need := cloudDeployment.Needs[0]; need.Need != "postgres" || need.Name != "apptown-db-"+deploy.HostKey(env.ID) || need.StatusLabel.String() != "passed" {
		t.Fatalf("the environment says what backs it: %+v", need)
	}

	// Teardown, chosen without a final copy, deletes the app and retains
	// the database with its ownership on record, never deleting it.
	if _, err := a.Access.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: "bls", Capability: string(access.CapDeploymentTeardown), Scope: access.ScopeProject, ScopeID: fmt.Sprint(p.ID), By: "fixture"}, ""); err != nil {
		t.Fatal(err)
	}
	if live, err := a.Deployment.Releases.Live(context.Background(), release); err != nil || !live {
		t.Fatalf("the deployment's newest release is live before the teardown: %v %v", live, err)
	}
	// The release's images were copied into the project's DOCR repository,
	// which a second deployment on the same account also runs from.
	f.SeedRepository("expense", 23_950_000)
	if _, err := a.Deployment.Deployments.EnsureStanding(t.Context(), p.ID, "staging", tg.ID, deployment.Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	// With no backup store no final copy can be taken, so the take-down is
	// refused until the person chooses to go without one, naming it.
	w = tgReq(httpapi.NewDeployments(a.Deployment, a.DataProtection, a.Projects).DeleteDeployment, "DELETE", "/api/v1/projects/expense/deployments/cloud", "", map[string]string{"slug": "expense", "name": "cloud"})
	if w.Code != 409 || w.Header().Get("X-AppTown-Deny-Code") != "no-final-copy" || len(f.Snapshot().Apps) != 1 {
		t.Fatalf("a take-down with no final copy possible, unchosen: %d %s", w.Code, w.Body.String())
	}
	w = tgReq(httpapi.NewDeployments(a.Deployment, a.DataProtection, a.Projects).DeleteDeployment, "DELETE", "/api/v1/projects/expense/deployments/cloud", `{"withoutFinalCopy":"cloud"}`, map[string]string{"slug": "expense", "name": "cloud"})
	state = f.Snapshot()
	if w.Code != 200 || !strings.Contains(w.Body.String(), "apptown-db-"+deploy.HostKey(env.ID)) || len(state.Apps) != 0 || len(state.Databases) != 3 {
		t.Fatalf("teardown: %d %s apps %d dbs %d", w.Code, w.Body.String(), len(state.Apps), len(state.Databases))
	}
	// The release it served is ended: its log says who took it down, and it
	// no longer reads as live, in the owner's read or on its permalink.
	if live, err := a.Deployment.Releases.Live(context.Background(), release); err != nil || live {
		t.Fatalf("a torn-down deployment's release still reads live: %v %v", live, err)
	}
	logged, err := a.Deployment.Releases.Events(context.Background(), release.ID)
	if err != nil || len(logged) == 0 || logged[len(logged)-1].Text != "taken down — bls deleted the deployment cloud on do-syd; it no longer serves this release" {
		t.Fatalf("the release's log ends with its taking down: %+v %v", logged, err)
	}
	if earlier, _ := a.Deployment.Releases.ByNumber(context.Background(), p.ID, number-1); earlier != nil {
		if rows, _ := a.Deployment.Releases.Events(context.Background(), earlier.ID); strings.Contains(mustJSON(rows), "taken down") {
			t.Fatalf("only the release the deployment served is taken down: %+v", rows)
		}
	}
	w = tgReq(httpapi.NewReleaseHistory(a.Projects, a.Work, a.Deployment, a.DB).ReleaseDetail, "GET", fmt.Sprintf("/api/v1/projects/expense/releases/%d", number), "", map[string]string{"slug": "expense", "number": fmt.Sprint(number)})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"isLive":false`) {
		t.Fatalf("the permalink of a taken-down release: %d %s", w.Code, w.Body.String())
	}
	retained, err := a.Deployment.Retained.ForProject(context.Background(), p.ID)
	if err != nil || len(retained) != 1 {
		t.Fatalf("retained: %v %+v", err, retained)
	}
	rr := retained[0]
	if rr.Deployment != "cloud" || rr.Target != "do-syd" || rr.Need != "postgres" || rr.Handle["id"] != "db-1" || rr.RetainedBy != "bls" || rr.Disposition != deployment.RetainedOpen {
		t.Fatalf("the record outlives the environment with owner, intent and identity: %+v", rr)
	}
	w = tgReq(httpapi.NewRetention(a.Deployment, a.Projects).ProjectRetained, "GET", "/api/v1/projects/expense/retained", "", map[string]string{"slug": "expense"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"apptown-db-`+deploy.HostKey(env.ID)+`"`) {
		t.Fatalf("project retained list: %d %s", w.Code, w.Body.String())
	}
	// After the teardown the cluster is still at the provider, marked retained.
	w = tgReq(httpapi.NewInfrastructure(a.Deployment, a.hosts, a.targetProviders, a.Projects, a.DB, a.inventory, a.jobs, a.cfg.usingBuiltinDB, currentBuiltinDB.Container, currentBuiltinDB.Volume, nil).Infrastructure, "GET", "/api/v1/infrastructure", "", nil)
	infra.Targets = nil
	_ = json.Unmarshal(w.Body.Bytes(), &infra)
	byName = map[string]deploy.Resource{}
	for _, tv := range infra.Targets {
		if tv.Target != "do-syd" {
			continue
		}
		for _, r := range tv.Resources {
			byName[r.Kind+"/"+r.Name] = r
		}
	}
	if r := byName["database/apptown-db-"+deploy.HostKey(env.ID)]; r.Record != "recorded" || r.Retained == nil || r.Retained.Deployment != "cloud" || len(r.Hosts) != 0 {
		t.Fatalf("the retained cluster is shown as left behind: %+v", r)
	}
	if _, ok := byName["app/apptown-"+deploy.HostKey(env.ID)]; ok {
		t.Fatalf("the app is gone: %+v", byName["app/apptown-"+deploy.HostKey(env.ID)])
	}
	w = tgReq(httpapi.NewRetention(a.Deployment, a.Projects).SettleRetained, "POST", "/api/v1/projects/expense/retained/"+fmt.Sprint(rr.ID), `{"disposition":"released","note":"dropped it in the console"}`, map[string]string{"slug": "expense", "id": fmt.Sprint(rr.ID)})
	if w.Code != 200 {
		t.Fatalf("settle: %d %s", w.Code, w.Body.String())
	}
	retained, _ = a.Deployment.Retained.ForProject(context.Background(), p.ID)
	if retained[0].Disposition != deployment.RetainedReleased || retained[0].SettledBy != "bls" || !retained[0].SettledAt.Valid {
		t.Fatalf("settled: %+v", retained[0])
	}
	w = tgReq(httpapi.NewRetention(a.Deployment, a.Projects).SettleRetained, "POST", "/api/v1/projects/expense/retained/"+fmt.Sprint(rr.ID), `{"disposition":"released"}`, map[string]string{"slug": "expense", "id": fmt.Sprint(rr.ID)})
	if w.Code != 404 {
		t.Fatalf("a settled record is settled once: %d", w.Code)
	}
	// The project's DOCR repository is AppTown's: the release's first
	// copy made it and recorded it. Taking down a deployment another still
	// runs from leaves it; taking down the last empties it, starts no
	// garbage collection (it would block the next push), and nothing about it is retained.
	if got := f.Snapshot().RepositoryManifests["expense"]; len(got) != 1 {
		t.Fatalf("a repository another deployment still runs from was emptied: %v", got)
	}
	w = tgReq(httpapi.NewDeployments(a.Deployment, a.DataProtection, a.Projects).DeleteDeployment, "DELETE", "/api/v1/projects/expense/deployments/staging", "", map[string]string{"slug": "expense", "name": "staging"})
	state = f.Snapshot()
	if w.Code != 200 || !strings.Contains(w.Body.String(), "1 images in the image repository forge-test/expense") || len(state.RepositoryManifests["expense"]) != 0 || state.GarbageCollections != 0 {
		t.Fatalf("staging teardown: %d %s manifests %v gc %d", w.Code, w.Body.String(), state.RepositoryManifests["expense"], state.GarbageCollections)
	}
	all, err := a.Deployment.Retained.ForProject(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range all {
		if r.Need == "image repository" {
			t.Fatalf("AppTown's repository was retained: %+v", r)
		}
	}
}
