package forge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/execfile"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/deploy/docker/dockerfake"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// workerCheckDocker is a worker's Docker for checks that answers as a real
// one would, enough for every check path: images (inspect, pull, build,
// save), and runs that write a clean SARIF report when a scanner asks. A
// run whose command says "exit 7" exits 7, as a failing test would, and a
// run given a mise configuration records it. A screenshot capture answers
// with two pictures of the home page and a named page that left the
// preview, or fails when a page is /shots-fail.
func workerCheckDocker(t *testing.T, calls string) string {
	t.Helper()
	dir := t.TempDir()
	have := filepath.Join(dir, "have")
	if err := os.MkdirAll(have, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "docker")
	script := `#!/bin/sh
echo "worker $@" >> "` + calls + `"
[ "$1" = "--config" ] && shift 2
case "$*" in inspect*OOMKilled*) echo false; exit 0;; "rm -f "*) exit 0;; esac
case "$1" in
image)
  [ "$2" = inspect ] || exit 0
  name=$(echo "$5" | tr '/:@' '___')
  [ -f "` + have + `/$name" ] || { echo "Error: No such image: $5" >&2; exit 1; }
  echo "\"sha256:$name\" {\"forge.go.version\":\"1.24.4\"}"; exit 0;;
pull) touch "` + have + `/$(echo "$2" | tr '/:@' '___')"; exit 0;;
build) touch "` + have + `/$(echo "$5" | tr '/:@' '___')"; exit 0;;
save) exit 0;;
inspect) echo "running healthy"; exit 0;;
run)
  prev=""; work=""
  for arg in "$@"; do
    case "$arg" in *:/work|*:/work:ro) [ "$prev" = "-v" ] && work="${arg%%:/work*}";; esac
    case "$arg" in *:/etc/mise/config.toml:ro) [ "$prev" = "-v" ] && { cat "${arg%%:/etc/mise/config.toml:ro}"; echo END; } >> "` + calls + `";; esac
    prev="$arg"
  done
  for arg in "$@"; do
    case "$arg" in */shots-fail) echo "the browser crashed" >&2; exit 2;; esac
  done
  for arg in "$@"; do
    case "$arg" in /work/.apptown/*.sarif) mkdir -p "$work/.apptown"; echo '{"version":"2.1.0","runs":[]}' > "$work/${arg#/work/}";; esac
    case "$arg" in /work/.apptown/screenshots.json) mkdir -p "$work/.apptown"; printf '%s' '` + shotsAnswer + `' > "$work/.apptown/screenshots.json";; esac
  done
  for arg in "$@"; do
    case "$arg" in *"exit 7"*) exit 7;; esac
  done
  exit 0;;
esac
exit 0
`
	if err := execfile.Write(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary
}

// onWorkerWithATripwire moves a's checks to an in-process worker and gives
// this machine a Docker that fails any command and records every call, as
// the calls file's "local" lines, beside the worker's. Container listings
// answer "none": taking a change asks who writes its folder, which is not a
// check. The worker's agent image is named from the repository's context.
func onWorkerWithATripwire(t *testing.T, a *App) (calls string) {
	t.Helper()
	calls = filepath.Join(t.TempDir(), "calls")
	ca := workertest.NewCA(t)
	srv := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: t.TempDir(),
		Checks: execution.Runner{DockerBinary: workerCheckDocker(t, calls)}})
	a.cfg.worker = &execution.WorkerTasks{URL: srv.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	a.cfg.agentContext = "../../deploy/apptown-agent"
	localDockerTripwire(t, a, calls)
	recomposeChecks(t, a)
	return calls
}

// localDockerTripwire gives this machine a Docker, for its process runner
// and its Docker client, that fails any command and records every call as
// a "local" line in calls. Container listings answer "none": taking a
// change asks who writes its folder, which runs nothing.
func localDockerTripwire(t *testing.T, a *App, calls string) {
	t.Helper()
	local := filepath.Join(t.TempDir(), "local-docker")
	if err := execfile.Write(local, []byte("#!/bin/sh\necho \"local $@\" >> "+calls+"\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.processRunner = execution.Runner{DockerBinary: local}
	refused := dockerfake.New()
	refused.SetReply(func(args []string) ([]byte, error, bool) {
		f, _ := os.OpenFile(calls, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
		f.WriteString("local " + strings.Join(args, " ") + "\n")
		f.Close()
		if len(args) > 0 && args[0] == "ps" {
			return nil, nil, true
		}
		return nil, os.ErrPermission, true
	})
	a.docker.Client = refused
}

// recomposeChecks composes a's checks and Work from its configuration
// again, as a start with that configuration does.
func recomposeChecks(t *testing.T, a *App) {
	t.Helper()
	a.evaluator = verification.NewEffects(checksFor(a).Run, runtimeFor(a), a.inference)
	a.scanners = scannersFor(a)
	a.scanSteps = scanStepsFor(a)
	a.verification = changesFor(a)
	a.guidance = guidanceFor(a)
	rebuildWork(t, a, func(*workflow.Dependencies) {})
}

// onlyTheWorker says every check ran on the worker: the calls it expects
// reached it, and this machine's Docker was asked nothing but who writes a
// change's folder.
func onlyTheWorker(t *testing.T, calls string, want ...string) {
	t.Helper()
	recorded, _ := os.ReadFile(calls)
	got := string(recorded)
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("the worker was not asked for %q:\n%s", w, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "local ") && !strings.HasPrefix(line, "local ps ") {
			t.Fatalf("a check touched this machine's Docker: %s\n%s", line, got)
		}
	}
}

// With a worker configured, no check runs on this machine.
// This machine's Docker fails any command; every check path goes to the
// worker: apptown check, a working run's guidance step, a landing's gates
// (guidance, a source scan, an image scan and the history scan), and the
// merge-only compile check through the shipping AppTown composes.
func TestNoCheckRunsOnThisMachineWhenAWorkerIsConfigured(t *testing.T) {
	t.Parallel()
	app := "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"
	t.Run("apptown check and a working run", func(t *testing.T) {
		t.Parallel()
		a, ch := step1ReviewGuidance(t, true, `[{"name":"source","evaluator":"sh","sh":"true"},{"name":"image","evaluator":"tool","tool":"self-check","severity":"high"}]`)
		attachScanner(t, a, "self-check", map[string]any{"command": []string{"python", "manage.py", "check"}, "target": "exec-image", "manual": true})
		attachScanner(t, a, "semgrep", map[string]any{"image": "semgrep:test", "command": []string{"semgrep", "--sarif", "--output", "/work/.apptown/semgrep.sarif", "/work"},
			"report": ".apptown/semgrep.sarif", "format": "sarif", "kind": "sast", "target": "source"})
		calls := onWorkerWithATripwire(t, a)
		ws := (workflow.Layout{DataDir: a.cfg.dataDir}).ChangeWorkspace(ch.ID)
		for name, body := range map[string]string{".apptown/app.yaml": app, "Dockerfile": "FROM scratch\n"} {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(ws, name)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		gittest.Run(t, ws, "add", "-f", ".")
		gittest.Run(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "an app")
		if out, err := a.verification.Check(a.asEngine(context.Background()), ch, verification.Selection{}); err != nil || out["ok"] != true {
			t.Fatalf("apptown check on the worker: %v %v", out, err)
		}
		run := step1ReviewRun(t, a, ch)
		if step := reviewExecute(t, a, run, "guidance"); step.Status != workflow.StatusPass {
			t.Fatalf("the guidance step on the worker: %s %s", step.Status, step.Evidence)
		}
		onlyTheWorker(t, calls, "worker build --label forge.class=check -t forge-conform:", "--init --name forge-check-", "python manage.py check", "semgrep --sarif")
	})
	t.Run("a landing's gates", func(t *testing.T) {
		t.Parallel()
		a := testApp(t)
		attachScanner(t, a, "self-check", map[string]any{"command": []string{"python", "manage.py", "check"}, "target": "exec-image", "manual": true})
		attachScanner(t, a, "semgrep", map[string]any{"image": "semgrep:test", "command": []string{"semgrep", "--sarif", "--output", "/work/.apptown/semgrep.sarif", "/work"},
			"report": ".apptown/semgrep.sarif", "format": "sarif", "kind": "sast", "target": "source"})
		attachScanner(t, a, "trivy", map[string]any{"image": "trivy:test", "command": []string{"image", "--input", "${IMAGE_TAR}", "--format", "sarif", "--output", "/work/.apptown/trivy.sarif"},
			"report": ".apptown/trivy.sarif", "format": "sarif", "kind": "vuln", "target": "image"})
		attachScanner(t, a, "secrets", map[string]any{"image": "gitleaks:test", "command": []string{"detect", "--source", "/work", "--report-path", "/work/.apptown/secrets.sarif"},
			"historyCommand": []string{"git", "/history", "--log-opts", "${HISTORY_RANGE}", "--report-path", "/work/.apptown/secrets.sarif"},
			"report":         ".apptown/secrets.sarif", "format": "sarif", "kind": "secret", "target": "source"})
		calls := onWorkerWithATripwire(t, a)
		run, _ := reviewLanding(t, a, []schema.Step{
			{Name: "guidance", Uses: "guidance/conformance"},
			{Name: "source", Uses: "scan", With: map[string]any{"scanner": "semgrep"}},
			{Name: "image", Uses: "scan", With: map[string]any{"scanner": "trivy"}},
			{Name: "history", Uses: "scan", With: map[string]any{"scanner": "secrets"}},
		}, map[string]string{".apptown/app.yaml": app, "Dockerfile": "FROM scratch\n"}, func(pid ids.ID) {
			adopt(t, a, pid, "worker-gates", 1, `[{"name":"source","evaluator":"sh","sh":"true"},{"name":"image","evaluator":"tool","tool":"self-check","severity":"high"}]`)
		})
		for _, name := range []string{"guidance", "source", "image", "history"} {
			if step := reviewExecute(t, a, run, name); step.Status != workflow.StatusPass {
				t.Fatalf("the landing's %s gate on the worker: %s %s", name, step.Status, step.Evidence)
			}
		}
		onlyTheWorker(t, calls, "python manage.py check", "semgrep --sarif", "worker save sha256:", "image --input /work/.apptown/image.tar", "git /history --log-opts")
	})
	t.Run("the merge-only compile check", func(t *testing.T) {
		t.Parallel()
		a := testApp(t)
		calls := onWorkerWithATripwire(t, a)
		run, _ := reviewLanding(t, a, []schema.Step{{Name: "deploy", Uses: "deploy"}}, map[string]string{"go.mod": "module example\n"}, nil)
		// A merge-only release compiles main as merged: the landing's work is
		// put on main here (raw Git: the test has no merge to run).
		repo := a.projectRepoPath("review-landing")
		if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gittest.Run(t, repo, "add", "go.mod")
		gittest.Run(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "merged")
		step := reviewExecute(t, a, run, "deploy")
		if step.Status != workflow.StatusPass {
			t.Fatalf("the merge-only release on the worker: %s %s", step.Status, step.Evidence)
		}
		if !strings.Contains(string(step.Evidence), `"cleanTreeBuild":"pass"`) {
			t.Fatalf("the merge-only release did not compile its tree: %s", step.Evidence)
		}
		image, err := worker.AgentImage(a.cfg.agentContext)
		if err != nil {
			t.Fatal(err)
		}
		onlyTheWorker(t, calls, "--init --name forge-check-", image+" sh -c", "go build ./...")
	})
}
