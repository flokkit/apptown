package deployment

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/ids"
)

// Who holds a fixed host: each running container is counted once, under the
// deployment whose record names it (its public container, its stack's
// services), else by the class label AppTown put on it, else as software
// AppTown did not start. The target page draws these as its bars.

// The kinds of tenant, in the order a page lists the ones that are not a
// deployment.
const (
	tenantDeployment = "deployment" // a standing deployment's (or a restored copy's) parts and needs
	tenantPreviews   = "previews"   // every change's preview on the host
	tenantWork       = "work"       // steps, chats and their caches
	tenantAppTown    = "apptown"    // AppTown's own containers (its database); apptownd itself is in "the host itself" or its own container
	tenantUnrecorded = "unrecorded" // forge-labelled containers no record or known class names
	tenantOther      = "other"      // containers AppTown did not start
)

// Tenant is one holder of a fixed host's memory and CPUs: what its running
// containers use and are promised. A figure AppTown cannot read is
// unknown with the reason; a container with no limit is counted, never
// summed as zero.
type Tenant struct {
	ReadAt  time.Time `json:"readAt,omitzero"`
	Stale   bool      `json:"stale,omitempty"`
	Unknown bool      `json:"unknown,omitempty"`
	Kind    string    `json:"kind"`
	// Name is "<project>/<deployment>" for a deployment, else what the
	// tenant is in words.
	Name       string `json:"name"`
	Project    string `json:"project,omitempty"`
	Deployment string `json:"deployment,omitempty"`
	Containers int    `json:"containers"`
	// Used is its containers' memory use now, CPUUsed their CPU use in
	// cores; both come from one read, so they are known or unknown together.
	Used    Figure `json:"used"`
	CPUUsed Figure `json:"cpuUsed"`
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
	p, ok := owners.of(l.Name)
	if !ok {
		p, ok = owners.stackOf(l)
	}
	if ok {
		if d, ok := deps[p.deployment]; ok {
			if d.Kind == preview {
				return tenantPreviews, "previews", nil
			}
			return tenantDeployment, d.Project + "/" + d.DisplayLabel, &d
		}
	}
	switch {
	case !l.AppTown:
		return tenantOther, "other containers", nil
	case l.Class == "preview" || l.Class == "service":
		return tenantPreviews, "previews", nil
	case workClasses[l.Class]:
		return tenantWork, "agent work", nil
	case l.Class == "infra" || l.Class == diskspace.ReaderClass:
		return tenantAppTown, "AppTown", nil
	}
	return tenantUnrecorded, "AppTown containers not on record", nil
}

// tenantsOf counts each running container once under its tenant. Use is
// known for a tenant only when every one of its containers was reported;
// use read for a container the inspection did not see (it started between
// the readings) leaves every tenant's use unknown, as AppTown/other split
// does. apptownd's own memory is in no tenant: on the host it is part of the
// host's own use, in a container it is that container's (CV-C01).
func tenantsOf(limits []docker.ContainerLimit, used map[string]docker.Usage, usedErr error, owners containerOwners, deps map[ids.ID]ProjectDeployment) []Tenant {
	byKey := map[string]*Tenant{}
	var order []string
	missing := map[string]string{} // tenant key → a container of it the daemon did not report
	inspected := map[string]bool{}
	for _, l := range limits {
		inspected[l.Name] = true
		kind, name, dep := tenantOf(l, owners, deps)
		key := kind + "\x00" + name
		if dep != nil {
			key = kind + "\x00" + dep.ID.String()
		}
		t, ok := byKey[key]
		if !ok {
			t = &Tenant{Kind: kind, Name: name, Used: known(0), CPUUsed: known(0), Promised: known(0)}
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
				t.Used.Value += float64(b.MemoryBytes)
				t.CPUUsed.Value += b.CPUCores
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
		if !t.Used.Known {
			t.CPUUsed = t.Used
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

// ownersOnTarget is which standing deployment on t each container is,
// by the names and stacks AppTown's records give them.
func (s *Service) ownersOnTarget(ctx context.Context, t *Target) (containerOwners, map[ids.ID]ProjectDeployment, error) {
	deps, err := s.Deployments.OnTarget(ctx, t.ID)
	if err != nil {
		return containerOwners{}, nil, err
	}
	owners, err := s.ownersOn(ctx, deps)
	if err != nil {
		return containerOwners{}, nil, err
	}
	byID := make(map[ids.ID]ProjectDeployment, len(deps))
	for _, d := range deps {
		byID[d.ID] = d
	}
	return owners, byID, nil
}
