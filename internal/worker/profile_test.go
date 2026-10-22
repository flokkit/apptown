package worker_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/execution/executionfake"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// profileDocker is an in-process Docker for the worker's private Docker
// that records what it is asked. Its server version is 26.1.5 and its
// kernel 6.8.12 (a test changes them); the private daemon's image has a
// local id, a repository digest, and a dockerd that reports its version.
type profileDocker struct {
	mu                       sync.Mutex
	version, kernel, digests string
	noDockerd                bool
	calls                    []string
}

func newProfileDocker() *profileDocker {
	return &profileDocker{version: "26.1.5", kernel: "6.8.12", digests: `["docker@sha256:aa3"]`}
}

func (d *profileDocker) Run(_ context.Context, _ string, c docker.Command) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	args, line := c.Args, strings.Join(c.Args, " ")
	d.calls = append(d.calls, line)
	switch args[0] {
	case "info":
		return fmt.Appendf(nil, `{"OperatingSystem":"Debian GNU/Linux 13 (trixie)","OSType":"linux","Architecture":"x86_64","KernelVersion":%q,"CgroupVersion":"2","CgroupDriver":"systemd","ServerVersion":%q,"Driver":"overlay2"}`+"\n", d.kernel, d.version), nil
	case "image":
		return []byte("sha256:d1d|" + d.digests + "\n"), nil
	case "run":
		if strings.Contains(line, "--entrypoint dockerd") {
			if d.noDockerd {
				return []byte("no dockerd here\n"), errExit
			}
			return []byte("Docker version 27.5.1, build 4c9b3b0\n"), nil
		}
	case "network":
		if len(args) > 1 && args[1] == "inspect" {
			return []byte("Error: No such network: " + args[len(args)-1] + "\n"), errExit
		}
	}
	return nil, nil
}

// set changes the Docker under the lock.
func (d *profileDocker) set(change func(*profileDocker)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	change(d)
}

// made is every command since the last forget, one per line.
func (d *profileDocker) made() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.calls, "\n")
}

func (d *profileDocker) forget() { d.set(func(d *profileDocker) { d.calls = nil }) }

// anyWorkspace is a backend that takes a task with a workspace and runs
// nothing, as executionfake does for one without.
type anyWorkspace struct{ *executionfake.Tasks }

func (d anyWorkspace) Ensure(ctx context.Context, spec execution.TaskSpec) (execution.TaskObservation, error) {
	spec.Workspace, spec.Mounts, spec.Network = "", nil, ""
	return d.Tasks.Ensure(ctx, spec)
}

// A worker measures its profile when it starts and reports it: what docker
// info says of the Docker it runs private Docker on, the private daemon's
// image as it is on the worker (its local id, its repository digest, and
// the version its dockerd reports), a privileged container probed (after
// the privileged mark), and the operator's declaration. A check worker runs no
// probe, and reports privilege as unknown.
func TestAWorkerReportsTheProfileItMeasured(t *testing.T) {
	ctx := context.Background()
	binary := newProfileDocker()
	state := t.TempDir()
	srv := &worker.Server{Installation: "inst-1", State: state, Tasks: &executionfake.Tasks{}, Docker: binary, DindImage: "docker:27-dind", Role: worker.Role{Role: worker.RoleAgent}}
	ca := workertest.NewCA(t)
	driver := workertest.Driver(t, ca, workertest.StartServer(t, ca, srv))
	if _, err := driver.Profile(ctx); err == nil {
		t.Fatal("a worker that measured nothing reported a profile")
	}
	srv.Profile = srv.MeasureProfile(ctx, true)
	got, err := driver.Profile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got.Measured = time.Time{}
	want := execution.WorkerProfile{Name: "worker-vm", OS: "linux Debian GNU/Linux 13 (trixie)", Architecture: "x86_64", Kernel: "6.8.12", Cgroup: "v2 (systemd)",
		Docker: "26.1.5", StorageDriver: "overlay2", DindImage: "docker:27-dind", DindImageID: "sha256:d1d", DindDigest: "docker@sha256:aa3",
		DindVersion: "27.5.1, build 4c9b3b0", Privileged: "yes", Exclusive: true, Egress: "not contained"}
	if got != want {
		t.Fatalf("the profile:\n%+v, want\n%+v", got, want)
	}
	if _, err := os.Stat(filepath.Join(state, "privileged-agents")); err != nil {
		t.Errorf("the privileged probe ran without the mark: %v", err)
	}
	if recorded := binary.made(); !strings.Contains(recorded, "run --rm --privileged") {
		t.Errorf("no privileged container was probed:\n%s", recorded)
	}

	// An image without a repository digest, or whose daemon cannot be
	// asked its version, is measured as unknown, with the reason.
	binary.set(func(d *profileDocker) { d.digests, d.noDockerd = "[]", true })
	if p := srv.MeasureProfile(ctx, true); !strings.HasPrefix(p.DindDigest, "unknown: the image has no repository digest") || !strings.HasPrefix(p.DindVersion, "unknown: ") ||
		!strings.Contains(p.DindVersion, "no dockerd here") || p.DindImageID != "sha256:d1d" {
		t.Errorf("an image without a digest or a version: %+v", p)
	}

	check := &worker.Server{State: t.TempDir(), Docker: binary, DindImage: "docker:27-dind", Role: worker.Role{Role: worker.RoleCheck}}
	if p := check.MeasureProfile(ctx, false); !strings.HasPrefix(p.Privileged, "unknown") {
		t.Errorf("a check worker's privilege: %q", p.Privileged)
	}
}

// Private Docker is made only on the profile it was admitted on. A request
// naming another, or none, is refused before anything of it is made; one
// ensured on the same profile is refused at its start once the worker's
// Docker has changed (here, upgraded), and nothing private is made.
func TestPrivateDockerIsRefusedOnAChangedProfile(t *testing.T) {
	ctx := context.Background()
	binary := newProfileDocker()
	srv := &worker.Server{Installation: "inst-1", State: t.TempDir(), Tasks: anyWorkspace{&executionfake.Tasks{}}, Docker: binary, DindImage: "docker:27-dind", Role: worker.Role{Role: worker.RoleAgent}}
	srv.Profile = srv.MeasureProfile(ctx, false)
	ca := workertest.NewCA(t)
	driver := workertest.Driver(t, ca, workertest.StartServer(t, ca, srv))
	hub := sourceRepo(t)
	base, _ := os.ReadFile(filepath.Join(hub, ".git", "refs", "heads", "main"))
	spec := func(key string, profile *execution.WorkerProfile) execution.TaskSpec {
		s := fakeSpec(key, "", 0, false)
		s.Admission.Network, s.Admission.Limits = execution.NetworkDefault, execution.Limits{CPUs: 1, MemoryBytes: 1 << 30, PIDs: 256}
		s.Source = &execution.TaskSource{Repository: hub, Commit: strings.TrimSpace(string(base))}
		s.Private = &execution.PrivateDocker{Daemon: true, Profile: profile}
		return s
	}
	upgraded := srv.Profile
	upgraded.Docker = "27.0.1"
	kernel, os2, arch := srv.Profile, srv.Profile, srv.Profile
	kernel.Kernel, os2.OS, arch.Architecture = "6.8.11", "linux Debian GNU/Linux 12 (bookworm)", "aarch64"
	for name, profile := range map[string]*execution.WorkerProfile{"another profile": &upgraded, "no profile": nil, "another kernel": &kernel, "another OS": &os2, "another architecture": &arch} {
		binary.forget()
		if _, err := driver.Ensure(ctx, spec("refused-"+strings.ReplaceAll(strings.ToLower(name), " ", "-"), profile)); !errors.Is(err, execution.ErrUnsupported) || !strings.Contains(err.Error(), "profile") {
			t.Errorf("%s: %v", name, err)
		}
		if made := binary.made(); strings.Contains(made, "create") || strings.Contains(made, "run ") {
			t.Errorf("%s: private Docker was made:\n%s", name, made)
		}
	}

	// The daemon's image matters only to a request with a daemon.
	otherImage := srv.Profile
	otherImage.DindImageID, otherImage.DindDigest, otherImage.DindVersion = "sha256:other", "docker@sha256:other", "27.5.2"
	for what, want := range map[string]string{"id": "private daemon image id", "digest": "private daemon image digest", "version": "private daemon version"} {
		one := srv.Profile
		switch what {
		case "id":
			one.DindImageID = otherImage.DindImageID
		case "digest":
			one.DindDigest = otherImage.DindDigest
		case "version":
			one.DindVersion = otherImage.DindVersion
		}
		if _, err := driver.Ensure(ctx, spec("daemon-other-image-"+what, &one)); !errors.Is(err, execution.ErrUnsupported) || !strings.Contains(err.Error(), want) {
			t.Errorf("a daemon admitted on another image %s: %v", what, err)
		}
	}
	services := spec("services-other-image", &otherImage)
	services.Private.Daemon, services.Private.Services = false, []deploy.ServiceSpec{{Name: "cache", Image: "redis:7-alpine"}}
	if _, err := driver.Ensure(ctx, services); err != nil {
		t.Errorf("services admitted when the daemon's image was another: %v", err)
	}

	same := srv.Profile
	s := spec("upgraded-before-start", &same)
	created, err := driver.Ensure(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	binary.set(func(d *profileDocker) { d.version = "27.0.1" })
	binary.forget()
	err = driver.Start(ctx, execution.TaskRef{Key: s.Key, ProviderID: created.ID})
	if !errors.Is(err, execution.ErrStartRefused) || !strings.Contains(err.Error(), `Docker "26.1.5" is now "27.0.1"`) {
		t.Fatalf("a start after the upgrade: %v", err)
	}
	if made := binary.made(); strings.Contains(made, "run ") {
		t.Errorf("private Docker was made after the upgrade:\n%s", made)
	}
}

// A request admitted on one kernel is refused on another, whether the
// worker was restarted after a reboot into it before the request was
// ensured, or the kernel changed between its ensure and its start. Nothing
// private is made.
func TestPrivateDockerIsRefusedOnAnotherKernel(t *testing.T) {
	ctx := context.Background()
	binary := newProfileDocker()
	srv := &worker.Server{Installation: "inst-1", State: t.TempDir(), Tasks: anyWorkspace{&executionfake.Tasks{}}, Docker: binary, DindImage: "docker:27-dind", Role: worker.Role{Role: worker.RoleAgent}}
	admitted := srv.MeasureProfile(ctx, false)
	srv.Profile = admitted
	ca := workertest.NewCA(t)
	driver := workertest.Driver(t, ca, workertest.StartServer(t, ca, srv))
	hub := sourceRepo(t)
	base, err := os.ReadFile(filepath.Join(hub, ".git", "refs", "heads", "main"))
	if err != nil {
		t.Fatal(err)
	}
	spec := func(key string) execution.TaskSpec {
		s := fakeSpec(key, "", 0, false)
		s.Admission.Network, s.Admission.Limits = execution.NetworkDefault, execution.Limits{CPUs: 1, MemoryBytes: 1 << 30, PIDs: 256}
		s.Source = &execution.TaskSource{Repository: hub, Commit: strings.TrimSpace(string(base))}
		profile := admitted
		s.Private = &execution.PrivateDocker{Daemon: true, Profile: &profile}
		return s
	}
	rebooted := func() { binary.set(func(d *profileDocker) { d.kernel = "6.8.13" }) }

	ensured := spec("kernel-before-start")
	created, err := driver.Ensure(ctx, ensured)
	if err != nil {
		t.Fatal(err)
	}
	rebooted()
	binary.forget()
	if err := driver.Start(ctx, execution.TaskRef{Key: ensured.Key, ProviderID: created.ID}); !errors.Is(err, execution.ErrStartRefused) || !strings.Contains(err.Error(), `kernel "6.8.12" is now "6.8.13"`) {
		t.Fatalf("a start after the kernel changed: %v", err)
	}
	if made := binary.made(); strings.Contains(made, "run ") {
		t.Errorf("private Docker was made on another kernel:\n%s", made)
	}

	srv.Profile = srv.MeasureProfile(ctx, false) // the worker restarted after the reboot
	if srv.Profile.Kernel == admitted.Kernel {
		t.Fatal("the fixture did not change the measured kernel")
	}
	binary.forget()
	if obs, err := driver.Ensure(ctx, spec("kernel-before-ensure")); !errors.Is(err, execution.ErrUnsupported) || !strings.Contains(err.Error(), "kernel") {
		t.Fatalf("a request admitted on kernel %q, on kernel %q: %s %v", admitted.Kernel, srv.Profile.Kernel, obs.State, err)
	}
	if made := binary.made(); strings.Contains(made, "create") || strings.Contains(made, "run ") {
		t.Errorf("private Docker was made on another kernel:\n%s", made)
	}
}
