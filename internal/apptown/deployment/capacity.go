package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/hostmem"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/toolversions"
)

// Target capacity: how full a target is. A fixed host (a Docker host, a
// Portainer endpoint) has a total, and the limits and use of what runs on
// it; an elastic one (App Platform) grows, and shows instead what each
// deployment runs on and an estimate of what it costs. A figure AppTown
// cannot read is unknown with the reason, never zero.

// Figure is one reading: its value, or why it is unknown.
type Figure struct {
	Known  bool    `json:"known"`
	Value  float64 `json:"value"`
	Reason string  `json:"reason,omitempty"`
}

func known(v float64) Figure          { return Figure{Known: true, Value: v} }
func unknown(reason string) Figure    { return Figure{Reason: reason} }
func unknownErr(err error) Figure     { return unknown(err.Error()) }
func (f Figure) or(other Figure) bool { return f.Known && other.Known }

// Split is a figure for AppTown's own containers and for the rest of
// what runs on the host.
type Split struct {
	AppTown Figure `json:"apptown"`
	Other   Figure `json:"other"`
}

// Promised is the limits Docker enforces on the running containers:
// their sum, how many carry one, how many carry none (counted, never
// summed as zero), and how many set a limit AppTown could not read
// (which makes the sum partial).
type Promised struct {
	Sum       Split `json:"sum"`
	Limited   Count `json:"limited"`
	Unlimited Count `json:"unlimited"`
	Unread    Count `json:"unread"`
}

// Count is a number of containers, AppTown's and the rest.
type Count struct {
	AppTown int `json:"apptown"`
	Other   int `json:"other"`
}

func (c *Count) add(own bool) {
	if own {
		c.AppTown++
	} else {
		c.Other++
	}
}

// ResourceCapacity is one resource of a fixed host: its total, what the
// running containers are promised, what they use, and whether the
// promises exceed the total.
type ResourceCapacity struct {
	Total    Figure   `json:"total"`
	Promised Promised `json:"promised"`
	InUse    *Split   `json:"inUse,omitempty"` // memory only: the containers' use, not the host's
	// HostUsed, memory only, is the whole host's use (the OS, caches and
	// processes outside containers included): known on the built-in host
	// when this AppTown sees the daemon's own total (hostUse).
	HostUsed      *Figure `json:"hostUsed,omitempty"`
	Overcommitted bool    `json:"overcommitted"`
}

// DiskCapacity is the filesystem holding a Docker host's data.
type DiskCapacity struct {
	Total Figure `json:"total"`
	Free  Figure `json:"free"`
	// Low reports pressure below the shared disk warning threshold.
	Low     bool  `json:"low"`
	Warning int64 `json:"warning,omitempty"`
	// Refuses says less than Floor is free: a new release or preview on
	// this host is refused until space is freed. It is read again when one
	// starts (docker.AdmitDisk); this reading may be minutes old (ReadAt).
	Refuses bool  `json:"refuses"`
	Floor   int64 `json:"floor,omitempty"`
	// Path is the folder whose filesystem this is, on the host: the
	// daemon's data folder.
	Path   string    `json:"path,omitempty"`
	ReadAt time.Time `json:"readAt,omitzero"`
}

// ElasticPart is one part or need of a deployment on an elastic target:
// the size it runs on, and that size's listed monthly price.
type ElasticPart struct {
	Deployment  string `json:"deployment"` // "<project>/<name>"
	Part        string `json:"part"`
	Need        bool   `json:"need,omitempty"`
	RunsOn      string `json:"runsOn"`
	USDPerMonth Figure `json:"usdPerMonth"`
}

// TargetCapacity is how full a target is.
type TargetCapacity struct {
	Target  string `json:"target"`
	Kind    string `json:"kind"`
	Elastic bool   `json:"elastic"`
	// Words is the one sentence the view leads with.
	Words  string            `json:"words"`
	Memory *ResourceCapacity `json:"memory,omitempty"`
	CPU    *ResourceCapacity `json:"cpu,omitempty"`
	Disk   *DiskCapacity     `json:"disk,omitempty"`
	// Tenants, on a Docker host, is who holds it: each running container
	// counted once (capacity_tenants.go).
	Tenants []Tenant `json:"tenants,omitempty"`
	// Parts, on an elastic target, is what each deployment runs on, and
	// Estimate the monthly sum of the app components' listed prices.
	Parts    []ElasticPart `json:"parts,omitempty"`
	Estimate *Figure       `json:"estimate,omitempty"`
	ReadAt   time.Time     `json:"readAt"`
}

// capacityCache keeps each target's reading briefly (a deployment card
// asks its host's for every view) and its disk reading longer (the disk
// read runs a container).
type capacityCache struct {
	mu   sync.Mutex
	read map[ids.ID]capacityReading
	disk map[ids.ID]diskReading
	// host reads this machine's memory (nil: /proc/meminfo).
	host func() (hostmem.Memory, error)
}

type capacityReading struct {
	revision int64
	capacity TargetCapacity
}

type diskReading struct {
	revision int64
	at       time.Time
	disk     DiskCapacity
}

const (
	capacityFresh = 30 * time.Second
	diskFresh     = 5 * time.Minute
)

// Capacity reads how full target name is; the caller holds the deployment
// read grant (the target page's).
func (s *Service) Capacity(ctx context.Context, name string) (TargetCapacity, error) {
	t, err := s.Targets.ByName(ctx, name)
	if err != nil {
		return TargetCapacity{}, err
	}
	return s.targetCapacity(ctx, t)
}

func (s *Service) targetCapacity(ctx context.Context, t *Target) (TargetCapacity, error) {
	now := s.now()
	s.capacity.mu.Lock()
	if c, ok := s.capacity.read[t.ID]; ok && c.revision == t.SettingsRevision && now.Sub(c.capacity.ReadAt) < capacityFresh {
		s.capacity.mu.Unlock()
		return c.capacity, nil
	}
	s.capacity.mu.Unlock()
	var out TargetCapacity
	var err error
	switch t.Kind {
	case KindDockerHost:
		out, err = s.dockerCapacity(ctx, t)
	case KindPortainer:
		out = s.portainerCapacity(ctx, t)
	case KindDOApp:
		out, err = s.elasticCapacity(ctx, t)
	default:
		out = TargetCapacity{Words: "Serves files; no capacity to manage."}
	}
	if err != nil {
		return TargetCapacity{}, err
	}
	out.Target, out.Kind, out.ReadAt = t.Name, t.Kind, now
	s.capacity.mu.Lock()
	if s.capacity.read == nil {
		s.capacity.read = map[ids.ID]capacityReading{}
	}
	s.capacity.read[t.ID] = capacityReading{revision: t.SettingsRevision, capacity: out}
	s.capacity.mu.Unlock()
	return out, nil
}

func (s *Service) dockerCapacity(ctx context.Context, t *Target) (TargetCapacity, error) {
	out := TargetCapacity{Words: "A fixed host: it does not grow.", Memory: &ResourceCapacity{}, CPU: &ResourceCapacity{}}
	dk, err := s.hosts.DockerScope(ctx, t)
	if err != nil {
		reason := fmt.Sprintf("its daemon cannot be asked: %v", err)
		*out.Memory, *out.CPU = unknownResource(reason, true), unknownResource(reason, false)
		out.Disk = &DiskCapacity{Total: unknown(reason), Free: unknown(reason)}
		return out, nil
	}
	info, err := docker.ReadHostInfo(ctx, dk)
	if err != nil {
		out.Memory.Total, out.CPU.Total = unknownErr(err), unknownErr(err)
	} else {
		out.Memory.Total, out.CPU.Total = known(float64(info.MemoryBytes)), known(float64(info.CPUs))
	}
	limits, limitsErr := docker.RunningLimits(ctx, dk)
	if limitsErr != nil {
		out.Memory.Promised = unknownPromised(limitsErr)
		out.CPU.Promised = unknownPromised(limitsErr)
	} else {
		out.Memory.Promised, out.CPU.Promised = promisedOf(limits)
	}
	used, usedErr := docker.RunningMemoryUsage(ctx, dk)
	out.Memory.InUse = inUseOf(limits, limitsErr, used, usedErr)
	if out.Memory.Total.Known {
		host := s.hostUse(t, info.MemoryBytes)
		out.Memory.HostUsed = &host
	} else {
		why := unknown("the daemon's total is not known, so this host's own use is not comparable")
		out.Memory.HostUsed = &why
	}
	if limitsErr == nil {
		tenants, err := s.dockerTenants(ctx, t, limits, used, usedErr)
		if err != nil {
			return out, err
		}
		out.Tenants = tenants
	}
	overcommit(out.Memory)
	overcommit(out.CPU)
	if info.DataRoot != "" {
		d := s.disk(ctx, t, dk, info.DataRoot)
		out.Disk = &d
	} else {
		out.Disk = &DiskCapacity{Total: unknown("the daemon did not say where its data is"), Free: unknown("the daemon did not say where its data is")}
	}
	return out, nil
}

// inUseOf is what the running containers use, AppTown's and the rest:
// known only when every container's owner was read. Use read without that
// (the inspection failed, or a container started between the readings) is
// not split by a guess.
func inUseOf(limits []docker.ContainerLimit, limitsErr error, used map[string]int64, err error) *Split {
	if limitsErr != nil {
		why := unknown("whose containers they are cannot be read: " + limitsErr.Error())
		return &Split{AppTown: why, Other: why}
	}
	if err != nil {
		return &Split{AppTown: unknownErr(err), Other: unknownErr(err)}
	}
	own := map[string]bool{}
	for _, l := range limits {
		own[l.Name] = l.AppTown
	}
	var f, o float64
	for name, b := range used {
		mine, read := own[name]
		if !read {
			why := unknown(fmt.Sprintf("%s started after the containers were read", name))
			return &Split{AppTown: why, Other: why}
		}
		if mine {
			f += float64(b)
		} else {
			o += float64(b)
		}
	}
	return &Split{AppTown: known(f), Other: known(o)}
}

func unknownResource(reason string, memory bool) ResourceCapacity {
	r := ResourceCapacity{Total: unknown(reason), Promised: unknownPromised(fmt.Errorf("%s", reason))}
	if memory {
		r.InUse = &Split{AppTown: unknown(reason), Other: unknown(reason)}
	}
	return r
}

func unknownPromised(err error) Promised {
	return Promised{Sum: Split{AppTown: unknownErr(err), Other: unknownErr(err)}}
}

// promisedOf sums the enforced limits, AppTown's and others', memory
// and CPUs.
func promisedOf(limits []docker.ContainerLimit) (memory, cpu Promised) {
	var mem, cpus [2]float64
	for _, l := range limits {
		i := 1
		if l.AppTown {
			i = 0
		}
		if l.MemoryBytes > 0 {
			mem[i] += float64(l.MemoryBytes)
			memory.Limited.add(l.AppTown)
		} else {
			memory.Unlimited.add(l.AppTown)
		}
		switch {
		case l.CPUUnread:
			cpu.Unread.add(l.AppTown)
		case l.CPUs > 0:
			cpus[i] += l.CPUs
			cpu.Limited.add(l.AppTown)
		default:
			cpu.Unlimited.add(l.AppTown)
		}
	}
	memory.Sum = Split{AppTown: known(mem[0]), Other: known(mem[1])}
	cpu.Sum = Split{AppTown: known(cpus[0]), Other: known(cpus[1])}
	return memory, cpu
}

// overcommit marks a resource whose promised limits exceed its total.
func overcommit(r *ResourceCapacity) {
	p := r.Promised.Sum
	if r.Total.Known && p.AppTown.or(p.Other) && p.AppTown.Value+p.Other.Value > r.Total.Value {
		r.Overcommitted = true
	}
}

// disk is the host's disk reading, at most diskFresh old: the read runs a
// container.
func (s *Service) disk(ctx context.Context, target *Target, dk docker.Scope, root string) DiskCapacity {
	now := s.now()
	s.capacity.mu.Lock()
	if r, ok := s.capacity.disk[target.ID]; ok && r.revision == target.SettingsRevision && now.Sub(r.at) < diskFresh {
		s.capacity.mu.Unlock()
		return r.disk
	}
	s.capacity.mu.Unlock()
	out := DiskCapacity{Path: root, ReadAt: now}
	d, err := docker.ReadDisk(ctx, dk, s.env.ToolVersions().Images.DF, root)
	if err != nil {
		out.Total, out.Free = unknownErr(err), unknownErr(err)
	} else {
		out.Total, out.Free = known(float64(d.TotalBytes)), known(float64(d.FreeBytes))
		out.Low, out.Refuses, out.Floor = d.Low(), d.Refuses(), d.Floor()
		out.Warning = d.Warning()
	}
	s.capacity.mu.Lock()
	if s.capacity.disk == nil {
		s.capacity.disk = map[ids.ID]diskReading{}
	}
	s.capacity.disk[target.ID] = diskReading{revision: target.SettingsRevision, at: now, disk: out}
	s.capacity.mu.Unlock()
	return out
}

// portainerCapacity is a Portainer endpoint's total, and what the
// AppTown's own deployments there declare; other stacks and use are not
// read in 1.0.
func (s *Service) portainerCapacity(ctx context.Context, t *Target) TargetCapacity {
	const notRead = "not read through Portainer in 1.0"
	out := TargetCapacity{Words: "A fixed host behind Portainer: it does not grow. The limits shown are those AppTown's deployments declare; Portainer's other stacks, use and disk are not read.",
		Memory: &ResourceCapacity{InUse: &Split{AppTown: unknown(notRead), Other: unknown(notRead)}},
		CPU:    &ResourceCapacity{},
		Disk:   &DiskCapacity{Total: unknown(notRead), Free: unknown(notRead)}}
	if cfg, err := s.providers.PortainerConfig(ctx, t); err != nil {
		out.Memory.Total, out.CPU.Total = unknownErr(err), unknownErr(err)
	} else if endpoint, err := portainer.EndpointID(ctx, cfg); err != nil {
		out.Memory.Total, out.CPU.Total = unknownErr(err), unknownErr(err)
	} else if mem, cpus, err := portainer.EndpointCapacity(ctx, cfg, endpoint); err != nil {
		out.Memory.Total, out.CPU.Total = unknownErr(err), unknownErr(err)
	} else {
		out.Memory.Total, out.CPU.Total = known(float64(mem)), known(float64(cpus))
	}
	declared, err := s.declaredOn(ctx, t.ID)
	if err != nil {
		out.Memory.Promised, out.CPU.Promised = unknownPromised(err), unknownPromised(err)
		return out
	}
	out.Memory.Promised, out.CPU.Promised = declared[0], declared[1]
	out.Memory.Promised.Sum.Other = unknown("other stacks are " + notRead)
	out.CPU.Promised.Sum.Other = unknown("other stacks are " + notRead)
	return out
}

// declaredOn sums what AppTown's settled deployments on a target
// applied, memory and CPUs: AppTown's share of a host it does not
// inspect.
func (s *Service) declaredOn(ctx context.Context, targetID ids.ID) ([2]Promised, error) {
	deps, err := s.Deployments.All(ctx)
	if err != nil {
		return [2]Promised{}, err
	}
	var settled []AppliedProfile
	for _, d := range deps {
		if d.Kind != Standing || d.TargetID != targetID {
			continue
		}
		if d.ObservedGeneration == 0 {
			// Nothing settled. An attempt still open may have applied its
			// limits there already: its share is unknown, not none.
			open, err := s.Outcomes.HasOpenAttempt(ctx, d.ID)
			if err != nil {
				return [2]Promised{}, err
			}
			if open {
				settled = append(settled, AppliedProfile{})
			}
			continue
		}
		applied, err := s.SettledApplied(ctx, &d.Deployment)
		if err != nil {
			return [2]Promised{}, err
		}
		settled = append(settled, applied)
	}
	return sumDeclared(settled), nil
}

// sumDeclared sums what each running generation applied, memory and CPUs.
// One with no record (released before limits were recorded, or a release
// still open, which may have applied) makes the sum unknown: its share is
// unknown, not none.
func sumDeclared(settled []AppliedProfile) [2]Promised {
	var mem, cpu Promised
	var memSum, cpuSum float64
	unrecorded := 0
	for _, applied := range settled {
		if !applied.Recorded() {
			unrecorded++
			continue
		}
		parts := make([]AppliedPart, 0, len(applied.Parts)+len(applied.Services))
		for _, p := range applied.Parts {
			parts = append(parts, p)
		}
		for _, p := range applied.Services {
			parts = append(parts, p)
		}
		for _, p := range parts {
			if p.Resources.MemoryBytes > 0 {
				memSum += float64(p.Resources.MemoryBytes)
				mem.Limited.add(true)
			} else {
				mem.Unlimited.add(true)
			}
			if p.Resources.CPUs > 0 {
				cpuSum += p.Resources.CPUs
				cpu.Limited.add(true)
			} else {
				cpu.Unlimited.add(true)
			}
		}
	}
	mem.Sum.AppTown, cpu.Sum.AppTown = known(memSum), known(cpuSum)
	if unrecorded > 0 {
		why := unknown(fmt.Sprintf("%d deployment(s) here hold limits with no record: released before limits were recorded (release them again), or a release still open", unrecorded))
		mem.Sum.AppTown, cpu.Sum.AppTown = why, why
	}
	return [2]Promised{mem, cpu}
}

// elasticCapacity is what each deployment on an App Platform target runs
// on, with each size's listed price, and their sum as an estimate. A
// managed database's size is what its cluster record names; its price is
// not listed by the API.
func (s *Service) elasticCapacity(ctx context.Context, t *Target) (TargetCapacity, error) {
	out := TargetCapacity{Elastic: true, Words: "Grows on demand: you pay for the size each part runs on."}
	deps, err := s.Deployments.All(ctx)
	if err != nil {
		return out, err
	}
	var prices map[string]float64
	var priceErr error
	var standing []ids.ID
	for _, d := range deps {
		if d.Kind != Standing || d.TargetID != t.ID {
			continue
		}
		standing = append(standing, d.ID)
		applied, err := s.SettledApplied(ctx, &d.Deployment)
		if err != nil {
			return out, err
		}
		if prices == nil && priceErr == nil {
			drv, err := s.providers.Driver(ctx, t, &d.Deployment)
			if pricer, ok := drv.(deploy.SizePricer); err == nil && ok {
				prices, priceErr = pricer.SizePrices(ctx)
			} else if err != nil {
				priceErr = err
			} else {
				priceErr = fmt.Errorf("the target lists no prices")
			}
		}
		names := make([]string, 0, len(applied.Parts))
		for n := range applied.Parts {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p := applied.Parts[n]
			if p.Need {
				continue // its size is the cluster's, below
			}
			ep := ElasticPart{Deployment: d.Project + "/" + d.Name, Part: n, RunsOn: p.RunsOn}
			slug := ""
			if f := strings.Fields(p.RunsOn); len(f) > 0 {
				slug = f[0]
			}
			switch {
			case p.RunsOn == "":
				ep.RunsOn, ep.USDPerMonth = "not recorded", unknown("the generation recorded no size")
			case priceErr != nil:
				ep.USDPerMonth = unknown(fmt.Sprintf("the size list cannot be read: %v", priceErr))
			default:
				if usd, ok := prices[slug]; ok {
					ep.USDPerMonth = known(usd)
				} else {
					ep.USDPerMonth = unknown("the size list does not name " + slug)
				}
			}
			out.Parts = append(out.Parts, ep)
		}
		if !applied.Recorded() {
			out.Parts = append(out.Parts, ElasticPart{Deployment: d.Project + "/" + d.Name, RunsOn: "not recorded", USDPerMonth: unknown("released before sizes were recorded; release it again")})
		}
	}
	if len(standing) > 0 {
		rows, err := deploymentsql.New(s.db.SQL()).NeedInstances(ctx, standing)
		if err != nil {
			return out, err
		}
		depName := map[ids.ID]string{}
		for _, d := range deps {
			depName[d.ID] = d.Project + "/" + d.Name
		}
		for _, r := range rows {
			var h struct {
				Size string `json:"size"`
			}
			var runsOn string
			if err := json.Unmarshal(r.HandleJson, &h); err != nil {
				runsOn = fmt.Sprintf("unknown: its record cannot be read (%v)", err)
			} else if runsOn = h.Size; runsOn == "" {
				runsOn = "not recorded"
			}
			out.Parts = append(out.Parts, ElasticPart{Deployment: depName[r.DeploymentID], Part: r.Need, Need: true, RunsOn: runsOn,
				USDPerMonth: unknown("the API lists no prices for managed databases")})
		}
	}
	sum, complete := 0.0, true
	for _, p := range out.Parts {
		if p.Need {
			continue
		}
		if !p.USDPerMonth.Known {
			complete = false
			continue
		}
		sum += p.USDPerMonth.Value
	}
	est := known(sum)
	switch {
	case !complete:
		est = Figure{Known: true, Value: sum, Reason: "partial: some sizes have no listed price; managed databases are not included"}
	default:
		est.Reason = "an estimate of the app components' listed prices; managed databases are not included"
	}
	out.Estimate = &est
	return out, nil
}

// HostShare is what a deployment holds of its fixed host: its applied
// limits and its use against the host's total, and the host's state.
type HostShare struct {
	Target        string `json:"target"`
	Elastic       bool   `json:"elastic"`
	HostMemory    Figure `json:"hostMemory"`
	Declared      Figure `json:"declared"` // the sum of its parts' and needs' memory limits; unknown when any has none
	Used          Figure `json:"used"`
	Overcommitted bool   `json:"overcommitted"`
	LowDisk       bool   `json:"lowDisk"`
	// DiskRefuses: the host's last disk reading was below the floor, so a
	// new deploy there is refused until space is freed.
	DiskRefuses bool `json:"diskRefuses"`
}

// share is a deployment's share of its target, from the target's
// reading and the deployment's own resources.
func (s *Service) share(ctx context.Context, dep *Deployment, parts []PartResource, applied AppliedProfile) *HostShare {
	if dep.TargetID.IsZero() {
		return nil
	}
	t, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		return nil
	}
	c, err := s.targetCapacity(ctx, t)
	if err != nil {
		return &HostShare{Target: t.Name, HostMemory: unknownErr(err)}
	}
	if c.Elastic || c.Memory == nil {
		return &HostShare{Target: t.Name, Elastic: c.Elastic}
	}
	out := &HostShare{Target: t.Name, HostMemory: c.Memory.Total, Overcommitted: c.Memory.Overcommitted, LowDisk: c.Disk != nil && c.Disk.Low, DiskRefuses: c.Disk != nil && c.Disk.Refuses}
	var declared, used float64
	declaredKnown, usedKnown := applied.Recorded(), len(parts) > 0
	for _, p := range applied.Parts {
		if p.Resources.MemoryBytes <= 0 {
			declaredKnown = false
		}
		declared += float64(p.Resources.MemoryBytes)
	}
	for _, p := range parts {
		if !p.Observed.Known {
			usedKnown = false
		}
		used += float64(p.Observed.UsedBytes)
	}
	out.Declared = unknown("a part or need has no limit")
	if !applied.Recorded() {
		out.Declared = unknown("not recorded")
	} else if declaredKnown {
		out.Declared = known(declared)
	}
	out.Used = unknown("not every part's use is known")
	if usedKnown {
		out.Used = known(used)
	}
	return out
}

// ToolVersions is the current tool versions snapshot, for a unit of work
// that runs AppTown's own images (a release build's static server): it
// takes one at its start and keeps it.
func (s *Service) ToolVersions() toolversions.Snapshot { return s.env.ToolVersions() }
