//go:build integration

package worker_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/diskspace"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/poll"
	"flokkit.io/forge/internal/worker/workertest"
)

// Qualification of the worker-vm profile (design/operations/workers.md,
// "Private Docker on the worker"): the private-Docker suite with the worker as a real apptown-worker
// process on a separate Linux machine that shares no files with this one.
// This test is the forge's side: it sends requests over mutual TLS
// (workertest.External names the worker), observes the worker's Docker
// through DOCKER_HOST, and restarts the worker through its machine's
// control command. Skipped unless APPTOWN_QUAL_WORKER_URL is set. The record
// of a run is design/reviews/qualification/.

// newQualFixture is a fixture over the external worker, checked to observe
// that worker's Docker and not this machine's.
func newQualFixture(t *testing.T) *privateFixture {
	t.Helper()
	f := newPrivateFixtureOn(t, workertest.ExternalWorker(t))
	out, err := exec.Command("docker", "info", "--format", "{{.KernelVersion}}|{{.ServerVersion}}|{{.Driver}}").CombinedOutput()
	if err != nil {
		t.Fatalf("docker info: %v: %s", err, out)
	}
	if got, want := strings.TrimSpace(string(out)), f.profile.Kernel+"|"+f.profile.Docker+"|"+f.profile.StorageDriver; got != want {
		t.Fatalf("DOCKER_HOST is not the worker's Docker: it reports %s, the worker %s", got, want)
	}
	t.Logf("the worker's profile: %+v", f.profile)
	return f
}

// A task on the separate worker builds an image with its own daemon and
// runs it with its tree at the same path, reaches a service by name, and
// is given nothing else of the worker. The service's parameters (a need's,
// as the forge froze them) come from the request the worker recorded:
// the worker process is killed between the ensure and the start and
// started again over the same state. Limits are configured; after the task
// exits its daemon and service are stopped; its release leaves nothing.
func TestIntegrationQualificationATaskHasItsOwnDockerAndServices(t *testing.T) {
	f := newQualFixture(t)
	ctx := context.Background()
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
	f.restart()
	ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}
	if err := f.driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	service := network + "-cache"
	if env := inspect(t, "{{json .Config.Env}}", service); !strings.Contains(env, "GREETING_DB=sourdough") {
		t.Errorf("the service's parameters after the restart: %s", env)
	}
	code, err := f.driver.Wait(ctx, ref)
	page, _ := f.driver.Output(ctx, ref, execution.OutputRead{})
	out := string(page.Data)
	if err != nil || code != 0 {
		t.Fatalf("the task: %d %v\n%s", code, err, out)
	}
	t.Logf("the task said: %s", strings.ReplaceAll(strings.TrimSpace(out), "\n", "; "))
	for _, want := range []string{"hello from a nested build", "same path", "nested wrote", "reached the cache", "builder apptown"} {
		if !strings.Contains(out, want) {
			t.Errorf("the task's output lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"HOST SOCKET", "HOST DAEMON", "FORGE CACHE"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("the task was supplied %s:\n%s", unwanted, out)
		}
	}
	limits := "{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}} {{.HostConfig.MemorySwap}} {{.HostConfig.PidsLimit}}"
	if got := inspect(t, limits, daemon); got != "1000000000 536870912 536870912 512" {
		t.Errorf("the daemon's limits: %s", got)
	}
	if got := inspect(t, limits, service); got != "1000000000 1073741824 1073741824 256" {
		t.Errorf("the service's limits: %s", got)
	}
	if state := inspect(t, "{{.State.Running}}", daemon) + inspect(t, "{{.State.Running}}", service); state != "falsefalse" {
		t.Errorf("after the task exited, its daemon and service run: %s", state)
	}
	if err := f.driver.Release(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if left := labelled(t, spec.Key); len(left) > 0 {
		t.Errorf("released, the task left %v", left)
	}
}

// The worker process killed while a task with private Docker runs, and
// started again over the same state, finds the task running with the same
// daemon and makes nothing again; the task finishes and its result is
// read. A stop covers the whole invocation: while it runs, its result is
// not read and its workspace not collected; once the stop is confirmed the
// nested writer has stopped, the captured bytes do not change, a late start
// is refused, and after the release the request is stale. A task whose
// admission expires is stopped and its private Docker removed by the
// worker's own sweep, with nothing asking it.
func TestIntegrationQualificationAnInterruptedWorkerRecoversAndAStopFences(t *testing.T) {
	f := newQualFixture(t)
	ctx := context.Background()
	spec := f.spec("interrupted", `docker info >/dev/null && echo up; sleep 15; echo done > /work/done.txt`, &execution.PrivateDocker{Daemon: true},
		execution.Result{Name: "done", Path: "/work/done.txt", Max: 64})
	created, err := f.driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}
	if err := f.driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	awaitOutput(t, f, ref, "up")
	_, daemon := f.names(spec.Key)
	first := inspect(t, "{{.Id}}", daemon)
	f.restart() // killed while the task runs
	if obs, err := f.driver.Observe(ctx, ref); err != nil || obs.State != execution.TaskRunning {
		t.Fatalf("after the worker was killed and started again, the task: %+v %v", obs, err)
	}
	if err := f.driver.Start(ctx, ref); err != nil {
		t.Fatalf("a repeated start after the restart: %v", err)
	}
	if again := inspect(t, "{{.Id}}", daemon); again != first {
		t.Fatalf("the restarted worker made the daemon again: %s, then %s", first, again)
	}
	if code, err := f.driver.Wait(ctx, ref); err != nil || code != 0 {
		t.Fatalf("the task: %d %v", code, err)
	}
	if got, err := f.driver.Results(ctx, ref); err != nil || string(got["done"].Content) != "done\n" {
		t.Fatalf("its result: %+v %v", got, err)
	}
	if err := f.driver.Release(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if left := labelled(t, spec.Key); len(left) > 0 {
		t.Errorf("released, it left %v", left)
	}

	// The stop and its fence.
	stop := f.spec("stop", `set -e
docker run -d -v /work:/work busybox:1.37 sh -c 'while true; do echo tick >> /work/ticks.txt; sleep 0.1; done'
echo writer-ready
sleep 300`, &execution.PrivateDocker{Daemon: true}, execution.Result{Name: "ticks", Path: "/work/ticks.txt", Max: 1 << 20})
	created, err = f.driver.Ensure(ctx, stop)
	if err != nil {
		t.Fatal(err)
	}
	ref = execution.TaskRef{Key: stop.Key, ProviderID: created.ID}
	if err := f.driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	awaitOutput(t, f, ref, "writer-ready")
	if _, err := f.driver.Results(ctx, ref); err == nil {
		t.Error("the results were read while the task and its nested writer run")
	}
	if tip, err := f.driver.Collect(ctx, stop.Key, f.base, "capture", t.TempDir()+"/result.bundle"); err == nil {
		t.Errorf("the workspace was collected (tip %s) while the nested writer writes", tip)
	}
	if err := f.driver.Stop(ctx, ref); err != nil {
		t.Fatal(err)
	}
	_, daemon = f.names(stop.Key)
	if running := inspect(t, "{{.State.Running}}", daemon); running != "false" {
		t.Fatalf("a confirmed stop left the daemon running: %s", running)
	}
	got, err := f.driver.Results(ctx, ref)
	if err != nil || got["ticks"].Size == 0 {
		t.Fatalf("the result after the stop: %+v %v", got, err)
	}
	// The fence is the guarantee: results and collection were refused while
	// the nested writer ran (above), and are given once its daemon is
	// confirmed stopped; a result read again is the one captured.
	if again, err := f.driver.Results(ctx, ref); err != nil || string(again["ticks"].Content) != string(got["ticks"].Content) {
		t.Fatalf("the result read again after the confirmed stop: %v", err)
	}
	tip, err := f.driver.Collect(ctx, stop.Key, f.base, "capture", t.TempDir()+"/result.bundle")
	if err != nil {
		t.Fatalf("collecting after the confirmed stop: %v", err)
	}
	if again, err := f.driver.Collect(ctx, stop.Key, f.base, "capture", t.TempDir()+"/result.bundle"); err != nil || again != tip {
		t.Fatalf("collected again: %s, then %s (%v)", tip, again, err)
	}
	if err := f.driver.Start(ctx, ref); !errors.Is(err, execution.ErrStartRefused) {
		t.Fatalf("a start after the stop: %v", err)
	}
	if err := f.driver.Release(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if left := labelled(t, stop.Key); len(left) > 0 {
		t.Errorf("released, it left %v", left)
	}
	if _, err := f.driver.Ensure(ctx, stop); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("the request replayed after the release: %v", err)
	}

	// Expiry, with nothing asking the worker: its sweep runs each minute.
	expiring := f.spec("expiring", "sleep 600", &execution.PrivateDocker{Daemon: true, Services: []deploy.ServiceSpec{{Name: "svc", Image: "busybox:1.37", Command: []string{"sleep", "3600"}}}})
	expiring.Admission.Expires = time.Now().Add(20 * time.Second)
	created, err = f.driver.Ensure(ctx, expiring)
	if err != nil {
		t.Fatal(err)
	}
	ref = execution.TaskRef{Key: expiring.Key, ProviderID: created.ID}
	if err := f.driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if n := len(labelled(t, expiring.Key)); n != 3 {
		t.Fatalf("the expiring task's private Docker: %v", labelled(t, expiring.Key))
	}
	if !poll.Until(ctx, 3*time.Minute, 5*time.Second, func() bool { return len(labelled(t, expiring.Key)) == 0 }) {
		t.Fatalf("three minutes after its expiry, the task kept %v", labelled(t, expiring.Key))
	}
	t.Logf("swept %s after its expiry", time.Since(expiring.Admission.Expires).Round(time.Second))
	if obs, err := f.driver.Observe(ctx, ref); err != nil || obs.State != execution.TaskExited {
		t.Errorf("expired, the task: %+v %v", obs, err)
	}
	if err := f.driver.Release(ctx, ref); err != nil {
		t.Fatal(err)
	}
}

// The daemon's limits and a service's budget hold on the separate worker
// (limitsHold, observed from the cgroups there).
func TestIntegrationQualificationLimitsHold(t *testing.T) {
	limitsHold(t, newQualFixture(t))
}

// Disk exhaustion by nested work. A task's nested container writes until
// the worker's disk refuses writes (measured there: nothing available). While it is full, the worker still
// answers, and a new request is refused for disk space before anything of
// it is made (the worker's disk admission). Once the operator frees a little space (the reserve, made
// before the fill so that recovery never depends on the fill), the full
// task is stopped and released, which removes its daemon with everything
// it wrote; the disk returns to what it was, and new work runs.
//
// It fills the worker's real disk, so it runs only with APPTOWN_QUAL_DISK=1
// exactly, and the worker's control command must answer reserve (write a
// reserve file), unreserve (remove it) and avail (bytes available on the
// worker's state directory's file system).
func TestIntegrationQualificationDiskExhaustionIsRefusedAndRecovered(t *testing.T) {
	if os.Getenv("APPTOWN_QUAL_DISK") != "1" {
		t.Skip("APPTOWN_QUAL_DISK is not 1: this test fills the worker's disk")
	}
	f := newQualFixture(t)
	ctx := context.Background()
	avail := func() int64 {
		n, err := strconv.ParseInt(f.external.Ctl(t, "avail"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := avail()
	f.external.Ctl(t, "reserve")
	t.Cleanup(func() { f.external.Ctl(t, "unreserve") })
	t.Logf("available before: %d MiB; with the reserve: %d MiB", before>>20, avail()>>20)

	fill := f.spec("disk-fill", `docker run --name filler busybox:1.37 sh -c 'cat /dev/urandom > /fill; echo "filler stopped: exit $?"'
echo filled
sleep 1200`, &execution.PrivateDocker{Daemon: true})
	fill.Admission.Expires = time.Now().Add(40 * time.Minute)
	created, err := f.driver.Ensure(ctx, fill)
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: fill.Key, ProviderID: created.ID}
	if err := f.driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	var out string
	if !poll.Until(ctx, 30*time.Minute, 5*time.Second, func() bool {
		page, err := f.driver.Output(ctx, ref, execution.OutputRead{})
		if err != nil {
			t.Fatalf("the worker, filling: %v", err)
		}
		out = string(page.Data)
		return strings.Contains(out, "filled")
	}) {
		t.Fatalf("the disk was not filled in 30 minutes:\n%s", out)
	}
	full := avail()
	t.Logf("filled in %s; the nested writer said: %s; available: %d MiB", time.Since(began).Round(time.Second), strings.ReplaceAll(strings.TrimSpace(out), "\n", "; "), full>>20)
	// The nested writer's own report of its failed write is lost: its
	// daemon cannot write the container's log on a full disk. Fullness is
	// measured on the worker's machine instead.
	if full > 64<<20 {
		t.Fatalf("the nested writer stopped with %d MiB still available:\n%s", full>>20, out)
	}

	// Full: the worker answers, and refuses new work with the reason.
	if obs, err := f.driver.Observe(ctx, ref); err != nil || obs.State != execution.TaskRunning {
		t.Errorf("the full task, observed: %+v %v", obs, err)
	}
	refusedSpec := f.spec("disk-refused", "docker info >/dev/null && echo ran", &execution.PrivateDocker{Daemon: true})
	var refusedRef *execution.TaskRef
	refusal := func() error {
		made, err := f.driver.Ensure(ctx, refusedSpec)
		if err != nil {
			return err
		}
		refusedRef = &execution.TaskRef{Key: refusedSpec.Key, ProviderID: made.ID}
		return f.driver.Start(ctx, *refusedRef)
	}()
	t.Logf("a request while the disk is full: %v (ensured: %t)", refusal, refusedRef != nil)
	// The worker reads its disks before it makes anything of a new task, so
	// it is refused for disk space, saying what is free, with nothing made.
	if refusal == nil {
		t.Error("a request was run while the disk was full")
	} else if !errors.Is(refusal, diskspace.ErrRefused) || !strings.Contains(refusal.Error(), "free") {
		t.Errorf("refused without the disk's reason: %v", refusal)
	}
	if refusedRef != nil {
		t.Errorf("a task was made while the disk was full: %+v", *refusedRef)
	}
	if left := labelled(t, refusedSpec.Key); len(left) > 0 {
		t.Errorf("refused, it made %v", left)
	}

	// The operator frees a little space; the full task is stopped and
	// released, and with it everything its daemon wrote.
	f.external.Ctl(t, "unreserve")
	if err := f.driver.Stop(ctx, ref); err != nil {
		t.Fatalf("stopping the full task once space was freed: %v", err)
	}
	if err := f.driver.Release(ctx, ref); err != nil {
		t.Fatalf("releasing the full task: %v", err)
	}
	if left := labelled(t, fill.Key); len(left) > 0 {
		t.Errorf("released, the full task left %v", left)
	}
	if refusedRef != nil {
		if err := f.driver.Release(ctx, *refusedRef); err != nil {
			t.Errorf("releasing the refused task: %v", err)
		}
	}
	// The file system frees what was removed in the background (ZFS
	// does): what is available is polled, up to two minutes.
	released := time.Now()
	var after int64
	poll.Until(ctx, 2*time.Minute, 5*time.Second, func() bool { after = avail(); return after >= before-256<<20 })
	t.Logf("available %s after the release: %d MiB (before: %d MiB)", time.Since(released).Round(time.Second), after>>20, before>>20)
	if after < before-256<<20 {
		t.Errorf("the release did not give the disk back: %d MiB available, %d before", after>>20, before>>20)
	}

	// New work runs.
	again := f.spec("disk-recovered", `docker build -q -t nested-greeting /work >/dev/null && docker run --rm nested-greeting`, &execution.PrivateDocker{Daemon: true})
	created, err = f.driver.Ensure(ctx, again)
	if err != nil {
		t.Fatal(err)
	}
	ref = execution.TaskRef{Key: again.Key, ProviderID: created.ID}
	if err := f.driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	code, err := f.driver.Wait(ctx, ref)
	page, _ := f.driver.Output(ctx, ref, execution.OutputRead{})
	if err != nil || code != 0 || !strings.Contains(string(page.Data), "hello from a nested build") {
		t.Fatalf("work after the recovery: %d %v\n%s", code, err, page.Data)
	}
	if err := f.driver.Release(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if left := labelled(t, again.Key); len(left) > 0 {
		t.Errorf("released, it left %v", left)
	}
}
