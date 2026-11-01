package apptown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/diskspace/diskspacetest"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/ids"
)

// fakeDocker stands in for the daemon: it records every argv the code
// issues through its injected client and answers from a script. It remembers which
// containers a `run --name` created (until `rm -f`), so a query about a
// container that was never started fails the way the daemon's would — the
// "already up" reuse checks and the rebuild decision depend on that. The
// boot and reaper tests assert the recorded sequence: the argv order IS the
// behaviour.
type fakeDocker struct {
	disk              diskDaemon
	command           dockerClientFunc
	mu                sync.Mutex
	calls             [][]string
	started           map[string]bool
	volumeCreated     map[string]time.Time
	volumeSequence    int64
	storageContainers map[string]map[string]any
	objects           map[string]bool              // networks and volumes a create made ("network n", "volume v"), until rm
	labels            map[string]map[string]string // a started container's --label pairs
	browser           map[string]string            // a started container's browser values, as it was given them
	ids               map[string]string            // a container's ID when a test replaces it; the one its create printed otherwise
	names             map[string]string            // a printed container ID -> the name it was made under
	limits            map[string]string            // a started container's "memory nanocpus", as its run and updates set them
	entry             map[string][2][]string       // a container started by its own entrypoint: that entrypoint and its arguments
	// reply scripts an answer for a call; ok=false falls through to the
	// defaults below (a running, healthy world that maps one port).
	reply func(args []string) (out string, err error, ok bool)
	port  int // what `docker port` reports for any started container
	// told, when set, keeps the last release identity a `run` gave a
	// static part (APPTOWN_RELEASE): what AppTown's file server answers at
	// /_apptown/release. Daemons that share one front door share it.
	told *atomic.Pointer[string]
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{port: 43210, started: map[string]bool{}, objects: map[string]bool{}}
}

func (f *fakeDocker) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
	// --config chooses a prepared boot's client configuration, not what
	// the daemon does.
	if len(req.Args) >= 2 && req.Args[0] == "--config" {
		req.Args = req.Args[2:]
	}
	if f.command != nil {
		return f.command(ctx, host, req)
	}
	if req.Stdin != nil || req.Stdout != nil {
		return nil, errors.New("fake Docker streaming requires an explicit reply")
	}
	return f.run(req.Args...)
}

// dockerClientFunc adapts one test's transport without mutable package state.
type dockerClientFunc func(context.Context, string, docker.Command) ([]byte, error)

func (f dockerClientFunc) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
	return f(ctx, host, req)
}

// markStarted pretends a container is already up (a standing deployment's
// postgres from a previous release).
func (f *fakeDocker) markStarted(name string) {
	f.mu.Lock()
	f.started[name] = true
	f.mu.Unlock()
}

// markStartedAs pretends a service is up, started from svc as AppTown
// starts it: Docker reports its entrypoint and command.
func (f *fakeDocker) markStartedAs(name string, svc deploy.ServiceSpec) {
	f.mu.Lock()
	f.started[name] = true
	if f.entry == nil {
		f.entry = map[string][2][]string{}
	}
	if flag := svc.EntrypointFlag(); len(flag) > 0 {
		f.entry[name] = [2][]string{flag[1:], svc.RunArgs()}
	}
	f.mu.Unlock()
}

// markStartedWithBrowser pretends a static part is up, started with these
// browser values (a JSON object, as AppTown passes them).
func (f *fakeDocker) markStartedWithBrowser(name, values string) {
	f.mu.Lock()
	f.started[name] = true
	if f.browser == nil {
		f.browser = map[string]string{}
	}
	f.browser[name] = values
	f.mu.Unlock()
}

var errFakeExit = errors.New("exit status 1")

func (f *fakeDocker) run(args ...string) ([]byte, error) {
	// The disk admission's df container is a read of a host with room
	// unless the test scripts it; it is not recorded with the commands the
	// sequence tests compare.
	if f.disk.IsRead(args) || diskspacetest.IsCreate(args) {
		if f.reply != nil {
			if out, err, ok := f.reply(args); ok {
				return []byte(out), err
			}
		}
		if out, err, ok := f.disk.Run(args); ok {
			return out, err
		}
	}

	f.mu.Lock()
	// A container is named by the ID its create printed as by its name:
	// the fake keeps state by name.
	args = append([]string(nil), args...)
	for i, a := range args {
		if name, ok := f.names[a]; ok {
			args[i] = name
		}
	}
	f.calls = append(f.calls, append([]string(nil), args...))
	if len(args) > 0 {
		switch args[0] {
		case "run":
			if n := containerNameOf(strings.Join(args, " ")); n != "" {
				f.started[n] = true
				if f.storageContainers == nil {
					f.storageContainers = map[string]map[string]any{}
				}
				f.storageContainers[n] = dockerfake.StorageContainer(args)
				// Real docker keeps a container's labels; so does the fake,
				// for the source label the preview reuse check asks about.
				if f.labels == nil {
					f.labels = map[string]map[string]string{}
				}
				f.labels[n] = map[string]string{}
				if f.limits == nil {
					f.limits = map[string]string{}
				}
				f.limits[n] = runLimits(args, "0 0")
				if f.entry == nil {
					f.entry = map[string][2][]string{}
				}
				delete(f.entry, n)
				// AppTown names the image right after --entrypoint: what
				// follows it is the container's Cmd.
				for i := range args {
					if args[i] == "--entrypoint" && i+2 < len(args) {
						f.entry[n] = [2][]string{{args[i+1]}, append([]string{}, args[i+3:]...)}
					}
				}
				for i := 0; i+1 < len(args); i++ {
					if v, ok := strings.CutPrefix(args[i+1], docker.BrowserEnvVar+"="); ok && args[i] == "-e" {
						if f.browser == nil {
							f.browser = map[string]string{}
						}
						f.browser[n] = v
					}
					if v, ok := strings.CutPrefix(args[i+1], appcontract.ReleaseEnvVar+"="); ok && args[i] == "-e" && f.told != nil {
						f.told.Store(&v)
					}
					if args[i] == "--label" {
						if k, v, ok := strings.Cut(args[i+1], "="); ok {
							f.labels[n][k] = v
						}
					}
				}
			}
		case "update":
			if f.limits == nil {
				f.limits = map[string]string{}
			}
			f.limits[args[len(args)-1]] = runLimits(args, f.limits[args[len(args)-1]])
		case "rm":
			delete(f.started, args[len(args)-1])
		case "network", "volume":
			if len(args) > 2 && args[1] == "create" {
				name := args[len(args)-1]
				if args[0] == "volume" && !f.objects["volume "+name] {
					if f.volumeCreated == nil {
						f.volumeCreated = map[string]time.Time{}
					}
					f.volumeSequence++
					f.volumeCreated[name] = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(f.volumeSequence))
				}
				f.objects[args[0]+" "+name] = true
			}
			if len(args) > 2 && args[1] == "rm" {
				delete(f.objects, args[0]+" "+args[len(args)-1])
			}
		}
	}
	f.mu.Unlock()
	if f.reply != nil {
		if out, err, ok := f.reply(args); ok {
			return f.printID(args, []byte(out), err), err
		}
	}
	if len(args) == 0 {
		return nil, nil
	}
	switch args[0] {
	case "run", "create":
		return f.printID(args, nil, nil), nil
	case "info":
		if len(args) == 3 && strings.Contains(args[2], "MemTotal") {
			return []byte("8589934592\t4\t/var/lib/docker\n"), nil
		}
		return []byte("linux x86_64\n"), nil
	case "network", "volume":
		if args[0] == "volume" && slices.Contains(args, "{{json .}}") {
			name := args[len(args)-1]
			f.mu.Lock()
			defer f.mu.Unlock()
			if !f.objects["volume "+name] {
				return []byte("No such volume: " + name), errFakeExit
			}
			return json.Marshal(map[string]any{"Name": name, "Driver": "local", "CreatedAt": f.volumeCreated[name]})
		}
		// A network or volume exists once a create made it, as the
		// daemon answers.
		if len(args) == 3 && args[1] == "inspect" {
			f.mu.Lock()
			made := f.objects[args[0]+" "+args[2]]
			f.mu.Unlock()
			if !made {
				return []byte("Error response from daemon: No such " + args[0] + ": " + args[2] + "\n"), errFakeExit
			}
		}
		return nil, nil
	case "port":
		if !f.isStarted(args[1]) {
			return []byte("Error: No such container: " + args[1] + "\n"), errFakeExit
		}
		return []byte(fmt.Sprintf("0.0.0.0:%d\n", f.port)), nil
	case "inspect":
		name := args[len(args)-1]
		if !f.isStarted(name) {
			return []byte("Error: No such object: " + name + "\n"), errFakeExit
		}
		tmpl := ""
		for i, a := range args {
			if (a == "-f" || a == "--format") && i+1 < len(args) {
				tmpl = args[i+1]
			}
		}
		switch {
		case tmpl == "{{json .}}":
			f.mu.Lock()
			defer f.mu.Unlock()
			if v := f.storageContainers[name]; v != nil {
				return json.Marshal(v)
			}
			return nil, fmt.Errorf("fixture container %s has no image/mount evidence", name)
		case tmpl == docker.BrowserValuesFormat:
			f.mu.Lock()
			values, ok := f.browser[name]
			f.mu.Unlock()
			env := []any{nil}
			if ok {
				env = []any{docker.BrowserEnvVar + "=" + values, nil}
			}
			return json.Marshal(map[string]any{"id": dockerfake.ContainerID(name), "env": env})
		case tmpl == docker.RuntimeFormat:
			f.mu.Lock()
			id := f.ids[name]
			if id == "" {
				id = dockerfake.ContainerID(name)
			}
			out, err := json.Marshal(docker.Runtime{ID: id, Image: "sha256:built", Running: true, Labels: f.labels[name]})
			f.mu.Unlock()
			return out, err
		case strings.Contains(tmpl, ".Config.Entrypoint") || strings.Contains(tmpl, ".Config.Cmd"):
			f.mu.Lock()
			started, ok := f.entry[name]
			f.mu.Unlock()
			if !ok {
				return []byte("null\n"), nil
			}
			if strings.Contains(tmpl, ".Config.Entrypoint") {
				return json.Marshal(started[0])
			}
			return json.Marshal(started[1])
		case strings.Contains(tmpl, "HostConfig.Memory"):
			f.mu.Lock()
			l := f.limits[name]
			f.mu.Unlock()
			if l == "" {
				l = "0 0"
			}
			return []byte(l + "\n"), nil
		case strings.Contains(tmpl, ".Config.Labels"):
			key := ""
			if i := strings.Index(tmpl, `.Config.Labels "`); i >= 0 {
				key = tmpl[i+len(`.Config.Labels "`):]
				key = key[:strings.Index(key, `"`)]
			}
			f.mu.Lock()
			v := f.labels[name][key]
			f.mu.Unlock()
			return []byte(v + "\n"), nil
		case strings.Contains(tmpl, "Running"):
			return []byte("true\n"), nil
		case strings.Contains(tmpl, "Health"):
			return []byte("healthy\n"), nil
		case strings.Contains(tmpl, ".Image"):
			return []byte("sha256:built\n"), nil
		}
		return []byte(""), nil
	case "buildx", "context":
		// The client has buildx, and the daemon's builder is its own
		// (docker.Scope.Build asks before every build).
		switch {
		case len(args) == 2 && args[1] == "version":
			return []byte("github.com/docker/buildx v0.38.1"), nil
		case len(args) == 3 && args[1] == "inspect":
			return []byte("Name:          " + args[2] + "\nDriver:        docker\n"), nil
		case len(args) == 2 && args[1] == "show":
			return []byte("default\n"), nil
		}
		return nil, nil
	case "image":
		if slices.Contains(args, "{{json .}}") {
			return json.Marshal(map[string]any{"Id": dockerfake.ImageID(args[len(args)-1])})
		}
		if strings.Contains(strings.Join(args, " "), "{{.Config.User}}") {
			return []byte("\n"), nil // no USER declared: the volume is left alone
		}
		return []byte("sha256:built\n"), nil
	}
	return nil, nil
}

// printID is what Docker prints for a command: a `docker create` or
// `docker run -d` that made a named container prints its ID. A test's
// reply that says something itself is its own answer; any other command's
// output is its own.
func (f *fakeDocker) printID(args []string, out []byte, err error) []byte {
	if err != nil || len(out) > 0 || len(args) == 0 || (args[0] != "create" && !(args[0] == "run" && slices.Contains(args, "-d"))) {
		return out
	}
	name := containerNameOf(strings.Join(args, " "))
	if name == "" {
		return out
	}
	id := dockerfake.ContainerID(name)
	f.mu.Lock()
	if f.names == nil {
		f.names = map[string]string{}
	}
	f.names[id] = name
	f.mu.Unlock()
	return []byte(id + "\n")
}

func (f *fakeDocker) isStarted(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started[name]
}

// joined returns every recorded call as one space-joined line, in order.
func (f *fakeDocker) joined() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = strings.Join(c, " ")
	}
	return out
}

// matching returns the recorded calls whose joined form has the prefix.
func (f *fakeDocker) matching(prefix string) []string {
	var out []string
	for _, c := range f.joined() {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// indexOf: the position of the first call with the prefix, or -1.
func (f *fakeDocker) indexOf(prefix string) int {
	for i, c := range f.joined() {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

// reset forgets the recorded calls (the script and the started set stay).
func (f *fakeDocker) reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

// containerNameOf reads --name from a `docker run` argv line.
func containerNameOf(call string) string {
	fields := strings.Fields(call)
	for i, w := range fields {
		if w == "--name" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// hasArg reports whether a joined call carries the exact argument.
func hasArg(call, arg string) bool {
	for _, w := range strings.Fields(call) {
		if w == arg {
			return true
		}
	}
	return false
}

// labelOf reads a --label key=value from a joined call.
func labelOf(call, key string) string {
	for _, w := range strings.Fields(call) {
		if v, ok := strings.CutPrefix(w, key+"="); ok {
			return v
		}
	}
	return ""
}

// Integration tests use the production driver binding, not a public HostFor escape hatch.
func testDockerHost(t *testing.T, a *App, projectID ids.ID) *docker.Host {
	t.Helper()
	driver, err := a.targetProviders.Driver(context.Background(), &deployment.Target{Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn}, &deployment.Deployment{ProjectID: projectID})
	if err != nil {
		t.Fatal(err)
	}
	return driver.(*docker.Driver).Boot.(*docker.Host)
}

// runLimits is the memory and NanoCPUs a run or update sets ("memory
// nanocpus"), over was for what it does not name.
func runLimits(args []string, was string) string {
	var mem, nano int64
	fmt.Sscan(was, &mem, &nano)
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--memory":
			fmt.Sscan(args[i+1], &mem)
		case "--cpus":
			var c float64
			fmt.Sscan(args[i+1], &c)
			nano = int64(c * 1e9)
		}
	}
	return fmt.Sprintf("%d %d", mem, nano)
}

// diskDaemon composes the disk and result protocols for this package's tests.
type diskDaemon = diskspacetest.Daemon
