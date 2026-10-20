package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockerfake"
	"flokkit.io/forge/internal/execution"
	piharness "flokkit.io/forge/internal/forge/agentharness/pi"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/operations"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// Infrastructure on an AppTown with no local Docker.

// absentDocker is AppTown's own Docker in the mode, standing in for
// docker.Refusing: owners answer from the mode first, so a call that
// reaches it fails the test.
type absentDocker struct {
	docker.Refusing
	t *testing.T
}

func (d absentDocker) Run(_ context.Context, _ string, c docker.Command) ([]byte, error) {
	d.t.Errorf("AppTown's own Docker was asked: docker %s", strings.Join(c.Args, " "))
	return nil, docker.ErrNoLocalBackend
}

type agentLabels map[string]string

func (l agentLabels) Labels(context.Context, string) (map[string]string, error) { return l, nil }

// infrastructureModeApp is an AppTown in the mode, with projects game and
// open, whose worker reports an agent image carrying the subagent label
// and pulls check images.
func infrastructureModeApp(t *testing.T) *App {
	t.Helper()
	return infrastructureModeAppWith(t, ownDockerTripwire{t})
}

// infrastructureModeAppWith is infrastructureModeApp whose Docker client
// for attached hosts is client.
func infrastructureModeAppWith(t *testing.T, client docker.Client) *App {
	t.Helper()
	ca := workertest.NewCA(t)
	previews := dockerfake.New() // runs nothing: every preview is absent
	previews.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "inspect" {
			return []byte("Error: No such object\n"), errors.New("exit status 1"), true
		}
		return nil, nil, true
	})
	srv := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: t.TempDir(),
		AgentImage: "apptown-agent:fixture", ImageLabels: agentLabels{piharness.SubagentsLabel: "1.0.0"},
		Checks: execution.Runner{DockerBinary: workerCheckDocker(t, filepath.Join(t.TempDir(), "calls"))}, Previews: previews})
	a := authzFixture(t, Config{NoLocalDocker: true, ConsoleCLI: fixtureCLI(t), Worker: workerClientConfig(t, ca, srv.URL),
		PublicHost: "forge.example.test", TierDomains: []string{"main=main.example.test"}, DockerClient: client})
	a.docker.Client = absentDocker{t: t}
	return a
}

// Startup, reaping, console recovery, connector health and the agent
// image's labels answer from the mode: no local backend, the worker for
// the image, and the built-in host unavailable.
func TestInTheNoLocalDockerModeMaintenanceAndHealthAskNoLocalDocker(t *testing.T) {
	t.Parallel()
	a := infrastructureModeApp(t)
	if a.DockerAvailable(context.Background()) {
		t.Fatal("an AppTown with no local Docker has a daemon")
	}
	a.EnsureAgentImage()
	if state, _ := a.setup.ImageState(); state != "no local backend" {
		t.Fatalf("agent image at start: %q", state)
	}
	a.ReapPreviewOrphans(context.Background())
	if err := a.RecoverConsoles(context.Background()); err != nil {
		t.Fatal(err)
	}
	health := connectorHealthFor(a)
	if h := health.Health(t.Context(), integration.CapExecutor, "docker", nil); !h.OK || !strings.Contains(h.Detail, "worker · image apptown-agent:fixture") {
		t.Fatalf("executor health: %+v", h)
	}
	if h := health.Health(t.Context(), integration.CapDeploy, "docker", nil); h.OK || !strings.HasPrefix(h.Detail, "this AppTown has no Docker of its own") {
		t.Fatalf("Docker deploys with no attached host: %+v", h)
	}
	if h := health.Test(t.Context(), verification.ScannerCapability, "trivy", map[string]any{"image": "scanner:1"}); !h.OK || h.Detail != "image pulled on the worker" {
		t.Fatalf("scanner test: %+v", h)
	}
	if _, err := a.Integrations.Bindings.Upsert(t.Context(), "trivy", verification.ScannerCapability, "tool", mustJSON(map[string]any{"image": "scanner:1", "command": []string{"scan"}, "target": "image"}), true); err != nil {
		t.Fatal(err)
	}
	if w := authzCall(a.Handler(), "boss", "GET", "/api/v1/integrations", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "image scanner:1 pulls on the worker on first use") {
		t.Fatalf("integrations: %d %s", w.Code, w.Body.String())
	}
	if piHomeFor(a).Delegation(context.Background()) == "" {
		t.Fatal("the worker's agent image carries subagents, yet nothing promises them")
	}
}

// A deleted project's cleanup completes in the mode: the local Docker
// names are skipped, never failed on, and its folders still go.
func TestInTheNoLocalDockerModeProjectCleanupCompletes(t *testing.T) {
	t.Parallel()
	a := infrastructureModeApp(t)
	plan := projectlifecycle.CleanupPlan{Slug: "gone", RunIDs: []ids.ID{idstest.N(100)}, ChangeIDs: []ids.ID{idstest.N(100)}, PreviewSteps: []ids.ID{idstest.N(100)}}
	if err := a.DB.InTx(context.Background(), func(tx *database.Tx) error {
		return project.RecordDeletionIn(context.Background(), tx, idstest.N(100), "gone", access.Actor{Name: "boss"}, plan, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	// The change's and the run's folders, and the snapshots each was
	// served from (a run's landing snapshot among them).
	var folders []string
	for _, name := range []string{"change-00000000-0064-7000-8000-000000000000", "asof-change-00000000-0064-7000-8000-000000000000", "run-00000000-0064-7000-8000-000000000000", "asof-run-00000000-0064-7000-8000-000000000000"} {
		folder := filepath.Join(a.cfg.dataDir, "workspaces", name)
		if err := os.MkdirAll(folder, 0o700); err != nil {
			t.Fatal(err)
		}
		folders = append(folders, folder)
	}
	if _, err := projectCleanupFor(a).Run(context.Background(), projectlifecycle.CleanupKey(idstest.N(100), 0), nil); err != nil {
		t.Fatal(err)
	}
	if d, err := a.Projects.Deletion(context.Background(), idstest.N(100)); err != nil || d.State != project.CleanupDone {
		t.Fatalf("cleanup: %+v %v", d, err)
	}
	for _, folder := range folders {
		if _, err := os.Stat(folder); !os.IsNotExist(err) {
			t.Fatalf("%s stayed: %v", filepath.Base(folder), err)
		}
	}
}

// attachedHost is an attached Docker host's daemon, reached only through
// that host (DOCKER_HOST ssh://forge-target-<name>): a call for AppTown's
// own Docker fails the test.
func attachedHost(t *testing.T, name string) (*fakeDocker, docker.Client) {
	return attachedHostBeside(t, name, nil)
}

// attachedHostBeside is attachedHost on an AppTown whose own Docker is local
// (nil: it has none, and a call for it fails the test).
func attachedHostBeside(t *testing.T, name string, local *fakeDocker) (*fakeDocker, docker.Client) {
	remote := newFakeDocker()
	return remote, dockerClientFunc(func(ctx context.Context, host string, req docker.Command) ([]byte, error) {
		if host == "" && local != nil {
			return local.Run(ctx, "", req)
		}
		if host != "ssh://forge-target-"+name {
			t.Errorf("docker %s was sent to %q, not the attached host", strings.Join(req.Args, " "), host)
			return nil, docker.ErrNoLocalBackend
		}
		return remote.Run(ctx, "", req)
	})
}

// attachTarget records an attached Docker host whose key is in the
// forge's secrets store. Reaching it writes its ssh stanza to AppTown's
// SSH config, which each test AppTown keeps in a folder of its own.
func attachTarget(t *testing.T, a *App, name, address string) *deployment.Target {
	t.Helper()
	if err := os.WriteFile(filepath.Join(a.cfg.dataDir, "secrets.env"), []byte("LAB_KEY=-----BEGIN OPENSSH PRIVATE KEY-----\\nabc\\n-----END OPENSSH PRIVATE KEY-----\n"), 0o600); err != nil { // gitleaks:allow (test fixture, not a credential)
		t.Fatal(err)
	}
	target, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: name, Kind: deployment.KindDockerHost, Origin: deployment.OriginAttached, Status: deployment.TargetReady,
		Address: map[string]any{"host": "ssh://root@" + address, "keyFrom": "LAB_KEY", "hostname": address}})
	if err != nil {
		t.Fatal(err)
	}
	return target
}

// A landed project can be deleted in the mode: its main, on an attached
// Docker host, is taken down on that host the way any deployment is, before
// the project's records go. A host that cannot be reached refuses the
// deletion, naming the deployment; once it answers, main's recorded
// container goes and the project with it.
func TestInTheNoLocalDockerModeALandedProjectIsDeletedAndItsMainTornDown(t *testing.T) {
	t.Parallel()
	remote, client := attachedHost(t, "lab")
	a := infrastructureModeAppWith(t, client)
	fx := fixtureWork(t, a)
	target := attachTarget(t, a, "lab", "192.0.2.10")
	if _, err := a.Work.Abandon(asPerson("boss"), workflow.StopWork{RunID: fx.Run, Reason: "the project is being deleted"}); err != nil {
		t.Fatal(err)
	}
	env, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "main", target.ID, (deployment.Attributes{}).WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	// main runs in its container on the host, labelled with its identity.
	if _, err := a.Deployment.Deployments.ReserveHostPort(t.Context(), idstest.N(1), "main", "forge-env-game-main", 41000); err != nil {
		t.Fatal(err)
	}
	if err := a.Deployment.Deployments.OwnerOf(env).Created(context.Background(), deploy.Owned{Kind: "container", ID: "forge-env-game-main", Name: "forge-env-game-main"}); err != nil {
		t.Fatal(err)
	}
	remote.markStarted("forge-env-game-main")
	remote.mu.Lock()
	remote.labels = map[string]map[string]string{"forge-env-game-main": {"forge.environment": fmt.Sprint(env.ID), "forge.generation": "0"}}
	remote.mu.Unlock()
	var unreachable atomic.Bool
	unreachable.Store(true)
	remote.reply = func(args []string) (string, error, bool) {
		if unreachable.Load() {
			return "error during connect: ssh: connect to host 192.0.2.10 port 22: Connection refused", errors.New("exit status 1"), true
		}
		return "", nil, false
	}
	if st := deleteProject(t, a, "game"); st.State != project.Deleting || !strings.Contains(st.Stalled, "main") {
		t.Fatalf("deletion with the host unreachable: %+v", st)
	}
	if _, err := a.Projects.BySlug(context.Background(), "game"); err != nil {
		t.Fatalf("a stalled deletion removed the project: %v", err)
	}
	unreachable.Store(false)
	// Re-confirmed, the deletion goes on.
	if st := deleteProject(t, a, "game"); st.State != project.Deleted {
		t.Fatalf("re-confirmed deletion: %+v", st)
	}
	if remote.isStarted("forge-env-game-main") {
		t.Fatal("main still runs on the attached host after its take-down")
	}
	if op := purgeProject(t, a, "game"); op.State != operations.Succeeded {
		t.Fatalf("purge: %s %q", op.State, op.Detail)
	}
	if _, err := projectCleanupFor(a).Run(context.Background(), projectlifecycle.CleanupKey(idstest.N(1), 0), nil); err != nil {
		t.Fatal(err)
	}
	if d, err := a.Projects.Deletion(context.Background(), idstest.N(1)); err != nil || d.State != project.CleanupDone {
		t.Fatalf("cleanup: %+v %v", d, err)
	}
}

// releaseOn records release #1 of game's main on target, settled as
// running in container forge-env-game-main at port.
func releaseOn(t *testing.T, a *App, target *deployment.Target, port int) {
	t.Helper()
	env, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "main", target.ID, (deployment.Attributes{}).WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	rel, err := a.Deployment.Releases.Start(context.Background(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBranch, ProjectID: idstest.N(1), DeploymentID: env.ID, Deployment: "main", Revision: "game-first", Actor: "boss"}, "attached fixture")
	if err != nil {
		t.Fatal(err)
	}
	outcomes := deployment.NewOutcomes(a.DB, nil)
	at, _, err := outcomes.Admit(context.Background(), deployment.Start{DeploymentID: env.ID, TargetID: target.ID, Key: "attached-release", Inputs: deployment.Inputs{ReleaseID: rel.ID, Revision: "game-first"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outcomes.Complete(context.Background(), deployment.Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Deployments.ReserveHostPort(t.Context(), idstest.N(1), "main", "forge-env-game-main", port); err != nil {
		t.Fatal(err)
	}
}

// The release list and the deployment view agree about a release on an
// attached Docker host: both read the host, so a running container is
// serving in one and running in the other (a live trial's list said
// "unknown" while the deployment said running).
func TestInTheNoLocalDockerModeAReleaseOnAnAttachedHostIsServingWhereItsDeploymentRuns(t *testing.T) {
	t.Parallel()
	remote, client := attachedHost(t, "lab")
	a := infrastructureModeAppWith(t, client)
	releaseOn(t, a, attachTarget(t, a, "lab", "192.0.2.10"), 40001)
	remote.markStarted("forge-env-game-main")
	if err := a.Deployment.ObserveRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	w := authzCall(h, "boss", "GET", "/api/v1/projects/game/deployments", "")
	var deployments []struct{ Name, Observation string }
	if err := json.Unmarshal(w.Body.Bytes(), &deployments); err != nil || w.Code != 200 || len(deployments) != 1 || deployments[0].Observation != "running" {
		t.Fatalf("deployments: %d %s", w.Code, w.Body.String())
	}
	w = authzCall(h, "boss", "GET", "/api/v1/projects/game/releases", "")
	var releases []struct {
		Serving  bool
		Observed string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &releases); err != nil || w.Code != 200 || len(releases) != 1 || !releases[0].Serving || releases[0].Observed != "" {
		t.Fatalf("releases: %d %s", w.Code, w.Body.String())
	}
}

// An attached Docker host reads as a Docker host over SSH at its hostname,
// never as the built-in host ("this AppTown"), and its deployment's URL is the
// host's address, not this AppTown's (a live trial read both wrong).
func TestAnAttachedDockerHostIsNamedAndReachedAsThatHost(t *testing.T) {
	t.Parallel()
	remote, client := attachedHost(t, "lab")
	a := infrastructureModeAppWith(t, client)
	releaseOn(t, a, attachTarget(t, a, "lab", "192.0.2.10"), 40001)
	remote.markStarted("forge-env-game-main")
	h := a.Handler()

	w := authzCall(h, "boss", "GET", "/api/v1/projects/game/deployments", "")
	var deployments []struct {
		URL    string
		Target struct{ Title, Vendor, Family, Region string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &deployments); err != nil || w.Code != 200 || len(deployments) != 1 {
		t.Fatalf("deployments: %d %s", w.Code, w.Body.String())
	}
	if d := deployments[0]; d.URL != "http://192.0.2.10:40001" || d.Target.Title != "Docker host" || d.Target.Vendor != "over SSH" || d.Target.Family != "attached" || d.Target.Region != "192.0.2.10" {
		t.Fatalf("deployment: %s", w.Body.String())
	}
	w = authzCall(h, "boss", "GET", "/api/v1/targets", "")
	var targets struct {
		Targets []struct {
			Name     string
			KindInfo struct{ Title, Vendor, Family string }
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &targets); err != nil || w.Code != 200 {
		t.Fatalf("targets: %d %s", w.Code, w.Body.String())
	}
	for _, target := range targets.Targets {
		if target.Name == "lab" && (target.KindInfo.Title != "Docker host" || target.KindInfo.Family != "attached") {
			t.Fatalf("lab target: %+v", target)
		}
	}
	if w := authzCall(h, "boss", "GET", "/api/v1/infrastructure", ""); w.Code != 200 || strings.Contains(w.Body.String(), "this AppTown") || !strings.Contains(w.Body.String(), `"place":"Docker host · over SSH · 192.0.2.10"`) {
		t.Fatalf("infrastructure: %d %s", w.Code, w.Body.String())
	}
	if url := projectReadsMainURL(t, a); url != "http://192.0.2.10:40001" {
		t.Fatalf("get_project's main URL: %q", url)
	}
	// The host is reached through the SSH config AppTown was given.
	if b, err := os.ReadFile(a.cfg.sshConfigPath); err != nil || !strings.Contains(string(b), "Host forge-target-lab\n  HostName 192.0.2.10\n") {
		t.Fatalf("AppTown's SSH config %s: %q %v", a.cfg.sshConfigPath, b, err)
	}
}

// projectReadsMainURL is game's main URL as get_project (MCP) gives it.
func projectReadsMainURL(t *testing.T, a *App) string {
	t.Helper()
	res := mcp_call(t, a, mcp_token(t, a, "laptop", "boss", "member"), "get_project", map[string]any{"slug": "game"})
	deployments, _ := res["structuredContent"].(map[string]any)["deployments"].([]any)
	if len(deployments) != 1 {
		t.Fatalf("get_project deployments: %v", res)
	}
	url, _ := deployments[0].(map[string]any)["url"].(string)
	return url
}

// A main on an attached host is observed on that host, in the mode and out
// of it: the Deployments page's main row says it runs, and the conformance
// runtime check probes it there, at the host's address. Neither asks the
// forge's own Docker (in the mode it has none; out of it, it has no such
// container and would say "not running").
func TestAMainOnAnAttachedHostIsObservedAndProbedThere(t *testing.T) {
	t.Parallel()
	// The app answers on the attached host, named "localhost" so that a
	// probe sent to this AppTown's loopback instead (127.0.0.1) is told apart.
	var mu sync.Mutex
	var asked []string
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Host)
		mu.Unlock()
		fmt.Fprint(w, "ok")
	}))
	defer app.Close()
	port := app.Listener.Addr().(*net.TCPAddr).Port
	for _, mode := range []string{"no local Docker", "local Docker"} {
		t.Run(mode, func(t *testing.T) {
			var remote *fakeDocker
			var a *App
			if mode == "no local Docker" {
				var client docker.Client
				remote, client = attachedHost(t, "lab")
				a = infrastructureModeAppWith(t, client)
			} else {
				var client docker.Client
				remote, client = attachedHostBeside(t, "lab", newFakeDocker())
				a = authzFixture(t, Config{DockerClient: client})
			}
			releaseOn(t, a, attachTarget(t, a, "lab", "localhost"), port)
			remote.markStarted("forge-env-game-main")

			w := authzCall(a.Handler(), "boss", "GET", "/api/v1/tiers", "")
			var tiers struct {
				Tiers []struct {
					Name string
					Apps []struct {
						Slug, Observed string
						Running        bool
					}
				}
			}
			if err := json.Unmarshal(w.Body.Bytes(), &tiers); err != nil || w.Code != 200 {
				t.Fatalf("tiers: %d %s", w.Code, w.Body.String())
			}
			running := false
			for _, tier := range tiers.Tiers {
				for _, app := range tier.Apps {
					if tier.Name == "main" && app.Slug == "game" {
						running = app.Running && app.Observed == ""
					}
				}
			}
			if !running {
				t.Fatalf("main tier: %s", w.Body.String())
			}
			mu.Lock()
			asked = nil
			mu.Unlock()
			if state, why := a.evaluator.Observe(t.Context(), idstest.N(1), "", "GET /"); state != conformance.Conforms {
				t.Fatalf("runtime check: %s %q", state, why)
			}
			mu.Lock()
			for _, host := range asked {
				if host != fmt.Sprintf("localhost:%d", port) {
					t.Errorf("the runtime check asked %s, not the attached host", host)
				}
			}
			mu.Unlock()
			if d := mcpDeployment(t, a); d["running"] != true || d["observed"] != nil {
				t.Fatalf("get_project deployment: %v", d)
			}
			remote.reply = func(args []string) (string, error, bool) {
				if args[0] == "inspect" {
					return "error during connect: ssh: connect to host localhost port 22: Connection refused", errors.New("exit status 1"), true
				}
				return "", nil, false
			}
			if state, why := a.evaluator.Observe(t.Context(), idstest.N(1), "", "GET /"); state != conformance.CannotAssess || !strings.Contains(why, "is unknown: ") {
				t.Fatalf("runtime check, host unreachable: %s %q", state, why)
			}
		})
	}
}

// mcpDeployment is game's one deployment as get_project (MCP) gives it.
func mcpDeployment(t *testing.T, a *App) map[string]any {
	t.Helper()
	res := mcp_call(t, a, mcp_token(t, a, "laptop", "boss", "member"), "get_project", map[string]any{"slug": "game"})
	deployments, _ := res["structuredContent"].(map[string]any)["deployments"].([]any)
	if len(deployments) != 1 {
		t.Fatalf("get_project deployments: %v", res)
	}
	return deployments[0].(map[string]any)
}

// Read views that would ask AppTown's own Docker whether an app runs say
// "unavailable" for the built-in host and "unknown" for what they cannot
// see, never "not running".
func TestInTheNoLocalDockerModeReadViewsSayUnavailableOrUnknown(t *testing.T) {
	t.Parallel()
	a := infrastructureModeApp(t)
	releasedPreviewFixture(t, a, idstest.N(1), "game-first") // game's main, on the built-in host
	if _, err := a.Deployment.Deployments.ReserveHostPort(t.Context(), idstest.N(1), "main", "forge-env-game-main", 40001); err != nil {
		t.Fatal(err)
	}
	attached, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: "lab", Kind: deployment.KindDockerHost, Origin: deployment.OriginAttached, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(2), "main", attached.ID, (deployment.Attributes{}).WithDefaults()); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()

	w := authzCall(h, "boss", "GET", "/api/v1/tiers", "")
	var tiers struct {
		Tiers []struct {
			Name, Observed string
			Apps           []struct {
				Slug, Observed string
				Running        bool
			}
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tiers); err != nil || w.Code != 200 {
		t.Fatalf("tiers: %d %s", w.Code, w.Body.String())
	}
	observed := map[string]string{}
	for _, tier := range tiers.Tiers {
		// Previews run on the worker, which answered: none, observed.
		if tier.Name == "preview" && tier.Observed != "" {
			t.Fatalf("preview tier: %s", w.Body.String())
		}
		for _, app := range tier.Apps {
			if tier.Name == "main" && !app.Running {
				observed[app.Slug] = app.Observed
			}
		}
	}
	if observed["game"] != "unavailable" || observed["open"] != "unknown" {
		t.Fatalf("main tier: %s", w.Body.String())
	}
	if w := authzCall(h, "boss", "GET", "/api/v1/tiers/departures", ""); w.Code != 200 || strings.Contains(w.Body.String(), "previewsObserved") {
		t.Fatalf("departures: %d %s", w.Code, w.Body.String())
	}

	w = authzCall(h, "boss", "GET", "/api/v1/projects/game/releases", "")
	var releases []struct {
		Serving  bool
		Observed string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &releases); err != nil || len(releases) != 1 || releases[0].Serving || releases[0].Observed != "unavailable" {
		t.Fatalf("releases: %d %s", w.Code, w.Body.String())
	}

	res := mcp_call(t, a, mcp_token(t, a, "laptop", "boss", "member"), "get_project", map[string]any{"slug": "game"})
	deployments, _ := res["structuredContent"].(map[string]any)["deployments"].([]any)
	if len(deployments) != 1 {
		t.Fatalf("get_project deployments: %v", res)
	}
	if d := deployments[0].(map[string]any); d["observed"] != "unavailable" || d["running"] != nil {
		t.Fatalf("get_project deployment: %v", d)
	}

	if state, why := a.evaluator.Observe(t.Context(), idstest.N(1), "", "GET /"); state != conformance.CannotAssess || !strings.Contains(why, "unavailable: this AppTown has no local Docker") {
		t.Fatalf("runtime: %s %q", state, why)
	}

	rec := httptest.NewRecorder()
	a.VHost(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest("GET", "http://game.main.example.test/", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "unavailable: this AppTown has no local Docker") {
		t.Fatalf("vhost: %d %s", rec.Code, rec.Body.String())
	}
}

// A change's release on the built-in host of an AppTown with no local Docker
// is "unavailable", never "not serving".
func TestInTheNoLocalDockerModeAChangesReleaseOnTheBuiltInHostIsUnavailable(t *testing.T) {
	t.Parallel()
	a := infrastructureModeApp(t)
	change, err := workflow.NewCreator(a.DB, func(context.Context, workflow.CreatedChange) error { return nil }).Create(context.Background(), workflow.ChangeDraft{ProjectID: idstest.N(1), Type: "feature", Title: "Shipped"}, "boss")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DB.InTx(context.Background(), func(tx *database.Tx) error {
		_, err := workflow.TransitionChangeIn(context.Background(), tx, idstest.N(1), change.ID, workflow.ChangeMove{Action: workflow.ShipChange}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	releasedPreviewFixture(t, a, idstest.N(1), "game-first") // game's main, on the built-in host
	if _, err := a.Deployment.Deployments.ReserveHostPort(t.Context(), idstest.N(1), "main", "forge-env-game-main", 40001); err != nil {
		t.Fatal(err)
	}
	w := authzCall(a.Handler(), "boss", "GET", fmt.Sprintf("/api/v1/changes/%d", change.ID), "")
	var view struct {
		Evidence struct {
			Releases []struct {
				Serving  bool
				Observed string
			}
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || w.Code != 200 || len(view.Evidence.Releases) != 1 {
		t.Fatalf("change: %d %s", w.Code, w.Body.String())
	}
	if r := view.Evidence.Releases[0]; r.Serving || r.Observed != "unavailable" {
		t.Fatalf("release: %s", w.Body.String())
	}
}

// Release history judges each release by its own deployment: one whose
// deployment cannot be observed (here, it records no container) is
// "unknown"; one whose deployment is gone is simply not serving.
func TestInTheNoLocalDockerModeReleaseHistoryJudgesEachDeployment(t *testing.T) {
	t.Parallel()
	a := infrastructureModeApp(t)
	releasedPreviewFixture(t, a, idstest.N(1), "game-first") // game's main, on the built-in host
	if _, err := a.Deployment.Releases.Start(context.Background(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBundle, ProjectID: idstest.N(1), Deployment: "retired", Revision: "game-old", Actor: "boss"}, "to a deployment since removed"); err != nil {
		t.Fatal(err)
	}
	w := authzCall(a.Handler(), "boss", "GET", "/api/v1/projects/game/releases", "")
	var releases []struct {
		Deployment, Observed string
		Serving              bool
	}
	if err := json.Unmarshal(w.Body.Bytes(), &releases); err != nil || w.Code != 200 || len(releases) != 2 {
		t.Fatalf("releases: %d %s", w.Code, w.Body.String())
	}
	observed := map[string]string{}
	for _, r := range releases {
		observed[r.Deployment] = r.Observed
	}
	if observed["main"] != "unknown" || observed["retired"] != "" {
		t.Fatalf("releases: %s", w.Body.String())
	}
}

// Portainer deploys are not Docker's: neither AppTown's daemon nor an
// attached Docker host answers for them, so they read as not checked.
func TestPortainerDeploysAreNotReadAsDockerOnes(t *testing.T) {
	t.Parallel()
	_, client := attachedHost(t, "lab")
	a := infrastructureModeAppWith(t, client)
	if h := connectorHealthFor(a).Health(t.Context(), integration.CapDeploy, "portainer", nil); h.Checked {
		t.Fatalf("portainer read as Docker: %+v", h)
	}
}
