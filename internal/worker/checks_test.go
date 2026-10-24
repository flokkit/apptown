package worker_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execfile"
	"github.com/flokkit/apptown/stock"

	"github.com/flokkit/apptown/internal/gitexec/gittest"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// fakeCheckDocker is the worker's Docker for checks: `run` finds the tree
// and history mounts, records them, and does what the command says; `ps`
// and `rm` are recorded for the sweep.
func fakeCheckDocker(t *testing.T) (binary, calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	binary = filepath.Join(dir, "docker")
	script := `#!/bin/sh
echo "$@" >> "` + calls + `"
case "$1" in
ps) cat "` + dir + `/ps" 2>/dev/null; exit 0;;
rm) exit 0;;
inspect) case "$3" in *OOMKilled*) echo false;; *) echo "running healthy";; esac; exit 0;;
network) [ "$2" = ls ] && cat "` + dir + `/networks" 2>/dev/null; exit 0;;
run) case "$2" in -d) exit 0;; esac;;
*) exit 0;;
esac
# A container has none of the host's GIT_* variables.
for v in $(env | sed -n 's/^\(GIT_[A-Za-z0-9_]*\)=.*/\1/p'); do unset "$v"; done
prev=""; work=""; history=""
for arg in "$@"; do
  case "$arg" in *:/work|*:/work:ro) [ "$prev" = "-v" ] && work="${arg%%:/work*}";; esac
  case "$arg" in *:/history:ro) [ "$prev" = "-v" ] && history="${arg%%:/history:ro}";; esac
  prev="$arg"
done
last=""; for arg in "$@"; do last="$arg"; done
case "$last" in
findings) mkdir -p "$work/.apptown"; echo '{"found":1}' > "$work/.apptown/r.json"; cat "$work/src.txt"; exit 1;;
escape) mkdir -p "$work/.apptown"; ln -s /etc/passwd "$work/.apptown/r.json"; exit 0;;
history-*) git -C "$history" log --format=%s "${last#history-}"; exit 0;;
slow) sleep 5; exit 0;;
esac
exit 0
`
	if err := execfile.Write(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary, calls
}

func checkWorker(t *testing.T, binary string) (worker.Checks, *worker.Server) {
	t.Helper()
	ca := workertest.NewCA(t)
	state := t.TempDir()
	srv := &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: state, Checks: execution.Runner{DockerBinary: binary},
		ToolCache: filepath.Join(state, "tool-cache", "mise")}
	hs := workertest.StartServer(t, ca, srv)
	return worker.Checks{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}, srv
}

// A check runs on the worker from the tree it is sent, and answers as the
// local runner does: a normal exit's code, with no process error, the
// output, and the declared report carried back. A report that is a link is
// refused, never followed; a check past its timeout has timed out.
func TestACheckRunsOnTheWorkerAndAnswersLikeTheLocalRunner(t *testing.T) {
	t.Parallel()
	binary, calls := fakeCheckDocker(t)
	checks, srv := checkWorker(t, binary)
	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "src.txt"), []byte("the staged tree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(cmd string, timeout time.Duration) execution.Outcome {
		return checks.Run(context.Background(), execution.Request{Workspace: tree, Image: "scanner:test", Argv: []string{cmd},
			Network: "none", Timeout: timeout, Report: ".apptown/r.json", Name: "check-test"})
	}
	out := run("findings", time.Minute)
	if out.Termination != execution.Exited || out.ExitCode != 1 || out.RunErr != nil || string(out.Report) != "{\"found\":1}\n" || !strings.Contains(string(out.Output), "the staged tree") {
		t.Fatalf("a check's answer: %+v (%s)", out, out.Output)
	}
	if _, err := os.Stat(filepath.Join(tree, ".apptown", "r.json")); err == nil {
		t.Fatal("the check wrote into AppTown's tree: only its report comes back")
	}
	if out := run("escape", time.Minute); !out.Exited0() || out.ReportErr == nil || len(out.Report) != 0 {
		t.Fatalf("a report that links out of the tree: %+v", out)
	}
	if out := run("slow", time.Second); out.Termination != execution.TimedOut || out.ExitCode != -1 {
		t.Fatalf("a check past its timeout: %+v", out)
	}
	recorded, _ := os.ReadFile(calls)
	// Labelled as a check of this worker, so this worker's sweep, and no
	// other's, finds it.
	if !strings.Contains(string(recorded), "--network none") || !strings.Contains(string(recorded), "apptown.class=check") ||
		!strings.Contains(string(recorded), srv.CheckOwnerLabel()) || !strings.Contains(string(recorded), "apptown.deadline=") {
		t.Fatalf("the check container was not confined and labelled: %s", recorded)
	}
}

// A check's services run beside it on the worker, on a network of its own
// that the check joins (internal when the check has no egress), labelled as
// the check is so the sweep finds them, and are removed after it; the
// check's environment reaches it.
func TestACheckWithServicesRunsBesideThemOnTheWorker(t *testing.T) {
	t.Parallel()
	binary, calls := fakeCheckDocker(t)
	checks, srv := checkWorker(t, binary)
	out := checks.Run(context.Background(), execution.Request{Workspace: t.TempDir(), Image: "agent:test", Argv: []string{"make", "test"},
		Network: "none", Timeout: time.Minute, Name: "tests", Env: []string{"DATABASE_URL=postgres://app@postgres/app"},
		Services: []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16-alpine", Env: map[string]string{"POSTGRES_DB": "app"}, Health: "pg_isready"}}})
	if !out.Exited0() {
		t.Fatalf("the check with services: %+v (%s)", out, out.Output)
	}
	recorded, _ := os.ReadFile(calls)
	lines := strings.Split(string(recorded), "\n")
	find := func(prefix string) string {
		for _, l := range lines {
			if strings.HasPrefix(l, prefix) {
				return l
			}
		}
		t.Fatalf("no %q in %s", prefix, recorded)
		return ""
	}
	network := find("network create")
	if !strings.Contains(network, "--internal") || !strings.Contains(network, "apptown.class=check") || !strings.Contains(network, srv.CheckOwnerLabel()) {
		t.Fatalf("the check's network: %s", network)
	}
	name := network[strings.LastIndex(network, " ")+1:]
	svc := find("run -d")
	if !strings.Contains(svc, "--network "+name+" --network-alias postgres") || !strings.Contains(svc, "POSTGRES_DB=app") || !strings.Contains(svc, "apptown.deadline=") {
		t.Fatalf("the service: %s", svc)
	}
	// Bounded, so one project's test database cannot starve the worker:
	// the worker's checks name no limits here, so the defaults.
	if !strings.Contains(svc, "--cpus 1 --memory 1073741824 --memory-swap 1073741824 --pids-limit 256") {
		t.Fatalf("the service runs without limits: %s", svc)
	}
	// The check itself is bounded too: this AppTown named no limits, so the
	// task defaults.
	check := find("run --cpus 4 --memory 8589934592 --memory-swap 8589934592 --pids-limit 4096")
	if !strings.Contains(check, "--network "+name) || !strings.Contains(check, "DATABASE_URL=postgres://app@postgres/app") {
		t.Fatalf("the check did not join its services' network with its environment: %s", check)
	}
	// The check's container shares the worker's tool cache, writable, where
	// mise's download cache is; the service does not.
	if !strings.Contains(check, "-v "+srv.ToolCache+":"+execution.ToolCacheMount+" ") || strings.Contains(svc, srv.ToolCache) {
		t.Fatalf("the tool cache: check %s; service %s", check, svc)
	}
	if !strings.Contains(string(recorded), "rm -f -v "+strings.TrimSuffix(name, "-net")+"-postgres") || !strings.Contains(string(recorded), "network rm "+name) {
		t.Fatalf("the services were not removed after the check: %s", recorded)
	}
}

// A request the worker cannot honour is not run anywhere, and a worker that
// does not answer, or answers nonsense, is a check that did not run.
func TestACheckThatCannotRunOnTheWorkerIsNotRun(t *testing.T) {
	t.Parallel()
	binary, _ := fakeCheckDocker(t)
	checks, _ := checkWorker(t, binary)
	tree := t.TempDir()
	req := execution.Request{Workspace: tree, Image: "x", Argv: []string{"true"}, Timeout: time.Minute, Name: "c"}
	for name, r := range map[string]execution.Request{
		"a mount of AppTown's": func() execution.Request { r := req; r.Mounts = []string{"/etc:/etc"}; return r }(),
		"a host route":         func() execution.Request { r := req; r.ExtraHosts = []string{"h:1.2.3.4"}; return r }(),
		"a named network":      func() execution.Request { r := req; r.Network = "host"; return r }(),
	} {
		if out := checks.Run(context.Background(), r); out.Termination != execution.LaunchFailed || out.RunErr == nil {
			t.Errorf("%s: %+v", name, out)
		}
	}
	gone := worker.Checks{URL: "https://127.0.0.1:1", Client: checks.Client}
	if out := gone.Run(context.Background(), req); out.Termination != execution.LaunchFailed || out.Exited0() {
		t.Fatalf("an unreachable worker: %+v", out)
	}
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"termination":"passed"}`)) }))
	defer junk.Close()
	if out := (worker.Checks{URL: junk.URL, Client: junk.Client()}).Run(context.Background(), req); out.Termination != execution.LaunchFailed {
		t.Fatalf("a malformed answer: %+v", out)
	}
}

// A landing's history scan reads a repository on the worker made from a
// bundle of the landing's commits: the range between them resolves there.
func TestAHistoryScanReadsTheLandingsCommitsOnTheWorker(t *testing.T) {
	t.Parallel()
	binary, _ := fakeCheckDocker(t)
	checks, _ := checkWorker(t, binary)
	repo := t.TempDir()
	git := func(args ...string) string {
		out, err := gittest.Command(append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@x"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "the target")
	target := git("rev-parse", "HEAD")
	git("commit", "-q", "--allow-empty", "-m", "added a secret")
	git("commit", "-q", "--allow-empty", "-m", "deleted it again")
	candidate := git("rev-parse", "HEAD")
	out := checks.Run(context.Background(), execution.Request{Workspace: t.TempDir(), Image: "gitleaks:test", Argv: []string{"history-" + target + ".." + candidate},
		Network: "none", Timeout: time.Minute, Name: "h",
		History: &execution.History{Repository: repo, Commits: []string{target, candidate}}})
	if !out.Exited0() || !strings.Contains(string(out.Output), "added a secret") || strings.Contains(string(out.Output), "the target") {
		t.Fatalf("the history on the worker: %+v (%s)", out, out.Output)
	}
}

// The sweep removes this worker's check containers and folders past their
// deadline, and at the worker's start every one of its own: an interrupted
// check is discarded.
func TestTheSweepRemovesChecksPastTheirDeadline(t *testing.T) {
	t.Parallel()
	srv := &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: t.TempDir()}
	past, future := time.Now().Add(-time.Minute).Unix(), time.Now().Add(time.Hour).Unix()
	binary, _ := sweepDocker(t)
	binary.reply = func(args []string) ([]byte, error, bool) {
		switch {
		case args[0] == "ps":
			return []byte("old " + itoa(past) + "\nnew " + itoa(future) + "\n"), nil, true
		case args[0] == "network" && args[1] == "ls":
			// A check's services' network goes at its deadline too.
			return []byte("oldnet " + itoa(past) + "\nnewnet " + itoa(future) + "\n"), nil, true
		}
		return nil, nil, true
	}
	for _, name := range []string{itoa(past) + "-a", itoa(future) + "-b"} {
		if err := os.MkdirAll(filepath.Join(srv.State, "checks", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	srv.SweepChecks(context.Background(), binary, false)
	recorded := strings.Join(binary.commands(""), "\n")
	// Only this worker's checks are listed: another worker's on the same
	// Docker are not its to remove.
	for _, listing := range append(binary.commands("ps"), binary.commands("network ls")...) {
		if !strings.Contains(listing, "--filter label="+srv.CheckOwnerLabel()) {
			t.Fatalf("the sweep lists checks that are not this worker's: %s", listing)
		}
	}
	if !strings.Contains(recorded, "network rm oldnet") || strings.Contains(recorded, "network rm newnet") {
		t.Fatalf("the sweep's network removals: %s", recorded)
	}
	if !strings.Contains(recorded, "rm -f -v old") || strings.Contains(recorded, "rm -f -v new") {
		t.Fatalf("the sweep's removals: %s", recorded)
	}
	if _, err := os.Stat(filepath.Join(srv.State, "checks", itoa(past)+"-a")); !os.IsNotExist(err) {
		t.Fatal("a folder past its deadline was kept")
	}
	if _, err := os.Stat(filepath.Join(srv.State, "checks", itoa(future)+"-b")); err != nil {
		t.Fatal("a folder before its deadline was removed")
	}
	srv.SweepChecks(context.Background(), binary, true)
	recorded = strings.Join(binary.commands(""), "\n")
	if !strings.Contains(recorded, "rm -f -v new") {
		t.Fatal("the start's sweep kept a running check")
	}
	if entries, _ := os.ReadDir(filepath.Join(srv.State, "checks")); len(entries) != 0 {
		t.Fatalf("the start's sweep kept folders: %v", entries)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
