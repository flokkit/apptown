package docker

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

// HostInfo is what a daemon says it can give containers: its memory and
// CPUs (docker info: the host's, or its container's limits when the
// daemon itself runs in one), and the folder its data lives in.
type HostInfo struct {
	MemoryBytes int64
	CPUs        int
	DataRoot    string
}

// ReadHostInfo asks the daemon what it can give containers.
func ReadHostInfo(ctx context.Context, dk Scope) (HostInfo, error) {
	out, err := dk.Run(ctx, "info", "--format", "{{.MemTotal}}\t{{.NCPU}}\t{{.DockerRootDir}}")
	if err != nil {
		return HostInfo{}, fmt.Errorf("docker info: %v: %s", err, strings.TrimSpace(string(out)))
	}
	f := strings.Split(strings.TrimSpace(string(out)), "\t")
	if len(f) != 3 {
		return HostInfo{}, fmt.Errorf("docker info answered %q", strings.TrimSpace(string(out)))
	}
	mem, err1 := strconv.ParseInt(f[0], 10, 64)
	cpus, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil || mem <= 0 || cpus <= 0 || f[2] == "" {
		return HostInfo{}, fmt.Errorf("docker info answered %q", strings.TrimSpace(string(out)))
	}
	return HostInfo{MemoryBytes: mem, CPUs: cpus, DataRoot: f[2]}, nil
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
}

// RunningLimits is the limits Docker enforces on every running container:
// the daemon's own record (docker inspect), so a preview, a need, an
// override and software AppTown did not start are all in it. A CPU
// ceiling may be set three ways (--cpus, a quota over a period, a set of
// CPUs); the tightest one holds.
func RunningLimits(ctx context.Context, dk Scope) ([]ContainerLimit, error) {
	out, err := dk.Run(ctx, "ps", "-q", "--no-trunc")
	if err != nil {
		return nil, fmt.Errorf("docker ps: %v: %s", err, strings.TrimSpace(string(out)))
	}
	running := strings.Fields(string(out))
	if len(running) == 0 {
		return nil, nil
	}
	format := "{{.Name}}\t{{.HostConfig.Memory}}\t{{.HostConfig.NanoCpus}}\t{{.HostConfig.CpuQuota}}\t{{.HostConfig.CpuPeriod}}\t{{.HostConfig.CpusetCpus}}\t" + `{{index .Config.Labels "` + dockerlabel.Class + `"}}` + "\t" + `{{index .Config.Labels "` + dockerlabel.Owner + `"}}`
	out, err = dk.Run(ctx, append([]string{"inspect", "--type", "container", "--format", format}, running...)...)
	if err != nil {
		return nil, fmt.Errorf("docker inspect: %v: %s", err, strings.TrimSpace(string(out)))
	}
	var limits []ContainerLimit
	// Lines are split, not trimmed whole: a container without the class
	// label ends its line in an empty field, which trimming would take.
	for _, line := range strings.Split(strings.TrimRight(string(out), "\r\n"), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		l, err := parseContainerLimit(line)
		if err != nil {
			return nil, err
		}
		limits = append(limits, l)
	}
	return limits, nil
}

func parseContainerLimit(line string) (ContainerLimit, error) {
	f := strings.Split(line, "\t")
	if len(f) != 8 {
		return ContainerLimit{}, fmt.Errorf("docker inspect answered %q", line)
	}
	mem, err1 := strconv.ParseInt(f[1], 10, 64)
	nano, err2 := strconv.ParseInt(f[2], 10, 64)
	quota, err3 := strconv.ParseInt(f[3], 10, 64)
	period, err4 := strconv.ParseInt(f[4], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return ContainerLimit{}, fmt.Errorf("docker inspect answered %q", line)
	}
	label := func(v string) string {
		if v == "<no value>" {
			return ""
		}
		return v
	}
	l := ContainerLimit{Name: strings.TrimPrefix(f[0], "/"), MemoryBytes: mem, Class: label(f[6]), Owner: label(f[7])}
	l.AppTown = l.Class != ""
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
	if set := strings.TrimSpace(f[5]); set != "" {
		n, ok := cpusetSize(set)
		if !ok {
			l.CPUUnread = true
		} else {
			tightest(float64(n))
		}
	}
	return l, nil
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

// RunningMemoryUsage is what every running container uses now, by name
// (docker stats, one sample).
func RunningMemoryUsage(ctx context.Context, dk Scope) (map[string]int64, error) {
	out, err := dk.Run(ctx, "stats", "--no-stream", "--format", "{{.Name}}\t{{.MemUsage}}")
	if err != nil {
		return nil, fmt.Errorf("docker stats: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return parseMemoryUsage(out)
}

// ReadDisk reads the filesystem holding the daemon's data folder
// (diskspace.ReadThroughDocker). Each call's output is combined, as every
// call through a scope's client is.
func ReadDisk(ctx context.Context, dk Scope, image, dataRoot string) (diskspace.Space, error) {
	if dk.Lifetime != nil {
		// The scope's lifetime bounds the read through ctx instead, so the
		// read's removal of its container, which outlives ctx, outlives it
		// too.
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		defer context.AfterFunc(dk.Lifetime, cancel)()
		dk.Lifetime = nil
	}
	space, err := diskspace.ReadThroughDocker(ctx, func(ctx context.Context, args ...string) (string, string, error) {
		out, err := dk.Run(ctx, args...)
		return string(out), "", err
	}, image, dataRoot, clock.Or(dk.Now), func(s string) { log.Print(s) })
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
	d, err := ReadDisk(ctx, dk, image, info.DataRoot)
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
