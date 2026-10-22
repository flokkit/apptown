package digitalocean

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// InstanceSize is one App Platform instance size as the API lists it.
type InstanceSize struct {
	Slug        string
	CPUs        float64
	MemoryBytes int64
	USDPerMonth float64
	Deprecated  bool
}

// InstanceSizes is App Platform's current list of instance sizes: the
// source of truth for what a part's declared resources become.
func (c *Client) InstanceSizes(ctx context.Context) ([]InstanceSize, error) {
	var got struct {
		Sizes []struct {
			Slug        string `json:"slug"`
			CPUs        string `json:"cpus"`
			MemoryBytes string `json:"memory_bytes"`
			USDPerMonth string `json:"usd_per_month"`
			Deprecation *bool  `json:"deprecation_intent"`
		} `json:"instance_sizes"`
	}
	if err := c.Call(ctx, "GET", "/v2/apps/tiers/instance_sizes", nil, &got); err != nil {
		return nil, err
	}
	out := make([]InstanceSize, 0, len(got.Sizes))
	for _, s := range got.Sizes {
		cpus, err1 := strconv.ParseFloat(s.CPUs, 64)
		mem, err2 := strconv.ParseInt(s.MemoryBytes, 10, 64)
		usd, err3 := strconv.ParseFloat(s.USDPerMonth, 64)
		if err1 != nil || err2 != nil || err3 != nil || s.Slug == "" {
			return nil, fmt.Errorf("App Platform listed an instance size AppTown cannot read (%q)", s.Slug)
		}
		out = append(out, InstanceSize{Slug: s.Slug, CPUs: cpus, MemoryBytes: mem, USDPerMonth: usd, Deprecated: s.Deprecation != nil && *s.Deprecation})
	}
	return out, nil
}

// SizeFor is the cheapest current instance size whose memory and CPUs
// cover r, or an error naming what no size covers. Sizes App Platform
// marks for deprecation are never chosen. Equal prices take the smaller
// memory, then the slug, so the choice is the same every time.
func SizeFor(sizes []InstanceSize, r appcontract.Resources) (InstanceSize, error) {
	var fit []InstanceSize
	for _, s := range sizes {
		if !s.Deprecated && s.MemoryBytes >= r.MemoryBytes && s.CPUs >= r.CPUs {
			fit = append(fit, s)
		}
	}
	if len(fit) == 0 {
		return InstanceSize{}, fmt.Errorf("no App Platform instance size has %s of memory and %s CPUs", r.MemoryWords(), r.CPUWords())
	}
	sort.Slice(fit, func(i, j int) bool {
		a, b := fit[i], fit[j]
		if a.USDPerMonth != b.USDPerMonth {
			return a.USDPerMonth < b.USDPerMonth
		}
		if a.MemoryBytes != b.MemoryBytes {
			return a.MemoryBytes < b.MemoryBytes
		}
		return a.Slug < b.Slug
	})
	return fit[0], nil
}

// partSizes is the instance size each part of c runs on: the configured
// size for a part that declares no resources, and the cheapest covering
// size for one that does; a need that declares resources is refused
// (refuseNeedSizes). The list is read only when a part declares
// resources. A list that cannot be read is an error a later attempt may
// not meet; a part no size covers is terminal. Either comes before
// anything is made.
func partSizes(ctx context.Context, list func(context.Context) ([]InstanceSize, error), configured string, c *appcontract.Contract) (map[string]string, error) {
	if err := refuseNeedSizes(c); err != nil {
		return nil, err
	}
	out := map[string]string{}
	var sizes []InstanceSize
	for _, p := range c.Parts {
		out[p.Name] = configured
		if !p.Resources.Declared() {
			continue
		}
		if sizes == nil {
			var err error
			if sizes, err = list(ctx); err != nil {
				return nil, fmt.Errorf("reading App Platform's instance sizes to fit part %s's declared resources: %w", p.Name, err)
			}
		}
		s, err := SizeFor(sizes, p.Resources)
		if err != nil {
			// No retry fits it: the contract must change.
			return nil, deploy.Terminal(fmt.Errorf("part %s: %w", p.Name, err))
		}
		out[p.Name] = s.Slug
	}
	return out, nil
}

// FitResources says what each part of c runs on, before anything is made
// (deploy.ResourceFitter): the cheapest covering size for a part that
// declares resources, with its capacity, and the configured size for one
// that does not. A need that declares resources is refused
// (refuseNeedSizes).
func (d *Driver) FitResources(ctx context.Context, c *appcontract.Contract) (map[string]string, error) {
	client, cfg, err := d.Client(ctx)
	if err != nil {
		return nil, err
	}
	var listed []InstanceSize
	list := func(ctx context.Context) ([]InstanceSize, error) {
		var err error
		listed, err = client.InstanceSizes(ctx)
		return listed, err
	}
	sizes, err := partSizes(ctx, list, cfg.InstanceSize, c)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range c.Parts {
		if !p.Resources.Declared() {
			out[p.Name] = sizes[p.Name] + " (configured size, not declared)"
			continue
		}
		for _, s := range listed {
			if s.Slug == sizes[p.Name] {
				out[p.Name] = fmt.Sprintf("%s (%s, %s CPU)", s.Slug, appcontract.FormatMemory(s.MemoryBytes), strconv.FormatFloat(s.CPUs, 'f', -1, 64))
			}
		}
	}
	return out, nil
}

// refuseNeedSizes refuses, before any effect, a need that declares
// resources: App Platform's managed Postgres is made at the integration's
// database size, and per-deployment database sizes are not offered in
// 1.0; no other need is fulfilled here at all.
func refuseNeedSizes(c *appcontract.Contract) error {
	for _, n := range c.Needs {
		if _, selected := c.NeedSizes[n]; selected {
			return deploy.Terminal(fmt.Errorf("needs: %s selects a local-container size; App Platform does not support catalogue sizes", n))
		}
		if _, declared := c.NeedResources[n]; declared {
			return deploy.Terminal(fmt.Errorf("needs: %s declares resources, but App Platform makes its managed Postgres at the integration's database size; per-deployment database sizes are not offered yet, so remove %s's resources for this target", n, n))
		}
	}
	return nil
}

// SizePrices is App Platform's listed monthly price of each instance size,
// by slug (deploy.SizePricer): what an estimate of a deployment's cost is
// made from. Deprecated sizes are listed too: a part may still run on one.
func (d *Driver) SizePrices(ctx context.Context) (map[string]float64, error) {
	client, _, err := d.Client(ctx)
	if err != nil {
		return nil, err
	}
	sizes, err := client.InstanceSizes(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(sizes))
	for _, s := range sizes {
		out[s.Slug] = s.USDPerMonth
	}
	return out, nil
}
