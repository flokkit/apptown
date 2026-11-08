package docker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Usage is what one container uses now: its memory in bytes and its CPU in
// cores (docker stats' CPU percentage is of one core, so 150% is 1.5).
type Usage struct {
	MemoryBytes int64
	CPUCores    float64
}

// ContainerUsage is what each named container uses now, as the daemon
// reports it (docker stats, one sample). A container the daemon does not
// report is absent from the answer, including an explicit "-- / --"
// no-stats row; other answers that cannot be read are errors, never zero.
func ContainerUsage(ctx context.Context, dk Scope, names []string) (map[string]Usage, error) {
	if len(names) == 0 {
		return map[string]Usage{}, nil
	}
	args := append([]string{"stats", "--no-stream", "--format", "{{.Name}}\t{{.MemUsage}}\t{{.CPUPerc}}"}, names...)
	out, err := dk.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("docker stats: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return parseUsage(out)
}

// parseUsage reads docker stats' name, memory and CPU lines.
func parseUsage(out []byte) (map[string]Usage, error) {
	used := map[string]Usage{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, rest, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		memory, cpu, _ := strings.Cut(rest, "\t")
		if strings.TrimSpace(memory) == "-- / --" {
			continue
		}
		current, _, _ := strings.Cut(memory, "/")
		b, err := parseDockerBytes(strings.TrimSpace(current))
		if err != nil {
			return nil, fmt.Errorf("docker stats reported %q for %s: %w", memory, name, err)
		}
		percent, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(cpu), "%"), 64)
		if err != nil || percent < 0 {
			return nil, fmt.Errorf("docker stats reported CPU %q for %s", cpu, name)
		}
		used[strings.TrimPrefix(strings.TrimSpace(name), "/")] = Usage{MemoryBytes: b, CPUCores: percent / 100}
	}
	return used, nil
}

// parseDockerBytes reads a size as docker stats prints it: 45.5MiB,
// 1.2GiB, 512KiB, 0B, and the decimal kB, MB, GB.
func parseDockerBytes(s string) (int64, error) {
	units := []struct {
		suffix string
		mult   float64
	}{{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40}, {"kB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12}, {"B", 1}}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			n, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
			if err != nil || n < 0 {
				return 0, fmt.Errorf("not a size")
			}
			return int64(n * u.mult), nil
		}
	}
	return 0, fmt.Errorf("not a size")
}
