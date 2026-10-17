// Package hostmem reads the memory of the machine this process runs on, as
// the kernel reports it to this process: its total and what is available.
// It reads /proc/meminfo only; on a system without it every read is an
// error, never zero.
package hostmem

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Memory is the machine's memory as /proc/meminfo reports it, in bytes.
type Memory struct {
	TotalBytes     int64
	AvailableBytes int64
}

// UsedBytes is what is not available: the processes', the kernel's and the
// caches the kernel will not give back.
func (m Memory) UsedBytes() int64 { return m.TotalBytes - m.AvailableBytes }

// Read reads /proc/meminfo.
func Read() (Memory, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return Memory{}, fmt.Errorf("reading the host's memory: %w", err)
	}
	return ParseMeminfo(b)
}

// ParseMeminfo reads MemTotal and MemAvailable from a meminfo listing. Both
// must be present: a kernel without MemAvailable (older than 3.14) gives no
// honest figure for what is free.
func ParseMeminfo(b []byte) (Memory, error) {
	fields := kib(b, "MemTotal:", "MemAvailable:")
	total, okT := fields["MemTotal:"]
	avail, okA := fields["MemAvailable:"]
	if !okT || !okA || total <= 0 || avail < 0 || avail > total {
		return Memory{}, fmt.Errorf("the host's meminfo has no readable MemTotal and MemAvailable")
	}
	return Memory{TotalBytes: total, AvailableBytes: avail}, nil
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
		if len(f) != 3 || !want[f[0]] || f[2] != "kB" {
			continue
		}
		n, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		out[f[0]] = n * 1024
	}
	return out
}
