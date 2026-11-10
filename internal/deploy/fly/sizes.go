package fly

import (
	"context"
	"fmt"
	"math"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

const (
	mb = 1 << 20
	// defaultMemoryMB is a part's memory when it declares none: App
	// Platform's smallest default size has the same.
	defaultMemoryMB = 512
	// A shared-cpu-Nx Machine takes 256 MB to 2 GB of memory per CPU, in
	// steps of 256 MB, on 1, 2, 4 or 8 CPUs.
	memoryStepMB     = 256
	minMemoryPerCPU  = 256
	maxMemoryPerCPU  = 2048
	largestSharedCPU = 8
)

// fit is the shared-CPU guest that covers r: the fewest CPUs that cover
// the declared CPUs and memory, and the declared memory rounded up to Fly's
// step (or the default when none is declared).
func fit(r appcontract.Resources) (guest, error) {
	memory := int64(defaultMemoryMB)
	if r.MemoryBytes > 0 {
		memory = int64(math.Ceil(float64(r.MemoryBytes)/float64(memoryStepMB*mb))) * memoryStepMB
	}
	for cpus := 1; cpus <= largestSharedCPU; cpus *= 2 {
		if float64(cpus) < r.CPUs || memory > int64(maxMemoryPerCPU*cpus) {
			continue
		}
		return guest{CPUKind: "shared", CPUs: cpus, MemoryMB: int(max(memory, int64(minMemoryPerCPU*cpus)))}, nil
	}
	return guest{}, fmt.Errorf("no shared-CPU Fly Machine covers %s of memory and %s CPUs (the largest is 8 CPUs with 16 GB)", r.MemoryWords(), r.CPUWords())
}

func (g guest) words() string {
	return fmt.Sprintf("shared-cpu-%dx (%d MB)", g.CPUs, g.MemoryMB)
}

// FitResources says what each part will run on, before anything is made: a
// declaration no shared-CPU Machine covers stops the release here.
func (d *Driver) FitResources(_ context.Context, c *appcontract.Contract) (map[string]string, error) {
	out := map[string]string{}
	if c == nil {
		return out, nil
	}
	for _, p := range c.Parts {
		g, err := fit(p.Resources)
		if err != nil {
			return nil, terminal(fmt.Errorf("part %s: %w", p.Name, err))
		}
		out[p.Name] = g.words()
	}
	return out, nil
}
