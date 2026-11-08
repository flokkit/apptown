package hostmem

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// CPU is an aggregate /proc/stat sample. Guest time is already included in
// user and nice; only the first eight counters contribute to the aggregate total.
type CPU struct {
	Counters [8]uint64
	Cores    int
}

// ParseStat requires the aggregate counters and the online per-CPU lines.
func ParseStat(b []byte) (CPU, error) {
	var c CPU
	seen := map[string]bool{}
	aggregate := false
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		if seen[f[0]] {
			return CPU{}, fmt.Errorf("duplicate CPU line in /proc/stat")
		}
		seen[f[0]] = true
		if f[0] != "cpu" {
			if _, err := strconv.ParseUint(strings.TrimPrefix(f[0], "cpu"), 10, 32); err != nil {
				return CPU{}, fmt.Errorf("invalid CPU name in /proc/stat")
			}
			c.Cores++
		}
		if len(f) < 9 || len(f) > 11 {
			return CPU{}, fmt.Errorf("unreadable CPU counters in /proc/stat")
		}
		var total uint64
		for i, value := range f[1:] {
			n, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return CPU{}, fmt.Errorf("unreadable CPU counter in /proc/stat")
			}
			if i < 8 {
				if n > math.MaxUint64-total {
					return CPU{}, fmt.Errorf("CPU counters overflow in /proc/stat")
				}
				total += n
				if f[0] == "cpu" {
					c.Counters[i] = n
				}
			}
		}
		if f[0] == "cpu" {
			aggregate = true
		}
	}
	if !aggregate || c.Cores == 0 {
		return CPU{}, fmt.Errorf("no aggregate CPU or online CPUs in /proc/stat")
	}
	return c, nil
}

// UsedCores calculates busy cores between samples, excluding idle and iowait.
func UsedCores(a, b CPU) (float64, error) {
	if a.Cores <= 0 || a.Cores != b.Cores {
		return 0, fmt.Errorf("online CPU count changed between samples")
	}
	var total, idle uint64
	for i, n := range b.Counters {
		if n < a.Counters[i] {
			return 0, fmt.Errorf("CPU counter went backwards")
		}
		d := n - a.Counters[i]
		total += d
		if i == 3 || i == 4 {
			idle += d
		}
	}
	if total == 0 {
		return 0, fmt.Errorf("CPU counters did not advance")
	}
	return float64(total-idle) / float64(total) * float64(b.Cores), nil
}
