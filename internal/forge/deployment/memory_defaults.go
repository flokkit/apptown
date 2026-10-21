package deployment

import (
	"context"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/ids"
	"fmt"
	"maps"
)

const ForgeDefaultMemory int64 = 512 << 20

// MemoryDefaultSource identifies the default that supplied the effective cap.
type MemoryDefaultSource struct {
	Kind    string `json:"kind"`
	Name    string `json:"name,omitempty"`
	Version int64  `json:"version,omitempty"`
}

// DefaultMemory resolves a fresh container generation, after profile/override.
// Replay must use AppliedProfile.On instead; a default is not a declaration.
func (e *Environment) DefaultMemory(ctx context.Context, projectID ids.ID, c *appcontract.Contract, a AppliedProfile) (*appcontract.Contract, AppliedProfile, error) {
	out := *c
	out.Parts = append([]appcontract.Part(nil), c.Parts...)
	out.NeedResources = maps.Clone(c.NeedResources)
	a.Parts = maps.Clone(a.Parts)
	if a.Parts == nil {
		a.Parts = map[string]AppliedPart{}
	}
	amount := ForgeDefaultMemory
	source := MemoryDefaultSource{Kind: "apptown"}
	if e.memoryDefaults != nil {
		d, name, version, err := e.memoryDefaults.MemoryDefault(ctx, projectID)
		if err != nil {
			return nil, AppliedProfile{}, err
		}
		b, err := d.Bytes()
		if err != nil {
			return nil, AppliedProfile{}, err
		}
		if b > 0 {
			amount = b
			source = MemoryDefaultSource{Kind: "pack", Name: name, Version: version}
		}
	}
	for i := range out.Parts {
		p := &out.Parts[i]
		ap := a.Parts[p.Name]
		if ap.MemoryUnlimited {
			p.Resources.MemoryBytes = 0
		} else if p.Resources.MemoryBytes == 0 {
			p.Resources.MemoryBytes = amount
			v := source
			ap.MemoryDefault = &v
		}
		ap.Resources = p.Resources
		a.Parts[p.Name] = ap
	}
	for _, n := range out.Needs {
		ap, service := a.Parts[n]
		if !service || !ap.Need {
			continue
		}
		r := out.NeedResources[n]
		if ap.MemoryUnlimited {
			r.MemoryBytes = 0
		} else if r.MemoryBytes == 0 {
			b := ForgeDefaultMemory
			src := MemoryDefaultSource{Kind: "apptown"}
			if e.needs != nil {
				k, err := e.needs.ByCapability(ctx, n)
				if err != nil {
					return nil, AppliedProfile{}, err
				}
				v, err := k.Resources.Bytes()
				if err != nil {
					return nil, AppliedProfile{}, err
				}
				if v > 0 {
					b = v
					src = MemoryDefaultSource{Kind: "catalogue", Name: n}
				}
			}
			r.MemoryBytes = b
			ap.MemoryDefault = &src
		}
		if out.NeedResources == nil {
			out.NeedResources = map[string]appcontract.Resources{}
		}
		out.NeedResources[n] = r
		ap.Resources = r
		a.Parts[n] = ap
	}
	return &out, a, nil
}

// SuppliedServiceDefaults caps workflow sidecars; their arbitrary names/images
// are not catalogue capabilities. Reject ambiguous assembly before effects.
func SuppliedServiceDefaults(c *appcontract.Contract, services []deploy.ServiceSpec) ([]deploy.ServiceSpec, map[string]AppliedPart, error) {
	names := map[string]bool{}
	for _, p := range c.Parts {
		names[p.Name] = true
	}
	for _, n := range c.Needs {
		names[n] = true
	}
	out := append([]deploy.ServiceSpec(nil), services...)
	applied := map[string]AppliedPart{}
	for i := range out {
		s := &out[i]
		if s.Name == "" || names[s.Name] {
			return nil, nil, fmt.Errorf("supplied service %q has an ambiguous stack identity", s.Name)
		}
		names[s.Name] = true
		ap := AppliedPart{Resources: s.Resources}
		if s.Resources.MemoryBytes == 0 {
			s.Resources.MemoryBytes = ForgeDefaultMemory
			ap.Resources = s.Resources
			ap.MemoryDefault = &MemoryDefaultSource{Kind: "apptown"}
		}
		if s.Resources.MemoryBytes < appcontract.MinMemoryBytes || s.Resources.CPUs < 0 {
			return nil, nil, fmt.Errorf("supplied service %s has invalid resources", s.Name)
		}
		applied[s.Name] = ap
	}
	return out, applied, nil
}

// OnServices reapplies exact standing service caps; missing older records keep
// their source values and never acquire today's fallback during recovery.
func (a AppliedProfile) OnServices(services []deploy.ServiceSpec) []deploy.ServiceSpec {
	out := append([]deploy.ServiceSpec(nil), services...)
	for i := range out {
		if p, ok := a.Services[out[i].Name]; ok {
			out[i].Resources = p.Resources
		}
	}
	return out
}
