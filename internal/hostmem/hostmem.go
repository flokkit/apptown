// Package hostmem parses whole-host kernel memory and CPU readings.
// Missing or malformed readings are errors, never zero.
package hostmem

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// memory is the machine's memory as /proc/meminfo reports it, in bytes.
type memory struct {
	TotalBytes     int64
	AvailableBytes int64
}

// UsedBytes is what is not available: the processes', the kernel's and the
// caches the kernel will not give back.
func (m memory) UsedBytes() int64 { return m.TotalBytes - m.AvailableBytes }

// ParseMeminfo reads MemTotal and MemAvailable from a meminfo listing. Both
// must be present: a kernel without MemAvailable (older than 3.14) gives no
// honest figure for what is free.
func ParseMeminfo(b []byte) (memory, error) {
	fields := kib(b, "MemTotal:", "MemAvailable:")
	total, okT := fields["MemTotal:"]
	avail, okA := fields["MemAvailable:"]
	if !okT || !okA || total <= 0 || avail < 0 || avail > total {
		return memory{}, fmt.Errorf("the host's meminfo has no readable MemTotal and MemAvailable")
	}
	return memory{TotalBytes: total, AvailableBytes: avail}, nil
}

// kib reads the named "Key:  123 kB" lines, in bytes.
func kib(b []byte, keys ...string) map[string]int64 {
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	out := map[string]int64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 || !want[f[0]] {
			continue
		}
		if len(f) != 3 || f[2] != "kB" {
			return nil
		}
		if _, duplicate := out[f[0]]; duplicate {
			return nil
		}
		n, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil || n < 0 || n > math.MaxInt64/1024 {
			return nil
		}
		out[f[0]] = n * 1024
	}
	if sc.Err() != nil {
		return nil
	}
	return out
}
