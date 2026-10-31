package docker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// MemoryUsage is what each named container uses now, as the daemon reports
// it (docker stats, one sample). A container the daemon does not report is
// absent from the answer, including an explicit "-- / --" no-stats row;
// other answers that cannot be read are errors, never
// zero.
func MemoryUsage(ctx context.Context, dk Scope, names []string) (map[string]int64, error) {
	if len(names) == 0 {
		return map[string]int64{}, nil
	}
	args := append([]string{"stats", "--no-stream", "--format", "{{.Name}}\t{{.MemUsage}}"}, names...)
	out, err := dk.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("docker stats: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return parseMemoryUsage(out)
}

// parseMemoryUsage reads docker stats' name and usage lines.
func parseMemoryUsage(out []byte) (map[string]int64, error) {
	used := map[string]int64{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, usage, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		// Docker can emit this placeholder when a container has no stats.
		// Omit it so callers retain unknown use for that container.
		if strings.TrimSpace(usage) == "-- / --" {
			continue
		}
		current, _, _ := strings.Cut(usage, "/")
		b, err := parseDockerBytes(strings.TrimSpace(current))
		if err != nil {
			return nil, fmt.Errorf("docker stats reported %q for %s: %w", usage, name, err)
		}
		used[strings.TrimPrefix(strings.TrimSpace(name), "/")] = b
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
