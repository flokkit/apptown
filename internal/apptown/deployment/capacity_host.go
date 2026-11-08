package deployment

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/hostmem"
)

// Every Docker host uses the daemon's standing disk reader, including the
// built-in host. No reading of apptownd's own machine is substituted.
func (s *Service) hostUse(ctx context.Context, dk docker.Scope, memory int64, cpus int, root string) (*Figure, *Figure) {
	mem, cpu := unknown("the daemon's totals or data folder are not known"), unknown("the daemon's totals or data folder are not known")
	if root == "" || memory <= 0 || cpus <= 0 {
		return &mem, &cpu
	}
	image := s.env.ToolVersions().Images.DF
	read := func(file string) ([]byte, error) {
		return diskspace.ReadKernel(ctx, func(ctx context.Context, args ...string) (string, string, error) {
			b, err := dk.Run(ctx, args...)
			return string(b), "", err
		}, image, root, file, s.now)
	}
	scope, err := read("scope")
	if err != nil {
		m := unknownErr(err)
		c := unknownErr(err)
		return &m, &c
	}
	switch strings.TrimSpace(string(scope)) {
	case "cgroup":
		return s.cgroupUse(ctx, read, memory)
	case "proc": // The cgroup v2 root has no slice counters; /proc describes the machine.
	case "v1":
		m := unknown("whole-host CPU and memory readings require cgroup v2; this host has no cgroup v2 root")
		c := m
		return &m, &c
	default:
		m := unknown("the reader's cgroup scope is not readable")
		c := m
		return &m, &c
	}
	b, err := read("meminfo")
	if err != nil {
		mem = unknownErr(err)
	} else if m, e := hostmem.ParseMeminfo(b); e != nil {
		mem = unknownErr(e)
	} else if m.TotalBytes != memory {
		mem = unknown(fmt.Sprintf("the reader sees %d bytes of memory and the daemon %d; /proc may describe a virtualised host or VM", m.TotalBytes, memory))
	} else {
		mem = known(float64(m.UsedBytes()))
	}
	sample := func() (hostmem.CPU, error) {
		b, err := read("stat")
		if err != nil {
			return hostmem.CPU{}, err
		}
		return hostmem.ParseStat(b)
	}
	first, err := sample()
	if err == nil {
		err = waitHostSample(ctx)
	}
	if err == nil {
		var second hostmem.CPU
		second, err = sample()
		if err == nil {
			if first.Cores != cpus || second.Cores != cpus {
				err = fmt.Errorf("the reader sees %d then %d online CPUs and the daemon %d; /proc may describe a virtualised host or VM", first.Cores, second.Cores, cpus)
			} else {
				var used float64
				used, err = hostmem.UsedCores(first, second)
				if err == nil {
					cpu = known(used)
				}
			}
		}
	}
	if err != nil {
		cpu = unknownErr(err)
	}
	return &mem, &cpu
}

// A cgroup slice's counters belong to the daemon's own machine, so /proc's
// totals are irrelevant. Failures in memory and CPU remain independent.
func (s *Service) cgroupUse(ctx context.Context, read func(string) ([]byte, error), total int64) (*Figure, *Figure) {
	mem := unknown("the host's cgroup memory could not be read")
	current, err := read("memory.current")
	if err == nil {
		var stat, limit []byte
		stat, err = read("memory.stat")
		if err == nil {
			limit, err = read("memory.max")
		}
		if err == nil {
			var used int64
			used, err = hostmem.CgroupMemory(current, stat, limit, total)
			if err == nil {
				mem = known(float64(used))
			}
		}
	}
	if err != nil {
		mem = unknownErr(err)
	}
	sample := func() (int64, error) {
		b, err := read("cpu.stat")
		if err != nil {
			return 0, err
		}
		return hostmem.ParseCgroupCPU(b)
	}
	cpu := unknown("the host's cgroup CPU could not be read")
	first, err := sample()
	at := s.now()
	if err == nil {
		err = waitHostSample(ctx)
	}
	if err == nil {
		var second int64
		second, err = sample()
		if err == nil {
			var used float64
			used, err = hostmem.CgroupUsedCores(first, second, s.now().Sub(at))
			if err == nil {
				cpu = known(used)
			}
		}
	}
	if err != nil {
		cpu = unknownErr(err)
	}
	return &mem, &cpu
}

func waitHostSample(ctx context.Context) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
