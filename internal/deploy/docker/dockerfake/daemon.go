// Package dockerfake models the small Docker daemon contract used by tests.
// It is a recording daemon, not a deployment driver or AppTown fixture.
package dockerfake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/diskspace/diskspacetest"
)

type Daemon struct {
	mu                sync.Mutex
	commands          [][]string
	running           map[string]bool
	volumes           map[string]bool
	volumeCreated     map[string]time.Time
	volumeSequence    int64
	storageContainers map[string]map[string]any
	stopped           map[string]bool // stopped by a command, still present
	restarts          map[string]int  // Docker's restart-policy counter
	exitCodes         map[string]int  // the last exit code
	oomKilled         map[string]bool // the last exit was the kernel's out-of-memory kill
	labels            map[string]map[string]string
	started           map[string][2][]string // a container started by its own entrypoint: that entrypoint and its arguments
	limits            map[string][2]int64    // the memory and NanoCPUs Docker holds a container to
	host              string                 // docker info's memory, CPUs and data folder
	disk              string                 // what df says of the data folder's filesystem
	port              int
	reply             func([]string) ([]byte, error, bool)
	results           *containerresulttest.Daemon
	diskResults       diskspacetest.Daemon
	commandReply      func(docker.Command) ([]byte, error, bool)
	configs           []string              // each command's --config, "" for the ambient one
	published         map[string]*published // nil: every container answers at port (PublishPorts)
	names             map[string]string     // a created container's ID -> its name
}

// published is one container's published port: fixed is the host port its
// run named (0: Docker picks one at each start), ln the listener that
// relays there, and up whether the container runs to answer it. A fixed
// port's listener is kept from run to rm, and a stopped container's
// connections are dropped: listening again at each start could lose the
// port to another socket in the test process, which Docker's own hold of a
// published port does not.
type published struct {
	fixed int
	ln    net.Listener
	up    atomic.Bool
}

// start opens the container's published port, relaying to target: the
// fixed port, kept, or one picked now.
func (p *published) start(target int) error {
	if p.fixed > 0 && p.ln != nil {
		p.up.Store(true)
		return nil
	}
	p.close()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p.fixed))
	if err != nil {
		return err
	}
	p.ln = ln
	p.up.Store(true)
	go func() {
		for {
			in, err := ln.Accept()
			if err != nil {
				return
			}
			if !p.up.Load() {
				_ = in.Close() // stopped: nothing answers
				continue
			}
			go func() {
				defer in.Close()
				out, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", target))
				if err != nil {
					return
				}
				defer out.Close()
				go func() { _, _ = io.Copy(out, in) }()
				_, _ = io.Copy(in, out)
			}()
		}
	}()
	return nil
}

// stop is the container stopping: a fixed port stops answering, a picked
// one is given up.
func (p *published) stop() {
	p.up.Store(false)
	if p.fixed == 0 {
		p.close()
	}
}

func (p *published) close() {
	if p.ln != nil {
		_ = p.ln.Close()
		p.ln = nil
	}
}

func New() *Daemon {
	return &Daemon{running: make(map[string]bool), stopped: make(map[string]bool), restarts: make(map[string]int), exitCodes: make(map[string]int), oomKilled: make(map[string]bool), labels: make(map[string]map[string]string), started: make(map[string][2][]string), limits: make(map[string][2]int64), host: "8589934592\t8\t/var/lib/docker", port: 43210, disk: dfLines(100<<30, 90<<30)}
}

func (d *Daemon) SetPort(port int) {
	d.mu.Lock()
	d.port = port
	d.mu.Unlock()
}

// PublishPorts makes the daemon publish a container's port as Docker does:
// a container run with `-p addr:N:inner` answers at N from every start, and
// one run with `-p addr::inner` answers at a port Docker picks at each start
// (run, start, restart), so the port it had before goes dead. Each published
// port relays to the port SetPort names. Without it every running container
// answers at that port. Close stops the relays.
func (d *Daemon) PublishPorts() {
	d.mu.Lock()
	d.published = map[string]*published{}
	d.mu.Unlock()
}

// Close stops every published port's relay.
func (d *Daemon) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range d.published {
		p.close()
	}
}

// publishedPort reads a run's -p: the fixed host port it names, 0 for one
// Docker picks; ok is false when it publishes none.
func publishedPort(args []string) (fixed int, ok bool) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-p" {
			parts := strings.Split(args[i+1], ":")
			if len(parts) == 3 {
				fmt.Sscan(parts[1], &fixed)
			}
			return fixed, true
		}
	}
	return 0, false
}

// SetReply scripts a daemon response after recording the command. Returning
// false lets the daemon answer from its ordinary container state.
// SetResults answers result reads (containerresult.Run and Exec) as
// answers says, before anything else this daemon does; ok false leaves a
// call to the rest. The containers keep their stdout to themselves, as a
// daemon that lost it would.
func (d *Daemon) SetResults(answers func(containerresulttest.Command) (containerresulttest.Answer, bool)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.results = &containerresulttest.Daemon{Answers: answers}
}

func (d *Daemon) SetReply(reply func([]string) ([]byte, error, bool)) {
	d.mu.Lock()
	d.reply = reply
	d.mu.Unlock()
}

// SetCommandReply scripts stream-bearing commands such as backup copies.
// The callback must not call methods on this daemon while Run holds its lock.
func (d *Daemon) SetCommandReply(reply func(docker.Command) ([]byte, error, bool)) {
	d.mu.Lock()
	d.commandReply = reply
	d.mu.Unlock()
}

// SetVolume supplies an existing or absent named volume in a host fixture.
func (d *Daemon) SetVolume(name string, exists bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.volumes == nil {
		d.volumes = map[string]bool{}
	}
	d.setVolume(name, exists)
}

func (d *Daemon) SetRunning(name string, running bool) {
	d.mu.Lock()
	d.running[name] = running
	d.mu.Unlock()
}

// SetRestartCount scripts Docker's restart-policy counter for a container,
// what `inspect -f {{.RestartCount}}` answers.
func (d *Daemon) SetRestartCount(name string, count int) {
	d.mu.Lock()
	d.restarts[name] = count
	d.mu.Unlock()
}

// SetExitCode scripts the container's last exit code.
func (d *Daemon) SetExitCode(name string, code int) {
	d.mu.Lock()
	d.exitCodes[name] = code
	d.mu.Unlock()
}

// SetOOMKilled scripts whether the container's last exit was an
// out-of-memory kill, what its inspected state says.
func (d *Daemon) SetOOMKilled(name string, killed bool) {
	d.mu.Lock()
	d.oomKilled[name] = killed
	d.mu.Unlock()
}

func (d *Daemon) Run(_ context.Context, _ string, req docker.Command) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// --config chooses the client's configuration, not what the daemon does:
	// the daemon's record is the command, and the configuration is kept.
	config := ""
	if len(req.Args) >= 2 && req.Args[0] == "--config" {
		config, req.Args = req.Args[1], req.Args[2:]
	}
	d.configs = append(d.configs, config)
	// A copy's command runs inside its frame (docker.Scope.RunStep): this
	// daemon's containers run the frame, so what it records and answers is
	// the command inside, and a copy it writes is framed
	// (containerresulttest.WriteString).
	req.Args = containerresulttest.UnframeArgs(req.Args)
	// A container is named by its ID as by its name, as Docker names it:
	// the fake keeps state by name, so an ID it printed is read as that name.
	args := append([]string(nil), req.Args...)
	for i, a := range args {
		if name, ok := d.names[a]; ok {
			args[i] = name
		}
	}
	d.commands = append(d.commands, append([]string(nil), args...))
	if len(args) == 0 {
		return nil, nil
	}
	if d.results != nil && !d.diskResults.Owns(args) {
		if out, err, ok := d.results.RunEnv(args, req.Env); ok {
			return out, err
		}
	}
	if d.diskResults.Answer == nil {
		d.diskResults.Answer = func() containerresulttest.Answer { return containerresulttest.Answer{Answer: []byte(d.disk)} }
	}
	// The standing disk reader is the disk fake's, ahead of the containers
	// this daemon keeps for everything else.
	if d.diskResults.Owns(args) {
		if out, err, ok := d.diskResults.Run(args); ok {
			return out, err
		}
	}
	if args[0] == "run" || args[0] == "update" {
		name := args[len(args)-1]
		for i := range args {
			if args[i] == "--name" && i+1 < len(args) {
				name = args[i+1]
			}
		}
		d.limits[name] = limitsOf(args, d.limits[name], args[0] == "run")
	}
	if args[0] == "run" {
		for i := range args {
			if args[i] == "--name" && i+1 < len(args) {
				d.running[args[i+1]] = true
				if d.storageContainers == nil {
					d.storageContainers = map[string]map[string]any{}
				}
				d.storageContainers[args[i+1]] = StorageContainer(args)
				d.labels[args[i+1]] = make(map[string]string)
				delete(d.started, args[i+1])
				if d.published != nil {
					if p := d.published[args[i+1]]; p != nil {
						p.close()
						delete(d.published, args[i+1])
					}
					if fixed, ok := publishedPort(args); ok {
						p := &published{fixed: fixed}
						if err := p.start(d.port); err != nil {
							return []byte("docker: Error response from daemon: " + err.Error()), err
						}
						d.published[args[i+1]] = p
					}
				}
				// AppTown names the image right after --entrypoint: what
				// follows it is the container's Cmd.
				for j := range args {
					if args[j] == "--entrypoint" && j+2 < len(args) {
						d.started[args[i+1]] = [2][]string{{args[j+1]}, append([]string{}, args[j+3:]...)}
					}
				}
				for j := range args {
					if (args[j] == "--label" || args[j] == "-l") && j+1 < len(args) {
						if k, v, ok := strings.Cut(args[j+1], "="); ok {
							d.labels[args[i+1]][k] = v
						}
					}
				}
			}
		}
	}
	if args[0] == "rm" && len(args) > 1 {
		delete(d.running, args[len(args)-1])
		delete(d.stopped, args[len(args)-1])
		if p := d.published[args[len(args)-1]]; p != nil {
			p.close()
			delete(d.published, args[len(args)-1])
		}
	}
	if args[0] == "stop" && len(args) > 1 {
		name := args[len(args)-1]
		d.stopped[name] = d.running[name] || d.stopped[name] // still there, not running
		d.running[name] = false                              // as Docker does; unless-stopped then leaves it
		if p := d.published[name]; p != nil {
			p.stop()
		}
	}
	if args[0] == "start" && len(args) > 1 && d.stopped[args[len(args)-1]] {
		d.running[args[len(args)-1]] = true
		delete(d.stopped, args[len(args)-1])
		if p := d.published[args[len(args)-1]]; p != nil {
			if err := p.start(d.port); err != nil {
				return []byte(err.Error()), err
			}
		}
	}
	if args[0] == "restart" && len(args) > 1 && d.running[args[len(args)-1]] {
		if p := d.published[args[len(args)-1]]; p != nil {
			if err := p.start(d.port); err != nil {
				return []byte(err.Error()), err
			}
		}
	}
	if d.reply != nil {
		if out, err, ok := d.reply(append([]string(nil), args...)); ok {
			return d.printID(args, out, err), err
		}
	}
	if d.commandReply != nil {
		if out, err, ok := d.commandReply(req); ok {
			return d.printID(args, out, err), err
		}
	}
	// Named volumes exist only after creation, unless a test explicitly
	// supplies an inspection response above. Unknown objects are not present.
	if args[0] == "volume" && len(args) >= 3 {
		name := args[len(args)-1]
		switch args[1] {
		case "create":
			if d.volumes == nil {
				d.volumes = map[string]bool{}
			}
			d.setVolume(name, true)
			return []byte(name + "\n"), nil
		case "inspect", "rm":
			if !d.volumes[name] {
				return []byte("Error: No such volume: " + name), fmt.Errorf("volume %s absent", name)
			}
			if args[1] == "rm" {
				d.setVolume(name, false)
			}
			if args[1] == "inspect" && slices.Contains(args, "{{json .}}") {
				return json.Marshal(map[string]any{"Name": name, "Driver": "local", "CreatedAt": d.volumeCreated[name]})
			}
			return nil, nil
		}
	}
	if args[0] == "ps" && len(args) > 1 && args[1] == "-aq" {
		return []byte(d.labelled(args[2:])), nil
	}
	if args[0] == "ps" && len(args) > 1 && args[1] == "-a" {
		return []byte(d.formatted(args[2:])), nil
	}
	if args[0] == "port" && len(args) > 1 {
		if !d.running[args[1]] {
			return []byte("No such container"), fmt.Errorf("container %s absent", args[1])
		}
		if p := d.published[args[1]]; p != nil && p.ln != nil {
			return []byte(fmt.Sprintf("0.0.0.0:%d\n", p.ln.Addr().(*net.TCPAddr).Port)), nil
		}
		return []byte(fmt.Sprintf("0.0.0.0:%d\n", d.port)), nil
	}
	if args[0] == "inspect" && len(args) == 2 && !d.running[args[1]] && !d.stopped[args[1]] {
		// As the Docker CLI answers for an object it does not have.
		return []byte("[]\nError: No such object: " + args[1]), fmt.Errorf("container %s absent", args[1])
	}
	if args[0] == "inspect" && len(args) == 6 && args[1] == "--type" && strings.Contains(args[4], "HostConfig.Memory") {
		l := d.limits[args[5]]
		return []byte(fmt.Sprintf("%d %d\n", l[0], l[1])), nil
	}
	if args[0] == "inspect" && len(args) >= 4 && args[1] == "--format" {
		if args[2] == "{{json .}}" {
			name := args[len(args)-1]
			if !d.running[name] && !d.stopped[name] {
				return []byte("No such object: " + name), fmt.Errorf("container %s absent", name)
			}
			if v := d.storageContainers[name]; v != nil {
				return json.Marshal(v)
			}
			return nil, fmt.Errorf("fixture container %s has no engine/mount evidence", name)
		}
		name := args[len(args)-1]
		status := "exited"
		if d.running[name] {
			status = "running"
		}
		return json.Marshal(docker.Runtime{ID: name, Image: "sha256:built", Running: d.running[name], Labels: d.labels[name],
			State: &docker.ContainerState{Status: status, ExitCode: d.exitCodes[name], OOMKilled: d.oomKilled[name], RestartCount: d.restarts[name]}})
	}
	if len(args) >= 4 && args[0] == "inspect" && args[1] == "-f" {
		if strings.Contains(args[2], "State.Running") && d.running[args[3]] {
			return []byte("true\n"), nil
		}
		if strings.Contains(args[2], "State.Running") {
			return []byte("false\n"), nil
		}
		if strings.Contains(args[2], "State.Health") {
			return []byte("healthy\n"), nil
		}
		if strings.Contains(args[2], "RestartCount") {
			return []byte(fmt.Sprintf("%d\n", d.restarts[args[3]])), nil
		}
		if strings.Contains(args[2], "State.ExitCode") {
			return []byte(fmt.Sprintf("%d\n", d.exitCodes[args[3]])), nil
		}
		if started, ok := d.started[args[3]]; ok && (strings.Contains(args[2], "Config.Entrypoint") || strings.Contains(args[2], "Config.Cmd")) {
			field := started[1]
			if strings.Contains(args[2], "Config.Entrypoint") {
				field = started[0]
			}
			return json.Marshal(field)
		}
		if strings.Contains(args[2], "Config.Labels") {
			key := strings.TrimSuffix(strings.TrimPrefix(args[2], `{{index .Config.Labels "`), `"}}`)
			return []byte(d.labels[args[3]][key] + "\n"), nil
		}
		// No StartedAt means the container is outside startup grace.
		return nil, nil
	}
	if args[0] == "image" {
		if len(args) > 3 && args[1] == "inspect" && slices.Contains(args, "{{json .}}") {
			return json.Marshal(map[string]any{"Id": ImageID(args[len(args)-1])})
		}
		if len(args) > 3 && args[1] == "inspect" && slices.Contains(args, "--format") && strings.Contains(strings.Join(args, " "), "{{json .Id}}") {
			// execution.InspectImage's question: the ID and the labels.
			return []byte(fmt.Sprintf("%q null\n", ImageID(args[len(args)-1]))), nil
		}
		return []byte("sha256:built\n"), nil
	}
	if out, err, ok := d.diskResults.Run(args); ok {
		return out, err
	}
	if args[0] == "info" && len(args) == 3 && args[2] == "{{.ID}}" {
		return []byte("fake-daemon"), nil
	}
	// The daemon has buildx, and its builder is its own (docker.Scope.Build).
	if len(args) == 2 && args[0] == "buildx" && args[1] == "version" {
		return []byte("github.com/docker/buildx v0.38.1"), nil
	}
	if len(args) == 3 && args[0] == "buildx" && args[1] == "inspect" {
		return []byte("Name:          " + args[2] + "\nDriver:        docker\n"), nil
	}
	if len(args) == 2 && args[0] == "context" && args[1] == "show" {
		return []byte("default\n"), nil
	}
	if args[0] == "info" && len(args) == 3 && strings.Contains(args[2], "MemTotal") {
		return []byte(d.host + "\n"), nil
	}
	return d.printID(args, nil, nil), nil
}

// printID is what Docker prints for a command: a `docker create` or
// `docker run -d` that made a named container prints its ID. A test's
// reply that says something itself is its own answer; any other command's
// output is its own.
func (d *Daemon) printID(args []string, out []byte, err error) []byte {
	if err != nil || len(out) > 0 || len(args) == 0 || (args[0] != "create" && !(args[0] == "run" && slices.Contains(args, "-d"))) {
		return out
	}
	for i := range args {
		if args[i] == "--name" && i+1 < len(args) {
			return []byte(d.created(args[i+1]) + "\n")
		}
	}
	return out
}

// created is the ID the fake gives a container made under name: one per
// name, so a container made again under it is named the same way.
func (d *Daemon) created(name string) string {
	id := ContainerID(name)
	if d.names == nil {
		d.names = map[string]string{}
	}
	d.names[id] = name
	return id
}

// ContainerID is the ID the fake printed for the container it made under
// name.
func ContainerID(name string) string {
	sum := sha256.Sum256([]byte("container " + name))
	return hex.EncodeToString(sum[:])
}

// labelled answers `ps -aq --filter label=k=v ...` as Docker does: the
// containers present (running or stopped) carrying every filtered label,
// one per line, by name.
func (d *Daemon) labelled(filters []string) string {
	var out []string
	for name, labels := range d.labels {
		if !d.running[name] && !d.stopped[name] {
			continue
		}
		match := true
		for i := 0; i+1 < len(filters); i += 2 {
			k, v, valued := strings.Cut(strings.TrimPrefix(filters[i+1], "label="), "=")
			if have, ok := labels[k]; filters[i] != "--filter" || !ok || (valued && have != v) {
				match = false
			}
		}
		if match {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// formatted answers `ps -a --filter label=k=v ... --format <format>` as
// Docker does for the containers present carrying every filtered label: the
// format with {{.Names}}, {{.ID}} (a container's ID is its name here) and
// {{.Label "k"}} filled in, one per line.
func (d *Daemon) formatted(args []string) string {
	var filters []string
	format := "{{.Names}}"
	for i := 0; i+1 < len(args); i += 2 {
		switch args[i] {
		case "--filter":
			filters = append(filters, args[i], args[i+1])
		case "--format":
			format = args[i+1]
		}
	}
	names := strings.Fields(d.labelled(filters))
	lines := make([]string, 0, len(names))
	for _, name := range names {
		line := strings.ReplaceAll(strings.ReplaceAll(format, "{{.Names}}", name), "{{.ID}}", name)
		for k, v := range d.labels[name] {
			line = strings.ReplaceAll(line, `{{.Label "`+k+`"}}`, v)
		}
		for strings.Contains(line, `{{.Label "`) { // a label it does not carry
			i := strings.Index(line, `{{.Label "`)
			j := strings.Index(line[i:], "}}")
			line = line[:i] + line[i+j+2:]
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// Running says whether the daemon holds the container running now.
func (d *Daemon) Running(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running[name]
}

// Configs are the client configuration each command named, in order ("" is
// the ambient one).
func (d *Daemon) Configs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.configs...)
}

func (d *Daemon) Commands() [][]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([][]string, len(d.commands))
	for i, args := range d.commands {
		out[i] = append([]string(nil), args...)
	}
	return out
}

func (d *Daemon) ResetCommands() {
	d.mu.Lock()
	d.commands, d.configs = nil, nil
	d.mu.Unlock()
}

func (d *Daemon) Matching(prefix string) [][]string {
	var out [][]string
	for _, args := range d.Commands() {
		if strings.HasPrefix(strings.Join(args, " "), prefix) {
			out = append(out, args)
		}
	}
	return out
}

// Limits is the memory and NanoCPUs the daemon holds a container to, as
// its runs and updates set them (0: no limit).
func (d *Daemon) Limits(name string) (memory, nanoCPUs int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	l := d.limits[name]
	return l[0], l[1]
}

// limitsOf reads --memory and --cpus from a run (which starts from none)
// or an update (which changes only what it names).
func limitsOf(args []string, was [2]int64, run bool) [2]int64 {
	out := was
	if run {
		out = [2]int64{}
	}
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--memory":
			fmt.Sscan(args[i+1], &out[0])
		case "--cpus":
			var c float64
			fmt.Sscan(args[i+1], &c)
			out[1] = int64(c * 1e9)
		}
	}
	return out
}

// SetHostInfo scripts what docker info says the daemon can give
// containers (8 GiB and 8 CPUs unless set).
func (d *Daemon) SetHostInfo(memoryBytes int64, cpus int, dataRoot string) {
	d.mu.Lock()
	d.host = fmt.Sprintf("%d\t%d\t%s", memoryBytes, cpus, dataRoot)
	d.mu.Unlock()
}

// SetDisk scripts what df says of the data folder's filesystem (100 GiB
// with 90 GiB free unless set).
func (d *Daemon) SetDisk(totalBytes, freeBytes int64) {
	d.mu.Lock()
	d.disk = dfLines(totalBytes, freeBytes)
	d.mu.Unlock()
}

func dfLines(total, free int64) string {
	return fmt.Sprintf("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/data %d %d %d 10%% /d\n", total>>10, (total-free)>>10, free>>10)
}

// IsDiskRead identifies this daemon's disk commands, including completed reads.
func (d *Daemon) IsDiskRead(args []string) bool { return d.diskResults.IsRead(args) }

// setVolume changes incarnation only when an absent volume is created.
func (d *Daemon) setVolume(name string, exists bool) {
	if d.volumes == nil {
		d.volumes = map[string]bool{}
	}
	if d.volumeCreated == nil {
		d.volumeCreated = map[string]time.Time{}
	}
	if exists && !d.volumes[name] {
		d.volumeSequence++
		d.volumeCreated[name] = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(d.volumeSequence))
	}
	d.volumes[name] = exists
	if !exists {
		delete(d.volumeCreated, name)
	}
}

// ImageID is the immutable image the fixture resolves a reference to.
func ImageID(ref string) string {
	if strings.HasPrefix(ref, "sha256:") && len(ref) == 71 {
		return ref
	}
	sum := sha256.Sum256([]byte("image " + ref))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// StorageContainer is the image/mount observation of a recorded docker run.
func StorageContainer(args []string) map[string]any {
	image, name := "", ""
	var mounts []map[string]string
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--name" && i+1 < len(args) {
			name = args[i+1]
		}
		if a == "-v" && i+1 < len(args) {
			parts := strings.Split(args[i+1], ":")
			if len(parts) >= 2 {
				mounts = append(mounts, map[string]string{"Type": "volume", "Name": parts[0], "Destination": parts[1]})
			}
		}
		if strings.HasPrefix(a, "-") {
			if a == "-d" || a == "--rm" || a == "--init" || a == "--read-only" || a == "-i" || a == "-t" || strings.Contains(a, "=") {
				continue
			}
			i++
			continue
		}
		image = a
		break
	}
	return map[string]any{"Id": ContainerID(name), "Image": ImageID(image), "Config": map[string]string{"Image": image}, "Mounts": mounts}
}

// SetStorageContainer supplies a fixture's observed engine and named data mount.
// SetRunning alone does not invent compatibility evidence for retained data.
func (d *Daemon) SetStorageContainer(name, image, volume, path string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.storageContainers == nil {
		d.storageContainers = map[string]map[string]any{}
	}
	d.storageContainers[name] = map[string]any{"Id": d.created(name), "Image": ImageID(image), "Config": map[string]string{"Image": image}, "Mounts": []map[string]string{{"Type": "volume", "Name": volume, "Destination": path}}}
}
