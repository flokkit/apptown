package docker

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

// hostInfo is what a daemon says it can give containers: its memory and
// CPUs (docker info: the host's, or its container's limits when the
// daemon itself runs in one), and the folder its data lives in.
type hostInfo struct {
	MemoryBytes int64
	CPUs        int
	DataRoot    string
}

// ReadHostInfo asks the daemon what it can give containers.
func ReadHostInfo(ctx context.Context, dk Scope) (hostInfo, error) {
	out, err := dk.Run(ctx, "info", "--format", "{{.MemTotal}}\t{{.NCPU}}\t{{.DockerRootDir}}")
	if err != nil {
		return hostInfo{}, fmt.Errorf("docker info: %v: %s", err, strings.TrimSpace(string(out)))
	}
	f := strings.Split(strings.TrimSpace(string(out)), "\t")
	if len(f) != 3 {
		return hostInfo{}, fmt.Errorf("docker info answered %q", strings.TrimSpace(string(out)))
	}
	mem, err1 := strconv.ParseInt(f[0], 10, 64)
	cpus, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil || mem <= 0 || cpus <= 0 || f[2] == "" {
		return hostInfo{}, fmt.Errorf("docker info answered %q", strings.TrimSpace(string(out)))
	}
	return hostInfo{MemoryBytes: mem, CPUs: cpus, DataRoot: f[2]}, nil
}

// ContainerLimit is the limit Docker enforces on one running container,
// and whether AppTown started it (its class label).
type ContainerLimit struct {
	Name        string
	MemoryBytes int64   // 0: no limit
	CPUs        float64 // 0: no limit
	// CPUUnread says a CPU constraint was set that AppTown could not
	// read as a number of CPUs: never counted as no limit.
	CPUUnread bool
	// AppTown says AppTown started it (it carries a class label); Class
	// and Owner are those labels, "" where absent, so a reader can say
	// whose it is: a deployment's, a preview's, agent work, AppTown's own.
	AppTown bool
	Class   string
	Owner   string
	// Stack is the Compose project it runs in (com.docker.compose.project),
	// "" outside one: a Portainer stack's containers carry no class label,
	// and AppTown knows its stacks by name.
	Stack string
}

// Limit reads the enforced limits from an inspected container.
func (c HostContainer) Limit() ContainerLimit {
	h := c.HostConfig
	l := ContainerLimit{Name: c.Name, MemoryBytes: h.Memory, Class: c.Config.Labels[dockerlabel.Class], Owner: c.Config.Labels[dockerlabel.Owner], Stack: c.Config.Labels[composeProject]}
	l.AppTown = l.Class != ""
	nano, quota, period := h.NanoCpus, h.CpuQuota, h.CpuPeriod
	tightest := func(c float64) {
		if c > 0 && (l.CPUs == 0 || c < l.CPUs) {
			l.CPUs = c
		}
	}
	tightest(float64(nano) / 1e9)
	if quota > 0 {
		if period <= 0 {
			period = 100000 // Docker's default period
		}
		tightest(float64(quota) / float64(period))
	}
	if set := strings.TrimSpace(h.CpusetCpus); set != "" {
		n, ok := cpusetSize(set)
		if !ok {
			l.CPUUnread = true
		} else {
			tightest(float64(n))
		}
	}
	return l
}

// cpusetSize counts the CPUs in a cpuset ("0-3,6").
func cpusetSize(set string) (int, bool) {
	n := 0
	for _, r := range strings.Split(set, ",") {
		lo, hi, isRange := strings.Cut(r, "-")
		a, err := strconv.Atoi(strings.TrimSpace(lo))
		if err != nil || a < 0 {
			return 0, false
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(strings.TrimSpace(hi)); err != nil || b < a {
				return 0, false
			}
		}
		n += b - a + 1
	}
	return n, true
}

// composeProject is the label Compose puts on each container of a project.
const composeProject = "com.docker.compose.project"

// readDisk reads the filesystem holding the daemon's data folder in the
// daemon's standing disk reader (diskspace.ReadThroughDocker). Each call's
// output is combined, as every call through a scope's client is.
func readDisk(ctx context.Context, dk Scope, image string) (diskspace.Space, error) {
	space, err := diskspace.ReadThroughDocker(ctx, func(ctx context.Context, args ...string) (string, string, error) {
		out, err := dk.Run(ctx, args...)
		return string(out), "", err
	}, image, clock.Or(dk.Now), dk.NoStreams)
	if err != nil {
		return diskspace.Space{}, fmt.Errorf("reading the disk: %w", err)
	}
	return space, nil
}

// AdmitDisk refuses new disk-heavy work on a daemon (a boot's pulls, builds
// and new containers) while its data folder's filesystem has less than its
// floor free, or cannot be read (diskspace.Admit). It reads the disk now:
// a target's capacity view keeps its reading for minutes, and admission
// does not trust it. Stopping, removing and taking down never ask.
func AdmitDisk(ctx context.Context, dk Scope, image string) error {
	const what = "this Docker host's data folder"
	info, err := ReadHostInfo(ctx, dk)
	if err != nil {
		return diskspace.Admit(what, diskspace.Space{}, fmt.Errorf("where its data is: %w", err))
	}
	d, err := readDisk(ctx, dk, image)
	return diskspace.Admit(what+" ("+info.DataRoot+")", d, err)
}

// AdmitResources refuses, before anything is made or removed, a part or
// need whose declared memory or CPUs exceed what the daemon can give any
// container: such a container could never run there. The sum of every
// limit on the host may exceed it (limits are caps, not reservations), so
// overcommit is admitted. A daemon that cannot say what it has is refused
// for now, not for good.
func AdmitResources(ctx context.Context, dk Scope, c *appcontract.Contract) error {
	if c == nil || !declaresResources(c) {
		return nil
	}
	host, err := ReadHostInfo(ctx, dk)
	if err != nil {
		return fmt.Errorf("reading what the Docker host can give a container, to admit the declared resources: %w", err)
	}
	check := func(kind, name string, r appcontract.Resources) error {
		if r.MemoryBytes > host.MemoryBytes {
			return deploy.Terminal(fmt.Errorf("%s %s declares %s of memory; this Docker host has %s", kind, name, appcontract.FormatMemory(r.MemoryBytes), appcontract.FormatMemory(host.MemoryBytes)))
		}
		if r.CPUs > float64(host.CPUs) {
			return deploy.Terminal(fmt.Errorf("%s %s declares %s CPUs; this Docker host has %d", kind, name, r.CPUWords(), host.CPUs))
		}
		return nil
	}
	for _, p := range c.Parts {
		if err := check("part", p.Name, p.Resources); err != nil {
			return err
		}
	}
	for _, n := range c.Needs {
		if err := check("need", n, c.NeedResources[n]); err != nil {
			return err
		}
	}
	return nil
}

func declaresResources(c *appcontract.Contract) bool {
	for _, p := range c.Parts {
		if p.Resources.Declared() {
			return true
		}
	}
	return len(c.NeedResources) > 0
}

// ResourceAdmissionContract is a read-only projection of all containers a
// stack will boot. Supplied services are included for the existing local/worker
// admission protocol; this projection is never used as the boot's app contract.
func ResourceAdmissionContract(c *appcontract.Contract, services []deploy.ServiceSpec) *appcontract.Contract {
	if c == nil {
		return nil
	}
	out := *c
	out.Parts = append([]appcontract.Part(nil), c.Parts...)
	for _, s := range services {
		out.Parts = append(out.Parts, appcontract.Part{Name: "service " + s.Name, Resources: s.Resources})
	}
	return &out
}
