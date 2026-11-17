package deployment

import (
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/hostmem"
)

// hostUse interprets kernel counters against the daemon's totals.
func hostUse(parts map[string][]byte, err error, memory int64, cpus int) (*Figure, *Figure) {
	mem, cpu := unknown("the daemon's totals or data folder are not known"), unknown("the daemon's totals or data folder are not known")
	if memory <= 0 || cpus <= 0 {
		return &mem, &cpu
	}
	if err != nil {
		m := unknownErr(err)
		c := unknownErr(err)
		return &m, &c
	}
	switch strings.TrimSpace(string(parts["scope"])) {
	case "cgroup":
		return cgroupUse(parts, memory)
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
	if m, e := hostmem.ParseMeminfo(parts["meminfo"]); e != nil {
		mem = unknownErr(e)
	} else if m.TotalBytes != memory {
		mem = unknown(fmt.Sprintf("the reader sees %d bytes of memory and the daemon %d; /proc may describe a virtualised host or VM", m.TotalBytes, memory))
	} else {
		mem = known(float64(m.UsedBytes()))
	}
	first, err := hostmem.ParseStat(parts["cpu.first"])
	if err == nil {
		var second hostmem.CPU
		second, err = hostmem.ParseStat(parts["cpu.second"])
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
func cgroupUse(parts map[string][]byte, total int64) (*Figure, *Figure) {
	mem := unknown("the host's cgroup memory could not be read")
	used, err := hostmem.CgroupMemory(parts["memory.current"], parts["memory.stat"], parts["memory.max"], total)
	if err != nil {
		mem = unknownErr(err)
	} else {
		mem = known(float64(used))
	}
	cpu := unknown("the host's cgroup CPU could not be read")
	first, err := hostmem.ParseCgroupCPU(parts["cpu.first"])
	if err == nil {
		var second int64
		second, err = hostmem.ParseCgroupCPU(parts["cpu.second"])
		if err == nil {
			var elapsed time.Duration
			elapsed, err = kernelElapsed(parts)
			if err == nil {
				var cores float64
				cores, err = hostmem.CgroupUsedCores(first, second, elapsed)
				if err == nil {
					cpu = known(cores)
				}
			}
		}
	}
	if err != nil {
		cpu = unknownErr(err)
	}
	return &mem, &cpu
}

func kernelElapsed(parts map[string][]byte) (time.Duration, error) {
	var uptime [2]time.Duration
	for i, name := range []string{"uptime.first", "uptime.second"} {
		fields := strings.Fields(string(parts[name]))
		if len(fields) != 2 {
			return 0, fmt.Errorf("the host's /proc/uptime is not readable")
		}
		value, err := time.ParseDuration(fields[0] + "s")
		if err != nil || value < 0 {
			return 0, fmt.Errorf("the host's /proc/uptime is not readable")
		}
		uptime[i] = value
	}
	return uptime[1] - uptime[0], nil
}
