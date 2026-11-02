package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/attention"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/internal/stamp"
)

type infrastructureResponse struct {
	Target    string
	Live      bool
	AsOf      stamp.Time
	Resources []deploy.Resource
}

// Pages remain passive before the first check, after idle, and after an act.
func TestInfrastructurePagesOnlyReadBackgroundEvidence(t *testing.T) {
	t.Parallel()
	a, f, project, bundle := doWorld(t)
	ctx := t.Context()
	read := func(query string) httpapi.InfrastructureTargets {
		t.Helper()
		before := f.Snapshot().ListRequests
		queued := len(operationJobs(t, a, "inventory"))
		response := tgReq(httpapi.NewInfrastructure(a.Deployment, a.hosts, a.targetProviders, a.Projects, a.DB, a.inventory, a.cfg.usingBuiltinDB, currentBuiltinDB.Container, currentBuiltinDB.Volume, nil).Infrastructure, "GET", "/api/v1/infrastructure"+query, "", nil)
		if response.Code != 200 {
			t.Fatal(response.Body.String())
		}
		if f.Snapshot().ListRequests != before || len(operationJobs(t, a, "inventory")) != queued {
			t.Fatal("page contacted a provider or queued a check")
		}
		var got httpapi.InfrastructureTargets
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	read("")
	f.SeedApp("outside", "outside", "")
	job := inventoryRefresh{targets: a.targets, providers: a.targetProviders, inventory: a.Deployment.Inventory, cache: a.inventory}
	if _, err := job.Run(ctx, "do-syd", nil); err != nil {
		t.Fatal(err)
	}
	a.cfg.clock.mu.Lock()
	a.cfg.clock.ahead += 2 * time.Hour
	a.cfg.clock.mu.Unlock()
	read("")
	read("?refresh=1") // the removed query parameter has no effect
	if _, err := job.Run(ctx, "do-syd", nil); err != nil {
		t.Fatal(err)
	}
	target, err := a.targets.ByName(ctx, "do-syd")
	if err != nil {
		t.Fatal(err)
	}
	dep, err := a.deployments.EnsureStanding(ctx, project.ID, "cloud", target.ID, deployment.Attributes{Feed: "promotion", Audience: "team", DataClass: "non-production"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := requestBundleForTest(t, a, project, dep, bundle, "bls"); err != nil {
		t.Fatal(err)
	}
	source := &attention.Inventory{Targets: inventoryTargetOnly{target}, Records: a.Deployment.Inventory, Observer: attentionInventory{service: a.Deployment}}
	pending, _ := source.Snapshot(ctx)
	if len(pending.Items) != 0 || pending.Stale {
		t.Fatalf("act created false warnings or expired a recent successful check: %+v", pending)
	}
	queued := operationJobs(t, a, "inventory")
	if len(queued) == 0 || queued[0].State != "available" || queued[0].Key != "do-syd/act" {
		t.Fatalf("act did not queue an immediate check: %+v", queued)
	}
	page := read("")
	for _, view := range page.Targets {
		if view.Target != target.Name {
			continue
		}
		if !view.Invalidated || view.Live || len(view.Resources) == 0 {
			t.Fatalf("post-release reading must show invalidated records: %+v", view)
		}
		for _, resource := range view.Resources {
			if resource.Record == deploy.RecordMissing || resource.Record == deploy.RecordUnrecorded {
				t.Fatalf("pre-act reading established a discrepancy: %+v", resource)
			}
		}
	}
	if _, err := job.Run(ctx, queued[0].Key, map[string]any{"target": "do-syd"}); err != nil {
		t.Fatal(err)
	}
	got, _ := a.Deployment.InventoryFor(ctx, target)
	if got.Unknown || got.Stale {
		t.Fatalf("post-act reading: %+v", got)
	}
}

type inventoryTargetOnly struct{ target *deployment.Target }

func (s inventoryTargetOnly) All(context.Context) ([]*deployment.Target, error) {
	return []*deployment.Target{s.target}, nil
}

func TestAttentionInventoryObservesActualProviderLossFailureAndRecovery(t *testing.T) {
	t.Parallel()
	a, f, p, b := doWorld(t)
	ctx := t.Context()
	target, err := a.targets.ByName(ctx, "do-syd")
	if err != nil {
		t.Fatal(err)
	}
	dep, err := a.deployments.EnsureStanding(ctx, p.ID, "cloud", target.ID, deployment.Attributes{Feed: "promotion", Audience: "team", DataClass: "non-production"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := requestBundleForTest(t, a, p, dep, b, "bls"); err != nil {
		t.Fatal(err)
	}
	current, err := a.deployments.ByID(ctx, dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	appID, ok := current.Handle["appId"].(string)
	if !ok || appID == "" {
		t.Fatal("fixture did not release an app")
	}
	cfg, err := a.targetProviders.DigitalOceanConfig(ctx, target.Provider)
	if err != nil {
		t.Fatal(err)
	}
	client, err := a.targetProviders.DigitalOceanClient(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	source := &attention.Inventory{Targets: inventoryTargetOnly{target}, Records: a.Deployment.Inventory, Observer: attentionInventory{service: a.Deployment}}
	read := func() attention.Snapshot {
		t.Helper()
		before := f.Snapshot().ListRequests
		got, err := source.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if f.Snapshot().ListRequests != before {
			t.Fatal("Attention performed inline provider IO")
		}
		return got
	}
	missing := func(s attention.Snapshot) []attention.Item {
		var out []attention.Item
		for _, i := range s.Items {
			if i.Title == "do-syd: resource missing" {
				out = append(out, i)
			}
		}
		return out
	}
	job := inventoryRefresh{targets: a.targets, providers: a.targetProviders, inventory: a.Deployment.Inventory, cache: a.inventory}
	if cold := read(); cold.Known || len(cold.Items) != 0 {
		t.Fatalf("cold coverage %+v", cold)
	}
	f.Fail("GET", "/v2/apps", 503)
	if _, err := job.Run(ctx, target.Name, nil); err == nil {
		t.Fatal("first inventory attempt did not fail")
	}
	if failed := read(); failed.Known || len(failed.Items) != 1 || failed.Items[0].Title != "do-syd: resources not known" {
		t.Fatalf("failed first attempt missing condition %+v", failed)
	}
	f.Fail("GET", "/v2/apps", 0)
	if receipt, err := job.Run(ctx, target.Name, nil); err != nil {
		t.Fatal(err)
	} else if receipt["target"] != target.DisplayLabel {
		t.Fatalf("inventory result lost target name: %+v", receipt)
	}
	if warm := read(); !warm.Known || len(missing(warm)) != 0 {
		t.Fatalf("released provider resource missing %+v", warm)
	}
	// Nobody reads for two hours, while background checks keep collecting.
	for range 24 {
		a.cfg.clock.mu.Lock()
		a.cfg.clock.ahead += 5 * time.Minute
		a.cfg.clock.mu.Unlock()
		if _, err := job.Run(ctx, target.Name, nil); err != nil {
			t.Fatal(err)
		}
	}
	if idle := read(); !idle.Known || len(idle.Items) != 0 {
		t.Fatalf("false warning after idle: %+v", idle)
	}
	a.cfg.clock.mu.Lock()
	a.cfg.clock.ahead += 30 * time.Minute
	a.cfg.clock.mu.Unlock()
	if stale := read(); len(stale.Items) != 0 || !stale.Stale || !stale.Known {
		t.Fatalf("old successful reading raised Attention: %+v", stale)
	}
	if _, err := job.Run(ctx, target.Name, nil); err != nil {
		t.Fatal(err)
	}
	// Change only the disposable provider account, keeping the deployed record.
	if err := client.Call(ctx, "DELETE", "/v2/apps/"+appID, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := job.Run(ctx, target.Name, nil); err != nil {
		t.Fatal(err)
	}
	absent := read()
	conditions := missing(absent)
	if !absent.Known || len(conditions) != 1 {
		t.Fatalf("complete provider absence %+v", absent)
	}
	key, at := conditions[0].Key, conditions[0].ObservedAt
	f.Fail("GET", "/v2/apps", 503)
	if _, err := job.Run(ctx, target.Name, nil); err == nil {
		t.Fatal("provider failure ignored")
	}
	failed := read()
	conditions = missing(failed)
	if !failed.Known || len(conditions) != 1 || conditions[0].Key != key || conditions[0].ObservedAt != at || !conditions[0].Stale {
		t.Fatalf("failed refresh erased evidence %+v", failed)
	}
	f.Fail("GET", "/v2/apps", 0)
	f.SeedApp(appID, "recovered", "")
	if _, err := job.Run(ctx, target.Name, nil); err != nil {
		t.Fatal(err)
	}
	if recovered := read(); !recovered.Known || len(missing(recovered)) != 0 {
		t.Fatalf("provider recovery not observed %+v", recovered)
	}
}

// A kind with no inventory reader is skipped, not failed: the job succeeds
// without asking anything, and attention neither requests a read nor warns
// that the target could not be assessed.
func TestKindWithoutInventoryReaderIsSkippedNotFailed(t *testing.T) {
	t.Parallel()
	a, _, _, _ := doWorld(t)
	ctx := t.Context()
	target, err := a.targets.Put(ctx, &deployment.Target{Name: "future-kind", Kind: "future-kind", Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	job := inventoryRefresh{targets: a.targets, providers: a.targetProviders, inventory: a.Deployment.Inventory, cache: a.inventory}
	if _, err := job.Run(ctx, target.Name, nil); err != nil {
		t.Fatalf("a kind without a reader failed its inventory job: %v", err)
	}
	source := &attention.Inventory{Targets: inventoryTargetOnly{target}, Records: a.Deployment.Inventory, Observer: attentionInventory{service: a.Deployment}}
	got, err := source.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range got.Items {
		if strings.HasSuffix(item.Key, "/unassessed") {
			t.Fatalf("a kind without a reader warned as unassessed: %+v", item)
		}
	}
}

// The schedule admits durable collection without any page or Attention read.
func TestInfrastructureScheduleChecksAllTargetsWithoutReaders(t *testing.T) {
	t.Parallel()
	a, f, project, _ := doWorld(t)
	target, err := a.targets.ByName(t.Context(), "do-syd")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.deployments.EnsureStanding(t.Context(), project.ID, "cloud", target.ID, deployment.Attributes{Feed: "promotion", Audience: "team", DataClass: "production"}); err != nil {
		t.Fatal(err)
	}
	targets, err := a.targets.All(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	before := f.Snapshot().ListRequests
	if err := infrastructureChecksFor(a).request(t.Context()); err != nil {
		t.Fatal(err)
	}
	runQueuedJobForTest(t, a, infrastructureCheckJob, passScheduledKey)
	rows := operationJobs(t, a, "inventory")
	for _, target := range targets {
		if target.Status == deployment.TargetRetired || target.Kind == deployment.KindDockerHost {
			continue
		}
		found := false
		for _, row := range rows {
			if row.Key == target.Name && row.State == "available" {
				found = true
			}
		}
		if !found {
			t.Errorf("no periodic inventory for %s", target.Name)
		}
	}
	if len(operationJobs(t, a, diskObservationJob)) != 1 || len(operationJobs(t, a, deployment.CapacityObservationJob)) != len(targets) {
		t.Fatal("periodic pass omitted disk checks")
	}
	if len(operationJobs(t, a, "protection-observation")) != 1 {
		t.Fatal("periodic pass omitted protection observations")
	}
	if f.Snapshot().ListRequests != before {
		t.Fatal("scheduler performed provider IO")
	}
	found := false
	for _, schedule := range apptownJobSchedules() {
		if schedule.Operation != infrastructureCheckJob {
			continue
		}
		found = true
		at := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
		if schedule.When.Next(at).Sub(at) != 5*time.Minute {
			t.Fatal("infrastructure schedule is not five minutes")
		}
	}
	if !found {
		t.Fatal("background schedule missing")
	}
}

func TestActsOnOneTargetQueueOnePostActCheck(t *testing.T) {
	t.Parallel()
	a, _, _, _ := doWorld(t)
	target, err := a.targets.ByName(t.Context(), "do-syd")
	if err != nil {
		t.Fatal(err)
	}
	checks := infrastructureChecksFor(a)
	checks.Applied(target.ID)
	checks.Applied(target.ID)
	rows := operationJobs(t, a, "inventory")
	if len(rows) != 1 || rows[0].Key != "do-syd/act" {
		t.Fatalf("acts did not coalesce: %+v", rows)
	}
}

func TestInfrastructurePassContinuesAfterAdmissionFailures(t *testing.T) {
	t.Parallel()
	a, _, _, _ := doWorld(t)
	// Fault injection: reject inventory admissions only, so later kinds can
	// independently prove that the pass continued.
	completionExec(t, a.DB, `CREATE FUNCTION veto_inventory() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	IF NEW.args->>'operation' = 'inventory' THEN RAISE EXCEPTION 'inventory refused'; END IF;
	RETURN NEW; END $$`)
	completionExec(t, a.DB, `CREATE TRIGGER veto_inventory BEFORE INSERT ON river_job FOR EACH ROW EXECUTE FUNCTION veto_inventory()`)
	_, err := infrastructureChecksFor(a).Run(t.Context(), "", nil)
	if err == nil {
		t.Fatal("admission failures were lost")
	}
	targets, readErr := a.targets.All(t.Context())
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, target := range targets {
		if target.Status == deployment.TargetRetired || target.Kind == deployment.KindDockerHost {
			continue
		}
		if !strings.Contains(err.Error(), target.Name) {
			t.Errorf("error omitted target %s: %v", target.Name, err)
		}
	}
	if len(operationJobs(t, a, deployment.CapacityObservationJob)) != len(targets) || len(operationJobs(t, a, diskObservationJob)) != 1 {
		t.Fatal("inventory admission failure skipped remaining checks")
	}
}

func TestInventoryJobHasThirtySecondBudget(t *testing.T) {
	t.Parallel()
	a, _, _, _ := doWorld(t)
	if got := apptownJobKinds(a)["inventory"].Timeout; got != 30*time.Second {
		t.Fatalf("inventory budget = %s", got)
	}
}

func TestAnActQueuesDeploymentDetailAndProtectionWithoutThePeriodicPass(t *testing.T) {
	t.Parallel()
	a, _, p, _ := doWorld(t)
	target, err := a.targets.ByName(t.Context(), "do-syd")
	if err != nil {
		t.Fatal(err)
	}
	dep, err := a.deployments.EnsureStanding(t.Context(), p.ID, "cloud", target.ID, deployment.Attributes{Feed: "promotion", Audience: "team", DataClass: "production"})
	if err != nil {
		t.Fatal(err)
	}
	a.infrastructure.Applied(target.ID)
	key, err := a.Deployment.DetailObservationKey(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	rows := operationJobs(t, a, deployment.DetailObservationJob)
	if len(rows) != 1 || !strings.HasPrefix(rows[0].Key, key+"/") {
		t.Fatalf("post-act detail: %+v", rows)
	}
	if rows := operationJobs(t, a, "protection-observation"); len(rows) != 1 {
		t.Fatalf("post-act protection: %+v", rows)
	}
}

func TestOutboundChecksHaveTheirOwnBoundedQueue(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	kinds := apptownJobKinds(a)
	for _, name := range []string{"inventory", "deployment-detail-observation", "target-observation", "error-report-observation", "backup-store-observation", "preview-observation", "integration-health", "capacity-observation", "protection-observation", "worker-observation", "disk-observation"} {
		k, ok := kinds[name]
		if !ok {
			t.Fatalf("missing kind %s", name)
		}
		if k.Queue != "observations" || k.Timeout <= 0 || k.Timeout > 100*time.Second {
			t.Errorf("unbounded or shared check %s: %+v", name, k)
		}
		if name != "integration-health" && k.Timeout > 30*time.Second {
			t.Errorf("slow host check %s: %s", name, k.Timeout)
		}
	}
	if kinds["rebuild"].Queue == "observations" {
		t.Fatal("ordinary work shares checks' queue")
	}
}

// Docker's minute pass uses one revision-keyed job; other providers retain their
// five-minute inventories and all capacity jobs remain until their conversion.
func TestHostSnapshotPassKeepsOtherProviderRefreshes(t *testing.T) {
	t.Parallel()
	a, _, _, _ := doWorld(t)
	ctx := t.Context()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/endpoints":
			fmt.Fprint(w, `[{"Id":3,"Name":"docker","Type":1}]`)
		case "/api/endpoints/3":
			fmt.Fprint(w, `{"Id":3,"Name":"docker","Type":1}`)
		case "/api/endpoints/3/docker/info":
			fmt.Fprint(w, `{"ID":"engine-a"}`)
		case "/api/stacks":
			fmt.Fprint(w, `[]`)
		default:
			t.Errorf("unexpected Portainer request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	path := filepath.Join(a.cfg.dataDir, "secrets.env")
	secrets, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(secrets, []byte("\nPT_URL="+server.URL+"\nPT_KEY=test\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	testAccounts(t, a.DB, "snapshot-admin")
	testAdministrator(t, a, "snapshot-admin")
	person := access.WithCaller(ctx, access.Caller{Verified: true, Principal: access.Principal{ID: testPrincipalOf(t, a.DB, "snapshot-admin"), Kind: "user", Name: "snapshot-admin", Via: "session"}})
	pt, err := a.Deployment.AttachPortainerTarget(person, deployment.PortainerAttachment{Name: "portainer-host", URLFrom: "PT_URL", APIKeyFrom: "PT_KEY", Environment: "docker"})
	if err != nil {
		t.Fatal(err)
	}
	checks := infrastructureChecksFor(a)
	for range 2 {
		if _, err := checks.HostSnapshotPass(ctx, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	targets, err := a.targets.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshots := operationJobs(t, a, deployment.HostSnapshotJob)
	var dockerTargets int
	for _, target := range targets {
		if target.Kind != deployment.KindDockerHost || target.Status == deployment.TargetRetired {
			continue
		}
		dockerTargets++
		found := false
		for _, row := range snapshots {
			if row.Key == target.ID.String()+"/"+strconv.FormatInt(target.SettingsRevision, 10) {
				found = true
			}
		}
		if !found {
			t.Fatalf("no snapshot for %s", target.Name)
		}
		checks.Applied(target.ID)
		reading, err := a.Deployment.InventoryFor(ctx, target)
		if err != nil || !reading.Invalidated {
			t.Fatalf("act not marked: %+v %v", reading, err)
		}
	}
	if len(operationJobs(t, a, deployment.HostSnapshotJob)) != dockerTargets {
		t.Fatal("duplicate snapshot jobs")
	}
	for _, row := range snapshots {
		if result := runJobForTest(t, a, row.ID); result.State != "completed" {
			t.Fatalf("snapshot job: %+v", result)
		}
	}

	if _, err := checks.Run(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	rows := operationJobs(t, a, "inventory")
	for _, target := range targets {
		found := false
		for _, row := range rows {
			if row.Key == target.Name {
				found = true
			}
		}
		if found != (target.Kind != deployment.KindDockerHost && target.Status != deployment.TargetRetired) {
			t.Errorf("wrong inventory source for %s", target.Name)
		}
	}
	job := inventoryRefresh{targets: a.targets, providers: a.targetProviders, inventory: a.Deployment.Inventory, cache: a.inventory}
	if _, err := job.Run(ctx, pt.Name, nil); err != nil {
		t.Fatal(err)
	}
	reading, err := a.Deployment.InventoryFor(ctx, pt)
	if err != nil || reading.Unknown || reading.ObservedAt.IsZero() {
		t.Fatalf("Portainer not refreshed: %+v %v", reading, err)
	}
	if _, err := job.Run(ctx, "do-syd", nil); err != nil {
		t.Fatal(err)
	}
	do, err := a.targets.ByName(ctx, "do-syd")
	if err != nil {
		t.Fatal(err)
	}
	reading, err = a.Deployment.InventoryFor(ctx, do)
	if err != nil || reading.Unknown {
		t.Fatalf("DigitalOcean not refreshed: %+v %v", reading, err)
	}
	kinds := apptownJobKinds(a)
	if kinds[deployment.HostSnapshotJob].Queue != "observations" || kinds[deployment.HostSnapshotJob].Timeout != time.Minute {
		t.Fatal("snapshot queue/budget")
	}
	for _, schedule := range apptownJobSchedules() {
		if schedule.Operation == hostSnapshotPassJob {
			at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
			if schedule.When.Next(at).Sub(at) != time.Minute {
				t.Fatal("snapshot cadence")
			}
			return
		}
	}
	t.Fatal("snapshot schedule missing")
}

// A Try it preview recorded after a successful collection has no absence evidence.
func TestAttentionDoesNotReportNewPreviewMissingFromEarlierSnapshot(t *testing.T) {
	t.Parallel()
	daemon := dockerfake.New()
	a := testApp(t, Config{DockerClient: daemon})
	a.Deployment.SeedTargets(t.Context(), nil)
	projectID := createReadTestProject(t, a, "preview-inventory")
	target, err := a.targets.ByName(t.Context(), deployment.BuiltInTargetName)
	if err != nil {
		t.Fatal(err)
	}
	worker := deployment.HostSnapshotWorker{Snapshots: a.hostSnapshots, Targets: a.targets, Hosts: a.hosts, Providers: a.targetProviders, Inventory: a.Deployment.Inventory}
	key := target.ID.String() + "/" + strconv.FormatInt(target.SettingsRevision, 10)
	if _, err := worker.Run(t.Context(), key, nil); err != nil {
		t.Fatal(err)
	}
	source := &attention.Inventory{Targets: inventoryTargetOnly{target}, Records: a.Deployment.Inventory, Observer: attentionInventory{service: a.Deployment}}
	check := func() {
		t.Helper()
		got, err := source.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !got.Known || got.Stale || len(got.Items) != 0 {
			t.Fatalf("fresh preview inventory: %+v", got)
		}
	}
	check()
	change := idstest.N(7)
	app := docker.PreviewChangeName(change)
	daemon.SetRunning(app, true)
	if err := a.deployments.RecordPreview(t.Context(), projectID, change, target.ID, app, 40001, "http://preview", nil); err != nil {
		t.Fatal(err)
	}
	check()
	// Once collected, the running preview still raises no condition.
	if _, err := worker.Run(t.Context(), key, nil); err != nil {
		t.Fatal(err)
	}
	check()
}
