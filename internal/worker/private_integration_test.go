//go:build integration

package worker_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dockercli "github.com/flokkit/apptown/internal/deploy/docker"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// Private Docker, services and needs for a task on a worker, with the worker
// server in this process driving this machine's Docker: two levels (the
// task's containers, and what its private daemon runs), not three. On Docker Desktop the VM and its disk are shared by
// everything here, so this is lifecycle and limit evidence as observed on
// this machine, not evidence of a separate host.

// privateFixture is a worker over one state folder, restartable, with a
// hub holding a Dockerfile.
type privateFixture struct {
	t         *testing.T
	ca        *workertest.CA
	state     string
	namespace string
	hub, base string
	docker    dockercli.Client // the Docker the worker's private Docker runs on
	srv       *worker.Server
	driver    execution.WorkerTasks
	stop      func()
	// profile is what the worker measured at its first start: every
	// request is admitted on it.
	profile execution.WorkerProfile
	// external is a worker process on another machine
	// (qualification_integration_test.go); srv then only names its labels.
	external *workertest.External
	// skew is how far the worker's clock is ahead of the real one: a test
	// moves it to pass an expiry without waiting for it.
	skew atomic.Int64
}

// now is the worker's clock: the real one, skew ahead.
func (f *privateFixture) now() time.Time { return time.Now().Add(time.Duration(f.skew.Load())) }

func newPrivateFixture(t *testing.T) *privateFixture {
	t.Helper()
	return newPrivateFixtureOn(t, nil)
}

// newPrivateFixtureOn is a fixture over the external worker, or, when it
// is nil, over a worker in this process.
func newPrivateFixtureOn(t *testing.T, external *workertest.External) *privateFixture {
	t.Helper()
	f := &privateFixture{t: t, ca: workertest.NewCA(t), state: t.TempDir(), namespace: fmt.Sprintf("private%d", time.Now().UnixNano()%1_000_000_000), docker: dockercli.CLI{Binary: "docker"}, external: external}
	removeNamespace(t, f.namespace)
	f.hub = t.TempDir()
	files := map[string]string{
		"Dockerfile":   "FROM busybox:1.37\nCOPY greeting.txt /greeting.txt\nCMD [\"cat\", \"/greeting.txt\"]\n",
		"greeting.txt": "hello from a nested build\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(f.hub, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-q", "-m", "base"}} {
		if out, err := gitexec.Run(t.Context(), f.hub, args...); err != nil {
			t.Fatal(err, out)
		}
	}
	base, err := gitexec.Output(t.Context(), f.hub, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	f.base = strings.TrimSpace(base)
	f.start()
	// Whatever a test leaves of this worker's private Docker goes: only
	// what carries this worker's label.
	owner := f.srv.CheckOwnerLabel()
	t.Cleanup(func() {
		out, _ := exec.Command("docker", "ps", "-aq", "--filter", "label="+owner).Output()
		for _, id := range strings.Fields(string(out)) {
			_ = exec.Command("docker", "rm", "-f", "-v", id).Run()
		}
		out, _ = exec.Command("docker", "network", "ls", "-q", "--filter", "label="+owner).Output()
		for _, id := range strings.Fields(string(out)) {
			_ = exec.Command("docker", "network", "rm", id).Run()
		}
	})
	return f
}

// start starts the worker service over the state folder (again, after a
// restart).
func (f *privateFixture) start() {
	if f.external != nil {
		f.srv = &worker.Server{Installation: workertest.Installation, State: f.external.State, Now: f.now}
		f.driver, f.stop = f.external.Driver(f.t), func() {}
		if f.profile.Name == "" {
			profile, err := f.driver.Profile(context.Background())
			if err != nil {
				f.t.Fatal(err)
			}
			f.profile = profile
		}
		return
	}
	f.srv = &worker.Server{Installation: "inst-1", State: f.state, Tasks: execution.DockerTasks{Namespace: f.namespace},
		DindImage: "docker:27-dind", Docker: f.docker, Role: worker.Role{Role: worker.RoleAgent}, Now: f.now}
	if f.profile.Name == "" {
		f.profile = f.srv.MeasureProfile(context.Background(), false)
	}
	f.srv.Profile = f.profile
	hs := workertest.StartServer(f.t, f.ca, f.srv)
	f.driver, f.stop = workertest.Driver(f.t, f.ca, hs), hs.Close
}

// restart stops the worker and starts it again over the same state: an
// external worker's process is killed by its recorded pid.
func (f *privateFixture) restart() {
	if f.external != nil {
		f.t.Logf("restarting the worker: %s", f.external.Ctl(f.t, "restart"))
		return
	}
	f.stop()
	f.start()
}

var privateLimits = execution.Limits{CPUs: 1, MemoryBytes: 512 << 20, PIDs: 512}

// spec is a task running script in the Docker CLI's image, with private.
func (f *privateFixture) spec(name, script string, private *execution.PrivateDocker, results ...execution.Result) execution.TaskSpec {
	if private != nil && private.Profile == nil {
		profile := f.profile
		private.Profile = &profile
	}
	return execution.TaskSpec{Key: f.namespace + "-" + name, Image: "docker:27-cli", Argv: []string{"sh", "-c", script},
		Admission: execution.Admission{Profile: "worker", Network: execution.NetworkDefault, Limits: privateLimits, Expires: time.Now().Add(15 * time.Minute)},
		Source:    &execution.TaskSource{Repository: f.hub, Commit: f.base}, Private: private, Results: results}
}

// withHostImage commits the host's copy of image to f's source as
// image.tar and returns that commit, for nested work that loads it (docker
// load -i /work/image.tar) rather than pulling it: a fresh private daemon
// has no images, and a pull depends on a registry that may not answer
// (22 Oct: Docker Hub's token request timed out, the nested memory hog
// never ran, and the test reported the daemon's memory limit as not
// holding).
func (f *privateFixture) withHostImage(image string) string {
	f.t.Helper()
	if out, err := exec.Command("docker", "save", "-o", filepath.Join(f.hub, "image.tar"), image).CombinedOutput(); err != nil {
		f.t.Fatalf("docker save %s: %v %s", image, err, out)
	}
	for _, args := range [][]string{{"add", "image.tar"}, {"-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-q", "-m", "the nested work's image"}} {
		if out, err := gitexec.Run(f.t.Context(), f.hub, args...); err != nil {
			f.t.Fatal(err, out)
		}
	}
	commit, err := gitexec.Output(f.t.Context(), f.hub, "rev-parse", "HEAD")
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.TrimSpace(commit)
}

// loadImage is the nested script's first line after withHostImage: the
// image is loaded from the source, and a failure ends the script saying so.
const loadImage = `docker load -q -i /work/image.tar >/dev/null || { echo "loading the nested image failed"; exit 1; }
`

// names are a task's private resources as the worker names them.
func (f *privateFixture) names(key string) (network, daemon string) {
	network = "apptown-private-" + strings.TrimPrefix(f.srv.CheckOwnerLabel(), "forge.worker=") + "-" + key
	return network, network + "-dind"
}

// inspect answers docker inspect's format for an object, or "" when there
// is none.
func inspect(t *testing.T, format, name string) string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "--format", format, name).CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "no such") {
			return ""
		}
		t.Fatalf("docker inspect %s: %v: %s", name, err, out)
	}
	return strings.TrimSpace(string(out))
}

// labelled lists the private containers and networks carrying a task's
// label.
func labelled(t *testing.T, key string) []string {
	t.Helper()
	var all []string
	for _, args := range [][]string{{"ps", "-a", "--format", "{{.Names}}"}, {"network", "ls", "--format", "{{.Name}}"}} {
		out, err := exec.Command("docker", append(args, "--filter", "label=forge.class=private-docker", "--filter", "label=forge.task="+key)...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
		all = append(all, strings.Fields(string(out))...)
	}
	return all
}

// A task builds an image with its own daemon and runs it with its tree
// mounted at the same path; it reaches a service by name; the service is
// started from the request the worker recorded, unchanged by a worker
// restart between the task's ensure and its start. The daemon is given the
// task's limits, each service its budget, and nothing else of the worker:
// no cache, no host socket, no other daemon. Nothing private is made before
// the task's record names it, so a worker stopped mid-way leaves nothing
// the record (and so release and the sweep) does not know of.
func TestIntegrationATaskHasItsOwnDockerAndServices(t *testing.T) {
	f := newPrivateFixture(t)
	ctx := context.Background()
	made := recordedBeforeMade(t, f)
	private := &execution.PrivateDocker{Daemon: true, Services: []deploy.ServiceSpec{
		{Name: "cache", Image: "redis:7-alpine", Env: map[string]string{"GREETING_DB": "sourdough"}, Health: "redis-cli ping"}}}
	script := `set -e
echo "same path $$" > /work/marker
docker build -q -t nested-greeting /work >/dev/null
docker run --rm nested-greeting
docker run --rm -v /work:/work busybox:1.37 sh -c 'cat /work/marker; echo nested wrote > /work/from-nested'
cat /work/from-nested
nc -z cache 6379 && echo "reached the cache"
test -S /var/run/docker.sock && echo "HOST SOCKET"
docker -H unix:///var/run/docker.sock info >/dev/null 2>&1 && echo "HOST DAEMON"
docker volume ls --format '{{.Name}}' | grep -q forge-cache && echo "FORGE CACHE"
docker buildx inspect apptown >/dev/null && echo "builder apptown"
echo "DOCKER_CONFIG=$DOCKER_CONFIG"
`
	spec := f.spec("build", script, private)
	created, err := f.driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	network, daemon := f.names(spec.Key)
	if inspect(t, "{{.Name}}", network) == "" || inspect(t, "{{.Name}}", daemon) != "" {
		t.Fatal("after ensure, the network exists and nothing else of the task's private Docker")
	}
	// The worker stops and starts again between the ensure and the start:
	// what is made is what the recorded request froze.
	f.restart()
	ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}
	if err := f.driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	service := network + "-cache"
	if env := inspect(t, "{{json .Config.Env}}", service); !strings.Contains(env, "GREETING_DB=sourdough") {
		t.Errorf("the service's parameters: %s", env)
	}
	code, err := f.driver.Wait(ctx, ref)
	page, _ := f.driver.Output(ctx, ref, execution.OutputRead{})
	out := string(page.Data)
	if err != nil || code != 0 {
		t.Fatalf("the task: %d %v\n%s", code, err, out)
	}
	for _, want := range []string{"hello from a nested build", "same path", "nested wrote", "reached the cache", "builder apptown", "DOCKER_CONFIG=/apptown/state/docker"} {
		if !strings.Contains(out, want) {
			t.Errorf("the task's output lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"HOST SOCKET", "HOST DAEMON", "FORGE CACHE"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("the task was supplied %s:\n%s", unwanted, out)
		}
	}

	// Configured: the daemon has the task's limits, the service its
	// budget; the daemon mounts the tree, its client state and its builder
	// configuration, and nothing else.
	limits := "{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}} {{.HostConfig.MemorySwap}} {{.HostConfig.PidsLimit}}"
	if got := inspect(t, limits, daemon); got != "1000000000 536870912 536870912 512" {
		t.Errorf("the daemon's limits: %s", got)
	}
	if got := inspect(t, limits, service); got != "1000000000 1073741824 1073741824 256" {
		t.Errorf("the service's limits: %s", got)
	}
	mounts := inspect(t, `{{range .Mounts}}{{.Destination}} {{end}}`, daemon)
	for _, m := range strings.Fields(mounts) {
		switch m {
		case "/work", "/apptown/state/docker", "/forge-dind", "/var/lib/docker": // the last its own anonymous storage
		default:
			t.Errorf("the daemon was given %s (mounts: %s)", m, mounts)
		}
	}
	if !strings.Contains(inspect(t, `{{range .Mounts}}{{.Type}}:{{.Destination}} {{end}}`, daemon), "volume:/var/lib/docker") {
		t.Errorf("the daemon's storage is not its own volume: %s", mounts)
	}
	// After the task exited, its daemon and service were stopped, and are
	// removed with it.
	if state := inspect(t, "{{.State.Running}}", daemon) + inspect(t, "{{.State.Running}}", service); state != "falsefalse" {
		t.Errorf("after the task exited, its daemon and service run: %s", state)
	}
	if err := f.driver.Release(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if left := labelled(t, spec.Key); len(left) > 0 {
		t.Errorf("released, the task left %v", left)
	}
	log, err := os.ReadFile(made)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"recorded network " + network, "recorded run " + daemon, "recorded run " + service} {
		if !strings.Contains(string(log), want+"\n") {
			t.Errorf("not seen made after its record: %q in\n%s", want, log)
		}
	}
	if strings.Contains(string(log), "unrecorded") {
		t.Errorf("private Docker made before the task's record named it:\n%s", log)
	}
}

// recordedBeforeMade has the worker's private Docker go through a wrapper
// that notes, for each network and container it makes, whether a task's
// record already names it; it answers the note's path.
func recordedBeforeMade(t *testing.T, f *privateFixture) string {
	t.Helper()
	dir := t.TempDir()
	made := filepath.Join(dir, "made")
	wrapper := recordsBeforeMade{real: dockercli.CLI{Binary: "docker"}, records: filepath.Join(f.state, "records"), made: made}
	if err := os.WriteFile(made, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f.docker = wrapper
	f.restart()
	return made
}

// A nested writer that goes on writing after the task's own process exits
// is stopped, and confirmed stopped, before the task's result is captured:
// the captured bytes are what the tree holds from then on, and every read
// of the result, after a worker restart too, answers them. A start after
// that is refused; a removal that fails leaves the task unreleased until
// one succeeds; and after the release a request for the key is stale.
func TestIntegrationNothingWritesAfterCaptureAndReleaseIsFinal(t *testing.T) {
	f := newPrivateFixture(t)
	// A wrapper that fails removals, or stops, while a flag file exists.
	flags := t.TempDir()
	flag, stopFlag := filepath.Join(flags, "rm"), filepath.Join(flags, "stop")
	wrapper := refusedWhileFlagged{real: dockercli.CLI{Binary: "docker"}, flags: flags}
	f.docker = wrapper
	f.restart()
	ctx := context.Background()
	script := `set -e
docker run -d -v /work:/work busybox:1.37 sh -c 'while true; do echo tick >> /work/ticks.txt; sleep 0.05; done' >/dev/null
sleep 1
echo exiting while the writer writes`
	spec := f.spec("writer", script, &execution.PrivateDocker{Daemon: true}, execution.Result{Name: "ticks", Path: "/work/ticks.txt", Max: 1 << 20})
	// Its daemon cannot be stopped at first: the task reads as running,
	// and nothing is captured, until the stop is confirmed.
	if err := os.WriteFile(stopFlag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := f.driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}
	if err := f.driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("docker", "wait", created.ID).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	_, daemon := f.names(spec.Key)
	if obs, err := f.driver.Observe(ctx, ref); err != nil || obs.State != execution.TaskRunning || inspect(t, "{{.State.Running}}", daemon) != "true" {
		t.Fatalf("exited with its stop unconfirmed, the task reads as %+v (%v)", obs, err)
	}
	if _, err := f.driver.Results(ctx, ref); err == nil || !strings.Contains(err.Error(), "not exited") {
		t.Fatalf("captured before the stop was confirmed: %v", err)
	}
	if err := os.Remove(stopFlag); err != nil {
		t.Fatal(err)
	}
	if code, err := f.driver.Wait(ctx, ref); err != nil || code != 0 {
		t.Fatalf("the task: %d %v", code, err)
	}
	if running := inspect(t, "{{.State.Running}}", daemon); running != "false" {
		t.Fatalf("the task reads as exited while its daemon runs: %s", running)
	}
	got, err := f.driver.Results(ctx, ref)
	if err != nil || got["ticks"].State != execution.ResultPresent || got["ticks"].Size == 0 {
		t.Fatalf("the captured result: %+v %v", got, err)
	}
	// The fence is the guarantee: the result was captured only once the
	// daemon, and the writer inside it, were confirmed stopped (above), so
	// what was captured is the tree as it stays.
	captured := got["ticks"].Content
	if onDisk, err := os.ReadFile(filepath.Join(f.state, "workspaces", spec.Key, "ticks.txt")); err != nil || string(onDisk) != string(captured) {
		t.Fatalf("the capture is not the stopped tree: %d bytes captured, %d on disk (%v)", len(captured), len(onDisk), err)
	}
	f.restart()
	if again, err := f.driver.Results(ctx, ref); err != nil || string(again["ticks"].Content) != string(captured) {
		t.Fatalf("the result read again after a restart: %v", err)
	}
	// A late start, racing collection, makes nothing and is refused.
	if err := f.driver.Start(ctx, ref); !errors.Is(err, execution.ErrStartRefused) {
		t.Fatalf("a start after the task exited: %v", err)
	}
	if running := inspect(t, "{{.State.Running}}", daemon); running != "false" {
		t.Fatalf("a late start ran the daemon again")
	}

	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.driver.Release(ctx, ref); err == nil {
		t.Fatal("a release whose removal failed was confirmed")
	}
	if _, err := os.Stat(filepath.Join(f.state, "records", spec.Key+".json")); err != nil {
		t.Fatalf("a failed release lost the task's record: %v", err)
	}
	if err := os.Remove(flag); err != nil {
		t.Fatal(err)
	}
	if err := f.driver.Release(ctx, ref); err != nil {
		t.Fatalf("the release tried again: %v", err)
	}
	if left := labelled(t, spec.Key); len(left) > 0 {
		t.Errorf("released, the task left %v", left)
	}
	if _, err := f.driver.Ensure(ctx, spec); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("the request replayed after the release: %v", err)
	}
	if err := f.driver.Start(ctx, ref); err == nil {
		t.Fatal("a start after the release was accepted")
	}
}

// Requests the worker cannot honour are refused before anything is made;
// a start whose services cannot all be made removes what it made, and is
// refused with the reason.
func TestIntegrationPrivateDockerIsRefusedOrCleanedUp(t *testing.T) {
	f := newPrivateFixture(t)
	ctx := context.Background()
	for name, private := range map[string]*execution.PrivateDocker{
		"dind":      {Services: []deploy.ServiceSpec{{Name: "dind", Image: "busybox:1.37"}}},
		"duplicate": {Services: []deploy.ServiceSpec{{Name: "db", Image: "busybox:1.37"}, {Name: "db", Image: "busybox:1.37"}}},
	} {
		spec := f.spec("refused-"+name, "true", private)
		// Sent as AppTown would not send it: straight to the worker.
		body := fmt.Sprintf(`{"key":%q,"admission":{"Profile":"worker","Network":"default","Limits":{"CPUs":1,"MemoryBytes":1,"PIDs":1}},"image":"busybox","argv":["true"],"private":%s}`,
			spec.Key, map[string]string{"dind": `{"services":[{"name":"dind","image":"busybox"}]}`, "duplicate": `{"services":[{"name":"db","image":"busybox"},{"name":"db","image":"busybox"}]}`}[name])
		resp, err := f.driver.Client.Post(f.driver.URL+"/v1/tasks/"+spec.Key, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: the worker answered %s", name, resp.Status)
		}
		if _, err := f.driver.Ensure(ctx, spec); err == nil {
			t.Errorf("%s: ensured", name)
		}
		if left := labelled(t, spec.Key); len(left) > 0 {
			t.Errorf("%s: refused, it made %v", name, left)
		}
	}
	zero := f.spec("refused-zero", "true", &execution.PrivateDocker{Daemon: true})
	zero.Admission.Limits.PIDs = 0
	if _, err := f.driver.Ensure(ctx, zero); err == nil || !strings.Contains(err.Error(), "zero is not unlimited") {
		t.Errorf("a zero limit: %v", err)
	}
	f.srv.DindImage = ""
	if _, err := f.driver.Ensure(ctx, f.spec("refused-nodaemon", "true", &execution.PrivateDocker{Daemon: true})); !errors.Is(err, execution.ErrUnsupported) {
		t.Errorf("a daemon from a worker that runs none: %v", err)
	}

	// The second service exits at once: the first, made, is removed.
	spec := f.spec("partial", "true", &execution.PrivateDocker{Services: []deploy.ServiceSpec{
		{Name: "cache", Image: "redis:7-alpine"}, {Name: "broken", Image: "busybox:1.37", Command: []string{"false"}}}})
	created, err := f.driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}
	if err := f.driver.Start(ctx, ref); !errors.Is(err, execution.ErrStartRefused) || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("a start whose service exited: %v", err)
	}
	network, _ := f.names(spec.Key)
	if left := labelled(t, spec.Key); len(left) != 1 || left[0] != network {
		t.Errorf("a refused start left %v (the network stays with the task until it is released)", left)
	}
	if obs, err := f.driver.Observe(ctx, ref); err != nil || obs.State != execution.TaskCreated {
		t.Errorf("a refused start ran the task: %+v %v", obs, err)
	}
	if err := f.driver.Release(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if left := labelled(t, spec.Key); len(left) > 0 {
		t.Errorf("released, the task left %v", left)
	}
}

// The daemon's limits hold what the task's nested containers do, and each
// service's budget holds what runs in it: memory past the limit is killed,
// processes past it cannot start, and CPU is held to the limit while busy
// loops run in both, measured over one window from their cgroups while the
// loops are known to run. What is observed is logged; the configured flags
// alone establish nothing (TestIntegrationATaskHasItsOwnDockerAndServices
// asserts them).
func TestIntegrationPrivateDockerLimitsHold(t *testing.T) {
	limitsHold(t, newPrivateFixture(t))
}

// limitsHold is TestIntegrationPrivateDockerLimitsHold on f's worker.
func limitsHold(t *testing.T, f *privateFixture) {
	t.Helper()
	ctx := context.Background()
	// The nested work: a memory hog past the daemon's 512 MiB, processes
	// past its 512, then four busy loops, announced once they run.
	script := loadImage + `docker run --rm busybox:1.37 sh -c 'head -c 700m /dev/zero | tail > /dev/null'; echo "memory hog exit $?"
docker run --rm busybox:1.37 sh -c '` + forkProbe(700) + `'
docker run -d busybox:1.37 sh -c '` + spinners + `; sleep 60' >/dev/null
echo nested-spinning
sleep 30
echo done`
	spec := f.spec("limits", script, &execution.PrivateDocker{Daemon: true, Services: []deploy.ServiceSpec{{Name: "svc", Image: "busybox:1.37", Command: []string{"sleep", "3600"}}}})
	spec.Source.Commit = f.withHostImage("busybox:1.37")
	created, err := f.driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}
	if err := f.driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	network, daemon := f.names(spec.Key)
	service := network + "-svc"
	// The service's budget: 1 GiB, four busy loops, then 256 processes
	// (the probe's processes leave no room for another exec).
	// The hog is killed when the cgroup reaches its budget. The kernel may
	// kill the shell that started it as well, an instant later, before the
	// hog's memory is returned (the Docker VM's log shows a second "Memory
	// cgroup out of memory" victim, sh, in the same cgroup): then the shell
	// says nothing and the exec itself ends with 137. Either is the budget
	// killing the work.
	hog, hogErr := exec.Command("docker", "exec", service, "sh", "-c", "head -c 1500m /dev/zero | tail > /dev/null; echo exit $?").CombinedOutput()
	var hogExit *exec.ExitError
	hogKilled := strings.Contains(string(hog), "exit 137") || (errors.As(hogErr, &hogExit) && hogExit.ExitCode() == 137)
	if out, err := exec.Command("docker", "exec", "-d", service, "sh", "-c", spinners).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	forks, _ := exec.Command("docker", "exec", service, "sh", "-c", forkProbe(400)).CombinedOutput()
	awaitOutput(t, f, ref, "nested-spinning")
	used := measureCgroups(t, 4*time.Second, daemon, service)
	code, err := f.driver.Wait(ctx, ref)
	page, _ := f.driver.Output(ctx, ref, execution.OutputRead{})
	out := string(page.Data)
	if err != nil || code != 0 {
		t.Fatalf("the task: %d %v\n%s", code, err, out)
	}
	t.Logf("observed, nested: %s", strings.ReplaceAll(strings.TrimSpace(out), "\n", "; "))
	t.Logf("observed, service: memory hog %s; %s", strings.TrimSpace(string(hog)), strings.TrimSpace(string(forks)))
	t.Logf("observed, CPU under four busy loops each: daemon %s; service %s", used[daemon], used[service])

	if !strings.Contains(out, "memory hog exit 137") {
		t.Errorf("nested memory past the daemon's limit was not killed:\n%s", out)
	}
	checkForks(t, "nested processes past the daemon's limit", out, 700)
	if !hogKilled {
		t.Errorf("a service's memory past its budget was not killed: %s %v", hog, hogErr)
	}
	checkForks(t, "a service's processes past its budget", string(forks), 256)
	checkCPU(t, "the daemon", used[daemon], 1)
	checkCPU(t, "the service", used[service], 1)
	if err := f.driver.Release(ctx, ref); err != nil {
		t.Fatal(err)
	}
}

// spinners is four busy loops, in the background.
const spinners = `for i in 1 2 3 4; do (while :; do :; done) & done`

// The agent, its daemon and its service under load at once stay within
// the stated aggregate, the agent's limits plus the daemon's plus the
// service's budget: each is held to its own CPU limit (throttled, not
// idle) and its memory, and together to their sum, measured over one
// window from their cgroups. The agent and the daemon get a quarter CPU
// each, so the sum (1.5 CPUs) is below what the machine could give them.
func TestIntegrationPrivateDockerCombinedLoadStaysWithinTheAggregate(t *testing.T) {
	f := newPrivateFixture(t)
	ctx := context.Background()
	script := loadImage + `head -c 300m /dev/zero | tail >/dev/null
docker run -d -v /work:/work busybox:1.37 sh -c 'head -c 300m /dev/zero | tail >/dev/null; cat /sys/fs/cgroup/memory.peak > /work/nested-peak; touch /work/nested-loaded; ` + spinners + `; sleep 60' >/dev/null
` + spinners + `
while [ ! -e /work/nested-loaded ]; do sleep 0.5; done
echo "nested container's own memory peak $(cat /work/nested-peak)"
echo all-under-load
sleep 30`
	spec := f.spec("aggregate", script, &execution.PrivateDocker{Daemon: true, Services: []deploy.ServiceSpec{{Name: "svc", Image: "busybox:1.37", Command: []string{"sleep", "3600"}}}})
	spec.Source.Commit = f.withHostImage("busybox:1.37")
	spec.Admission.Limits = execution.Limits{CPUs: 0.25, MemoryBytes: 512 << 20, PIDs: 512}
	created, err := f.driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}
	if err := f.driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	network, daemon := f.names(spec.Key)
	service := network + "-svc"
	if out, err := exec.Command("docker", "exec", service, "sh", "-c", "head -c 700m /dev/zero | tail >/dev/null").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if out, err := exec.Command("docker", "exec", "-d", service, "sh", "-c", spinners).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	awaitOutput(t, f, ref, "all-under-load")
	used := measureCgroups(t, 5*time.Second, created.ID, daemon, service)
	page, _ := f.driver.Output(ctx, ref, execution.OutputRead{})
	t.Logf("the task said: %s", strings.ReplaceAll(strings.TrimSpace(string(page.Data)), "\n", "; "))
	t.Logf("observed: agent %s; daemon %s; service %s", used[created.ID], used[daemon], used[service])
	limits := map[string]execution.Limits{created.ID: spec.Admission.Limits, daemon: spec.Admission.Limits, service: {CPUs: 1, MemoryBytes: 1 << 30}}
	var cpus, aggregateCPUs float64
	var peak, aggregateMemory int64
	for id, l := range limits {
		u := used[id]
		checkCPU(t, id, u, l.CPUs)
		if u.MemPeak > l.MemoryBytes || u.MemMax != l.MemoryBytes || u.MemPeak < 256<<20 {
			t.Errorf("%s: memory peak %d under a limit of %d (configured %d); the load reaches at least 256 MiB", id, u.MemPeak, u.MemMax, l.MemoryBytes)
		}
		cpus, aggregateCPUs = cpus+u.CPUs, aggregateCPUs+l.CPUs
		peak, aggregateMemory = peak+u.MemPeak, aggregateMemory+l.MemoryBytes
	}
	t.Logf("together: %.2f CPUs of an aggregate %.2f; memory peaks %d MiB of an aggregate %d MiB", cpus, aggregateCPUs, peak>>20, aggregateMemory>>20)
	if cpus > aggregateCPUs*1.1 || peak > aggregateMemory {
		t.Errorf("past the stated aggregate: %.2f CPUs of %.2f, %d of %d bytes", cpus, aggregateCPUs, peak, aggregateMemory)
	}
	if err := f.driver.Stop(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if err := f.driver.Release(ctx, ref); err != nil {
		t.Fatal(err)
	}
}

// awaitOutput waits until a task's output says what.
func awaitOutput(t *testing.T, f *privateFixture, ref execution.TaskRef, what string) {
	t.Helper()
	var page execution.OutputPage
	if !poll.Until(context.Background(), 3*time.Minute, 100*time.Millisecond, func() bool {
		var err error
		if page, err = f.driver.Output(context.Background(), ref, execution.OutputRead{}); err != nil {
			t.Fatal(err)
		}
		return strings.Contains(string(page.Data), what)
	}) {
		t.Fatalf("the task never said %q:\n%s", what, page.Data)
	}
}

// cgroupUse is what a container's cgroup used over a window.
type cgroupUse struct {
	CPUs            float64 // CPUs used, on average, over the window
	Throttled       int64   // periods it was throttled in the window
	CPUMax          string  // its cpu.max
	MemPeak, MemMax int64   // memory.peak and memory.max
}

func (u cgroupUse) String() string {
	return fmt.Sprintf("%.2f CPUs (cpu.max %s, throttled %d periods), memory peak %d MiB of %d MiB", u.CPUs, u.CPUMax, u.Throttled, u.MemPeak>>20, u.MemMax>>20)
}

// measureCgroups reads the cgroups of the named containers twice, window
// apart, in one helper container that sees the host's cgroup tree: the same
// on Docker Desktop and on Linux, and one window for all of them.
func measureCgroups(t *testing.T, window time.Duration, names ...string) map[string]cgroupUse {
	t.Helper()
	var ids []string
	for _, n := range names {
		ids = append(ids, inspect(t, "{{.Id}}", n))
	}
	script := `set -e
dirs=""
for id in ` + strings.Join(ids, " ") + `; do
  d=$(find /cg -maxdepth 6 -type d -name "*$id*" 2>/dev/null | head -n 1)
  [ -n "$d" ] || { echo "no cgroup for $id"; exit 1; }
  dirs="$dirs $d"
done
sample() { read up _ < /proc/uptime; echo "at $up"; for d in $dirs; do awk '/^usage_usec/{u=$2} /^nr_throttled/{n=$2} END{print "cpu", u, n}' $d/cpu.stat; done; }
sample; sleep ` + strconv.FormatFloat(window.Seconds(), 'f', -1, 64) + `; sample
for d in $dirs; do echo "mem $(cat $d/memory.peak) $(cat $d/memory.max) $(tr ' ' / < $d/cpu.max)"; done`
	out, err := exec.Command("docker", "run", "--rm", "--cgroupns", "host", "-v", "/sys/fs/cgroup:/cg:ro", "busybox:1.37", "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("measuring cgroups: %v\n%s", err, out)
	}
	var at []float64
	var cpu [][2]int64
	var mems []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 2 && fields[0] == "at":
			v, _ := strconv.ParseFloat(fields[1], 64)
			at = append(at, v)
		case len(fields) == 3 && fields[0] == "cpu":
			u, _ := strconv.ParseInt(fields[1], 10, 64)
			n, _ := strconv.ParseInt(fields[2], 10, 64)
			cpu = append(cpu, [2]int64{u, n})
		case len(fields) == 4 && fields[0] == "mem":
			mems = append(mems, line)
		}
	}
	if len(at) != 2 || len(cpu) != 2*len(ids) || len(mems) != len(ids) {
		t.Fatalf("could not measure: %s", out)
	}
	elapsed := at[1] - at[0]
	used := map[string]cgroupUse{}
	for i, name := range names {
		var u cgroupUse
		first, second := cpu[i], cpu[len(ids)+i]
		u.CPUs = float64(second[0]-first[0]) / 1e6 / elapsed
		u.Throttled = second[1] - first[1]
		fields := strings.Fields(mems[i])
		u.MemPeak, _ = strconv.ParseInt(fields[1], 10, 64)
		u.MemMax, _ = strconv.ParseInt(fields[2], 10, 64)
		u.CPUMax = fields[3]
		used[name] = u
	}
	return used
}

// checkCPU: a container under more busy loops than its limit was busy (at
// least half its limit), was throttled, and used no more than its limit
// (with a little tolerance for the sampling).
func checkCPU(t *testing.T, what string, u cgroupUse, limit float64) {
	t.Helper()
	if u.CPUs < limit/2 || u.CPUs > limit*1.15+0.02 || u.Throttled == 0 {
		t.Errorf("%s under load: %s; its limit is %.2f CPUs", what, u, limit)
	}
}

// forkProbe is a busybox script that tries to start n sleeping processes,
// then counts what runs in its container's cgroup. busybox sh exits as soon
// as a fork is refused, so the forks run in a child shell and the count is
// read with builtins only: the parent needs no fork to report.
func forkProbe(n int) string {
	return fmt.Sprintf(`read max < /sys/fs/cgroup/pids.max; sh -c "for i in \$(seq %d); do sleep 20 & done" 2>/dev/null; rc=$?; `+
		`read n < /sys/fs/cgroup/pids.current; echo "processes: cgroup limit $max, forks exit $rc, running $n"`, n)
}

// checkForks reads a fork probe's report. No report is no measurement, not
// a limit held; a limit holds when a fork was refused and fewer than limit
// processes run.
func checkForks(t *testing.T, what, out string, limit int) {
	t.Helper()
	i := strings.LastIndex(out, "processes: ")
	var max string
	var exit, running int
	if i < 0 {
		t.Errorf("%s: could not measure, no report:\n%s", what, out)
		return
	}
	if _, err := fmt.Sscanf(out[i:], "processes: cgroup limit %s forks exit %d, running %d", &max, &exit, &running); err != nil {
		t.Errorf("%s: could not measure, the report %q: %v", what, out[i:], err)
		return
	}
	if exit == 0 || running <= 0 || running >= limit {
		t.Errorf("%s were not refused: forks exit %d, %d running (limit %d; the cgroup's own %s)", what, exit, running, limit, strings.TrimSuffix(max, ","))
	}
}

// Replies lost and the worker stopped at each recorded decision: a repeated
// ensure finds the same task, a repeated start while it runs makes nothing
// again, a restarted worker waits for it, stops its private Docker and
// captures, and a task whose daemon is gone while it runs is lost, never
// given another. With AppTown away, the worker's own sweep stops a task
// whose admission has expired and removes its private Docker.
func TestIntegrationPrivateDockerSurvivesLostRepliesAndRestarts(t *testing.T) {
	f := newPrivateFixture(t)
	ctx := context.Background()
	spec := f.spec("restarts", `docker info >/dev/null && echo up; sleep 4; echo done > /work/done.txt`, &execution.PrivateDocker{Daemon: true},
		execution.Result{Name: "done", Path: "/work/done.txt", Max: 64})
	created, err := f.driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := f.driver.Ensure(ctx, spec); err != nil || again.ID != created.ID {
		t.Fatalf("a repeated ensure: %+v %v", again, err)
	}
	ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}
	if err := f.driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	_, daemon := f.names(spec.Key)
	first := inspect(t, "{{.Id}}", daemon)
	if err := f.driver.Start(ctx, ref); err != nil {
		t.Fatalf("a repeated start while it runs: %v", err)
	}
	if again := inspect(t, "{{.Id}}", daemon); again != first {
		t.Fatal("a repeated start made the daemon again")
	}
	f.restart() // after the start was recorded
	if err := f.driver.Stop(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if err := f.driver.Stop(ctx, ref); err != nil { // its reply lost
		t.Fatal(err)
	}
	f.restart() // after the stop
	if _, err := f.driver.Wait(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if obs, err := f.driver.Observe(ctx, ref); err != nil || obs.State != execution.TaskExited || inspect(t, "{{.State.Running}}", daemon) != "false" {
		t.Fatalf("after its stop: %+v %v", obs, err)
	}
	if _, err := f.driver.Results(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if err := f.driver.Release(ctx, ref); err != nil {
		t.Fatal(err)
	}

	// Its daemon removed behind the worker's back while it runs: lost.
	lost := f.spec("lost", "sleep 60", &execution.PrivateDocker{Daemon: true})
	created, err = f.driver.Ensure(ctx, lost)
	if err != nil {
		t.Fatal(err)
	}
	ref = execution.TaskRef{Key: lost.Key, ProviderID: created.ID}
	if err := f.driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	_, daemon = f.names(lost.Key)
	if out, err := exec.Command("docker", "rm", "-f", "-v", daemon).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	f.restart()
	if obs, err := f.driver.Observe(ctx, ref); err != nil || obs.State != execution.TaskAbsent {
		t.Fatalf("a task without its daemon: %+v %v", obs, err)
	}
	if inspect(t, "{{.Name}}", daemon) != "" {
		t.Fatal("the lost daemon was made again")
	}
	if err := f.driver.Release(ctx, ref); err != nil {
		t.Fatal(err)
	}

	// Expired, with no AppTown asking: the sweep stops it and removes its
	// private Docker; the record stays for AppTown to collect.
	expiring := f.spec("expiring", "sleep 120", &execution.PrivateDocker{Daemon: true, Services: []deploy.ServiceSpec{{Name: "svc", Image: "busybox:1.37", Command: []string{"sleep", "3600"}}}})
	// Far enough ahead that a slow start cannot pass it before the first
	// sweep; the skew below moves the worker's clock past it.
	expiring.Admission.Expires = time.Now().Add(10 * time.Minute)
	created, err = f.driver.Ensure(ctx, expiring)
	if err != nil {
		t.Fatal(err)
	}
	ref = execution.TaskRef{Key: expiring.Key, ProviderID: created.ID}
	if err := f.driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	f.srv.SweepPrivate(ctx)
	if len(labelled(t, expiring.Key)) != 3 {
		t.Fatalf("the sweep removed a task not expired: %v", labelled(t, expiring.Key))
	}
	// The worker's clock passes the admission's expiry: it judges expiry by
	// its own clock, and the sweep runs here.
	f.skew.Store(int64(time.Until(expiring.Admission.Expires) + time.Second))
	f.srv.SweepPrivate(ctx)
	f.skew.Store(0)
	if left := labelled(t, expiring.Key); len(left) > 0 {
		t.Errorf("expired, the task kept %v", left)
	}
	if obs, err := f.driver.Observe(ctx, ref); err != nil || obs.State != execution.TaskExited {
		t.Errorf("expired, the task: %+v %v", obs, err)
	}
	if err := f.driver.Release(ctx, ref); err != nil {
		t.Fatal(err)
	}
}

// A stop stops the whole invocation: the task, and its private daemon and
// services, each confirmed. While the private stop is refused, the stop
// fails (AppTown holds the task), its results are not read, and its
// workspace is not collected while the nested writer it started may still
// write. Once the stop is confirmed, the writer has stopped and the
// workspace is collected.
func TestIntegrationAStopCoversThePrivateDockerAndFencesCollection(t *testing.T) {
	f := newPrivateFixture(t)
	flags := t.TempDir()
	wrapper := refusedWhileFlagged{real: dockercli.CLI{Binary: "docker"}, flags: flags}
	f.docker = wrapper
	f.restart()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	spec := f.spec("stop", `set -e
docker run -d -v /work:/work busybox:1.37 sh -c 'while true; do echo tick >> /work/ticks.txt; sleep 0.1; done'
echo writer-ready
sleep 120`, &execution.PrivateDocker{Daemon: true}, execution.Result{Name: "ticks", Path: "/work/ticks.txt", Max: 1 << 20})
	made, err := f.driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: spec.Key, ProviderID: made.ID}
	if err := f.driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	for {
		page, err := f.driver.Output(ctx, ref, execution.OutputRead{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(page.Data), "writer-ready") {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	stopFlag := filepath.Join(flags, "stop")
	if err := os.WriteFile(stopFlag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.driver.Stop(ctx, ref); err == nil {
		t.Error("the stop was confirmed although the private daemon's stop was refused")
	}
	_, daemon := f.names(spec.Key)
	if inspect(t, "{{.State.Running}}", daemon) != "true" {
		t.Fatal("the test needs the private daemon still running")
	}
	if _, err := f.driver.Results(ctx, ref); err == nil {
		t.Error("the results were read while the private daemon runs")
	}
	if tip, err := f.driver.Collect(ctx, spec.Key, f.base, "capture", filepath.Join(t.TempDir(), "result.bundle")); err == nil {
		t.Errorf("the workspace was collected (tip %s) while the nested writer may still write", tip)
	}

	if err := os.Remove(stopFlag); err != nil {
		t.Fatal(err)
	}
	if err := f.driver.Stop(ctx, ref); err != nil {
		t.Fatalf("the stop asked again: %v", err)
	}
	if running := inspect(t, "{{.State.Running}}", daemon); running != "false" {
		t.Fatalf("a confirmed stop left the daemon running: %s", running)
	}
	// The fence is the guarantee: collection was refused while the writer's
	// daemon ran (above) and is admitted once its stop is confirmed.
	if ticks, _ := os.ReadFile(filepath.Join(f.state, "workspaces", spec.Key, "ticks.txt")); len(ticks) == 0 {
		t.Fatal("the nested writer never wrote: the fence was not exercised")
	}
	if _, err := f.driver.Collect(ctx, spec.Key, f.base, "capture", filepath.Join(t.TempDir(), "result.bundle")); err != nil {
		t.Fatalf("collecting after the confirmed stop: %v", err)
	}
	if err := f.driver.Release(ctx, ref); err != nil {
		t.Fatal(err)
	}
}

// A worker stopped between each durable decision about a task's private
// Docker and the Docker effect that follows it (abandoned there, then
// closed and started again over the same state) recovers: the request
// asked again finishes what was decided, a task that never ran gets its
// private Docker made once, a task that ran never gets it again, and the
// release leaves nothing. A durable record the worker cannot write refuses
// the ensure with nothing made.
func TestIntegrationAWorkerStoppedBetweenADecisionAndItsEffectRecovers(t *testing.T) {
	f := newPrivateFixture(t)
	ctx := context.Background()
	private := func() *execution.PrivateDocker {
		return &execution.PrivateDocker{Daemon: true, Services: []deploy.ServiceSpec{{Name: "svc", Image: "busybox:1.37", Command: []string{"sleep", "3600"}}}}
	}
	result := execution.Result{Name: "up", Path: "/work/up.txt", Max: 64}
	const script = `docker info >/dev/null && echo up > /work/up.txt`
	// abandoned runs call while the worker abandons at point, and restarts it.
	abandoned := func(t *testing.T, point, key string, call func() error) {
		t.Helper()
		restore := worker.AbandonAt(point, key)
		err := call()
		restore()
		if err == nil {
			t.Fatalf("the call abandoned at %s was answered", point)
		}
		f.restart()
	}
	running := func(name string) string { return inspect(t, "{{.State.Running}}", name) }
	finish := func(t *testing.T, spec execution.TaskSpec, ref execution.TaskRef) {
		t.Helper()
		if code, err := f.driver.Wait(ctx, ref); err != nil || code != 0 {
			t.Fatalf("the task: %d %v", code, err)
		}
		if got, err := f.driver.Results(ctx, ref); err != nil || string(got["up"].Content) != "up\n" {
			t.Fatalf("its result: %+v %v", got, err)
		}
		if err := f.driver.Start(ctx, ref); !errors.Is(err, execution.ErrStartRefused) {
			t.Fatalf("a start after it ran: %v", err)
		}
		if err := f.driver.Release(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if left := labelled(t, spec.Key); len(left) > 0 {
			t.Errorf("released, it left %v", left)
		}
	}
	ensure := func(t *testing.T, spec execution.TaskSpec) execution.TaskRef {
		t.Helper()
		made, err := f.driver.Ensure(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		return execution.TaskRef{Key: spec.Key, ProviderID: made.ID}
	}

	t.Run("record, then network", func(t *testing.T) {
		spec := f.spec("crash-recorded", script, private(), result)
		abandoned(t, "recorded", spec.Key, func() error { _, err := f.driver.Ensure(ctx, spec); return err })
		if left := labelled(t, spec.Key); len(left) > 0 {
			t.Fatalf("made before the record's effect: %v", left)
		}
		ref := ensure(t, spec)
		if err := f.driver.Start(ctx, ref); err != nil {
			t.Fatal(err)
		}
		finish(t, spec, ref)
	})
	t.Run("network, then services", func(t *testing.T) {
		spec := f.spec("crash-network", script, private(), result)
		ref := ensure(t, spec)
		abandoned(t, "network", spec.Key, func() error { return f.driver.Start(ctx, ref) })
		network, _ := f.names(spec.Key)
		if left := labelled(t, spec.Key); len(left) != 1 || left[0] != network {
			t.Fatalf("after the network, the worker made %v", left)
		}
		if obs, err := f.driver.Observe(ctx, ref); err != nil || obs.State != execution.TaskCreated {
			t.Fatalf("a task whose start was abandoned: %+v %v", obs, err)
		}
		if err := f.driver.Start(ctx, ref); err != nil {
			t.Fatal(err)
		}
		finish(t, spec, ref)
	})
	t.Run("services, then daemon", func(t *testing.T) {
		spec := f.spec("crash-services", script, private(), result)
		ref := ensure(t, spec)
		abandoned(t, "services", spec.Key, func() error { return f.driver.Start(ctx, ref) })
		network, daemon := f.names(spec.Key)
		service := network + "-svc"
		before := inspect(t, "{{.Id}}", service)
		if before == "" || inspect(t, "{{.Name}}", daemon) != "" {
			t.Fatalf("abandoned after the services, before the daemon: %v", labelled(t, spec.Key))
		}
		if obs, err := f.driver.Observe(ctx, ref); err != nil || obs.State != execution.TaskCreated {
			t.Fatalf("a task whose start was abandoned: %+v %v", obs, err)
		}
		// It never ran: what the abandoned start made is removed, and made
		// once more, whole.
		if err := f.driver.Start(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if after := inspect(t, "{{.Id}}", service); after == before || len(labelled(t, spec.Key)) != 3 {
			t.Fatalf("the started task's private Docker: %v (service %s, then %s)", labelled(t, spec.Key), before, after)
		}
		finish(t, spec, ref)
	})
	t.Run("exit, then stop", func(t *testing.T) {
		spec := f.spec("crash-exited", script, private(), result)
		ref := ensure(t, spec)
		if err := f.driver.Start(ctx, ref); err != nil {
			t.Fatal(err)
		}
		_, daemon := f.names(spec.Key)
		abandoned(t, "exited", spec.Key, func() error { _, err := f.driver.Wait(ctx, ref); return err })
		if running(daemon) != "true" {
			t.Fatal("abandoned before the stop, the daemon was stopped")
		}
		if _, err := f.driver.Results(ctx, ref); err != nil {
			t.Fatalf("read after the restart: %v", err)
		}
		if running(daemon) != "false" {
			t.Fatal("the results were read while the daemon runs")
		}
		finish(t, spec, ref)
	})
	t.Run("stop, then capture", func(t *testing.T) {
		spec := f.spec("crash-quiet", script, private(), result)
		ref := ensure(t, spec)
		if err := f.driver.Start(ctx, ref); err != nil {
			t.Fatal(err)
		}
		_, daemon := f.names(spec.Key)
		abandoned(t, "quiet", spec.Key, func() error { _, err := f.driver.Wait(ctx, ref); return err })
		if running(daemon) != "false" {
			t.Fatal("recorded quiet, the daemon runs")
		}
		if err := f.driver.Start(ctx, ref); !errors.Is(err, execution.ErrStartRefused) || running(daemon) != "false" {
			t.Fatalf("a start after the stop was recorded: %v", err)
		}
		finish(t, spec, ref)
	})
	t.Run("release decided, then removal", func(t *testing.T) {
		spec := f.spec("crash-releasing", script, private(), result)
		ref := ensure(t, spec)
		if err := f.driver.Start(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if code, err := f.driver.Wait(ctx, ref); err != nil || code != 0 {
			t.Fatalf("the task: %d %v", code, err)
		}
		abandoned(t, "releasing", spec.Key, func() error { return f.driver.Release(ctx, ref) })
		if left := labelled(t, spec.Key); len(left) != 3 {
			t.Fatalf("abandoned before the removal, the task has %v", left)
		}
		if _, err := f.driver.Ensure(ctx, spec); err == nil || !strings.Contains(err.Error(), "stale") {
			t.Fatalf("an ensure after the release was decided: %v", err)
		}
		if err := f.driver.Start(ctx, ref); err == nil || !strings.Contains(err.Error(), "stale") {
			t.Fatalf("a start after the release was decided: %v", err)
		}
		if err := f.driver.Release(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if left := labelled(t, spec.Key); len(left) > 0 {
			t.Errorf("released, it left %v", left)
		}
	})
	t.Run("a record that cannot be written", func(t *testing.T) {
		spec := f.spec("unwritable", script, private(), result)
		restore := worker.FailAfterRename(spec.Key+".json", errors.New("the records folder could not be synced"))
		_, err := f.driver.Ensure(ctx, spec)
		restore()
		if err == nil || !strings.Contains(err.Error(), "could not be synced") {
			t.Fatalf("an ensure whose record was not written: %v", err)
		}
		if left := labelled(t, spec.Key); len(left) > 0 {
			t.Fatalf("made without a durable record: %v", left)
		}
		if obs, err := f.driver.Observe(ctx, execution.TaskRef{Key: spec.Key}); err != nil || obs.State != execution.TaskAbsent {
			t.Fatalf("a task made without a durable record: %+v %v", obs, err)
		}
		// Asked again once the folder can be written, it is made.
		ref := ensure(t, spec)
		if err := f.driver.Start(ctx, ref); err != nil {
			t.Fatal(err)
		}
		finish(t, spec, ref)
	})
}

// refusedWhileFlagged refuses a command while a file named for its first
// word is in flags, and passes the rest to the real Docker.
type refusedWhileFlagged struct {
	real  dockercli.Client
	flags string
}

func (r refusedWhileFlagged) Run(ctx context.Context, host string, c dockercli.Command) ([]byte, error) {
	if _, err := os.Stat(filepath.Join(r.flags, c.Args[0])); err == nil {
		return []byte(c.Args[0] + " refused for the test\n"), errors.New("exit status 1")
	}
	return r.real.Run(ctx, host, c)
}

// recordsBeforeMade notes, for each network or container the worker makes,
// whether a record already named it, then passes the command to the real
// Docker.
type recordsBeforeMade struct {
	real          dockercli.Client
	records, made string
}

func (r recordsBeforeMade) Run(ctx context.Context, host string, c dockercli.Command) ([]byte, error) {
	args, name, what := c.Args, "", ""
	switch {
	case len(args) > 1 && args[0] == "network" && args[1] == "create":
		name, what = args[len(args)-1], "network"
	case args[0] == "run":
		for i := 1; i < len(args); i++ {
			if args[i-1] == "--name" {
				name = args[i]
			}
		}
		what = "run"
	}
	if name != "" {
		state := "unrecorded"
		files, _ := filepath.Glob(filepath.Join(r.records, "*.json"))
		for _, file := range files {
			if b, err := os.ReadFile(file); err == nil && strings.Contains(string(b), `"`+name+`"`) {
				state = "recorded"
			}
		}
		note, err := os.OpenFile(r.made, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return nil, err
		}
		_, _ = fmt.Fprintf(note, "%s %s %s\n", state, what, name)
		_ = note.Close()
	}
	return r.real.Run(ctx, host, c)
}
