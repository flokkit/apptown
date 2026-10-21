package deployment

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/digitalocean"
	"flokkit.io/forge/internal/deploy/digitalocean/dofake"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/stock"
)

type doReleaseSource struct {
	project  ReleaseProject
	contract *appcontract.Contract
	revision string
}

func (s doReleaseSource) Project(context.Context, ids.ID) (ReleaseProject, error) {
	return s.project, nil
}
func (s doReleaseSource) Contract(context.Context, ReleaseProject, string) (*appcontract.Contract, string, error) {
	return s.contract, s.revision, nil
}

type doReleaseQueue struct{}

func (doReleaseQueue) EnqueueReleaseIn(context.Context, *database.Tx, ids.ID) error { return nil }

type doReleaseSecrets struct{}

func (doReleaseSecrets) ResolveRelease(context.Context, Application, string, string, *appcontract.Contract) (deploy.ConsumerSecrets, []string, error) {
	return secret.Delivery{}, nil, nil
}

func doReleaseWorld(t *testing.T) (*Service, *dofake.API, *database.DB, ids.ID, *Deployment, *ReleaseBundle) {
	t.Helper()
	return doReleaseWorldNeeding(t, "")
}

// doReleaseWorldNeeding is doReleaseWorld with more needs declared after postgres.
func doReleaseWorldNeeding(t *testing.T, more string) (*Service, *dofake.API, *database.DB, ids.ID, *Deployment, *ReleaseBundle) {
	t.Helper()
	db := testDB(t)
	if err := jobs.Migrate(context.Background(), db.Pool()); err != nil {
		t.Fatal(err)
	}
	pid := createTestProject(t, db, "expense", "Expense")
	dir := t.TempDir()
	repo := filepath.Join(dir, "workspaces", "project-expense")
	if err := os.MkdirAll(filepath.Join(repo, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	contractBytes := []byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n    health:\n      readiness: { http: /healthz }\ninit:\n  - name: migrate\n    build: .\n    command: [\"dotnet\", \"App.dll\", \"--init\"]\nneeds:\n  postgres:\n    version: \"16\"\n" + more)
	if err := os.WriteFile(filepath.Join(repo, ".apptown", "app.yaml"), contractBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "selected"}} {
		if _, err := releaseGit(t.Context(), repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	revision, err := releaseGit(t.Context(), repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	revision = strings.TrimSpace(revision)
	contract, err := appcontract.Parse(contractBytes)
	if err != nil {
		t.Fatal(err)
	}
	catalog := dependency.NewCatalog(db, nil)
	if _, _, err := catalog.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	api := &dofake.API{}
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	options := digitalocean.Options{
		PollInterval: 10 * time.Millisecond, DBPoll: 10 * time.Millisecond,
		PushImage: func(_ *digitalocean.Driver, _ context.Context, _ *digitalocean.Client, _, _, _, _ string, _ func(string, ...any)) (string, error) {
			return "sha256:" + strings.Repeat("a", 64), nil
		},
		OwnDatabase:    func(context.Context, string, string, string) error { return nil },
		ProbeFrontDoor: func(context.Context, string, time.Time, func(string, ...any)) error { return nil },
	}
	var service *Service
	service = New(context.Background(), db, assembled(db, testRuntime{DataDir: dir, Needs: catalog, Secrets: secret.NewService(db, secret.Config{DataDir: dir}), Drivers: map[string]DriverFactory{KindDOApp: func(context.Context, *Target, *Deployment) (deploy.Driver, error) {
		return digitalocean.NewDriver(digitalocean.Dependencies{Slug: "expense", Needs: service.Needs, Client: func(context.Context) (*digitalocean.Client, digitalocean.Config, error) {
			return &digitalocean.Client{Base: server.URL, HTTP: server.Client()}, digitalocean.Config{Region: "syd", Registry: "forge-test", DBSize: "db-s-1vcpu-1gb", InstanceSize: "basic-xxs"}, nil
		}}, options), nil
	}}}, Dependencies{Followups: recoveryJobFollowups{}, Releases: ReleaseDependencies{Authority: &requestAuthority{}, Sources: doReleaseSource{project: ReleaseProject{Application: Application{ID: pid, Slug: "expense"}, Repository: repo}, contract: contract, revision: revision}, Secrets: doReleaseSecrets{}, Queue: doReleaseQueue{}}}))
	target, err := service.Targets.Put(context.Background(), &Target{Name: "do-syd", Kind: KindDOApp, Origin: OriginAttached, Status: TargetReady, NeedsMode: "managed", SecretsDelivery: "provider", Capabilities: TargetCapabilities{Needs: []string{"postgres"}}})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := service.Deployments.EnsureStanding(t.Context(), pid, "cloud", target.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "non-production"})
	if err != nil {
		t.Fatal(err)
	}
	bid, err := service.Bundles.Cut(t.Context(), pid, revision, map[string]string{"web": "sha256:web"}, map[string]string{"migrate": "sha256:web"}, contractBytes, BundleOrigin{})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := service.Bundles.ByID(context.Background(), bid)
	if err != nil {
		t.Fatal(err)
	}
	return service, api, db, pid, dep, bundle
}

func doAcceptRelease(t *testing.T, service *Service, pid ids.ID, dep *Deployment, bundle *ReleaseBundle) AcceptedRelease {
	t.Helper()
	accepted, err := service.RequestRelease(context.Background(), DeploySelection{ProjectID: pid, DeploymentName: dep.Name, BundleID: bundle.ID})
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}

func TestDeploymentRecoveryMissingIdentityRemainsUncertain(t *testing.T) {
	t.Parallel()
	service, _, db, pid, dep, bundle := doReleaseWorld(t)
	started, err := service.Releases.Start(context.Background(), ReleaseDraft{Source: ReleaseFromBundle, ProjectID: pid, DeploymentID: dep.ID, Deployment: "cloud", Revision: bundle.ContractRevision, Actor: ""}, "prepared for settlement")
	if err != nil {
		t.Fatal(err)
	}
	attempt, _, err := service.Outcomes.Admit(context.Background(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: fmt.Sprintf("release-%d", started.ID), Inputs: Inputs{ReleaseID: started.ID, Revision: bundle.ContractRevision}})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := service.Recoverer().Run(context.Background(), attempt.ID); err == nil || result.Outcome != "uncertain" {
		t.Fatalf("missing identity: %+v %v", result, err)
	}
	dbtest.Count(t, db, 1, `SELECT COUNT(*) FROM deployment_attempts WHERE id=? AND status='uncertain' AND finished_at IS NULL`, attempt.ID)
	dbtest.Count(t, db, 1, `SELECT COUNT(*) FROM releases WHERE id=? AND state='releasing'`, started.ID)
}

func TestDeploymentAdmissionReplayDoesNotExecuteProviderAgain(t *testing.T) {
	t.Parallel()
	service, provider, db, pid, dep, bundle := doReleaseWorld(t)
	accepted := doAcceptRelease(t, service, pid, dep, bundle)
	if err := service.ExecuteRelease(context.Background(), accepted.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.Count(t, db, 1, `SELECT COUNT(*) FROM releases WHERE id=? AND state='released'`, accepted.ID)
	if err := service.ExecuteRelease(context.Background(), accepted.ID); err != nil {
		t.Fatal(err)
	}
	state := provider.Snapshot()
	if state.DeploymentGeneration != 1 {
		t.Fatalf("replayed admission executed provider %d times", state.DeploymentGeneration)
	}
}

func TestDeploymentProviderCheckpointFailureStopsBeforeObservation(t *testing.T) {
	t.Parallel()
	service, provider, db, pid, dep, bundle := doReleaseWorld(t)
	accepted := doAcceptRelease(t, service, pid, dep, bundle)
	dbtest.FailNextStatement(t, db, "UPDATE deployment_attempts SET observed_json")
	if err := service.ExecuteRelease(context.Background(), accepted.ID); err == nil || !strings.Contains(err.Error(), "injected statement failure") {
		t.Fatalf("checkpoint failure: %v", err)
	}
	dbtest.Count(t, db, 1, `SELECT COUNT(*) FROM deployment_attempts WHERE deployment_id=? AND status='uncertain' AND NOT jsonb_exists(observed_json,'appId') AND NOT jsonb_exists(observed_json,'deploymentId')`, dep.ID)
	dbtest.Count(t, db, 1, `SELECT COUNT(*) FROM releases WHERE id=? AND state='releasing'`, accepted.ID)
	dbtest.Count(t, db, 1, `SELECT COUNT(*) FROM river_job WHERE (args->>'operation')=?`, RecoveryJob)
	state := provider.Snapshot()
	if state.DeploymentGeneration != 1 || state.DeploymentReads != 0 {
		t.Fatalf("checkpoint failure continued execution: created=%d observed=%d", state.DeploymentGeneration, state.DeploymentReads)
	}
}

func TestDigitalOceanRefusesSomebodyElsesCluster(t *testing.T) {
	t.Parallel()
	service, provider, _, pid, dep, bundle := doReleaseWorld(t)
	provider.SeedDatabase(map[string]any{"id": "db-x", "name": "apptown-expense-cloud", "engine": "pg", "version": "16", "region": "syd1", "size": "db-s-1vcpu-1gb", "status": "online", "tags": []string{"someone-elses"}})
	accepted := doAcceptRelease(t, service, pid, dep, bundle)
	_ = service.ExecuteRelease(context.Background(), accepted.ID)
	release, err := service.Releases.ByNumber(context.Background(), pid, accepted.Number)
	if err != nil {
		t.Fatal(err)
	}
	events, err := service.Releases.Events(context.Background(), release.ID)
	if err != nil || len(events) == 0 {
		t.Fatal(err)
	}
	last := events[len(events)-1].Text
	state := provider.Snapshot()
	if release.State != ReleaseFailed || !strings.Contains(last, "not this deployment's") || len(state.Apps) != 0 {
		t.Fatalf("a cluster without AppTown's marker is refused, not adopted: %s %q apps=%d", release.State, last, len(state.Apps))
	}
}

func TestDeploymentReleaseAdmissionIncludesItsChanges(t *testing.T) {
	t.Parallel()
	service, provider, db, pid, dep, bundle := doReleaseWorld(t)
	mustExec(t, db, `CREATE FUNCTION reject_release_membership() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'admission membership rejected'; END $$`)
	mustExec(t, db, `CREATE TRIGGER reject_release_membership BEFORE INSERT ON release_changes FOR EACH STATEMENT EXECUTE FUNCTION reject_release_membership()`)
	if _, err := service.RequestRelease(context.Background(), DeploySelection{ProjectID: pid, DeploymentName: dep.Name, BundleID: bundle.ID}); err == nil || !strings.Contains(err.Error(), "admission membership rejected") {
		t.Fatalf("release admission: %v", err)
	}
	dbtest.Count(t, db, 0, `SELECT COUNT(*) FROM releases`)
	dbtest.Count(t, db, 0, `SELECT COUNT(*) FROM deployment_attempts`)
	if provider.Snapshot().DeploymentGeneration != 0 {
		t.Fatal("release execution began before admission committed")
	}
}
