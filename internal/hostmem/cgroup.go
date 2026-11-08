package hostmem

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CgroupMemory reads a cgroup v2 slice's working set. Its limit is checked
// against Docker's total, never substituted for that total.
func CgroupMemory(current, stat, limit []byte, total int64) (int64, error) {
	used, err := counter(strings.TrimSpace(string(current)), "memory.current")
	if err != nil {
		return 0, err
	}
	inactive, err := statCounter(stat, "inactive_file")
	if err != nil {
		return 0, err
	}
	if inactive > used {
		return 0, fmt.Errorf("memory.stat inactive_file exceeds memory.current")
	}
	if strings.TrimSpace(string(limit)) != "max" {
		maximum, err := counter(strings.TrimSpace(string(limit)), "memory.max")
		if err != nil {
			return 0, err
		}
		if maximum != total {
			return 0, fmt.Errorf("the reader's memory.max is %d bytes and the daemon's memory total is %d", maximum, total)
		}
	}
	return used - inactive, nil
}

// ParseCgroupCPU reads accumulated CPU time in microseconds from cpu.stat.
func ParseCgroupCPU(b []byte) (int64, error) { return statCounter(b, "usage_usec") }

// CgroupUsedCores converts elapsed CPU time to cores over the owner's sample
// interval. Missing time and reset counters are unknown, never idle.
func CgroupUsedCores(first, second int64, elapsed time.Duration) (float64, error) {
	if first < 0 || second < first || elapsed <= 0 {
		return 0, fmt.Errorf("cgroup CPU counters reset or sample time did not advance")
	}
	return float64(second-first) / (float64(elapsed) / float64(time.Microsecond)), nil
}

func counter(s, name string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("the host's cgroup %s is not readable", name)
	}
	return n, nil
}

func statCounter(b []byte, name string) (int64, error) {
	found := false
	var value int64
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != name {
			continue
		}
		if len(f) != 2 || found {
			return 0, fmt.Errorf("the host's cgroup %s is malformed", name)
		}
		var err error
		value, err = counter(f[1], name)
		if err != nil {
			return 0, err
		}
		found = true
	}
	if !found {
		return 0, fmt.Errorf("the host's cgroup has no %s counter", name)
	}
	return value, nil
}
