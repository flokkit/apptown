// Package dockerfake models the small Docker daemon contract used by tests.
// It is a recording daemon, not a deployment driver or AppTown fixture.
package dockerfake

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/diskspace/diskspacetest"
)

type Daemon struct {
	mu           sync.Mutex
	commands     [][]string
	running      map[string]bool
	stopped      map[string]bool // stopped by a command, still present
	restarts     map[string]int  // Docker's restart-policy counter
	exitCodes    map[string]int  // the last exit code
	oomKilled    map[string]bool // the last exit was the kernel's out-of-memory kill
	labels       map[string]map[string]string
	started      map[string][2][]string // a container started by its own entrypoint: that entrypoint and its arguments
	limits       map[string][2]int64    // the memory and NanoCPUs Docker holds a container to
	host         string                 // docker info's memory, CPUs and data folder
	disk         string                 // what df says of the data folder's filesystem
	port         int
	reply        func([]string) ([]byte, error, bool)
	commandReply func(docker.Command) ([]byte, error, bool)
	configs      []string              // each command's --config, "" for the ambient one
	published    map[string]*published // nil: every container answers at port (PublishPorts)
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
	args := req.Args
	d.commands = append(d.commands, append([]string(nil), args...))
	if len(args) == 0 {
		return nil, nil
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
			return out, err
		}
	}
	if d.commandReply != nil {
		if out, err, ok := d.commandReply(req); ok {
			return out, err
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
		return []byte("sha256:built\n"), nil
	}
	if IsDiskRead(args) {
		return DiskReadAnswer(args, d.disk), nil
	}
	if args[0] == "info" && len(args) == 3 && strings.Contains(args[2], "MemTotal") {
		return []byte(d.host + "\n"), nil
	}
	return nil, nil
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

// RoomyDisk answers the disk admission's reads (docker.AdmitDisk: docker
// info's data folder, and each call of a df container's read,
// diskspace.ReadThroughDocker) as a daemon with room does; ok is false for
// any other command. A test's own scripted client asks it first.
func RoomyDisk(args []string) ([]byte, bool) {
	if len(args) > 1 && args[0] == "--config" {
		args = args[2:]
	}
	switch {
	case len(args) == 3 && args[0] == "info" && strings.Contains(args[2], "DockerRootDir"):
		return []byte("8589934592\t8\t/var/lib/docker\n"), true
	case IsDiskRead(args):
		return DiskReadAnswer(args, dfLines(100<<30, 90<<30)), true
	}
	return nil, false
}

// IsDiskRead says args is one call of the disk read's throwaway container
// (diskspace.ProbeStep), which leaves nothing on the host.
func IsDiskRead(args []string) bool {
	if len(args) > 1 && args[0] == "--config" {
		args = args[2:]
	}
	return diskspacetest.Step(args) != ""
}

// DiskReadAnswer is what a daemon answers to the disk read's call args when
// its df prints df: the exit code to its wait, df's result files to its cp
// (written where cp copies to), df to its logs, nothing to the rest (no
// leftover probes to sweep).
func DiskReadAnswer(args []string, df string) []byte {
	if len(args) > 1 && args[0] == "--config" {
		args = args[2:]
	}
	switch diskspacetest.Step(args) {
	case "cp":
		if _, err := diskspacetest.WriteResult(args, df, "", "0"); err != nil {
			return []byte(err.Error())
		}
	case "wait":
		return []byte("0\n")
	case "logs":
		return []byte(df)
	}
	return nil
}
