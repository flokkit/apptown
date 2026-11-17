package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// Target capacity: how full a target is. A fixed host (a Docker host, a
// Portainer endpoint, read through its Docker proxy) has a total, and the
// limits and use of what runs on it; an elastic one (App Platform) grows, and shows instead what each
// deployment runs on and an estimate of what it costs. A figure AppTown
// cannot read is unknown with the reason, never zero.

// Figure is one reading: its value, or why it is unknown.
type Figure struct {
	Known  bool      `json:"known"`
	Value  float64   `json:"value"`
	Reason string    `json:"reason,omitempty"`
	ReadAt time.Time `json:"readAt,omitzero"`
	Stale  bool      `json:"stale,omitempty"`
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
	// HostUsed is the whole host's memory or CPU use, including the OS
	// and processes outside containers: known on any Docker host
	// when the reader and daemon report the same total (hostUse).
	HostUsed      *Figure `json:"hostUsed,omitempty"`
	Overcommitted bool    `json:"overcommitted"`
}

// DiskCapacity is the filesystem holding a fixed host's Docker data.
type DiskCapacity struct {
	Total Figure `json:"total"`
	Free  Figure `json:"free"`
	// Low reports pressure below the shared disk warning threshold.
	Low     bool  `json:"low"`
	Warning int64 `json:"warning,omitempty"`
	// Refuses says less than Floor is free: a new release or preview on
	// this host is refused until space is freed. It is read again when one
	// starts (docker.AdmitDisk, on a Docker host and through Portainer);
	// this reading may be minutes old (ReadAt).
	Refuses bool  `json:"refuses"`
	Floor   int64 `json:"floor,omitempty"`
	// Path is the folder whose filesystem this is, on the host: the
	// daemon's data folder.
	Path   string    `json:"path,omitempty"`
	ReadAt time.Time `json:"readAt,omitzero"`
}

// Unread is what a person is told of a disk AppTown could not read: that
// it cannot, and why, in the figure's own words. It never says a check
// failed: when no check could run, the reason says so.
func (d DiskCapacity) Unread() string {
	why := d.Free.Reason
	if why == "" {
		why = d.Total.Reason
	}
	if why == "" {
		why = "no reason was recorded"
	}
	return "AppTown cannot read this host's disk: " + strings.TrimSuffix(why, ".") + "."
}

// StaleAt judges the disk's own evidence independently of the wrapper time.
func (d DiskCapacity) StaleAt(now time.Time) bool {
	return d.ReadAt.IsZero() || now.Sub(d.ReadAt) > diskObservationFresh
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
	// FixedHost says the target is one machine with a CPU, memory and disk
	// to fill (a Docker host, a Portainer endpoint); another target has none
	// of them, so they are not unknown there but absent. Set from the
	// target's kind on every answer, the first check's too.
	FixedHost bool `json:"fixedHost"`
	// Words is the one sentence the view leads with.
	Error       string            `json:"error,omitempty"`
	Stale       bool              `json:"stale"`
	LiveUnknown bool              `json:"liveUnknown,omitempty"`
	Words       string            `json:"words"`
	Memory      *ResourceCapacity `json:"memory,omitempty"`
	CPU         *ResourceCapacity `json:"cpu,omitempty"`
	Disk        *DiskCapacity     `json:"disk,omitempty"`
	// Tenants, on a fixed host, is who holds it: each running container
	// counted once (capacity_tenants.go).
	Tenants   []Tenant  `json:"tenants,omitempty"`
	TenantsAt time.Time `json:"tenantsAt,omitzero"`
	// Parts, on an elastic target, is what each deployment runs on, and
	// Estimate the monthly sum of the app components' listed prices.
	Parts    []ElasticPart `json:"parts,omitempty"`
	Estimate *Figure       `json:"estimate,omitempty"`
	ReadAt   time.Time     `json:"readAt,omitzero"`
}

// fixedHostKind says whether a target of kind is one machine whose CPU,
// memory and disk AppTown reads (fixedHostCapacity).
func fixedHostKind(kind string) bool { return kind == KindDockerHost || kind == KindPortainer }

// elasticKind says whether a target of kind grows on demand (elasticCapacity).
func elasticKind(kind string) bool {
	switch kind {
	case KindDOApp, kindFlyMachines, KindAWSECS:
		return true
	}
	return false
}

// Disk display freshness does not govern docker.AdmitDisk.
const diskObservationFresh = 15 * time.Minute

// Capacity reads how full target name is; the caller holds the deployment
// read grant (the target page's).
func (s *Service) Capacity(ctx context.Context, name string) (TargetCapacity, error) {
	t, err := s.Targets.ByName(ctx, name)
	if err != nil {
		return TargetCapacity{}, err
	}
	return s.recordedCapacity(ctx, t)
}

func (s *Service) targetCapacity(ctx context.Context, t *Target) (TargetCapacity, error) {
	var out TargetCapacity
	var err error
	switch t.Kind {
	case KindDockerHost:
		return TargetCapacity{}, fmt.Errorf("Docker capacity is collected by the host reader loop")
	case KindPortainer:
		out, err = s.portainerCapacity(ctx, t)
	case KindDOApp, kindFlyMachines, KindAWSECS:
		out, err = s.elasticCapacity(ctx, t)
	default:
		out = TargetCapacity{Words: "Serves files; no capacity to manage."}
	}
	if err != nil {
		return TargetCapacity{}, err
	}
	// A CPU reading spans a sampling interval. Stamp the completed check,
	// so retaining its figures cannot advance its age by that interval later.
	out.Target, out.Kind, out.ReadAt = t.Name, t.Kind, s.now()
	return out, nil
}

// portainerCapacity reads a Portainer endpoint as a Docker host is read,
// through the endpoint's Docker proxy (Providers.PortainerDataScope).
func (s *Service) portainerCapacity(ctx context.Context, t *Target) (TargetCapacity, error) {
	dk, err := s.providers.PortainerDataScope(ctx, t)
	dk.Now = s.now
	var facts docker.HostFacts
	if err == nil {
		facts, _ = docker.ReadHost(ctx, dk, docker.HostQuery{Host: true, Containers: true, Use: true, ReaderImage: s.env.ToolVersions().Images.DF})
	} else {
		facts.Host.Reason = fmt.Sprintf("its daemon cannot be asked: %v", err)
	}
	return s.fixedHostCapacity(ctx, t, facts, "A fixed host behind Portainer: it does not grow.")
}

// fixedHostCapacity computes capacity from the same facts for laps and Portainer.
func (s *Service) fixedHostCapacity(ctx context.Context, t *Target, facts docker.HostFacts, words string) (TargetCapacity, error) {
	out := TargetCapacity{Words: words, Memory: &ResourceCapacity{}, CPU: &ResourceCapacity{}, ReadAt: s.now(), Target: t.Name, Kind: t.Kind}
	var memory int64
	var cpus int
	var root string
	if facts.Host.Value == nil {
		out.Memory.Total, out.CPU.Total = unknown(facts.Host.Reason), unknown(facts.Host.Reason)
	} else {
		h := facts.Host.Value
		memory, cpus, root = h.MemTotal, h.NCPU, h.DockerRootDir
		out.Memory.Total, out.CPU.Total = known(float64(memory)), known(float64(cpus))
	}
	var limits []docker.ContainerLimit
	var limitsErr error
	if facts.Containers.Value == nil {
		limitsErr = fmt.Errorf("container limits could not be read: %s", facts.Containers.Reason)
	} else {
		for _, c := range *facts.Containers.Value {
			if c.State.Running {
				limits = append(limits, c.Limit())
			}
		}
	}
	var err error
	var owners containerOwners
	var deps map[ids.ID]ProjectDeployment
	if limitsErr == nil {
		if owners, deps, err = s.ownersOnTarget(ctx, t); err != nil {
			return out, err
		}
		// A Portainer stack's containers carry no class label: those of a
		// stack AppTown's record names are AppTown's.
		for i := range limits {
			if _, ok := owners.stackOf(limits[i]); ok {
				limits[i].AppTown = true
			}
		}
	}
	if limitsErr != nil {
		out.Memory.Promised = unknownPromised(limitsErr)
		out.CPU.Promised = unknownPromised(limitsErr)
	} else {
		out.Memory.Promised, out.CPU.Promised = promisedOf(limits)
	}
	var used map[string]docker.Usage
	var usedErr, kernelErr error
	var parts map[string][]byte
	if facts.Use.Value == nil {
		usedErr = fmt.Errorf("container use was not read")
		kernelErr = usedErr
	} else {
		u := facts.Use.Value
		used = u.Containers
		if u.ContainersReason != "" {
			usedErr = fmt.Errorf("%s", u.ContainersReason)
		}
		parts = u.Kernel
		if u.KernelReason != "" {
			kernelErr = fmt.Errorf("%s", u.KernelReason)
		}
	}
	out.Memory.InUse = inUseOf(limits, limitsErr, used, usedErr)
	out.Memory.HostUsed, out.CPU.HostUsed = hostUse(parts, kernelErr, memory, cpus)

	if limitsErr == nil {
		out.Tenants = tenantsOf(limits, used, usedErr, owners, deps)
		out.TenantsAt = s.now()
	}
	overcommit(out.Memory)
	overcommit(out.CPU)
	diskReason := facts.Host.Reason
	if diskReason == "" {
		diskReason = "the data-root disk was not read"
	}
	out.Disk = &DiskCapacity{Path: root, Total: unknown(diskReason), Free: unknown(diskReason)}
	if facts.Use.Value != nil {
		u := facts.Use.Value
		if u.DiskReason == "" {
			v := u.Disk
			out.Disk = &DiskCapacity{Path: root, ReadAt: facts.Use.ObservedAt, Total: known(float64(v.TotalBytes)), Free: known(float64(v.FreeBytes)), Low: v.Low(), Refuses: v.Refuses(), Floor: v.Floor(), Warning: v.Warning()}
		} else {
			out.Disk.Total, out.Disk.Free = unknown(u.DiskReason), unknown(u.DiskReason)
		}
	}

	return out, nil
}

// inUseOf is what the running containers use, AppTown's and the rest:
// known only when every container's owner was read. Use read without that
// (the inspection failed, or a container started between the readings) is
// not split by a guess.
func inUseOf(limits []docker.ContainerLimit, limitsErr error, used map[string]docker.Usage, err error) *Split {
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
			f += float64(b.MemoryBytes)
		} else {
			o += float64(b.MemoryBytes)
		}
	}
	return &Split{AppTown: known(f), Other: known(o)}
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
			ep := ElasticPart{Deployment: d.Project + "/" + d.DisplayLabel, Part: n, RunsOn: p.RunsOn}
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
			out.Parts = append(out.Parts, ElasticPart{Deployment: d.Project + "/" + d.DisplayLabel, RunsOn: "not recorded", USDPerMonth: unknown("released before sizes were recorded; release it again")})
		}
	}
	if len(standing) > 0 {
		rows, err := deploymentsql.New(s.db.SQL()).NeedInstances(ctx, standing)
		if err != nil {
			return out, err
		}
		depName := map[ids.ID]string{}
		for _, d := range deps {
			depName[d.ID] = d.Project + "/" + d.DisplayLabel
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
	if priceErr != nil {
		est = unknown("The capacity price check failed.")
		out.Error = "The capacity price check failed."
	}
	out.Estimate = &est
	return out, nil
}

// HostShare is what a deployment holds of its fixed host: its applied
// limits and its use against the host's total, and the host's state.
type HostShare struct {
	ReadAt        time.Time `json:"readAt,omitzero"`
	Stale         bool      `json:"stale,omitempty"`
	Error         string    `json:"error,omitempty"`
	Target        string    `json:"target"`
	Elastic       bool      `json:"elastic"`
	Limits        *Figure   `json:"limits,omitempty"` // every container's memory limit on the host summed, AppTown's and others'; Docker reserves none of it
	HostMemory    Figure    `json:"hostMemory"`
	Declared      Figure    `json:"declared"` // the sum of its parts' and needs' memory limits; unknown when any has none
	Used          Figure    `json:"used"`
	Overcommitted bool      `json:"overcommitted"`
	LowDisk       bool      `json:"lowDisk"`
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
	c, err := s.recordedCapacity(ctx, t)
	if err != nil {
		return &HostShare{Target: t.Name, Error: err.Error(), Stale: true}
	}
	if c.Elastic || c.Memory == nil {
		return &HostShare{Target: t.Name, Elastic: c.Elastic, ReadAt: c.ReadAt, Stale: c.Stale, Error: c.Error}
	}
	out := &HostShare{ReadAt: c.ReadAt, Stale: c.Stale, Error: c.Error, Target: t.Name, HostMemory: c.Memory.Total, Overcommitted: c.Memory.Overcommitted, LowDisk: c.Disk != nil && c.Disk.Low, DiskRefuses: c.Disk != nil && c.Disk.Refuses}
	if c.Memory.Promised.Sum.AppTown.Known && c.Memory.Promised.Sum.Other.Known {
		out.Limits = &Figure{Known: true, Value: c.Memory.Promised.Sum.AppTown.Value + c.Memory.Promised.Sum.Other.Value}
	}

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
