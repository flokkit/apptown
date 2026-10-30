package apptown

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/dbschema"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/console/consoletest"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/riverqueue/river/rivertype"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/gitexec"
)

// The one boot sequence, pinned per path through the recording fake: needs
// (healthy) → init → non-public parts → the public app → probe, with the
// labels and restart policy each target carries. Every path in this file
// went through bootStack; the assertions are on the docker argv it issued.

// bootFixtureYAML: a need (postgres, with a healthcheck), an init step, a
// non-public worker and a public web part with a data dir.
const bootFixtureYAML = `app:
  web:
    image: reg.example/app:1
    ports: ["8080"]
    public: true
    dataDir: /data
  worker:
    image: reg.example/worker:1
    command: ["node", "worker.js"]
init:
  - name: migrate
    image: reg.example/app:1
    command: ["./migrate.sh"]
needs:
  postgres: {}
`

func writeBootFixture(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".apptown", "app.yaml"), []byte(bootFixtureYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	// An unrelated Dockerfile must not override an image declaration.
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bootWorld: an AppTown with the stock dependency catalog, a fake daemon whose
// mapped port is a real HTTP server (the probe must get an answer), and no
// probe settle.
func bootWorld(t *testing.T, configs ...Config) (*App, *fakeDocker) {
	t.Helper()
	cfg := Config{}
	if len(configs) != 0 {
		cfg = configs[0]
	}
	f := newFakeDocker()
	cfg.DockerClient = f
	cfg.DockerProbe = &docker.Probe{Settle: time.Millisecond}
	a := testApp(t, cfg)
	a.SyncStockDependencies(context.Background())
	if id := createReadTestProject(t, a, "p"); id != idstest.N(1) {
		t.Fatalf("project fixture ID = %d, want 1", id)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body><script>/* app */</script></body></html>"))
	}))
	t.Cleanup(srv.Close)
	_, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	f.port, _ = strconv.Atoi(portStr)
	return a, f
}

// ran is one `docker run` the fake saw.
type ran struct {
	name, class, owner string
	restart, rm        bool
	call               string
}

func containersRun(f *fakeDocker) []ran {
	var out []ran
	for _, c := range f.joined() {
		if !strings.HasPrefix(c, "run ") {
			continue
		}
		out = append(out, ran{
			name: containerNameOf(c), class: labelOf(c, "apptown.class"), owner: labelOf(c, "apptown.owner"),
			restart: strings.Contains(c, "--restart unless-stopped"), rm: hasArg(c, "--rm"), call: c,
		})
	}
	return out
}

// expectBootSequence pins the order and the per-target labels/policy for a
// stack owned by stackID whose public app is appName.
func expectBootSequence(t *testing.T, f *fakeDocker, stackID, appName string, persistent bool) []ran {
	t.Helper()
	runs := containersRun(f)
	names := make([]string, len(runs))
	for i, r := range runs {
		names[i] = r.name
	}
	want := []string{docker.Current.Service(stackID, "postgres"), docker.Current.Init(stackID, "migrate"), docker.Current.Service(stackID, "worker"), appName}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("boot order: got %v, want %v\ncalls:\n%s", names, want, strings.Join(f.joined(), "\n"))
	}
	pg, init, worker, app := runs[0], runs[1], runs[2], runs[3]

	// The network exists before anything runs on it.
	ni := -1 // a throwaway stack's network carries labels before its name
	for i, c := range f.joined() {
		if strings.HasPrefix(c, "network create ") && strings.HasSuffix(c, " "+docker.Current.Network(stackID)) {
			ni = i
			break
		}
	}
	if ri := f.indexOf("run "); ni < 0 || ni > ri {
		t.Fatalf("the stack network must be created before the first container; calls:\n%s", strings.Join(f.joined(), "\n"))
	}
	// needs are WAITED healthy before init runs.
	hi := f.indexOf("inspect -f {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}} " + pg.name)
	ii := f.indexOf("run --rm --init --name " + init.name)
	if hi < 0 || ii < 0 || hi > ii {
		t.Fatalf("init must wait for the need's health (health inspect at %d, init at %d)", hi, ii)
	}
	// init: run to completion on the stack network, class step, owner = the stack.
	if init.class != "step" || init.owner != stackID || !init.rm || !hasArg(init.call, "--network") {
		t.Fatalf("init container: %+v", init)
	}
	// The need is started with the password made for this deployment or
	// preview, never the catalogue's, and init and the app are given it.
	password := ""
	for _, f := range strings.Fields(pg.call) {
		if v, ok := strings.CutPrefix(f, "POSTGRES_PASSWORD="); ok {
			password = v
		}
	}
	if len(password) != 64 {
		t.Fatalf("the need's password is not one AppTown made: %q in %s", password, pg.call)
	}
	url := "-e DATABASE_URL=postgres://app:" + password + "@postgres:5432/app"
	if !strings.Contains(init.call, url) {
		t.Fatalf("init must see the need's connection env: %s", init.call)
	}
	// the non-public part: a service on the stack, with the merged env.
	// A standing deployment's own parts live with it: class env, restart
	// unless-stopped, never --rm (finding 6); a preview's are throwaway.
	wantClass := "service"
	if persistent {
		wantClass = "env"
	}
	if worker.class != wantClass || worker.owner != stackID || !hasArg(worker.call, "--network-alias") ||
		!strings.Contains(worker.call, "-e DATABASE_URL=") || worker.restart != persistent || worker.rm == persistent {
		t.Fatalf("worker part: %+v", worker)
	}
	// the public app: env merged, data volume, port published, class + policy per target.
	if !strings.Contains(app.call, url) ||
		!strings.Contains(app.call, "-v "+docker.Current.DataVolume(appName)+":/data") ||
		!strings.Contains(app.call, "-e DATA_DIR=/data") || !strings.Contains(app.call, "-e PORT=8080") ||
		app.owner != appName || app.rm {
		t.Fatalf("app container: %+v", app)
	}
	if persistent {
		if pg.class != "env" || pg.owner != stackID || !pg.restart || pg.rm {
			t.Fatalf("a standing deployment's need must be class env, restart unless-stopped, not --rm: %+v", pg)
		}
		if app.class != "env" || !app.restart {
			t.Fatalf("a standing deployment's app must be class env with restart unless-stopped: %+v", app)
		}
	} else {
		if pg.class != "service" || pg.owner != stackID || pg.restart || !pg.rm || !hasArg(pg.call, "--init") ||
			!strings.Contains(pg.call, "--health-cmd test -f /tmp/forge-ready && pg_isready") {
			t.Fatalf("a preview's need must be a throwaway --rm --init service with its healthcheck: %+v", pg)
		}
		if app.class != "preview" || app.restart || !strings.Contains(app.call, "-p 127.0.0.1::8080") {
			t.Fatalf("a preview's app must be class preview, no restart policy, forge-assigned port: %+v", app)
		}
	}
	return runs
}

func TestRunPreviewSequence(t *testing.T) {
	t.Parallel()
	a, f := bootWorld(t)
	run, stepID := bootRun(t, a, "working", "preview")
	runID := run.ID
	ws := requireLocation(t)(workflowSourceForTest(t, a, run).Workspace(a.cfg.dataDir))
	writeBootFixture(t, ws)
	commitFolder(t, ws) // a run's preview boots the run's commit

	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)

	step, err := workflow.ReadStepIn(context.Background(), a.DB, stepID)
	if err != nil || step.Status != workflow.StatusPass {
		t.Fatalf("preview step: %+v %v\ncalls:\n%s", step, err, strings.Join(f.joined(), "\n"))
	}
	runs := expectBootSequence(t, f, docker.RunStepStackID(stepID), docker.PreviewRunName(runID), false)
	if len(f.matching("build ")) != 0 || f.indexOf("pull reg.example/app:1") < 0 || !strings.HasSuffix(runs[3].call, " reg.example/app:1") {
		t.Fatalf("preview step must run its declared image: %s", runs[3].call)
	}
	artifacts, err := a.Work.RunArtifacts(asPerson("bls"), runID, workflow.ArtifactSelection{Kinds: []string{"preview"}})
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("the preview URL must be recorded as one artifact: %+v %v", artifacts, err)
	}
}

// Admit the declared effect, then pause automatic dispatch so each test can
// arrange its external provider and explicitly execute the selected step.
func bootRun(t *testing.T, a *App, role, effect string) (*workflow.Run, ids.ID) {
	t.Helper()
	return bootStep(t, a, role, schema.Step{Name: effect, Uses: effect})
}
func bootStep(t *testing.T, a *App, role string, step schema.Step) (*workflow.Run, ids.ID) {
	t.Helper()
	effect := step.Uses
	installFakeGit(t, a, nil)
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "boot-" + effect, Version: 1}, Spec: schema.Spec{
		Kind: "pipeline", Role: role, Stages: []schema.Stage{{Name: "boot", Steps: []schema.Step{step}}},
	}})
	a.Work.Stop()
	changeID := createReadTestChange(t, a, idstest.N(1), "boot "+effect)
	run, err := a.Work.StartRun(asPerson("bls"), workflow.StartChangeRun{ChangeID: changeID, Workflow: "boot-" + effect})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := a.Work.Snapshot(asPerson("bls"), run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range snapshot.Steps {
		if step.Type == effect {
			return run, step.ID
		}
	}
	t.Fatalf("declared %s step was not materialized: %+v", effect, snapshot.Steps)
	return nil, ids.Nil
}

func deployRun(t *testing.T, a *App) (runID, stepID ids.ID) {
	t.Helper()
	a.SeedTargets(t.Context()) // The daemon establishes target bindings before workflow dispatch.
	run, stepID := bootRun(t, a, "landing", "deploy")
	runID = run.ID
	ws := requireLocation(t)(workflowSourceForTest(t, a, run).Workspace(a.cfg.dataDir))
	writeBootFixture(t, ws)
	// A standing deployment identifies committed source before contacting Docker.
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "deployment fixture"}} {
		if out, err := gitexec.Run(t.Context(), ws, args...); err != nil {
			t.Fatalf("git fixture: %v: %s", err, out)
		}
	}
	return runID, stepID
}

// mainNames are project p's main deployment's names on its host, from its
// record: by its key.
func mainNames(t *testing.T, a *App) docker.Names {
	t.Helper()
	env, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil {
		t.Fatal(err)
	}
	names, err := env.HostNames()
	if err != nil || !strings.HasPrefix(names.Stack(), "dep-") {
		t.Fatalf("p/main's names: %+v %v", names, err)
	}
	return names
}

// standingMain makes p's main deployment on the built-in host before its
// first release, and answers its names.
func standingMain(t *testing.T, a *App) docker.Names {
	t.Helper()
	target, err := a.Deployment.Targets.ByName(t.Context(), deployment.BuiltInTargetName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "main", target.ID, deployment.Attributes{}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	return mainNames(t, a)
}

func TestRunDeploySequence(t *testing.T) {
	t.Parallel()
	a, f := bootWorld(t)
	runID, stepID := deployRun(t, a)

	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)

	step, err := workflow.ReadStepIn(context.Background(), a.DB, stepID)
	if err != nil || step.Status != workflow.StatusPass {
		t.Fatalf("deploy step: %+v %v\ncalls:\n%s", step, err, strings.Join(f.joined(), "\n"))
	}
	names := mainNames(t, a)
	runs := expectBootSequence(t, f, names.Stack(), names.App(), true)
	env, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil || env.Container != names.App() || env.ReleaseRun != runID || env.HostPort == 0 {
		t.Fatalf("environment record: %+v %v", env, err)
	}
	if !strings.Contains(runs[3].call, "-p 127.0.0.1:"+strconv.Itoa(env.HostPort)+":8080") {
		t.Fatalf("a standing deployment publishes its stable port: %s", runs[3].call)
	}
	// Its app container says, for people, which project and deployment it
	// is for; previews do not carry these.
	p, err := a.Projects.ByID(t.Context(), idstest.N(1))
	if err != nil {
		t.Fatal(err)
	}
	if info := strings.Join(dockerlabel.Informational(p.Slug, "main"), " "); !strings.Contains(runs[3].call, info) {
		t.Fatalf("the app container does not say whose it is (%s): %s", info, runs[3].call)
	}
}

func TestRunDeployReusesStandingNeeds(t *testing.T) {
	t.Parallel()
	a, f := bootWorld(t)
	_, stepID := deployRun(t, a)
	names := standingMain(t, a)
	pg := names.Service("postgres")
	kind, err := a.Dependencies.ByCapability(t.Context(), "postgres")
	if err != nil {
		t.Fatal(err)
	}
	local := kind.ResolveLocal(nil, "", nil)
	f.markStartedAs(pg, local.Spec())
	// The previous engine actually has its declared data volume and mount.
	volume := names.NeedVolume("postgres")
	if _, err := f.Run(t.Context(), "", docker.Command{Args: []string{"volume", "create", volume}}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if f.storageContainers == nil {
		f.storageContainers = map[string]map[string]any{}
	}
	f.storageContainers[pg] = dockerfake.StorageContainer([]string{"run", "--name", pg, "-v", volume + ":" + local.Data, local.Image})
	f.mu.Unlock()

	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)

	step, err := workflow.ReadStepIn(context.Background(), a.DB, stepID)
	if err != nil || step.Status != workflow.StatusPass {
		t.Fatalf("deploy step: %+v %v\ncalls:\n%s", step, err, strings.Join(f.joined(), "\n"))
	}
	for _, r := range containersRun(f) {
		if r.name == pg {
			t.Fatalf("a standing need must be reused, never restarted (its data is the persistence): %s", r.call)
		}
	}
	// ...but still waited healthy before the init step runs against it.
	hi := f.indexOf("inspect -f {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}} " + pg)
	ii := f.indexOf("run --rm --init --name " + names.Init("migrate"))
	if hi < 0 || ii < 0 || hi > ii {
		t.Fatalf("init must wait for the reused need's health (inspect at %d, init at %d)", hi, ii)
	}
}

func TestDeploymentRebuildCarriesNeedsInitAndParts(t *testing.T) {
	t.Parallel()
	a, f := bootWorld(t)
	runID, stepID := deployRun(t, a)
	run, err := workflow.ReadRunIn(context.Background(), a.DB, runID)
	if err != nil {
		t.Fatal(err)
	}
	ws := requireLocation(t)(workflowSourceForTest(t, a, run).Workspace(a.cfg.dataDir))
	if _, err := gitexec.Run(t.Context(), ws, "clone", "--no-hardlinks", ws, a.projectRepoPath("p")); err != nil {
		t.Fatal(err)
	}
	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
	env, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil || env.ObservedGeneration != 1 {
		t.Fatalf("initial deployment generation: %+v %v", env, err)
	}
	reservedPort := env.HostPort
	f.reset()
	f.mu.Lock()
	f.started = map[string]bool{}
	// Container/network loss retains the database and app data. Missing bound
	// volumes are refused (TestStorageAdmissionRequiresObservedEngineAndIncarnation).
	for object := range f.objects {
		if !strings.HasPrefix(object, "volume ") {
			delete(f.objects, object)
		}
	}
	f.labels = map[string]map[string]string{}
	f.mu.Unlock()

	a.ensureDeploymentsOnce(context.Background()) // the container is not running: a rebuild job is asked for
	runQueuedJobForTest(t, a, "rebuild", idstest.N(1).String()+"/main")

	names := mainNames(t, a)
	runs := expectBootSequence(t, f, names.Stack(), names.App(), true)
	if !strings.Contains(runs[3].call, "-p 127.0.0.1:"+strconv.Itoa(reservedPort)+":8080") {
		t.Fatalf("a rebuilt environment keeps its stable port: %s", runs[3].call)
	}
	if audits := auditCount(t, a.DB, "deployment.rebuilt"); audits != 1 {
		t.Fatalf("the rebuild must be on the audit record, got %d", audits)
	}
	// A standing deployment is left standing.
	f.reset()
	a.ensureDeploymentsOnce(context.Background())
	requireJobs(t, a, 0, func(j *rivertype.JobRow, r jobs.Request) bool {
		return r.Operation == "rebuild" && slices.Contains([]rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled}, j.State)
	})
}

// Complaint #77: startup recovery rebuilt a change's preview from the
// project repo (main), and the reuse check then handed that container back
// as the change's preview. Recovery leaves previews cold.
func TestRecoveryLeavesPreviewsCold(t *testing.T) {
	t.Parallel()
	a, f := bootWorld(t)
	writeBootFixture(t, a.projectRepoPath("p"))
	if err := a.Deployment.Deployments.RecordPreview(context.Background(), idstest.N(1), idstest.N(235), ids.Nil, docker.PreviewChangeName(idstest.N(235)), 0, ""); err != nil {
		t.Fatal(err)
	}
	a.ensureDeploymentsOnce(context.Background())
	if got := containersRun(f); len(got) != 0 {
		t.Fatalf("a preview is never rebuilt from the project repo; ran %v", got)
	}
	previews, err := a.Deployment.Deployments.Previews(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(previews, func(d *deployment.Deployment) bool { return d.ChangeID == idstest.N(235) }) {
		t.Fatal("a preview whose container is gone is forgotten, not left reading missing")
	}
}

// A preview's row goes when Preview stops it for want of a look, and the
// standing-deployment watchers (health, rebuild, backups) never see previews.
func TestAStoppedPreviewLosesItsRow(t *testing.T) {
	t.Parallel()
	a, f := bootWorld(t)
	name := docker.PreviewChangeName(idstest.N(41))
	if err := a.Deployment.Deployments.RecordPreview(context.Background(), idstest.N(1), idstest.N(41), ids.Nil, name, 0, ""); err != nil {
		t.Fatal(err)
	}
	for _, e := range a.hosts.Watched(context.Background(), a.deployments) {
		if e.Name == "preview-41" {
			t.Fatal("a preview is not a standing deployment")
		}
	}
	f.reply = func(args []string) (string, error, bool) {
		if args[0] == "ps" && slices.Contains(args, "label=apptown.class=preview") {
			return name + "\t" + docker.ChangeStackID(idstest.N(41)) + "\n", nil, true
		}
		return "", nil, false
	}
	a.Preview.Tend(context.Background(), false)
	if named, err := a.Deployment.Deployments.Named(t.Context(), "preview-41"); err != nil || len(named) != 0 {
		t.Fatal("the stopped preview's row must go with its container")
	}
}

// The data directory says which layout wrote it: a new one is marked with
// this build's layout, the same layout is accepted, and an older or a newer
// build's directory is refused by name rather than misread.
func TestTheDataDirectorySaysWhichLayoutWroteIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := PrepareDataLayout(dir); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "layout")); err != nil || strings.TrimSpace(string(raw)) != strconv.Itoa(DataLayoutVersion) {
		t.Fatalf("a new directory was not marked: %q %v", raw, err)
	}
	if err := PrepareDataLayout(dir); err != nil {
		t.Fatalf("the same layout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "layout"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareDataLayout(dir); err == nil || !strings.Contains(err.Error(), "1.0.0-rc.9") {
		t.Fatalf("an older build's directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "layout"), []byte(strconv.Itoa(DataLayoutVersion+1)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareDataLayout(dir); !errors.Is(err, ErrNewerDataLayout) {
		t.Fatalf("a newer build's directory: %v", err)
	}
}

// updateUpgradeFixture freezes AppTown this build makes as the named
// release's upgrade fixture (testdata/upgrade/<release>): run at a release's
// tag, once, and commit the result.
var updateUpgradeFixture = flag.String("update-upgrade-fixture", "", "write the upgrade fixture for this release name from the current build")

// upgradeSubjects names the records the upgrade reads: what the fixture made.
// Pack is the team's own pack, which fixtures frozen from rc.1 on hold; the
// 1.0 baseline was frozen before it and names none, and is read as frozen.
type upgradeSubjects struct {
	Change, Run, Accept ids.ID
	Pack                string `json:",omitempty"`
}

type upgradeFixture struct {
	Subjects upgradeSubjects `json:"subjects"`
	Reads    map[string]any  `json:"reads"`
}

// upgradeConfig is the configuration the fixture AppTown and the upgraded one
// run with: a chat can be opened, and one is.
func upgradeConfig(t *testing.T, dataDir string, agent *consoletest.Agent) Config {
	return Config{DataDir: dataDir, DockerClient: newFakeDocker(), ConsoleCLI: fixtureCLI(t), ConsoleAgent: agent,
		InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}}
}

// upgradeApprovalMeans is what the fixture's approval says assent means.
const upgradeApprovalMeans = "the change does what its title says"

// upgradeWorld makes, through the product, AppTown a release leaves
// behind: a project with a repository; a change on it with its own branch
// and folder, its run and a person's acceptance of the branch's commit, an
// approval whose stated meaning is recorded with the vote; adopted ways of
// working with a guidance block, and the team's own pack pinning it; a
// release of the repository's main; an ended chat with its transcript.
func upgradeWorld(t *testing.T) (*App, upgradeSubjects) {
	t.Helper()
	agent := consoletest.NewAgent()
	a := authzActors(t, upgradeConfig(t, t.TempDir(), agent))
	authzWorkflow(t, a, workflow.ApprovalConfig{Means: upgradeApprovalMeans})
	configureConsoleModel(t, a)
	rebuildWork(t, a, func(deps *workflow.Dependencies) { deps.Git = workflow.GitWorkspace{DataDir: a.cfg.dataDir} })
	h := a.Handler()
	call := func(who, method, path, body string, want int) []byte {
		t.Helper()
		w := authzCall(h, who, method, path, body)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	call("owner", "POST", "/api/v1/projects/game/repo", `{}`, 200)
	var made struct{ Change struct{ ID ids.ID } }
	if err := json.Unmarshal(call("owner", "POST", "/api/v1/projects/game/changes", `{"type":"change","title":"Make the duck faster"}`, 201), &made); err != nil {
		t.Fatal(err)
	}
	run, err := a.Work.StartRun(asPerson("owner"), workflow.StartChangeRun{ChangeID: made.Change.ID, Workflow: "implement"})
	if err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	snapshot, err := a.Work.Snapshot(asPerson("owner"), run.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 2 || snapshot.Steps[0].Name != "accept" {
		t.Fatalf("the fixture's run: %+v %v", snapshot.Steps, err)
	}
	accept := snapshot.Steps[0].ID
	// The person is shown the approval's question, what the change adds
	// among it, and accepts what they were shown.
	question, err := a.Work.ApprovalQuestion(asPerson("owner"), accept)
	if err != nil || question.Adds == nil || question.Adds.Digest == "" {
		t.Fatalf("the acceptance's question: %+v %v", question, err)
	}
	call("owner", "POST", "/api/v1/steps/"+accept.String()+"/approve", fmt.Sprintf(`{"decision":"approve","adds":%q}`, question.Adds.Digest), 200)
	adopt(t, a, idstest.N(1), "upgrade-guidance", 1, "[]")
	// The team's own way of working: a pack of its own, pinning that block,
	// which the project adopts.
	call("boss", "POST", "/api/v1/packs", `{"name":"upgrade-team","description":"How the team works","guidance":["upgrade-guidance"]}`, 200)
	var offer struct {
		ExpectedRevision int64            `json:"expectedRevision"`
		Packs            []map[string]any `json:"packs"`
	}
	if err := json.Unmarshal(call("owner", "POST", "/api/v1/projects/game/adoption/prepare", `{"packs":[{"pack":"upgrade-team","version":1}]}`, 200), &offer); err != nil {
		t.Fatal(err)
	}
	adoptBody, _ := json.Marshal(map[string]any{"expectedRevision": offer.ExpectedRevision, "packs": offer.Packs, "viaChange": false})
	call("boss", "POST", "/api/v1/projects/game/adoption/adopt", string(adoptBody), 200)
	main, err := gitexec.ResolveCommit(t.Context(), a.projectRepoPath("game"), "main")
	if err != nil {
		t.Fatal(err)
	}
	releasedPreviewFixture(t, a, idstest.N(1), main)
	var opened struct{ Session struct{ ID string } }
	if err := json.Unmarshal(call("owner", "POST", "/api/v1/projects/game/console", `{"model":"fake/model"}`, 201), &opened); err != nil {
		t.Fatal(err)
	}
	call("owner", "POST", "/api/v1/console/"+opened.Session.ID+"/say", `{"text":"what does it do"}`, 200)
	agent.Reply(opened.Session.ID, "ok")
	call("owner", "DELETE", "/api/v1/console/"+opened.Session.ID, "", 200)
	a.Work.Wait()
	return a, upgradeSubjects{Change: made.Change.ID, Run: run.ID, Accept: accept, Pack: "upgrade-team"}
}

// upgradeReads is what the fixture's records say, read through the router as
// people read them, kept to the recorded facts: identities, numbers, names,
// links between records, decisions, meanings and words, not what the page
// works out at read time. The release's commit is resolved in the project's
// repository and a file read from it, so the repository's bytes are part of
// what must survive.
func upgradeReads(t *testing.T, a *App, s upgradeSubjects) map[string]any {
	t.Helper()
	h := a.Handler()
	get := func(path string) map[string]any {
		t.Helper()
		w := authzCall(h, "boss", "GET", path, "")
		if w.Code != 200 {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		var v any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if list, ok := v.([]any); ok {
			return map[string]any{"list": list}
		}
		return v.(map[string]any)
	}
	pick := func(m any, keys ...string) map[string]any {
		out := map[string]any{}
		if mm, ok := m.(map[string]any); ok {
			for _, k := range keys {
				out[k] = mm[k]
			}
		}
		return out
	}
	change := get("/api/v1/changes/" + s.Change.String())
	detail := change["detail"].(map[string]any)
	var approval map[string]any
	for _, step := range detail["steps"].([]any) {
		if st := step.(map[string]any); st["id"] == s.Accept.String() {
			approval = pick(st, "id", "runId", "name", "type", "status", "config", "evidence")
		}
	}
	var releases []any
	for _, r := range get("/api/v1/projects/game/releases")["list"].([]any) {
		releases = append(releases, pick(r, "id", "number", "state", "deployment", "deploymentId", "commitSha", "externalRef", "approvedBy", "generation"))
	}
	repository := map[string]any{}
	if len(releases) > 0 {
		sha, _ := releases[0].(map[string]any)["commitSha"].(string)
		resolved, err := gitexec.ResolveCommit(t.Context(), a.projectRepoPath("game"), sha)
		if err != nil {
			t.Fatalf("the release's commit in the project's repository: %v", err)
		}
		file, err := gitexec.Output(t.Context(), a.projectRepoPath("game"), "show", resolved+":.gitignore")
		if err != nil {
			t.Fatalf("a file of the release's commit: %v", err)
		}
		repository = map[string]any{"commit": resolved, ".gitignore": file}
	}
	// The commit the person accepted, resolved in the change's own folder.
	workspace := map[string]any{}
	if ev, ok := approval["evidence"].(map[string]any); ok {
		candidate, _ := ev["candidate"].(string)
		source, err := workflow.ReadSourceIn(t.Context(), a.DB, workflow.RunRef{ID: s.Run, ProjectID: idstest.N(1)})
		if err != nil {
			t.Fatal(err)
		}
		folder := requireLocation(t)(source.Workspace(a.cfg.dataDir))
		resolved, err := gitexec.ResolveCommit(t.Context(), folder, candidate)
		if err != nil {
			t.Fatalf("the accepted commit %q in the change's folder: %v", candidate, err)
		}
		file, err := gitexec.Output(t.Context(), folder, "show", resolved+":.gitignore")
		if err != nil {
			t.Fatalf("a file of the accepted commit: %v", err)
		}
		workspace = map[string]any{"candidate": resolved, ".gitignore": file}
		if s.Pack != "" {
			// The branch is a name AppTown gave outside itself: an upgrade
			// keeps it.
			workspace["branch"] = source.Branch
		}
	}
	adoption := get("/api/v1/projects/game/adoption")
	var sessions []any
	for _, sess := range get("/api/v1/projects/game/transcript")["sessions"].([]any) {
		var words []any
		for _, m := range sess.(map[string]any)["messages"].([]any) {
			words = append(words, pick(m, "role", "text"))
		}
		p := pick(sess, "id", "actor", "ended")
		p["messages"] = words
		sessions = append(sessions, p)
	}
	reads := map[string]any{
		"change":     pick(change["change"], "id", "number", "title", "type", "openedBy", "openedByPrincipal", "projectId", "runId"),
		"run":        pick(detail["run"], "id", "number", "projectId", "workflowName", "workflowVersion", "kind", "changeId", "adoptionId"),
		"approval":   approval,
		"adoption":   pick(adoption["current"], "id", "revision", "kind", "reason", "digest", "createdBy", "receipt"),
		"releases":   releases,
		"repository": repository,
		"workspace":  workspace,
		"transcript": sessions,
	}
	if s.Pack != "" {
		pack := get("/api/v1/packs/" + s.Pack)
		// Where each version came from is recorded from schema 168 on: a
		// release before it had no such fields, and the upgrade records the
		// team's own pack as this AppTown's, unsigned. The rest reads as it did.
		versions, _ := pack["versions"].([]any)
		for _, v := range versions {
			if m, ok := v.(map[string]any); ok {
				if m["source"] != "here" || m["signer"] != "" {
					t.Errorf("after the upgrade, %s v%v came from %v signed %q, not here", s.Pack, m["version"], m["source"], m["signer"])
				}
				delete(m, "source")
				delete(m, "signer")
			}
		}
		reads["pack"] = map[string]any{"pack": pick(pack, "name", "description", "origin", "declaration", "recommended", "latest"), "versions": versions}
	}
	return reads
}

// writeTarGz packs dir's files (directories, regular files, links) into path.
func writeTarGz(t *testing.T, dir, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	err = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		} else if !info.Mode().IsRegular() && !info.IsDir() {
			return nil // sockets and pipes are not records
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name, hdr.ModTime, hdr.Uname, hdr.Gname, hdr.Uid, hdr.Gid = filepath.ToSlash(rel), time.Unix(0, 0), "", "", 0, 0
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			src, err := os.Open(p)
			if err != nil {
				return err
			}
			defer src.Close()
			_, err = io.Copy(tw, src)
			return err
		}
		return nil
	})
	if err == nil {
		err = errors.Join(tw.Close(), gz.Close(), f.Close())
	}
	if err != nil {
		t.Fatal(err)
	}
}

// readTarGz unpacks path into dir.
func readTarGz(t *testing.T, path, dir string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, filepath.FromSlash(hdr.Name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, os.FileMode(hdr.Mode))
		case tar.TypeSymlink:
			err = os.Symlink(hdr.Linkname, target)
		case tar.TypeReg:
			var out *os.File
			if out, err = os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)); err == nil {
				_, err = io.Copy(out, tr)
				err = errors.Join(err, out.Close())
			}
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func writeUpgradeFixture(t *testing.T, dir string) {
	t.Helper()
	a, subjects := upgradeWorld(t)
	reads := upgradeReads(t, a, subjects)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	schemaSQL, err := os.ReadFile(filepath.Join("dbschema", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release-schema.sql"), schemaSQL, 0o644); err != nil {
		t.Fatal(err)
	}
	// River's tables are the job library's: job rows are not history, and a
	// restarted AppTown recreates its queue.
	rows, err := dbtest.Snapshot(t.Context(), a.DB, func(table string) bool { return strings.HasPrefix(table, "river_") })
	if err != nil {
		t.Fatal(err)
	}
	if err := dbtest.WriteRows(filepath.Join(dir, "rows.jsonl.gz"), rows); err != nil {
		t.Fatal(err)
	}
	// This generator created current-layout repositories directly through New;
	// freeze that known layout, rather than claiming a legacy migration ran.
	if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "layout"), []byte(strconv.Itoa(DataLayoutVersion)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTarGz(t, a.cfg.dataDir, filepath.Join(dir, "files.tar.gz"))
	raw, err := json.MarshalIndent(upgradeFixture{Subjects: subjects, Reads: reads}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reads.json"), append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// restoreRelease gives db and a fresh data directory AppTown the fixture's
// release left, as it left it: not yet upgraded.
func restoreRelease(t *testing.T, dir string, db *database.DB) string {
	t.Helper()
	schemaSQL, err := os.ReadFile(filepath.Join(dir, "release-schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := dbtest.ReadRows(filepath.Join(dir, "rows.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: restoring a release's storage is what no owner operation can do; the upgrade under test starts from it.
	if err := dbtest.Restore(t.Context(), db, string(schemaSQL), rows); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	readTarGz(t, filepath.Join(dir, "files.tar.gz"), dataDir)
	return dataDir
}

// upgradeAppTown is what apptownd does to an AppTown when it starts: the data
// directory's layout, then the normal open path's migrations.
func upgradeAppTown(t *testing.T, db *database.DB, dataDir string) {
	t.Helper()
	if err := PrepareDataLayout(dataDir); err != nil {
		t.Fatalf("the release's data directory: %v", err)
	}
	if _, err := dbschema.Migrate(t.Context(), db); err != nil {
		t.Fatalf("upgrading the release's database: %v", err)
	}
	if err := jobs.Migrate(t.Context(), db.Pool()); err != nil {
		t.Fatal(err)
	}
	if err := RelocateDataLayout(t.Context(), db, upgradeConfig(t, dataDir, consoletest.NewAgent()), dataDir); err != nil {
		t.Fatalf("starting on the release's data directory: %v", err)
	}
	if err := FinalizeDataLayout(dataDir); err != nil {
		t.Fatal(err)
	}
}

// apptownRecords is an AppTown's stored records (the job library's queue left
// out) and the digest of its data directory: what the copies taken before
// an upgrade hold.
func apptownRecords(t *testing.T, db *database.DB, dataDir string) (string, string) {
	t.Helper()
	rows, err := dbtest.Snapshot(t.Context(), db, func(table string) bool { return strings.HasPrefix(table, "river_") })
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	err = filepath.Walk(dataDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dataDir, p)
		fmt.Fprintf(digest, "%s %v\n", rel, info.Mode().Type())
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			fmt.Fprintln(digest, link)
			return err
		case info.Mode().IsRegular():
			body, err := os.ReadFile(p)
			digest.Write(body)
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), hex.EncodeToString(digest.Sum(nil))
}

// An AppTown an earlier release left behind, its database and its files,
// upgrades with this build's startup and keeps what it recorded: the same
// records read back through the router with the same identities, links and
// meaning, the release's commit still resolves in its repository, and the
// upgraded schema is a fresh one's. Rolling back is restoring the copies
// taken, with apptownd stopped, before the upgrade: after the new build has
// upgraded and written, the restored database and data directory are the
// copies exactly, with the new build's write gone.
//
// Kills: a migration that drops, renumbers or re-means a record or the link
// between two; a reader that no longer reads what an earlier release wrote;
// a lost repository; a schema step that a fresh database and an upgraded one
// take differently.
func TestAnUpgradeKeepsWhatThePreviousReleaseRecorded(t *testing.T) {
	t.Parallel()
	if *updateUpgradeFixture != "" {
		writeUpgradeFixture(t, filepath.Join("testdata", "upgrade", *updateUpgradeFixture))
	}
	fixtures, err := filepath.Glob(filepath.Join("testdata", "upgrade", "*", "reads.json"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no upgrade fixture (testdata/upgrade/<release>): %v", err)
	}
	fresh, err := dbtest.Shape(t.Context(), testDB(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, readsPath := range fixtures {
		dir := filepath.Dir(readsPath)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			raw, err := os.ReadFile(readsPath)
			if err != nil {
				t.Fatal(err)
			}
			var want upgradeFixture
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			// A receipt's single "pack" name is not read since projects take
			// several packs (migration 192): the stored receipt keeps it, the
			// read no longer shows it.
			if adoption, ok := want.Reads["adoption"].(map[string]any); ok {
				if receipt, ok := adoption["receipt"].(map[string]any); ok {
					delete(receipt, "pack")
				}
			}
			wantReads, _ := json.Marshal(want.Reads)
			db := testDB(t)
			dataDir := restoreRelease(t, dir, db)
			// A release also retains history written before its live-session
			// metadata contract. Such ended records need no checkout and are never moved.
			historyDir := filepath.Join(dataDir, "console", "01900000-0000-7000-8000-000000000001")
			if err := os.MkdirAll(historyDir, 0o700); err != nil {
				t.Fatal(err)
			}
			historyPath := filepath.Join(historyDir, "ended.json")
			history := []byte(`{"id":"01900000-0000-7000-8000-000000000001","scope":"project:retained","lastUsed":"2026-09-13T00:00:00Z","future":{"precise":1234567890123456789}}`)
			if err := os.WriteFile(historyPath, history, 0o600); err != nil {
				t.Fatal(err)
			}
			// The copies the upgrade guide has an operator take, apptownd stopped.
			copiedRows, copiedFiles := apptownRecords(t, db, dataDir)
			copyArchive := filepath.Join(t.TempDir(), "data-directory.tar.gz")
			writeTarGz(t, dataDir, copyArchive)

			upgradeAppTown(t, db, dataDir)
			if got, err := os.ReadFile(historyPath); err != nil || string(got) != string(history) {
				t.Fatalf("upgrade changed retained history: %q %v", got, err)
			}
			if shape, err := dbtest.Shape(t.Context(), db); err != nil || shape != fresh {
				t.Fatalf("the upgraded schema is not a fresh one's: %v\n%s", err, lineDiff(fresh, shape))
			}
			a := newApp(t, db, upgradeConfig(t, dataDir, consoletest.NewAgent()))
			gotReads, _ := json.Marshal(upgradeReads(t, a, want.Subjects))
			if string(gotReads) != string(wantReads) {
				t.Fatalf("an upgraded record reads differently:\nwant %s\n got %s", wantReads, gotReads)
			}
			// The new build's first write: a change, with its branch in the repository.
			if w := authzCall(a.Handler(), "owner", "POST", "/api/v1/projects/game/changes", `{"type":"feature","title":"after the upgrade"}`); w.Code != 201 {
				t.Fatalf("a write after the upgrade: %d %s", w.Code, w.Body.String())
			}
			if rows, files := apptownRecords(t, db, dataDir); rows == copiedRows || files == copiedFiles {
				t.Fatalf("the write after the upgrade changed nothing to roll back (rows changed %v, files changed %v)", rows != copiedRows, files != copiedFiles)
			}

			// Rollback: the upgraded AppTown stopped, the copies put back in its place.
			if err := a.ShutdownBackground(t.Context()); err != nil {
				t.Fatal(err)
			}
			copiedSchema, err := os.ReadFile(filepath.Join(dir, "release-schema.sql"))
			if err != nil {
				t.Fatal(err)
			}
			var copied []dbtest.Row
			if err := json.Unmarshal([]byte(copiedRows), &copied); err != nil {
				t.Fatal(err)
			}
			// sql-guarantee: the rollback under test is restoring a dump, which is storage, not an owner operation.
			if err := dbtest.Restore(t.Context(), db, string(copiedSchema), copied); err != nil {
				t.Fatal(err)
			}
			backDir := t.TempDir()
			readTarGz(t, copyArchive, backDir)
			if rows, files := apptownRecords(t, db, backDir); rows != copiedRows || files != copiedFiles {
				t.Fatalf("the rollback did not give back the copies (rows %v, files %v)", rows == copiedRows, files == copiedFiles)
			}
		})
	}
}

// lineDiff names the lines one text has and the other lacks.
func lineDiff(a, b string) string {
	in := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(s, "\n") {
			m[l] = true
		}
		return m
	}
	ma, mb := in(a), in(b)
	var out []string
	for l := range ma {
		if !mb[l] {
			out = append(out, "- "+l)
		}
	}
	for l := range mb {
		if !ma[l] {
			out = append(out, "+ "+l)
		}
	}
	slices.Sort(out)
	return strings.Join(out, "\n")
}

// A preview step and a landing's deploy whose migration fails printing its
// DATABASE_URL keep, as evidence, a failure that does not hold the password
// AppTown made for that preview or deployment.
func TestAFailedBootsEvidenceHoldsNoNeedCredential(t *testing.T) {
	t.Parallel()
	for _, effect := range []string{"preview", "deploy"} {
		t.Run(effect, func(t *testing.T) {
			t.Parallel()
			a, f := bootWorld(t)
			var stepID ids.ID
			if effect == "deploy" {
				_, stepID = deployRun(t, a)
			} else {
				var run *workflow.Run
				run, stepID = bootRun(t, a, "working", "preview")
				ws := requireLocation(t)(workflowSourceForTest(t, a, run).Workspace(a.cfg.dataDir))
				writeBootFixture(t, ws)
				commitFolder(t, ws)
			}
			var mu sync.Mutex
			password := ""
			f.reply = func(args []string) (string, error, bool) {
				mu.Lock()
				defer mu.Unlock()
				line := strings.Join(args, " ")
				if args[0] != "run" {
					return "", nil, false
				}
				for _, a := range args {
					if v, ok := strings.CutPrefix(a, "POSTGRES_PASSWORD="); ok {
						password = v
					}
				}
				if strings.Contains(line, "-migrate ") && password != "" {
					return "django.db.utils.OperationalError: connection to postgres://app:" + password + "@postgres:5432/app failed", errors.New("exit status 1"), true
				}
				return "", nil, false
			}
			a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
			step, err := workflow.ReadStepIn(context.Background(), a.DB, stepID)
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(password) != 64 {
				t.Fatalf("the need's password: %q", password)
			}
			evidence := string(step.Evidence)
			if step.Status == workflow.StatusPass || !strings.Contains(evidence, "OperationalError") {
				t.Fatalf("the failed migration is not the step's evidence: %s %s", step.Status, evidence)
			}
			if strings.Contains(evidence, password[:16]) {
				t.Fatalf("the evidence holds the password: %s", evidence)
			}
		})
	}
}

// builtinDBDaemon is a daemon with no AppTown-db container and, when label is
// not nil, an AppTown-data-forge-db volume carrying that installation label and
// binding to /old/data/pgdata.
func builtinDBDaemon(label *string) *dockerfake.Daemon {
	d := dockerfake.New()
	exists := label != nil
	owner := "th1s1nstall"
	if label != nil {
		owner = *label
	}
	device := "/old/data/pgdata"
	d.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 1 && args[0] == "volume" && args[1] == "create" {
			exists = true
			device = ""
			for _, a := range args {
				if strings.HasPrefix(a, "device=") {
					device = strings.TrimPrefix(a, "device=")
				}
			}
			return []byte(currentBuiltinDB.Volume), nil, true
		}
		if len(args) > 1 && args[0] == "volume" && args[1] == "inspect" {
			if !exists {
				return []byte("Error response from daemon: get " + currentBuiltinDB.Volume + ": no such volume"), errors.New("exit status 1"), true
			}
			v := builtinVolume{Name: currentBuiltinDB.Volume, Driver: "local", CreatedAt: time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC), Labels: map[string]string{dockerlabel.Instance: owner}}
			if device != "" {
				v.Options = map[string]string{"type": "none", "o": "bind", "device": device}
			}
			raw, _ := json.Marshal(v)
			return raw, nil, true
		}
		return nil, nil, false
	})
	return d
}

func ranContainer(d *dockerfake.Daemon) bool {
	return slices.ContainsFunc(d.Commands(), func(c []string) bool { return len(c) > 0 && c[0] == "run" })
}

func TestBuiltinDBRefusesAVolumeOfAnotherInstallation(t *testing.T) {
	for name, label := range map[string]string{"another installation's id": "0ther1nstall", "no label": ""} {
		t.Run(name, func(t *testing.T) {
			d := builtinDBDaemon(&label)
			waited := false
			_, err := ensureBuiltinDB(context.Background(), docker.Scope{Client: d}, t.TempDir(), "postgres://db", "pw", "th1s1nstall", currentBuiltinDB,
				func(context.Context, string) error { waited = true; return nil })
			if err == nil {
				t.Fatal("a volume that is not this installation's was used")
			}
			for _, want := range []string{currentBuiltinDB.Volume, "th1s1nstall", "-db-url"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not name %q: %v", want, err)
				}
			}
			if label == "" && !strings.Contains(err.Error(), "no matching installation label") {
				t.Errorf("the refusal does not identify the missing label: %v", err)
			}
			if label != "" && !strings.Contains(err.Error(), label) {
				t.Errorf("the refusal does not name the other installation %q: %v", label, err)
			}
			if strings.Contains(err.Error(), "docker volume rm") {
				t.Error("refusal recommends deleting retained data")
			}
			if ranContainer(d) || waited {
				t.Errorf("a container was started on the refused volume (ran %v, waited %v)", ranContainer(d), waited)
			}
		})
	}
}

// A running built-in database container is this installation's only when it
// carries this installation's id under apptown.instance. One that carries
// only the key an AppTown before 1.0 wrote (forge.instance), or another
// installation's id, is refused before the database is waited for.
//
// Kills: a container check that reads forge.instance, or that lets an
// absent apptown.instance through.
func TestBuiltinDBUsesARunningContainerOnlyWhenItIsThisInstallations(t *testing.T) {
	for name, c := range map[string]struct {
		labels string
		ok     bool
	}{
		"this installation's":            {"th1s1nstall", true},
		"only the label from before 1.0": {"", false},
		"another installation's":         {"0ther1nstall", false},
	} {
		t.Run(name, func(t *testing.T) {
			d := dockerfake.New()
			d.SetReply(func(args []string) ([]byte, error, bool) {
				if len(args) == 2 && args[0] == "inspect" && args[1] == currentBuiltinDB.Container {
					return []byte("{}"), nil, true
				}
				if len(args) > 1 && args[0] == "volume" && args[1] == "inspect" {
					raw, _ := json.Marshal(builtinVolume{Name: currentBuiltinDB.Volume, Driver: "local", Options: map[string]string{"type": "none", "o": "bind", "device": "/old/data/pgdata"}, Labels: map[string]string{dockerlabel.Instance: "th1s1nstall"}})
					return raw, nil, true
				}
				if len(args) == 4 && args[0] == "inspect" && args[1] == "-f" && args[3] == currentBuiltinDB.Container {
					if strings.Contains(args[2], "forge.instance") {
						return []byte("true\tth1s1nstall\n"), nil, true
					}
					return []byte("true\t" + c.labels + "\tvolume:" + currentBuiltinDB.Volume + "\n"), nil, true
				}
				return nil, nil, false
			})
			waited := false
			_, err := ensureBuiltinDB(context.Background(), docker.Scope{Client: d}, t.TempDir(), "postgres://db", "pw", "th1s1nstall", currentBuiltinDB,
				func(context.Context, string) error { waited = true; return nil })
			if c.ok != (err == nil) || waited != c.ok {
				t.Fatalf("container labelled %q: err %v, waited %v", c.labels, err, waited)
			}
			if !c.ok && !strings.Contains(err.Error(), currentBuiltinDB.Container) {
				t.Errorf("the refusal does not name the container: %v", err)
			}
		})
	}
}

func TestBuiltinDBStartsOnItsOwnOrANewVolume(t *testing.T) {
	own := "th1s1nstall"
	for name, label := range map[string]*string{"its own volume": &own, "no volume yet": nil} {
		t.Run(name, func(t *testing.T) {
			d := builtinDBDaemon(label)
			waited := false
			url, err := ensureBuiltinDB(context.Background(), docker.Scope{Client: d}, t.TempDir(), "postgres://db", "pw", own, currentBuiltinDB,
				func(context.Context, string) error { waited = true; return nil })
			if err != nil || url != "postgres://db" {
				t.Fatalf("got %q, %v", url, err)
			}
			if !ranContainer(d) || !waited {
				t.Errorf("the database was not started and waited for (ran %v, waited %v)", ranContainer(d), waited)
			}
			created := slices.ContainsFunc(d.Commands(), func(c []string) bool { return len(c) > 1 && c[0] == "volume" && c[1] == "create" })
			if created != (label == nil) {
				t.Errorf("volume created %v with an existing volume %v", created, label != nil)
			}
		})
	}
}

// A landing copies the data a deployment holds before it releases onto it,
// and does not release over data it could not copy: the step is refused
// before anything is built or started, until a person retries it accepting
// that, which the attempt records. An agent's connection cannot accept. A
// first release has nothing to copy.
func TestALandingDoesNotReleaseOverDataItCouldNotCopy(t *testing.T) {
	t.Parallel()
	a, f := bootWorld(t)
	runID, stepID := deployRun(t, a)
	// main holds production data.
	attrs := deployment.Attributes{Feed: "landing", Audience: "team", DataClass: "production"}
	target, err := a.Deployment.LandingTarget(t.Context(), idstest.N(1), "main", attrs)
	if err != nil {
		t.Fatal(err)
	}
	testAdministrator(t, a, "bls")
	if _, err := a.Deployment.Configure(asPerson("bls"), deployment.ConfigureDeployment{ProjectID: idstest.N(1), Name: "main", Target: target.Name, Attributes: attrs}); err != nil {
		t.Fatal(err)
	}
	engine := a.asEngine(context.Background())
	a.Work.ExecuteStep(engine, stepID)
	if s, err := workflow.ReadStepIn(t.Context(), a.DB, stepID); err != nil || s.Status != workflow.StatusPass {
		t.Fatalf("the first release, with nothing released before it to copy: %+v %v\n%s", s, err, strings.Join(f.joined(), "\n"))
	}

	// Its database cannot be copied now.
	f.reply = func(args []string) (string, error, bool) {
		if len(args) > 0 && args[0] == "exec" && strings.Contains(strings.Join(args, " "), "pg_dump") {
			return "pg_dump: connection to server failed", errFakeExit, true
		}
		return "", nil, false
	}
	app := mainNames(t, a).App()
	retry := func(ctx context.Context, accept bool) error {
		_, err := a.Work.RunStep(ctx, workflow.RetryCommand{StepID: stepID, AcceptUncopiedData: accept})
		if errors.Is(err, workflow.ErrDispatchStopped) {
			err = nil
		}
		return err
	}

	f.reset()
	if err := retry(asPerson("bls"), false); err != nil {
		t.Fatal(err)
	}
	a.Work.ExecuteStep(engine, stepID)
	s, err := workflow.ReadStepIn(t.Context(), a.DB, stepID)
	if err != nil || s.Status != workflow.StatusError || !strings.Contains(string(s.Evidence), "AppTown will not release over data it could not copy") {
		t.Fatalf("released over data it could not copy: %+v %v", s, err)
	}
	for _, c := range f.joined() {
		if strings.HasPrefix(c, "run ") || strings.HasPrefix(c, "build ") || strings.HasPrefix(c, "stop "+app) {
			t.Fatalf("something was built or started before the refusal: %s", c)
		}
	}

	// Acting for bls is not bls's decision: not an agent's connection, nor
	// a chat that may otherwise retry the step.
	for _, p := range []access.Principal{
		{Kind: "user", Name: "bls", Via: "token", Purpose: access.TokenAgent, Credential: "token:robot"},
		{Kind: "instrument", Name: "bls via console/c1", Person: "bls", Instrument: "console", Via: "console", ConsoleID: "c1", ProjectID: idstest.N(1)},
	} {
		if err := retry(verifiedContext(context.Background(), p), true); err == nil {
			t.Fatalf("%+v accepted releasing over uncopied data", p)
		}
	}
	if s, err := workflow.ReadStepIn(t.Context(), a.DB, stepID); err != nil || s.Status != workflow.StatusError {
		t.Fatalf("a refused acceptance changed the step: %+v %v", s, err)
	}

	if err := retry(asPerson("bls"), true); err != nil {
		t.Fatal(err)
	}
	a.Work.ExecuteStep(engine, stepID)
	s, err = workflow.ReadStepIn(t.Context(), a.DB, stepID)
	if err != nil || s.Status != workflow.StatusPass {
		t.Fatalf("a person's acceptance did not release: %+v %v", s, err)
	}
	logs, err := a.Work.RunArtifacts(asPerson("bls"), runID, workflow.ArtifactSelection{Kinds: []string{"agent-log"}})
	if err != nil {
		t.Fatal(err)
	}
	accepted := false
	for _, l := range logs {
		accepted = accepted || strings.Contains(l.Body, "bls accepted releasing over data AppTown could not copy")
	}
	if !accepted {
		t.Fatalf("the acceptance is not recorded with the attempt: %+v", logs)
	}

	// The acceptance was that attempt's: a later retry accepts nothing.
	if err := retry(asPerson("bls"), false); err != nil {
		t.Fatal(err)
	}
	a.Work.ExecuteStep(engine, stepID)
	if s, err := workflow.ReadStepIn(t.Context(), a.DB, stepID); err != nil || s.Status != workflow.StatusError {
		t.Fatalf("an earlier acceptance carried over to a later attempt: %+v %v", s, err)
	}
}
