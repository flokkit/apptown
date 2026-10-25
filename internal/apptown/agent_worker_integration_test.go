//go:build integration

package apptown

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execfile"
	"github.com/flokkit/apptown/stock"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/tasks"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// Agent steps on a real worker: apptown-worker's server, in this process,
// running its tasks on this machine's Docker, in a small image whose `pi`
// is a script (fakePiScript) that behaves as the change's fake-pi.sh says. The
// AppTown talks to it over mutual TLS, as to any worker; AppTown's own
// agent process is a tripwire. Nothing here uses the packaged Pi runtime.

// fakePiScript stands in for Pi: it uses the transcript file AppTown
// points it at (--session), reads the session from its header, counts its invocations in the workspace's state,
// and runs the change's /work/fake-pi.sh, which may say something (an
// assistant turn, also appended to the transcript), stay silent (an empty
// turn) or write files.
const fakePiScript = `#!/bin/sh
PI_ARGS="$*"
session=""; prompt=""
while [ $# -gt 0 ]; do
  case "$1" in
    --session) session="$2"; shift ;;
    *) prompt="$1" ;;
  esac
  shift
done
n=$(( $(cat /apptown/state/fake-pi-count 2>/dev/null || echo 0) + 1 ))
echo "$n" > /apptown/state/fake-pi-count
sid=$(head -n 1 "$session" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
PI_INVOCATION="$n"; PI_SESSION="$sid"; PI_PROMPT="$prompt"
say() {
  text=$(printf '%s' "$1" | tr '\n"\\' '   ')
  line=$(printf '{"type":"message_end","message":{"role":"assistant","model":"fake-model","provider":"fake","content":[{"type":"text","text":"%s"}],"usage":{"input":10,"output":5,"totalTokens":15}}}' "$text")
  echo "$line"
  if [ -n "$session" ]; then echo "$line" >> "$session"; fi
}
silent() {
  line='{"type":"message_end","message":{"role":"assistant","model":"fake-model","provider":"fake","content":[],"usage":{"input":10,"output":0,"totalTokens":10}}}'
  echo "$line"
  if [ -n "$session" ]; then echo "$line" >> "$session"; fi
}
if [ -f /work/fake-pi.sh ]; then . /work/fake-pi.sh; else say "done"; fi
`

// fakePiLabels are the fake image's labels: the runtime's version, and no
// subagents (so usage is complete when the stream is).
const fakePiVersion = "fake-pi-1"

type fakePiBuilt struct {
	once        sync.Once
	image, fail string
}

var fakePiBuild, fakePiGitBuild fakePiBuilt

// fakePiImage builds the fake agent image from the pinned busybox on this
// machine's Docker, once per test binary, named by its content.
func fakePiImage(t *testing.T) string {
	t.Helper()
	return fakePiImageFrom(t, &fakePiBuild, "FROM "+stock.Images().DF+"\n")
}

// fakePiGitImage is the fake agent image with git, for an agent that
// commits and pushes as a real one does: the check image (stock.Images().Sh's
// alpine/git) without its git entrypoint.
func fakePiGitImage(t *testing.T) string {
	t.Helper()
	return fakePiImageFrom(t, &fakePiGitBuild, "FROM alpine/git:v2.47.2\nENTRYPOINT []\n")
}

func fakePiImageFrom(t *testing.T, fakePiBuild *fakePiBuilt, base string) string {
	t.Helper()
	fakePiBuild.once.Do(func() {
		dockerfile := base + "LABEL apptown.pi.version=\"" + fakePiVersion + "\"\nCOPY pi /usr/local/bin/pi\n"
		sum := sha256.Sum256([]byte(dockerfile + fakePiScript))
		image := "forge-it-fake-pi:" + hex.EncodeToString(sum[:])[:12]
		if dockertest.Run("image", "inspect", image) == nil {
			fakePiBuild.image = image
			return
		}
		dir, err := os.MkdirTemp("", "apptown-fake-pi-*")
		if err != nil {
			fakePiBuild.fail = err.Error()
			return
		}
		defer os.RemoveAll(dir)
		for name, body := range map[string]string{"Dockerfile": dockerfile, "pi": fakePiScript} {
			if err := execfile.Write(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
				fakePiBuild.fail = err.Error()
				return
			}
		}
		if out, err := (docker.CLI{}).Run(context.Background(), "", docker.Command{Args: []string{"build", "-q", "-t", image, dir}, Env: []string{"BUILDX_CONFIG=" + filepath.Join(dir, "buildx")}}); err != nil {
			fakePiBuild.fail = fmt.Sprintf("building %s: %v\n%s", image, err, out)
			return
		}
		fakePiBuild.image = image
	})
	if fakePiBuild.fail != "" {
		t.Fatal(fakePiBuild.fail)
	}
	return fakePiBuild.image
}

// agentWorkerWorld is an AppTown with a real worker, and one admitted agent
// step of a change. The worker can be made to stop answering (down), to
// say it no longer holds the start of an output (truncate), to refuse
// waits and not answer stops (waitRefused), not to confirm a stop
// (refuseStop), or not to answer waits (waitsUnanswered).
type agentWorkerWorld struct {
	a           *App
	cfg         Config
	namespace   string
	image       string
	worker      *worker.Server
	run         *workflow.Run
	step        *workflow.Step
	source      workflow.Source
	down        atomic.Bool
	truncate    atomic.Bool
	waitRefused atomic.Bool
	// refuseStop: the worker does not confirm a stop.
	refuseStop atomic.Bool
	// waitsUnanswered: a wait's answer, even one already being made, is
	// replaced by "unavailable", which is asked again.
	waitsUnanswered atomic.Bool
}

func (w *agentWorkerWorld) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if w.down.Load() || (w.refuseStop.Load() && strings.HasSuffix(r.URL.Path, "/stop")) {
			http.Error(rw, "the worker is gone", http.StatusServiceUnavailable)
			return
		}
		if w.waitRefused.Load() && strings.HasSuffix(r.URL.Path, "/wait") {
			http.Error(rw, "no waiting here", http.StatusBadRequest)
			return
		}
		if w.waitRefused.Load() && strings.HasSuffix(r.URL.Path, "/stop") {
			http.Error(rw, "the worker is busy", http.StatusServiceUnavailable)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/wait") {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if w.waitsUnanswered.Load() {
				http.Error(rw, "the worker is busy", http.StatusServiceUnavailable)
				return
			}
			for k, v := range rec.Header() {
				rw.Header()[k] = v
			}
			rw.WriteHeader(rec.Code)
			_, _ = io.Copy(rw, rec.Body)
			return
		}
		if w.truncate.Load() && strings.HasSuffix(r.URL.Path, "/output") {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			var page map[string]any
			if rec.Code == http.StatusOK && json.Unmarshal(rec.Body.Bytes(), &page) == nil {
				page["Truncated"] = true
				rw.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(rw).Encode(page)
				return
			}
			rw.WriteHeader(rec.Code)
			_, _ = io.Copy(rw, rec.Body)
			return
		}
		h.ServeHTTP(rw, r)
	})
}

// removeTaskContainers removes, when the test ends, the containers this
// test's worker made: only those named in its namespace.
func removeTaskContainers(t *testing.T, namespace string) {
	t.Cleanup(func() {
		out, _ := dockertest.Output("ps", "-aq", "--filter", "name=apptown-task-"+namespace+"-")
		for _, id := range strings.Fields(string(out)) {
			_ = dockertest.Run("rm", "-f", id)
		}
	})
}

func newAgentWorkerWorld(t *testing.T, with map[string]any, files map[string]string) *agentWorkerWorld {
	t.Helper()
	step := map[string]any{"prompt": "do the work", "executor": "docker"}
	for k, v := range with {
		step[k] = v
	}
	return newWorkerStepWorld(t, schema.Step{Name: "build", Uses: "agent/pi", With: step}, files)
}

// newWorkerStepWorld is an AppTown with a real worker and one admitted step of
// a change, whatever the step's kind; configure adjusts the worker before
// it starts.
func newWorkerStepWorld(t *testing.T, step schema.Step, files map[string]string, configure ...func(*worker.Server)) *agentWorkerWorld {
	t.Helper()
	requireDocker(t)
	w := &agentWorkerWorld{image: fakePiImage(t), namespace: fmt.Sprintf("agentit%d", time.Now().UnixNano()%1_000_000_000)}
	removeTaskContainers(t, w.namespace)
	ca := workertest.NewCA(t)
	server := &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: t.TempDir(), Tasks: execution.DockerTasks{ToolVersions: stock.Versions(), Namespace: w.namespace},
		AgentImage: w.image, ImageLabels: worker.DockerImages{Binary: "docker"}, DindImage: "docker:27-dind"}
	for _, c := range configure {
		c(server)
	}
	server.Profile = server.MeasureProfile(context.Background(), false)
	w.worker = server
	// Before the worker can make any: a test that fails leaves none running.
	removePrivateDockerOf(t, server)
	srv := workertest.StartServerWith(t, ca, server, w.wrap)
	w.cfg = Config{DataDir: t.TempDir(), AgentExecutor: ExecutorDocker, AgentImage: "apptown-agent", ConsoleCLI: fixtureCLI(t), Worker: workerClientConfig(t, ca, srv.URL), PrivateDocker: true}
	w.a = testApp(t, w.cfg)
	w.a.tasks.Poll, w.a.tasks.Patience = 100*time.Millisecond, 2*time.Second
	run, steps := agentWorkerChange(t, w.a, []schema.Step{step}, files)
	onlyOnTheWorker(t, w.a)
	w.run, w.step, w.source = run, steps[step.Name], workflowSourceForTest(t, w.a, run)
	return w
}

// execute runs the step's current attempt, as the dispatcher would.
func (w *agentWorkerWorld) execute(t *testing.T, a *App) {
	t.Helper()
	if err := a.Work.ExecuteStep(a.asEngine(context.Background()), w.step.ID); err != nil {
		t.Fatal(err)
	}
}

// restart is AppTown after a restart: a new App on the same database,
// data directory and worker, which runs startup repair and resumes recorded
// work, as the daemon does.
func (w *agentWorkerWorld) restart(t *testing.T) *App {
	t.Helper()
	return w.restartDispatching(t, true)
}

// restartForRetry is restart with the new App's dispatch stopped before it
// resumes recorded work, so a stage whose steps all passed stays open for
// the retry the test asks for next. A restart that dispatches advances that
// stage (its run is a restart candidate), and stopping dispatch only
// afterwards races that advance.
func (w *agentWorkerWorld) restartForRetry(t *testing.T) *App {
	t.Helper()
	return w.restartDispatching(t, false)
}

func (w *agentWorkerWorld) restartDispatching(t *testing.T, dispatching bool) *App {
	t.Helper()
	b := newApp(t, w.a.DB, w.cfg)
	b.tasks.Poll, b.tasks.Patience = 100*time.Millisecond, 2*time.Second
	b.agents = agentExecutionFor(b, localTripwire{t})
	rebuildWork(t, b, func(*workflow.Dependencies) {})
	if !dispatching {
		b.Work.Stop()
	}
	if err := b.RecoverInterruptedWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.ResumePendingAutoSteps(context.Background())
	return b
}

func (w *agentWorkerWorld) invocations(t *testing.T, attempt int64) []tasks.Attempt {
	t.Helper()
	got, err := tasks.New(w.a.DB).Invocations(context.Background(), w.step.ID, attempt)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (w *agentWorkerWorld) branchFile(t *testing.T, name string) string {
	t.Helper()
	hub := requireLocation(t)(w.source.Repository(w.a.cfg.dataDir))
	return gittest.Run(t, hub, "show", scm.Ref(t.Context(), hub, "refs/heads/"+w.source.Branch)+":"+name)
}

func (w *agentWorkerWorld) transcript(t *testing.T, attempt int64) (workflow.TranscriptReceipt, []byte) {
	t.Helper()
	ref := workflow.RunRef{ID: w.run.ID, ProjectID: w.run.ProjectID}
	receipt, err := workflow.StepTranscriptIn(context.Background(), w.a.DB, ref, w.step.ID, attempt)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := workflow.ReadTranscript(w.a.cfg.dataDir, receipt)
	if err != nil {
		t.Fatalf("attempt %d's transcript: %+v %v", attempt, receipt, err)
	}
	return receipt, raw
}

func (w *agentWorkerWorld) artifacts(t *testing.T, kind string) map[string]string {
	t.Helper()
	got, err := workflow.ReadRunArtifactsIn(context.Background(), w.a.DB, workflow.RunRef{ID: w.run.ID, ProjectID: w.run.ProjectID}, workflow.ArtifactSelection{Kinds: []string{kind}})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, a := range got {
		out[a.Name] = a.Body
	}
	return out
}

// An agent step on an AppTown with a worker runs there as one invocation, in
// the attempt's workspace, with the request AppTown admitted: the
// worker's image, the /apptown/in inputs, the declared transcript. Its work
// is published to the change's branch, its written files and typed output
// are recorded with the pass, its usage once under the invocation, its
// transcript kept and named by the attempt's receipt, and then the
// invocation and the workspace are released. Nothing runs on AppTown's
// own machine.
func TestIntegrationAnAgentStepRunsOnTheWorkerAsOneInvocation(t *testing.T) {
	t.Parallel()
	w := newAgentWorkerWorld(t, map[string]any{"output": map[string]any{"kind": "report", "name": "summary"}},
		map[string]string{"fake-pi.sh": `echo "made on the worker" > made.txt; test -x /apptown/in/bin/apptown && test -f /apptown/in/steering.md && echo inputs > inputs.txt; say "I made it"`})
	w.execute(t, w.a)
	status, ev := stepStatus(t, w.a, w.step.ID)
	if status != workflow.StatusPass {
		t.Fatalf("the step is %s: %v", status, ev)
	}
	if got := w.branchFile(t, "made.txt"); got != "made on the worker" {
		t.Fatalf("the published branch holds %q", got)
	}
	if got := w.branchFile(t, "inputs.txt"); got != "inputs" {
		t.Fatalf("the CLI and steering were not at their /apptown/in paths: %q", got)
	}
	invocations := w.invocations(t, 1)
	if len(invocations) != 1 || invocations[0].Invocation != "agent" || invocations[0].Observed != tasks.Settled || !invocations[0].Released ||
		!invocations[0].WorkspaceReleased || invocations[0].Workspace != fmt.Sprintf("step-%d-a1", w.step.ID) || invocations[0].Backend != "worker at "+w.cfg.Worker.URL {
		t.Fatalf("the attempt's invocations: %+v", invocations)
	}
	spent, found, err := w.a.Usage.Ledger.Observed(context.Background(), invocations[0].Operation)
	if err != nil || !found || spent.Totals.Total != 15 || spent.Incomplete != "" {
		t.Fatalf("the invocation's usage: %+v %v %v", spent, found, err)
	}
	receipt, raw := w.transcript(t, 1)
	if receipt.Session != fmt.Sprintf("step-%d-a1", w.step.ID) || !bytes.Contains(raw, []byte("I made it")) || !bytes.HasPrefix(raw, []byte(`{"type":"session"`)) {
		t.Fatalf("the kept transcript: %+v %s", receipt, raw)
	}
	if ev["worker"] != "worker at "+w.cfg.Worker.URL || ev["image"] != w.image || ev["piVersion"] != fakePiVersion || ev["scmAccess"] != "" || ev["output"] != "I made it" {
		t.Fatalf("the evidence: %v", ev)
	}
	files := w.artifacts(t, "file")
	if _, source := files["fake-pi.sh"]; files["made.txt"] != "made on the worker\n" || source {
		t.Fatalf("the written files: %v", files)
	}
	if reports := w.artifacts(t, "report"); reports["summary"] != "I made it" {
		t.Fatalf("the typed output: %v", reports)
	}
}

// An empty turn (no text, no error, the process exited) earns one more
// invocation, agent-retry, in the same workspace and session, reserved only
// after the first exited. Each invocation's usage is recorded once, under
// its own operation, and the transcript receipt names the later capture.
func TestIntegrationAnEmptyTurnOnTheWorkerIsRetriedOnceAfterItExits(t *testing.T) {
	t.Parallel()
	w := newAgentWorkerWorld(t, nil, map[string]string{"fake-pi.sh": `if [ "$PI_INVOCATION" = 1 ]; then silent; else say "second time $PI_INVOCATION"; fi`})
	w.execute(t, w.a)
	status, ev := stepStatus(t, w.a, w.step.ID)
	if status != workflow.StatusPass || ev["output"] != "second time 2" {
		t.Fatalf("the step is %s: %v", status, ev)
	}
	invocations := w.invocations(t, 1)
	if len(invocations) != 2 || invocations[0].Invocation != "agent" || invocations[1].Invocation != "agent-retry" ||
		invocations[0].Workspace != invocations[1].Workspace || invocations[0].ExitCode == nil || *invocations[0].ExitCode != 0 {
		t.Fatalf("the attempt's invocations: %+v", invocations)
	}
	for _, inv := range invocations {
		if inv.Observed != tasks.Settled || !inv.Released || !inv.WorkspaceReleased {
			t.Fatalf("invocation %s: %+v", inv.Invocation, inv)
		}
		spent, found, err := w.a.Usage.Ledger.Observed(context.Background(), inv.Operation)
		if err != nil || !found || spent.Totals.Total == 0 {
			t.Fatalf("invocation %s's usage: %+v %v %v", inv.Invocation, spent, found, err)
		}
	}
	_, raw := w.transcript(t, 1)
	if !bytes.Contains(raw, []byte("second time 2")) || bytes.Count(raw, []byte(`"type":"session"`)) != 1 {
		t.Fatalf("the kept transcript: %s", raw)
	}
}

// An agent step declaring private Docker and a service runs on the worker:
// it reaches its own daemon as dind and the service by name, its
// empty turn is not run again (as on AppTown's host), its evidence says
// the private Docker is unqualified, and its release leaves nothing of it.
func TestIntegrationAnAgentStepWithPrivateDockerRunsOnTheWorker(t *testing.T) {
	t.Parallel()
	w := newAgentWorkerWorld(t, map[string]any{"docker": true, "services": []map[string]any{{"name": "cache", "image": "redis:7-alpine", "health": "redis-cli ping"}}},
		map[string]string{"fake-pi.sh": `nc -z dind 2375 && echo daemon > daemon.txt; nc -z cache 6379 && echo cache > cache.txt; echo "$DOCKER_HOST" > host.txt; silent`})
	w.execute(t, w.a)
	_, ev := stepStatus(t, w.a, w.step.ID)
	for name, want := range map[string]string{"daemon.txt": "daemon", "cache.txt": "cache", "host.txt": "tcp://dind:2375"} {
		if got := w.branchFile(t, name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	if private, _ := ev["privateDocker"].(string); !strings.HasPrefix(private, "unqualified") {
		t.Errorf("the evidence: %v", ev)
	}
	invocations := w.invocations(t, 1)
	if len(invocations) != 1 || !invocations[0].Released {
		t.Fatalf("an empty turn with private Docker was run again, or not released: %+v", invocations)
	}
	// This worker's: tests running beside it have invocations of the same
	// key on workers of their own.
	out, err := dockertest.CombinedOutput("ps", "-a", "--filter", "label=apptown.class=private-docker", "--filter", "label="+w.worker.CheckOwnerLabel(),
		"--filter", "label=apptown.task="+invocations[0].Operation, "--format", "{{.Names}}")
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Errorf("released, the invocation left %s %v", out, err)
	}
}

// kvKind is a catalog kind whose service is image, reached at
// kv://kv/{flavour} in the given scheme.
func kvKind(image, scheme string) string {
	return "capability: kv\ntitle: Key-value\ndefaults:\n  flavour: plain\nlocal:\n  image: " + image + "\n  health: redis-cli ping\n" +
		"  env:\n    KV_FLAVOUR: \"{flavour}\"\n  inject:\n    KV_URL: \"" + scheme + "://{host}/{flavour}\"\n"
}

// A need with parameters is frozen in the invocation's kept request. A
// AppTown stopped after the worker made the invocation, before it recorded
// that, recovers by sending the kept request again, after the folder's
// declaration and the catalog have both changed: the recovered invocation
// runs with the frozen parameter and the frozen service (the worker would
// refuse a different request under its key).
func TestIntegrationARecoveredInvocationKeepsItsFrozenNeed(t *testing.T) {
	t.Parallel()
	const app = "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  kv:\n    flavour: "
	w := newAgentWorkerWorld(t, map[string]any{"needs": true}, map[string]string{
		".apptown/app.yaml": app + "sourdough\n",
		"fake-pi.sh":        `sleep 3; nc -z kv 6379 && echo reached > kv.txt; echo "$KV_URL" > url.txt; say "done"`,
	})
	if _, err := w.a.Dependencies.Save(context.Background(), kvKind("redis:7-alpine", "redis"), "tester"); err != nil {
		t.Fatal(err)
	}
	w.a.tasks.AbandonAt = func(stage string) bool { return stage == "created" }
	w.execute(t, w.a)
	if invocations := w.invocations(t, 1); len(invocations) != 1 || invocations[0].ProviderID != "" {
		t.Fatalf("before the restart: %+v", invocations)
	}
	// The folder now declares another parameter, and the catalog another
	// service and connection.
	folder := requireLocation(t)(w.source.Workspace(w.a.cfg.dataDir))
	if err := os.WriteFile(filepath.Join(folder, ".apptown", "app.yaml"), []byte(app+"rye\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, folder, "-c", "user.name=alice", "-c", "user.email=alice@example.org", "commit", "-q", "-am", "another flavour")
	if _, err := w.a.Dependencies.Save(context.Background(), kvKind(stock.Images().DF, "changed"), "tester"); err != nil {
		t.Fatal(err)
	}
	b := w.restart(t)
	awaitInvocationStarted(t, w)
	// Recovered: the folder's own change is undone, so publishing the
	// result is not refused for it.
	gittest.Run(t, folder, "reset", "-q", "--hard", "HEAD~1")
	settled := awaitStep(t, b, w.step.ID, workflow.StatusPass)
	if got := w.branchFile(t, "url.txt"); got != "redis://kv/sourdough" {
		t.Errorf("the recovered invocation's connection: %q (evidence %s)", got, settled.Evidence)
	}
	if got := w.branchFile(t, "kv.txt"); got != "reached" {
		t.Errorf("the recovered invocation did not reach the frozen service: %q", got)
	}
	if invocations := w.invocations(t, 1); len(invocations) != 1 {
		t.Errorf("invocations: %+v", invocations)
	}
}

// An AppTown restarted while an invocation runs on the worker finds it again
// (RecoverAgent): it waits for it, reads it and settles the attempt once.
// Pi ran once, and one invocation is recorded.
func TestIntegrationARestartedAppTownReattachesToARunningAgentInvocation(t *testing.T) {
	t.Parallel()
	w := newAgentWorkerWorld(t, nil, map[string]string{"fake-pi.sh": `sleep 2; say "invocation $PI_INVOCATION"; echo late > late.txt`})
	w.a.tasks.AbandonAt = func(stage string) bool { return stage == "started" }
	w.execute(t, w.a)
	if invocations := w.invocations(t, 1); len(invocations) != 1 || invocations[0].Observed != tasks.Started {
		t.Fatalf("before the restart: %+v", invocations)
	}
	b := w.restart(t)
	settled := awaitStep(t, b, w.step.ID, workflow.StatusPass)
	var ev map[string]any
	_ = json.Unmarshal(settled.Evidence, &ev)
	if ev["output"] != "invocation 1" {
		t.Fatalf("the step's evidence: %v", ev)
	}
	if got := w.branchFile(t, "late.txt"); got != "late" {
		t.Fatalf("the published branch holds %q", got)
	}
	awaitAttemptReleased(t, w, 1)
	if invocations := w.invocations(t, 1); len(invocations) != 1 || invocations[0].Invocation != "agent" {
		t.Fatalf("after the restart: %+v", invocations)
	}
}

// awaitAttemptReleased waits until every invocation of the attempt is released,
// and its workspace.
func awaitAttemptReleased(t *testing.T, w *agentWorkerWorld, attempt int64) {
	t.Helper()
	var invocations []tasks.Attempt
	if !poll.Until(context.Background(), 30*time.Second, 20*time.Millisecond, func() bool {
		invocations = w.invocations(t, attempt)
		done := len(invocations) > 0
		for _, inv := range invocations {
			done = done && inv.Released && inv.WorkspaceReleased
		}
		return done
	}) {
		t.Fatalf("not released: %+v", invocations)
	}
}

// awaitInvocation waits until the attempt's newest invocation is recorded
// started on the worker.
func awaitInvocationStarted(t *testing.T, w *agentWorkerWorld) tasks.Attempt {
	t.Helper()
	var invocations []tasks.Attempt
	if !poll.Until(context.Background(), 30*time.Second, 20*time.Millisecond, func() bool {
		invocations = w.invocations(t, 1)
		return len(invocations) > 0 && invocations[len(invocations)-1].Observed == tasks.Started
	}) {
		t.Fatal("the invocation never started")
	}
	return invocations[len(invocations)-1]
}

// A worker that says an invocation no longer exists has lost the step's
// work: the attempt is an error saying so, at once, not after its deadline,
// and nothing is retried by itself. Its waits go unanswered from the
// removal on: removing a running container also ends its wait with the
// kill's exit (137), which, seen first, is an exit and not a loss.
func TestIntegrationAnInvocationTheWorkerLostIsAnError(t *testing.T) {
	t.Parallel()
	w := newAgentWorkerWorld(t, nil, map[string]string{"fake-pi.sh": `sleep 60; say "never"`})
	done := make(chan struct{})
	go func() { defer close(done); w.execute(t, w.a) }()
	inv := awaitInvocationStarted(t, w)
	w.waitsUnanswered.Store(true)
	if out, err := dockertest.CombinedOutput("rm", "-f", "apptown-task-"+w.namespace+"-"+inv.Operation); err != nil {
		t.Fatalf("removing the invocation's container: %v %s", err, out)
	}
	select {
	case <-done:
	case <-time.After(45 * time.Second):
		t.Fatal("the lost invocation was waited out")
	}
	status, ev := stepStatus(t, w.a, w.step.ID)
	if reason, _ := ev["reason"].(string); status != workflow.StatusError || !strings.Contains(reason, "the worker lost this step's work") {
		t.Fatalf("the step is %s: %v", status, ev)
	}
	if invocations := w.invocations(t, 1); len(invocations) != 1 {
		t.Fatalf("invocations: %+v", invocations)
	}
}

// A worker that stops answering mid-invocation is waited for until the
// invocation's deadline, then asked to stop; a stop it does not confirm
// leaves the invocation's outcome unknown: the attempt is an error, the
// invocation is held (recorded before the step's error) and its workspace
// is not released, even by the release pass once the worker answers again.
func TestIntegrationAnUnconfirmedStopHoldsTheWorkspace(t *testing.T) {
	t.Parallel()
	w := newAgentWorkerWorld(t, map[string]any{"timeoutMinutes": 1}, map[string]string{"fake-pi.sh": `sleep 600`})
	done := make(chan struct{})
	go func() { defer close(done); w.execute(t, w.a) }()
	awaitInvocationStarted(t, w)
	w.down.Store(true)
	select {
	case <-done:
	case <-time.After(3 * time.Minute):
		t.Fatal("the attempt never ended")
	}
	status, ev := stepStatus(t, w.a, w.step.ID)
	if reason, _ := ev["reason"].(string); status != workflow.StatusError || !strings.Contains(reason, "outcome is unknown") {
		t.Fatalf("the step is %s: %v", status, ev)
	}
	w.down.Store(false)
	if _, err := w.a.tasks.ReleaseSettled(context.Background()); err != nil {
		t.Fatal(err)
	}
	invocations := w.invocations(t, 1)
	if len(invocations) != 1 || invocations[0].Observed != tasks.Held ||
		invocations[0].Released || invocations[0].WorkspaceReleased {
		t.Fatalf("the held invocation: %+v", invocations)
	}
}

// An invocation with private Docker that times out is stopped whole, its
// private daemon included. When the worker cannot confirm that daemon
// stopped, the stop is not confirmed: the attempt is an error whose
// outcome is unknown, nothing the agent wrote is published, and the
// invocation and its workspace are held, even by the release pass.
func TestIntegrationATimedOutInvocationWhosePrivateDockerWillNotStopIsHeld(t *testing.T) {
	t.Parallel()
	// The real Docker, but every stop is refused.
	wrapper := stopRefused{docker.CLI{Binary: "docker"}}
	w := newWorkerStepWorld(t, schema.Step{Name: "build", Uses: "agent/pi", With: map[string]any{"prompt": "do the work", "executor": "docker", "docker": true, "timeoutMinutes": 1}},
		map[string]string{"fake-pi.sh": `echo "written before the timeout" > made.txt; sleep 600`},
		func(s *worker.Server) {
			s.Docker = wrapper
			// What the refused stop leaves of this worker's private Docker.
			t.Cleanup(func() {
				out, _ := dockertest.Output("ps", "-aq", "--filter", "label="+s.CheckOwnerLabel())
				for _, id := range strings.Fields(string(out)) {
					_ = dockertest.Run("rm", "-f", "-v", id)
				}
				out, _ = dockertest.Output("network", "ls", "-q", "--filter", "label="+s.CheckOwnerLabel())
				for _, id := range strings.Fields(string(out)) {
					_ = dockertest.Run("network", "rm", id)
				}
			})
		})
	w.execute(t, w.a)
	status, ev := stepStatus(t, w.a, w.step.ID)
	if reason, _ := ev["reason"].(string); status != workflow.StatusError || !strings.Contains(reason, "did not confirm its stop") {
		t.Fatalf("the step is %s: %v", status, ev)
	}
	hub := requireLocation(t)(w.source.Repository(w.a.cfg.dataDir))
	if gittest.Command("-C", hub, "cat-file", "-e", scm.Ref(t.Context(), hub, "refs/heads/"+w.source.Branch)+":made.txt").Run() == nil {
		t.Fatal("the agent's work was published to the branch")
	}
	if candidates := gittest.Run(t, hub, "for-each-ref", "refs/apptown/candidates"); candidates != "" {
		t.Fatalf("a candidate was recorded: %s", candidates)
	}
	if _, err := w.a.tasks.ReleaseSettled(context.Background()); err != nil {
		t.Fatal(err)
	}
	invocations := w.invocations(t, 1)
	if len(invocations) != 1 || invocations[0].Observed != tasks.Held || invocations[0].Released || invocations[0].WorkspaceReleased {
		t.Fatalf("the held invocation: %+v", invocations)
	}
}

// A wait the worker refuses (not a timeout, and not a worker that cannot
// be reached) for an invocation it still holds, whose stop it then does not
// confirm, leaves the invocation's outcome unknown: it is held, recorded
// before the step's error, and its workspace is not released.
func TestIntegrationARefusedWaitWithAnUnconfirmedStopHoldsTheWorkspace(t *testing.T) {
	t.Parallel()
	w := newAgentWorkerWorld(t, nil, map[string]string{"fake-pi.sh": `sleep 600`})
	w.waitRefused.Store(true)
	w.execute(t, w.a)
	status, ev := stepStatus(t, w.a, w.step.ID)
	if reason, _ := ev["reason"].(string); status != workflow.StatusError || !strings.Contains(reason, "waiting for the invocation") || !strings.Contains(reason, "outcome is unknown") {
		t.Fatalf("the step is %s: %v", status, ev)
	}
	w.waitRefused.Store(false)
	if _, err := w.a.tasks.ReleaseSettled(context.Background()); err != nil {
		t.Fatal(err)
	}
	invocations := w.invocations(t, 1)
	if len(invocations) != 1 || invocations[0].Observed != tasks.Held ||
		invocations[0].Released || invocations[0].WorkspaceReleased {
		t.Fatalf("the held invocation: %+v", invocations)
	}
}

// Recording an invocation's history is part of settling it, for an agent
// step and a review alike: when the receipt cannot be written (after its
// tries), the attempt is neither settled nor released and its invocation
// stays collecting; a restarted AppTown reads it again, records the history,
// then settles the step.
func TestIntegrationAHistoryReceiptThatCannotBeWrittenIsRecordedAfterARestart(t *testing.T) {
	t.Parallel()
	for name, world := range map[string]func(t *testing.T) *agentWorkerWorld{
		"an agent": func(t *testing.T) *agentWorkerWorld {
			return newAgentWorkerWorld(t, nil, map[string]string{"fake-pi.sh": `say "done"`})
		},
		"a review": func(t *testing.T) *agentWorkerWorld {
			return newReviewWorkerWorld(t, nil, map[string]string{"fake-pi.sh": reviewPass + `say "done"`})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := world(t)
			w.a.agents.Settle = workflow.Settlement{Tries: 1}
			dbtest.FailNextStatement(t, w.a.DB, "-- name: RecordAgentTranscript")
			w.execute(t, w.a)
			if status, ev := stepStatus(t, w.a, w.step.ID); status != workflow.StatusRunning {
				t.Fatalf("the step settled without its history recorded: %s %v", status, ev)
			}
			if receipt, err := workflow.StepTranscriptIn(context.Background(), w.a.DB, workflow.RunRef{ID: w.run.ID, ProjectID: w.run.ProjectID}, w.step.ID, 1); err != nil || receipt.Recorded {
				t.Fatalf("the receipt: %+v %v", receipt, err)
			}
			if inv := w.invocations(t, 1); len(inv) != 1 || inv[0].Observed != tasks.Collecting || inv[0].Released || inv[0].WorkspaceReleased {
				t.Fatalf("the invocation: %+v", inv)
			}
			b := w.restart(t)
			awaitStep(t, b, w.step.ID, workflow.StatusPass)
			if receipt, raw := w.transcript(t, 1); receipt.Digest == "" || !bytes.Contains(raw, []byte("done")) {
				t.Fatalf("the history after the restart: %+v", receipt)
			}
			awaitAttemptReleased(t, w, 1)
		})
	}
}

// An invocation's answer and usage are its events file, never its log: a
// log the worker no longer holds the start of (a stand-in for one whose
// output was lost) leaves the step passing with its usage recorded whole.
//
// Kills: the result read from the task's log.
func TestIntegrationALogCutShortLeavesTheAnswerAndUsageWhole(t *testing.T) {
	t.Parallel()
	w := newAgentWorkerWorld(t, nil, map[string]string{"fake-pi.sh": `say "done"`})
	w.truncate.Store(true)
	w.execute(t, w.a)
	status, ev := stepStatus(t, w.a, w.step.ID)
	if reason, _ := ev["usageIncomplete"].(string); status != workflow.StatusPass || reason != "" || ev["output"] != "done" {
		t.Fatalf("the step is %s: %v", status, ev)
	}
	inv := w.invocations(t, 1)[0]
	if spent, found, err := w.a.Usage.Ledger.Observed(context.Background(), inv.Operation); err != nil || !found || spent.Incomplete != "" {
		t.Fatalf("the invocation's usage: %+v %v %v", spent, found, err)
	}
}

// Session history on a worker is kept by AppTown: two consecutive
// continue retries, each after the earlier attempt's workspace was released
// and one after an AppTown restart, resume exactly the bytes the earlier
// receipt names, and each receipt still reads its own bytes afterwards. A
// second file in the session folder naming the same session is not what is
// kept.
func TestIntegrationContinuedSessionsOnTheWorkerReadTheirOwnTranscripts(t *testing.T) {
	t.Parallel()
	w := newAgentWorkerWorld(t, nil, map[string]string{"fake-pi.sh": `
printf '{"type":"session","version":3,"id":"%s"}\n{"competing":true}\n' "$PI_SESSION" > /apptown/state/sessions/9999-competing_$PI_SESSION.jsonl
say "turn: $PI_PROMPT"`})
	w.execute(t, w.a)
	if status, ev := stepStatus(t, w.a, w.step.ID); status != workflow.StatusPass {
		t.Fatalf("the first attempt is %s: %v", status, ev)
	}
	awaitAttemptReleased(t, w, 1)
	first, firstRaw := w.transcript(t, 1)
	if bytes.Contains(firstRaw, []byte("competing")) || !bytes.Contains(firstRaw, []byte("turn: do the work")) {
		t.Fatalf("the first transcript: %s", firstRaw)
	}

	if _, err := w.a.Work.RetryStep(asPerson("alice"), workflow.RetryCommand{StepID: w.step.ID, Mode: workflow.RetryContinue, Feedback: "second note"}); err != nil {
		t.Fatal(err)
	}
	w.execute(t, w.a)
	if status, ev := stepStatus(t, w.a, w.step.ID); status != workflow.StatusPass {
		t.Fatalf("the second attempt is %s: %v", status, ev)
	}
	awaitAttemptReleased(t, w, 2)
	second, secondRaw := w.transcript(t, 2)
	if second.Session != first.Session || !bytes.HasPrefix(secondRaw, firstRaw) || !bytes.Contains(secondRaw[len(firstRaw):], []byte("second note")) {
		t.Fatalf("the second transcript does not continue the first:\n%s\n---\n%s", firstRaw, secondRaw)
	}

	b := w.restartForRetry(t)
	if _, err := b.Work.RetryStep(asPerson("alice"), workflow.RetryCommand{StepID: w.step.ID, Mode: workflow.RetryContinue, Feedback: "third note"}); err != nil {
		t.Fatal(err)
	}
	w.execute(t, b)
	if status, ev := stepStatus(t, b, w.step.ID); status != workflow.StatusPass {
		t.Fatalf("the third attempt is %s: %v", status, ev)
	}
	third, thirdRaw := w.transcript(t, 3)
	if third.Session != first.Session || !bytes.HasPrefix(thirdRaw, secondRaw) || !bytes.Contains(thirdRaw[len(secondRaw):], []byte("third note")) {
		t.Fatalf("the third transcript does not continue the second:\n%s\n---\n%s", secondRaw, thirdRaw)
	}
	// Each receipt still reads its own bytes.
	for attempt, want := range map[int64][]byte{1: firstRaw, 2: secondRaw} {
		if _, raw := w.transcript(t, attempt); !bytes.Equal(raw, want) {
			t.Fatalf("attempt %d's receipt reads other bytes", attempt)
		}
	}
}

// The step's output on a worker: the declared output file, when present
// and not blank (even one the repository ignores, or one left unchanged);
// else the single markdown file the attempt wrote; a declared output file
// that cannot be read, or is too large, is an error, never "no output".
func TestIntegrationAnAgentsOutputOnTheWorker(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		output map[string]any
		files  map[string]string
		status workflow.StepState
		report string
		reason string
	}{
		"an ignored output file": {output: map[string]any{"kind": "report", "name": "r", "file": "out/report.md"},
			files:  map[string]string{".gitignore": "out/\n", "fake-pi.sh": `mkdir -p out; printf '# Report\n\nfrom the file\n' > out/report.md; say "wrote it"`},
			status: workflow.StatusPass, report: "# Report\n\nfrom the file\n"},
		"an unchanged output file": {output: map[string]any{"kind": "report", "name": "r", "file": "REPORT.md"},
			files:  map[string]string{"REPORT.md": "# Kept\n", "fake-pi.sh": `say "nothing new"`},
			status: workflow.StatusPass, report: "# Kept\n"},
		"files only": {output: map[string]any{"kind": "report", "name": "r"},
			files:  map[string]string{"fake-pi.sh": `silent; printf '# Notes\n' > notes.md`},
			status: workflow.StatusPass, report: "# Notes\n"},
		"an oversized output file": {output: map[string]any{"kind": "report", "name": "r", "file": "big.md"},
			files:  map[string]string{"fake-pi.sh": `head -c 1100000 /dev/zero | tr '\000' a > big.md; say "wrote a lot"`},
			status: workflow.StatusError, reason: "the output file could not be read: it is 1100000 bytes"},
		"an unreadable output file": {output: map[string]any{"kind": "report", "name": "r", "file": "report.md"},
			files:  map[string]string{"fake-pi.sh": `ln -s /etc/hostname report.md; say "linked it"`},
			status: workflow.StatusError, reason: "the output file could not be read: the worker refused to read it"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newAgentWorkerWorld(t, map[string]any{"output": c.output}, c.files)
			w.execute(t, w.a)
			status, ev := stepStatus(t, w.a, w.step.ID)
			if reason, _ := ev["reason"].(string); status != c.status || !strings.Contains(reason, c.reason) {
				t.Fatalf("the step is %s: %v", status, ev)
			}
			if c.report != "" {
				if got := w.artifacts(t, "report")["r"]; got != c.report {
					t.Fatalf("the typed output is %q", got)
				}
			}
			if _, ok := w.artifacts(t, "file")["out/report.md"]; ok {
				t.Fatal("an ignored file was recorded as written")
			}
		})
	}
}

// A nonzero agent exit is an error, not retried. The partial work a failed process left is kept as the
// attempt's candidate before the error is settled.
func TestReviewS5NonzeroAgentExitIsErrorWithoutRetry(t *testing.T) {
	for name, script := range map[string]string{"with text": `echo partial > partial.txt; say "partial result"; exit 7`, "empty": `silent; exit 7`} {
		t.Run(name, func(t *testing.T) {
			w := newAgentWorkerWorld(t, nil, map[string]string{"fake-pi.sh": script})
			w.execute(t, w.a)
			if status, ev := stepStatus(t, w.a, w.step.ID); status != workflow.StatusError {
				t.Errorf("exit 7 should be an error: %s %v", status, ev)
			}
			if inv := w.invocations(t, 1); len(inv) != 1 {
				t.Errorf("exit 7 must not earn empty-turn retry: %d invocations", len(inv))
			}
			if _, ev := stepStatus(t, w.a, w.step.ID); name == "with text" && ev["result"] == nil {
				t.Errorf("the failed attempt's work was not kept: %v", ev)
			}
		})
	}
}

// Pi reads the named session file: a second file naming the session is
// not the history, and does not make it unavailable.
func TestReviewS5AmbiguousWorkerHistoryIsUnavailable(t *testing.T) {
	w := newAgentWorkerWorld(t, nil, map[string]string{"fake-pi.sh": `say "done"; cp "$session" /apptown/state/sessions/9999-competing.jsonl`})
	w.execute(t, w.a)
	receipt, err := workflow.StepTranscriptIn(context.Background(), w.a.DB, workflow.RunRef{ID: w.run.ID, ProjectID: w.run.ProjectID}, w.step.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Recorded || receipt.Unavailable != "" || receipt.Digest == "" {
		t.Fatalf("the named file's history must be kept, got %+v", receipt)
	}
}

func TestReviewS5ReceiptWriteFailureDoesNotSettleWithoutHistoryState(t *testing.T) {
	w := newAgentWorkerWorld(t, nil, map[string]string{"fake-pi.sh": `say "done"`})
	dbtest.FailNextStatement(t, w.a.DB, "-- name: RecordAgentTranscript")
	w.execute(t, w.a)
	receipt, err := workflow.StepTranscriptIn(context.Background(), w.a.DB, workflow.RunRef{ID: w.run.ID, ProjectID: w.run.ProjectID}, w.step.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	status, _ := stepStatus(t, w.a, w.step.ID)
	inv := w.invocations(t, 1)
	if !receipt.Recorded && status == workflow.StatusPass && inv[0].Released {
		t.Fatalf("settled and released without a digest or unavailable receipt: %+v", receipt)
	}
	// Extended: the write is a settlement obligation, tried again, so one
	// failed statement still ends with the receipt recorded and the step
	// passed.
	if !receipt.Recorded || receipt.Digest == "" || status != workflow.StatusPass {
		t.Fatalf("one failed receipt write was not tried again: %+v %s", receipt, status)
	}
}

// Simulate an upgraded run: its start receipt and valid folder transcript
// predate agent_transcripts. Raw deletion is only to construct that era.
// The restarted AppTown imports the folder's file at startup, then the
// continue runs on the worker.
func TestReviewS5OldRunContinuesItsFolderTranscriptOnWorker(t *testing.T) {
	w := newAgentWorkerWorld(t, nil, map[string]string{"fake-pi.sh": `say "done"`})
	w.execute(t, w.a)
	receipt, raw := w.transcript(t, 1)
	// sql-guarantee: A run from before agent_transcripts has a start receipt and no record; no operation removes a record, so it is deleted to construct that era.
	if _, err := w.a.DB.Exec("DELETE FROM agent_transcripts WHERE step_id=$1", w.step.ID); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(requireLocation(t)(w.source.Workspace(w.a.cfg.dataDir)), ".apptown", "sessions")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-10-01T00-00-00-000Z_"+receipt.Session+".jsonl"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	b := w.restartForRetry(t)
	if imported, got := w.transcript(t, 1); imported.Digest != receipt.Digest || string(got) != string(raw) {
		t.Fatalf("the import recorded %+v", imported)
	}
	if _, err := b.Work.RetryStep(asPerson("alice"), workflow.RetryCommand{StepID: w.step.ID, Mode: workflow.RetryContinue, Feedback: "again"}); err != nil {
		t.Fatal(err)
	}
	w.execute(t, b)
	if status, ev := stepStatus(t, b, w.step.ID); status != workflow.StatusPass {
		t.Fatalf("valid legacy folder history should continue: %s %v", status, ev)
	}
	if _, second := w.transcript(t, 2); !strings.HasPrefix(string(second), string(raw)) {
		t.Fatalf("the continue did not resume the imported history:\n%s", second)
	}
}

// stopRefused refuses every docker stop and passes the rest to the real
// Docker.
type stopRefused struct{ real docker.Client }

func (r stopRefused) Run(ctx context.Context, host string, c docker.Command) ([]byte, error) {
	if len(c.Args) > 0 && c.Args[0] == "stop" {
		return []byte("stop refused for the test\n"), errors.New("exit status 1")
	}
	return r.real.Run(ctx, host, c)
}
