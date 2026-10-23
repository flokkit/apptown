package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execfile"

	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// fakeDocker is the daemon as the runtime sees it through docker commands:
// one console container, one private daemon, one network, one builder.
type fakeDocker struct {
	mu                                sync.Mutex
	id, checkout, dir                 string
	network, daemon, running, builder bool
	console                           bool // the console container exists
	readOnly                          bool
	owner                             string
	calls                             [][]string
	fail                              string
	volumes                           map[string]string // name → its AppTown.console label
}

func (f *fakeDocker) run(ctx context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{}, args...))
	call := strings.Join(args, " ")
	if f.fail != "" && strings.Contains(call, f.fail) {
		return []byte("daemon unavailable"), errors.New("injected Docker failure")
	}
	missing := func() ([]byte, error) { return []byte("Error: No such object"), errors.New("missing") }
	if strings.Contains(call, scriptName(f.id)) { // no script runs in these tests
		if args[0] == "inspect" {
			return missing()
		}
		return nil, nil
	}
	switch {
	case args[0] == "volume" && args[1] == "create":
		if f.volumes == nil {
			f.volumes = map[string]string{}
		}
		if _, ok := f.volumes[args[len(args)-1]]; !ok {
			f.volumes[args[len(args)-1]] = strings.TrimPrefix(args[5], "apptown.console=")
		}
	case args[0] == "volume" && args[1] == "inspect":
		owner, ok := f.volumes[args[len(args)-1]]
		if !ok {
			return []byte("Error response from daemon: get x: no such volume"), errors.New("missing")
		}
		return []byte(owner), nil
	case args[0] == "volume" && args[1] == "rm":
		delete(f.volumes, args[len(args)-1])
	case args[0] == "volume" && args[1] == "ls":
		var owners []string
		for _, owner := range f.volumes {
			owners = append(owners, owner)
		}
		return []byte(strings.Join(owners, "\n")), nil
	case args[0] == "network" && args[1] == "inspect":
		if !f.network {
			return missing()
		}
		return []byte(f.owner), nil
	case args[0] == "network" && args[1] == "create":
		f.network = true
		f.owner = f.id
	case args[0] == "network" && args[1] == "rm":
		f.network = false
	case args[0] == "network" && args[1] == "ls" && f.network, args[0] == "ps" && f.daemon:
		return []byte(f.id), nil
	case args[0] == "network" && args[1] == "ls", args[0] == "ps":
		return nil, nil
	case args[0] == "inspect":
		if args[len(args)-1] == ContainerName(f.id) {
			if !f.console {
				return missing()
			}
			return []byte(`false 0001-01-01T00:00:00Z img {"apptown.console.dind":"1","apptown.console.model":"m"}`), nil
		}
		if !f.daemon {
			if f.network && !strings.Contains(call, "--type container") {
				return []byte("template parsing error: network has no State"), errors.New("inspect found network")
			}
			return missing()
		}
		if strings.Contains(call, `"running"`) {
			return json.Marshal(map[string]any{"running": f.running, "owner": f.owner,
				"mounts":   []map[string]any{{"Type": "bind", "Source": f.checkout, "Destination": "/work", "RW": !f.readOnly}},
				"networks": map[string]any{dindName(f.id): map[string]any{}}})
		}
		if strings.Contains(call, "{{.State.Running}}") {
			return []byte(fmt.Sprintf("%s %t", f.owner, f.running)), nil
		}
		return []byte(f.owner), nil
	case args[0] == "run":
		f.daemon = true
		f.running = true
	case args[0] == "create":
		f.console = true
	case args[0] == "start":
		f.running = true
	case args[0] == "stop":
		f.running = false
	case args[0] == "rm":
		if strings.Contains(call, ContainerName(f.id)) {
			f.console = false
		} else {
			f.daemon = false
			f.running = false
		}
	case strings.Contains(call, "buildx inspect"):
		if !f.builder {
			return missing()
		}
	case strings.Contains(call, "buildx create"):
		f.builder = true
	}
	return nil, ctx.Err()
}

func fixture(t *testing.T) (*Runtime, console.Launch, *fakeDocker) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "console", "c0123")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	in := console.Launch{ID: "c0123", Dir: dir, Workspace: console.Workspace{Path: t.TempDir(), Writable: true}, AgentSession: "console", Model: "m", Docker: true}
	f := &fakeDocker{id: in.ID, checkout: in.Workspace.Path, dir: dir, owner: in.ID}
	r := New(Config{Run: f.run, Image: func() string { return "apptown-agent" }, Dind: DindConfig{Image: "docker:dind"}})
	t.Cleanup(r.Close)
	return r, in, f
}

func TestDindRecoveryAndDisposal(t *testing.T) {
	r, in, f := fixture(t)
	for i := 0; i < 2; i++ {
		if err := r.ensureDind(in); err != nil {
			t.Fatal(err)
		}
	}
	runs := 0
	for _, args := range f.calls {
		if args[0] == "run" {
			runs++
			call := strings.Join(args, " ")
			for _, want := range []string{"--privileged", "apptown.class=console-dind", "--network-alias dind", in.Workspace.Path + ":/work", "ZFS_MODULE_TIMEOUT=0", "--tls=false"} {
				if !strings.Contains(call, want) {
					t.Errorf("missing %s: %s", want, call)
				}
			}
			if strings.Contains(call, "docker.sock") || strings.Contains(call, "-p ") || strings.Contains(call, in.Dir+":/console") {
				t.Fatalf("sidecar leaked a host socket, port, or session secrets: %s", call)
			}
		}
	}
	if runs != 1 {
		t.Fatalf("repeated attachment started %d daemons", runs)
	}
	if err := r.disposeDindWithin(context.Background(), in.ID, false); err != nil {
		t.Fatal(err)
	}
	if !f.daemon || f.running || !f.network {
		t.Fatal("idle stop removed durable Docker state")
	}
	if err := r.ensureDind(in); err != nil {
		t.Fatal(err)
	}
	if !f.running {
		t.Fatal("idle daemon was not resumed")
	}
	// A fresh runtime adopts the same sidecar after a process restart.
	b := New(Config{Run: f.run, Image: func() string { return "apptown-agent" }, Dind: DindConfig{Image: "docker:dind"}})
	t.Cleanup(b.Close)
	if err := b.ensureDind(in); err != nil {
		t.Fatal(err)
	}
	if err := b.disposeDindWithin(context.Background(), in.ID, true); err != nil {
		t.Fatal(err)
	}
	if f.daemon || f.network {
		t.Fatal("session end left Docker resources")
	}
	if err := b.disposeDindWithin(context.Background(), in.ID, true); err != nil {
		t.Fatalf("repeated cleanup: %v", err)
	}
}

func TestDindRefusesUncertainAndForeignResources(t *testing.T) {
	for _, failure := range []string{"network inspect", "foreign owner", "different workspace", "read-only workspace"} {
		t.Run(failure, func(t *testing.T) {
			r, in, f := fixture(t)
			switch failure {
			case "foreign owner":
				f.network = true
				f.owner = "someoneelse"
			case "different workspace":
				f.network, f.daemon = true, true
				f.checkout = t.TempDir()
			case "read-only workspace":
				f.network, f.daemon, f.readOnly = true, true, true
			default:
				f.fail = failure
			}
			if err := r.ensureDind(in); err == nil {
				t.Fatal("unsafe recovery succeeded")
			}
			for _, args := range f.calls {
				if args[0] == "run" || args[0] == "rm" || args[0] == "start" || args[0] == "network" && args[1] == "create" {
					t.Fatalf("mutated after uncertain ownership: %v", args)
				}
			}
		})
	}
}

func TestDindAdoptsSameWorkspaceThroughHostPathAliases(t *testing.T) {
	for _, alias := range []string{"symlink", "desktop"} {
		t.Run(alias, func(t *testing.T) {
			if alias == "desktop" && runtime.GOOS != "darwin" {
				t.Skip("Docker Desktop macOS path translation")
			}
			r, in, f := fixture(t)
			f.network, f.daemon = true, true
			switch alias {
			case "symlink":
				f.checkout = filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(in.Workspace.Path, f.checkout); err != nil {
					t.Fatal(err)
				}
			case "desktop":
				resolved, err := filepath.EvalSymlinks(in.Workspace.Path)
				if err != nil {
					t.Fatal(err)
				}
				f.checkout = "/host_mnt" + resolved
			}
			if err := r.ensureDind(in); err != nil {
				t.Fatal(err)
			}
			if !f.running {
				t.Fatal("existing sidecar was not restarted")
			}
			for _, args := range f.calls {
				if args[0] == "run" || args[0] == "rm" {
					t.Fatalf("replaced surviving sidecar: %v", args)
				}
			}
		})
	}
}

func TestDindCloseStopsReadinessAndAdmission(t *testing.T) {
	r, in, f := fixture(t)
	entered := make(chan struct{})
	r.cfg.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "exec" {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return f.run(ctx, args...)
	}
	done := make(chan error, 1)
	go func() { done <- r.ensureDind(in) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("readiness never started")
	}
	r.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled readiness succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for readiness timeout")
	}
	before := len(f.calls)
	if err := r.ensureDind(in); err == nil {
		t.Fatal("closed runtime restarted")
	}
	if len(f.calls) != before {
		t.Fatal("Docker called after the runtime closed")
	}
}

// Reap removes what belongs to no live session and leaves the rest.
func TestReapPreservesLiveSessions(t *testing.T) {
	r, in, f := fixture(t)
	if err := r.ensureDind(in); err != nil {
		t.Fatal(err)
	}
	if err := r.Reap(context.Background(), map[string]bool{in.ID: true}); err != nil {
		t.Fatal(err)
	}
	if !f.daemon {
		t.Fatal("live session was reaped")
	}
	if err := r.Reap(context.Background(), map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if f.daemon || f.network {
		t.Fatal("ended session left orphan resources")
	}
}

// The provisioning path, including its runtime files, so the advertised
// capability cannot drift from the created container.
func TestProvisioning(t *testing.T) {
	for _, mode := range []string{"change", "project", "unavailable", "builder failure"} {
		t.Run(mode, func(t *testing.T) {
			r, in, f := fixture(t)
			if mode == "project" {
				in.Docker, in.Workspace.Writable = false, false
			}
			if mode == "unavailable" {
				f.fail = "network inspect"
			} else if mode == "builder failure" {
				f.fail = "buildx create"
			}
			in.Env = []string{"APPTOWN_URL=http://host.docker.internal:7778"}
			in.Mounts = []string{"/tools/apptown:/opt/apptown/bin/apptown:ro"}
			err := r.provision(in)
			failed := mode == "unavailable" || mode == "builder failure"
			if (err != nil) != failed {
				t.Fatalf("provisioning error: %v", err)
			}
			var create string
			for _, args := range f.calls {
				if args[0] == "create" {
					create = strings.Join(args, " ")
				}
			}
			if failed {
				if create != "" {
					t.Fatal("started a console while its promised Docker capability was unavailable")
				}
				f.fail = ""
				if err := r.provision(in); err != nil {
					t.Fatalf("retry failed: %v", err)
				}
				return
			}
			if create == "" {
				t.Fatal("console was not created")
			}
			for _, want := range []string{in.Dir + ":/console:ro", "APPTOWN_URL=http://host.docker.internal:7778", "/tools/apptown:/opt/apptown/bin/apptown:ro", "apptown.console.model=m"} {
				if !strings.Contains(create, want) {
					t.Errorf("create missing %s: %s", want, create)
				}
			}
			for _, dir := range writableDirs() {
				if !strings.Contains(create, filepath.Join(in.Dir, dir)+":/console/"+dir) {
					t.Fatalf("missing writable runtime directory %s", dir)
				}
			}
			if mode == "change" {
				for _, want := range []string{"--network " + dindName(in.ID), "DOCKER_HOST=tcp://dind:2375", "DOCKER_CONFIG=/console/docker-client", "BUILDX_BUILDER=apptown", "apptown.console.dind=1", in.Workspace.Path + ":/work "} {
					if !strings.Contains(create, want) {
						t.Errorf("client missing %s", want)
					}
				}
				if !f.running || !f.builder {
					t.Fatal("console created before Docker and its builder were ready")
				}
			} else if f.daemon || f.network || strings.Contains(create, "DOCKER_HOST") {
				t.Fatal("private Docker provisioned for an excluded console")
			}
			if mode == "project" && !strings.Contains(create, in.Workspace.Path+":/work:ro") {
				t.Fatal("project workspace lost its read-only mount")
			}
			for _, name := range []string{"run-pi.sh", "tmux.conf"} {
				if _, err := os.Stat(filepath.Join(in.Dir, name)); err != nil {
					t.Fatalf("launcher file %s: %v", name, err)
				}
			}
			run, _ := os.ReadFile(filepath.Join(in.Dir, "run-pi.sh"))
			if !strings.Contains(string(run), "--session-id 'console'") || !strings.Contains(string(run), "--model 'm'") {
				t.Fatalf("launcher: %s", run)
			}
			// Every chat installs the project's declared toolchain before
			// pi starts, whichever machine runs it: a worker's chats once
			// started without it and the declared Maven was not found.
			if install := strings.Index(string(run), console.ToolchainPrelude); install < 0 || install > strings.Index(string(run), "exec pi ") {
				t.Fatalf("launcher does not install the declared toolchain first: %s", run)
			}
		})
	}
}

// A container built before the runtime labelled its model reads as stale:
// it is rebuilt at the next safe wake, never mid-turn (the console's rule).
func TestStaleReadsTheLabels(t *testing.T) {
	r, in, f := fixture(t)
	f.console = true
	r.cfg.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" && args[len(args)-1] == ContainerName(in.ID) {
			return []byte("true 2026-09-09T00:00:00Z old-image {}"), nil
		}
		if args[0] == "image" {
			return []byte("old-image"), nil
		}
		return f.run(ctx, args...)
	}
	stale, err := r.Stale(context.Background(), in)
	if err != nil || !stale {
		t.Fatalf("a legacy container is stale: %v %v", stale, err)
	}
	r.cfg.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" && args[len(args)-1] == ContainerName(in.ID) {
			return []byte(`true 2026-09-09T00:00:00Z img {"apptown.console.dind":"1","apptown.console.model":"m","apptown.console.mounts":"` + r.mountsDigest(in) + `"}`), nil
		}
		if args[0] == "image" {
			return []byte("img"), nil
		}
		return f.run(ctx, args...)
	}
	if stale, err := r.Stale(context.Background(), in); err != nil || stale {
		t.Fatalf("a matching container is current: %v %v", stale, err)
	}
	in.Model = "other"
	if stale, _ := r.Stale(context.Background(), in); !stale {
		t.Fatal("a moved model makes the container stale")
	}
}

// A worker sends AppTown CLI as a content-named blob: a stopped chat
// created with one CLI is rebuilt when the launch mounts a newer one, so the
// chat runs the CLI AppTown now ships, not the one its container pinned.
// The labels Stale reads are the ones provision wrote, including an empty
// model (pi's default), which must not make an unchanged chat stale.
func TestANewCLIMountMakesTheChatStale(t *testing.T) {
	for _, model := range []string{"m", ""} {
		t.Run("model="+model, func(t *testing.T) {
			r, in, f := fixture(t)
			in.Model = model
			in.Mounts = []string{"/blobs/cli-a:/opt/apptown/bin/apptown:ro"}
			if err := r.provision(in); err != nil {
				t.Fatal(err)
			}
			labels := map[string]string{}
			for _, args := range f.calls {
				for i, a := range args {
					if args[0] == "create" && a == "--label" {
						k, v, _ := strings.Cut(args[i+1], "=")
						labels[k] = v
					}
				}
			}
			stale := func() bool {
				t.Helper()
				encoded, err := json.Marshal(labels)
				if err != nil {
					t.Fatal(err)
				}
				r.cfg.Run = func(ctx context.Context, args ...string) ([]byte, error) {
					if args[0] == "inspect" && args[len(args)-1] == ContainerName(in.ID) {
						return append([]byte("false 2026-09-09T00:00:00Z img "), encoded...), nil
					}
					if args[0] == "image" {
						return []byte("img"), nil
					}
					return f.run(ctx, args...)
				}
				stale, err := r.Stale(context.Background(), in)
				if err != nil {
					t.Fatal(err)
				}
				return stale
			}
			if stale() {
				t.Fatal("the container created for this launch is stale")
			}
			in.Mounts = []string{"/blobs/cli-b:/opt/apptown/bin/apptown:ro"}
			if !stale() {
				t.Fatal("a container pinning the old CLI is current")
			}
			in.Mounts = []string{"/blobs/cli-a:/opt/apptown/bin/apptown:ro"}
			delete(labels, "apptown.console.mounts")
			if !stale() {
				t.Fatal("a container from before the mounts label is current")
			}
		})
	}
}

// A chat that does not drive its change mounts the folder read-only in its
// Docker sidecar too, not only in its agent.
func TestAReadOnlyChatsSidecarCannotWriteTheWorkspace(t *testing.T) {
	r, in, f := fixture(t)
	in.Workspace.Writable = false
	if err := r.ensureDind(in); err != nil {
		t.Fatal(err)
	}
	for _, args := range f.calls {
		if args[0] == "run" {
			if call := strings.Join(args, " "); !strings.Contains(call, in.Workspace.Path+":/work:ro") {
				t.Fatalf("the sidecar mounts a read-only chat's workspace writable: %s", call)
			}
			return
		}
	}
	t.Fatal("no sidecar was started")
}

// A launch's own image is what its container is created from, after the
// end of Docker's options, so no image name can become an option.
func TestProvisionUsesTheLaunchImageAfterTheOptions(t *testing.T) {
	r, in, f := fixture(t)
	in.Image = "apptown-agent:from-the-forge"
	if err := r.provision(in); err != nil {
		t.Fatal(err)
	}
	var create string
	for _, args := range f.calls {
		if args[0] == "create" {
			create = strings.Join(args, " ")
		}
	}
	if !strings.HasSuffix(create, " -- apptown-agent:from-the-forge sleep infinity") {
		t.Fatalf("create: %s", create)
	}
}

// A worker's chat (CachePerSession, Limits, Mounts, a mirror from its
// AppTown): its own cache volume, labelled with it and mounted by its
// container and its daemon, never apptown-cache; the worker's tool cache in
// its container only; the same limits on both, and on its script;
// the mirror in the daemon's flags and the builder's configuration. The
// volume goes with the runtime, and the reap finds one left by a session
// that is gone.
func TestAWorkersChatHasItsOwnCacheLimitsAndMirror(t *testing.T) {
	r, in, f := fixture(t)
	r.cfg.CachePerSession = true
	r.cfg.Limits = &Limits{CPUs: 1.5, MemoryBytes: 512 << 20, PIDs: 300}
	toolCache := "/var/lib/apptown-worker/tool-cache/mise:/apptown-cache/mise-cache"
	r.cfg.Mounts = []string{toolCache}
	in.Mirror = "https://mirror.example:5000"
	if err := r.provision(in); err != nil {
		t.Fatal(err)
	}
	cache := cacheName(in.ID)
	if f.volumes[cache] != in.ID {
		t.Fatalf("the session's cache volume is not labelled with it: %v", f.volumes)
	}
	limits := "--cpus 1.5 --memory 536870912 --memory-swap 536870912 --pids-limit 300"
	for _, args := range f.calls {
		call := strings.Join(args, " ")
		if strings.Contains(call, "apptown-cache:") {
			t.Fatalf("a worker's chat mounts the shared cache: %s", call)
		}
		if args[0] != "create" && args[0] != "run" {
			continue
		}
		for _, want := range []string{cache + ":/apptown-cache", limits} {
			if !strings.Contains(call, want) {
				t.Errorf("%s missing %s: %s", args[0], want, call)
			}
		}
		if args[0] == "run" && !strings.Contains(call, "--registry-mirror=https://mirror.example:5000") {
			t.Errorf("the daemon does not pull through the mirror: %s", call)
		}
		// The worker's tool cache is the chat's container's, writable; its
		// daemon does not get it.
		if mounted := strings.Contains(call, "-v "+toolCache+" "); mounted != (args[0] == "create") {
			t.Errorf("%s and the worker's tool cache (mounted %v): %s", args[0], mounted, call)
		}
	}
	buildkit, err := os.ReadFile(filepath.Join(in.Dir, "docker-runtime", "buildkitd.toml"))
	if err != nil || !strings.Contains(string(buildkit), `mirrors = ["mirror.example:5000"]`) {
		t.Fatalf("the builder does not pull through the mirror: %s %v", buildkit, err)
	}
	// A save's script container runs with the docker binary itself.
	recorded := filepath.Join(t.TempDir(), "args")
	r.cfg.Binary = filepath.Join(t.TempDir(), "docker")
	if err := execfile.Write(r.cfg.Binary, []byte("#!/bin/sh\necho \"$@\" > "+recorded+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.Script(context.Background(), in, "true", nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if script, _ := os.ReadFile(recorded); !strings.Contains(string(script), limits) {
		t.Fatalf("the chat's script runs without its limits: %s", script)
	}
	if err := r.Remove(context.Background(), in.ID); err != nil {
		t.Fatal(err)
	}
	if _, left := f.volumes[cache]; left {
		t.Fatal("the session's cache volume outlived its removal")
	}

	// A volume left by a crash between the removal of its daemon and its own.
	f.volumes[cache] = in.ID
	if err := r.Reap(context.Background(), map[string]bool{in.ID: true}); err != nil || f.volumes[cache] != in.ID {
		t.Fatalf("a live session's cache was reaped: %v", err)
	}
	if err := r.Reap(context.Background(), map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if _, left := f.volumes[cache]; left {
		t.Fatal("the reap left the cache volume of a session that is gone")
	}
	// Another owner's volume under the name is neither used nor removed.
	f.volumes[cache] = "someoneelse"
	if err := r.provision(in); err == nil {
		t.Fatal("a chat mounted a cache volume with another owner")
	}
	if err := r.removeCache(context.Background(), in.ID); err == nil || f.volumes[cache] != "someoneelse" {
		t.Fatalf("another owner's cache volume was removed: %v", err)
	}
}

// Zero is never "unlimited": a runtime given limits with a zero creates
// nothing.
func TestAZeroChatLimitCreatesNothing(t *testing.T) {
	for _, l := range []Limits{{MemoryBytes: 1, PIDs: 1}, {CPUs: 1, PIDs: 1}, {CPUs: 1, MemoryBytes: 1}} {
		r, in, f := fixture(t)
		r.cfg.Limits = &l
		if err := r.provision(in); err == nil || !strings.Contains(err.Error(), "zero is not unlimited") {
			t.Fatalf("%+v: %v", l, err)
		}
		for _, args := range f.calls {
			if args[0] == "create" || args[0] == "run" {
				t.Fatalf("%+v: created %v", l, args)
			}
		}
	}
}

// On AppTown's own machine chats share apptown-cache, as before, and make
// no volume of their own.
func TestAppTownsChatsShareAppTownCache(t *testing.T) {
	r, in, f := fixture(t)
	if err := r.provision(in); err != nil {
		t.Fatal(err)
	}
	for _, args := range f.calls {
		if args[0] == "volume" {
			t.Fatalf("made a volume: %v", args)
		}
		if (args[0] == "create" || args[0] == "run") && !strings.Contains(strings.Join(args, " "), "-v apptown-cache:/apptown-cache") {
			t.Fatalf("%s without apptown-cache: %v", args[0], args)
		}
	}
}

// A chat is given the extra certificates this machine's containers trust
// (internal/trust) and its launcher runs them before pi; with none it is
// given nothing. On an AppTown with its own local HTTPS the launcher also
// reaches AppTown before pi starts; the authority itself comes with the
// extra certificates, never as a file of its own.
// Not parallel: it installs the process's trust.
func TestAChatIsGivenTheExtraCertificatesAndRunsThem(t *testing.T) {
	r, in, f := fixture(t)
	created := func() string {
		for _, args := range f.calls {
			if args[0] == "create" {
				return strings.Join(args, " ")
			}
		}
		t.Fatal("no chat container was created")
		return ""
	}
	if err := r.provision(in); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(created(), "-e "+trust.EnvVar) {
		t.Fatalf("a chat was given extra certificates with none configured: %s", created())
	}

	trusttest.Install(t, trusttest.New(t, "Test Extra CA").Bundle(t), trust.Bundle{})
	f.calls = nil
	tls := &console.LocalTLS{CACert: []byte("THE-CA-CERT"), Port: 7777, Target: "host.docker.internal:7777"}
	r.cfg.LocalTLS = tls
	if err := r.provision(in); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(created(), "-e "+trust.EnvVar+" ") {
		t.Fatalf("the chat's container is not given the extra certificates: %s", created())
	}
	run, _ := os.ReadFile(filepath.Join(in.Dir, "run-pi.sh"))
	pi := strings.Index(string(run), "exec pi ")
	if at := strings.Index(string(run), trust.Run); at < 0 || at > pi {
		t.Fatalf("the launcher does not run the extra certificates before pi: %s", run)
	}
	if at := strings.Index(string(run), console.LocalTLSPrelude(*tls)); at < 0 || at > pi {
		t.Fatalf("the launcher does not run the local HTTPS prelude before pi: %s", run)
	}
	if _, err := os.Stat(filepath.Join(in.Dir, "local-ca.pem")); err == nil {
		t.Fatal("the local authority was staged as a file of its own")
	}
}
