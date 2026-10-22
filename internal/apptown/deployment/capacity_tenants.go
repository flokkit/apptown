package deployment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/hostmem"
	"github.com/flokkit/apptown/internal/ids"
)

// Who holds a fixed host: each running container is counted once, under the
// deployment whose record names it (its public container, its stack's
// services), else by the class label AppTown put on it, else as software
// AppTown did not start. The target page draws these as its bars.

// The kinds of tenant, in the order a page lists the ones that are not a
// deployment.
const (
	TenantDeployment = "deployment" // a standing deployment's (or a restored copy's) parts and needs
	TenantPreviews   = "previews"   // every change's preview on the host
	TenantWork       = "work"       // steps, chats and their caches
	TenantAppTown    = "apptown"    // AppTown's own containers (its database); apptownd itself is in "the host itself" or its own container
	TenantUnrecorded = "unrecorded" // forge-labelled containers no record or known class names
	TenantOther      = "other"      // containers AppTown did not start
)

// Tenant is one holder of a fixed host's memory and CPUs: what its running
// containers use and are promised. A figure AppTown cannot read is
// unknown with the reason; a container with no limit is counted, never
// summed as zero.
type Tenant struct {
	Kind string `json:"kind"`
	// Name is "<project>/<deployment>" for a deployment, else what the
	// tenant is in words.
	Name       string `json:"name"`
	Project    string `json:"project,omitempty"`
	Deployment string `json:"deployment,omitempty"`
	Containers int    `json:"containers"`
	// Used is its containers' memory use now.
	Used Figure `json:"used"`
	// Promised is the sum of its containers' memory limits; Unlimited
	// counts those with none.
	Promised  Figure `json:"promised"`
	Unlimited int    `json:"unlimited"`
	// CPUs is the sum of its containers' CPU limits; CPUUnlimited counts
	// those with none, CPUUnread those with a limit not read as a number.
	CPUs         float64 `json:"cpus"`
	CPUUnlimited int     `json:"cpuUnlimited"`
	CPUUnread    int     `json:"cpuUnread"`
}

// workClasses are the classes of agent work: a step's container, a chat's
// and the containers a chat keeps beside it.
var workClasses = map[string]bool{"step": true, "console": true, "console-dind": true, "console-script": true, "console-cache": true}

// tenantOf names the tenant a container is counted under.
func tenantOf(l docker.ContainerLimit, owners containerOwners, deps map[ids.ID]ProjectDeployment) (kind, name string, dep *ProjectDeployment) {
	if p, ok := owners.of(l.Name); ok {
		if d, ok := deps[p.deployment]; ok {
			if d.Kind == Preview {
				return TenantPreviews, "previews", nil
			}
			return TenantDeployment, d.Project + "/" + d.Name, &d
		}
	}
	switch {
	case !l.AppTown:
		return TenantOther, "other containers", nil
	case l.Class == "preview" || l.Class == "service":
		return TenantPreviews, "previews", nil
	case workClasses[l.Class]:
		return TenantWork, "agent work", nil
	case l.Class == "infra":
		return TenantAppTown, "AppTown", nil
	}
	return TenantUnrecorded, "AppTown containers not on record", nil
}

// tenantsOf counts each running container once under its tenant. Use is
// known for a tenant only when every one of its containers was reported;
// use read for a container the inspection did not see (it started between
// the readings) leaves every tenant's use unknown, as AppTown/other split
// does. apptownd's own memory is in no tenant: on the host it is part of the
// host's own use, in a container it is that container's (CV-C01).
func tenantsOf(limits []docker.ContainerLimit, used map[string]int64, usedErr error, owners containerOwners, deps map[ids.ID]ProjectDeployment) []Tenant {
	byKey := map[string]*Tenant{}
	var order []string
	missing := map[string]string{} // tenant key → a container of it the daemon did not report
	inspected := map[string]bool{}
	for _, l := range limits {
		inspected[l.Name] = true
		kind, name, dep := tenantOf(l, owners, deps)
		key := kind + "\x00" + name
		t, ok := byKey[key]
		if !ok {
			t = &Tenant{Kind: kind, Name: name, Used: known(0), Promised: known(0)}
			if dep != nil {
				t.Project, t.Deployment = dep.Project, dep.Name
			}
			byKey[key] = t
			order = append(order, key)
		}
		t.Containers++
		if l.MemoryBytes > 0 {
			t.Promised.Value += float64(l.MemoryBytes)
		} else {
			t.Unlimited++
		}
		switch {
		case l.CPUUnread:
			t.CPUUnread++
		case l.CPUs > 0:
			t.CPUs += l.CPUs
		default:
			t.CPUUnlimited++
		}
		if usedErr == nil {
			if b, ok := used[l.Name]; ok {
				t.Used.Value += float64(b)
			} else if _, seen := missing[key]; !seen {
				missing[key] = l.Name
			}
		}
	}
	var late string
	if usedErr == nil {
		for name := range used {
			if !inspected[name] {
				late = name
				break
			}
		}
	}
	out := make([]Tenant, 0, len(order))
	for _, key := range order {
		t := byKey[key]
		switch {
		case usedErr != nil:
			t.Used = unknownErr(usedErr)
		case late != "":
			t.Used = unknown(fmt.Sprintf("%s started after the containers were read", late))
		case missing[key] != "":
			t.Used = unknown(fmt.Sprintf("the daemon did not report %s", missing[key]))
		}
		out = append(out, *t)
	}
	// Largest promise first, then largest use: the order the bars draw.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Promised.Value != out[j].Promised.Value {
			return out[i].Promised.Value > out[j].Promised.Value
		}
		return out[i].Used.Value > out[j].Used.Value
	})
	return out
}

// hostUse reads this host's own memory, for the built-in target only and
// only when the daemon reads back a marker apptownd wrote (sameMachine) and
// both see the same memory total. That witnesses that the daemon sees
// apptownd's files at the same path and the same total, and apptownd's
// /proc/meminfo is then taken as the daemon machine's: an AppTown in a
// container reads the kernel's figures (unless they are virtualised, when
// the totals differ). The case it cannot tell is two machines of equal
// memory sharing apptownd's temp folder over a network filesystem, accepted
// in the plan. Otherwise unknown with the reason.
func (s *Service) hostUse(ctx context.Context, t *Target, dk docker.Scope, daemonTotal int64) Figure {
	if t.Origin != OriginBuiltIn {
		return unknown("the host's own use is not read on an attached host in 1.0")
	}
	if same, why := s.sameMachine(ctx, t.ID, dk); !same {
		return unknown(why)
	}
	read := s.capacity.host
	if read == nil {
		read = hostmem.Read
	}
	m, err := read()
	if err != nil {
		return unknownErr(err)
	}
	if m.TotalBytes != daemonTotal {
		return unknown(fmt.Sprintf("this AppTown sees %d bytes of memory and the daemon %d", m.TotalBytes, daemonTotal))
	}
	return known(float64(m.UsedBytes()))
}

// sameMachine says whether the daemon sees apptownd's filesystem: apptownd
// writes a random marker into a file of its own temp folder, and the
// daemon mounts that path into a throwaway container and prints it. A
// remote daemon has no such path (its mount is refused); a apptownd in a
// container wrote the marker where the daemon does not look. Read at most
// every diskFresh, as the disk is.
func (s *Service) sameMachine(ctx context.Context, targetID ids.ID, dk docker.Scope) (bool, string) {
	now := s.now()
	s.capacity.mu.Lock()
	if w, ok := s.capacity.witness[targetID]; ok && now.Sub(w.at) < diskFresh {
		s.capacity.mu.Unlock()
		return w.same, w.why
	}
	path, mark, err := s.capacity.marker()
	s.capacity.mu.Unlock()
	same, why := false, ""
	switch read, rerr := docker.ReadMark(ctx, dk, path, mark); {
	case err != nil:
		why = "AppTown could not write its marker: " + err.Error()
	case rerr != nil:
		why = "the daemon does not run where this AppTown does, as far as AppTown can witness (it could not read AppTown's marker)"
	case !read:
		why = "the daemon does not run where this AppTown does, as far as AppTown can witness (it read a different marker)"
	default:
		same = true
	}
	s.capacity.mu.Lock()
	if s.capacity.witness == nil {
		s.capacity.witness = map[ids.ID]witnessReading{}
	}
	s.capacity.witness[targetID] = witnessReading{at: now, same: same, why: why}
	s.capacity.mu.Unlock()
	return same, why
}

type witnessReading struct {
	at   time.Time
	same bool
	why  string
}

// marker is the file apptownd's marker is in, written once; the caller holds mu.
func (c *capacityCache) marker() (string, string, error) {
	if c.markPath != "" {
		return c.markPath, c.mark, nil
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}
	f, err := os.CreateTemp(c.markDir, "apptown-host-mark-*")
	if err != nil {
		return "", "", err
	}
	mark := hex.EncodeToString(b[:])
	_, werr := f.WriteString(mark)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", "", werr
	}
	c.markPath, c.mark = f.Name(), mark
	return c.markPath, c.mark, nil
}

// dockerTenants is who holds a Docker host, from the readings capacity
// already made.
func (s *Service) dockerTenants(ctx context.Context, t *Target, limits []docker.ContainerLimit, used map[string]int64, usedErr error) ([]Tenant, error) {
	deps, err := s.Deployments.OnTarget(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	owners, err := s.ownersOn(ctx, deps)
	if err != nil {
		return nil, err
	}
	byID := make(map[ids.ID]ProjectDeployment, len(deps))
	for _, d := range deps {
		byID[d.ID] = d
	}
	return tenantsOf(limits, used, usedErr, owners, byID), nil
}
