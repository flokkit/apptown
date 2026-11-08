package deployment

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/diskspace"
	"github.com/flokkit/apptown/internal/ids"
)

func tenantNamed(ts []Tenant, name string) *Tenant {
	for i := range ts {
		if ts[i].Name == name {
			return &ts[i]
		}
	}
	return nil
}

// Each running container is counted once: under the deployment whose
// record names it (its public container and its stack's services), else by
// the class AppTown gave it (a preview, agent work, AppTown's own
// database), else as software AppTown did not start; an AppTown-labelled
// container no record names is its own tenant, not "other". A container
// with no limit is counted, never summed as zero.
//
// Kills: attribution by label alone (the shop's need lands in
// "unrecorded"); an unlimited container summed as zero promise; a preview
// record counted as a deployment.
func TestEveryRunningContainerIsCountedOnceUnderWhoHoldsIt(t *testing.T) {
	t.Parallel()
	shop, pv := ids.New(time.Unix(1, 0)), ids.New(time.Unix(2, 0))
	deps := map[ids.ID]ProjectDeployment{
		shop: {Deployment: Deployment{ID: shop, Name: "main", DisplayLabel: "main", Kind: Standing}, Project: "shop"},
		pv:   {Deployment: Deployment{ID: pv, Name: "preview-7", Kind: preview}, Project: "shop"},
	}
	owners := containerOwners{
		exact:    map[string]partOf{"apptown-env-shop-main": {deployment: shop, part: "web"}, "apptown-preview-7": {deployment: pv, part: "web"}},
		services: map[string]ids.ID{docker.Current.Service("env-shop-main", ""): shop},
	}
	limits := []docker.ContainerLimit{
		{Name: "apptown-env-shop-main", MemoryBytes: 256 << 20, CPUs: 1, AppTown: true, Class: "env"},
		{Name: docker.Current.Service("env-shop-main", "postgres"), AppTown: true, Class: "env"},
		{Name: "apptown-preview-7", MemoryBytes: 128 << 20, AppTown: true, Class: "preview"},
		{Name: "apptown-svc-pv-redis", MemoryBytes: 64 << 20, AppTown: true, Class: "service"},
		{Name: "apptown-step-1", MemoryBytes: 2 << 30, CPUs: 2, AppTown: true, Class: "step"},
		{Name: "apptown-console-a", AppTown: true, Class: "console", CPUUnread: true},
		{Name: "forge-db", AppTown: true, Class: "infra"},
		{Name: "apptown-env-gone-main", MemoryBytes: 64 << 20, AppTown: true, Class: "env"},
		{Name: "traefik", MemoryBytes: 256 << 20},
		{Name: "apptown-reader-disk-reader-0", AppTown: true, Class: diskspace.ReaderClass},
	}
	used := map[string]int64{}
	for i, l := range limits {
		used[l.Name] = int64(i+1) << 20
	}
	ts := tenantsOf(limits, memoryUse(used), nil, owners, deps)

	want := map[string]struct {
		kind               string
		containers         int
		promised           float64
		unlimited          int
		used               float64
		cpus               float64
		cpuUnread, cpuNone int
	}{
		"shop/main":                        {tenantDeployment, 2, 256 << 20, 1, (1 + 2) << 20, 1, 0, 1},
		"previews":                         {tenantPreviews, 2, 192 << 20, 0, (3 + 4) << 20, 0, 0, 2},
		"agent work":                       {tenantWork, 2, 2 << 30, 1, (5 + 6) << 20, 2, 1, 0},
		"AppTown":                          {tenantAppTown, 2, 0, 2, (7 + 10) << 20, 0, 0, 2},
		"AppTown containers not on record": {tenantUnrecorded, 1, 64 << 20, 0, 8 << 20, 0, 0, 1},
		"other containers":                 {tenantOther, 1, 256 << 20, 0, 9 << 20, 0, 0, 1},
	}
	if len(ts) != len(want) {
		t.Fatalf("%d tenants, want %d: %+v", len(ts), len(want), ts)
	}
	total := 0
	for name, w := range want {
		got := tenantNamed(ts, name)
		if got == nil {
			t.Errorf("no tenant %q", name)
			continue
		}
		total += got.Containers
		if got.Kind != w.kind || got.Containers != w.containers || got.Promised.Value != w.promised || !got.Promised.Known ||
			got.Unlimited != w.unlimited || got.Used.Value != w.used || !got.Used.Known || got.CPUs != w.cpus ||
			got.CPUUnread != w.cpuUnread || got.CPUUnlimited != w.cpuNone {
			t.Errorf("%s: %+v, want %+v", name, *got, w)
		}
	}
	if total != len(limits) {
		t.Errorf("%d containers counted of %d", total, len(limits))
	}
	if d := tenantNamed(ts, "shop/main"); d == nil || d.Project != "shop" || d.Deployment != "main" {
		t.Errorf("the deployment's tenant does not link to it: %+v", d)
	}
	// Largest promise first.
	if ts[0].Name != "agent work" {
		t.Errorf("order: %s first", ts[0].Name)
	}
}

// Use is known for a tenant only when every one of its containers was
// reported; a container read only by stats (it started between the
// readings) leaves every tenant's use unknown; a stats failure is every
// tenant's reason.
//
// Kills: a tenant summed without its unreported container; the late
// container's use dropped silently.
func TestATenantsUseIsUnknownWhenAnyOfItIsUnread(t *testing.T) {
	t.Parallel()
	limits := []docker.ContainerLimit{
		{Name: "a", AppTown: true, Class: "step"}, {Name: "b", AppTown: true, Class: "step"},
		{Name: "traefik"}, {Name: "forge-db", AppTown: true, Class: "infra"},
	}
	none := containerOwners{exact: map[string]partOf{}, services: map[string]ids.ID{}}
	ts := tenantsOf(limits, memoryUse(map[string]int64{"a": 1, "traefik": 1, "forge-db": 1}), nil, none, nil)
	if w := tenantNamed(ts, "agent work"); w.Used.Known || !strings.Contains(w.Used.Reason, "did not report b") {
		t.Errorf("agent work: %+v", w.Used)
	}
	if o := tenantNamed(ts, "other containers"); !o.Used.Known || o.Used.Value != 1 {
		t.Errorf("other: %+v", o.Used)
	}

	all := map[string]int64{"a": 1, "b": 1, "traefik": 1, "forge-db": 1, "late": 5}
	for _, tn := range tenantsOf(limits, memoryUse(all), nil, none, nil) {
		if tn.Used.Known || !strings.Contains(tn.Used.Reason, "late started after") {
			t.Errorf("%s with a late container: %+v", tn.Name, tn.Used)
		}
	}
	for _, tn := range tenantsOf(limits, nil, errors.New("docker stats: exit 1"), none, nil) {
		if tn.Used.Known || !strings.Contains(tn.Used.Reason, "docker stats") {
			t.Errorf("%s with stats failing: %+v", tn.Name, tn.Used)
		}
	}
}

// apptownd's own memory is counted once wherever it runs (CV-C01): no tenant
// adds it, so on the host it is part of the host's own use and in a
// container it is that container's. AppTown's tenant is its own containers.
//
// Kills: apptownd's memory added to AppTown on top of the container that
// holds it.
func TestApptowndsMemoryIsCountedOnceWhereverItRuns(t *testing.T) {
	t.Parallel()
	none := containerOwners{exact: map[string]partOf{}, services: map[string]ids.ID{}}
	limits := []docker.ContainerLimit{{Name: "apptownd-in-a-container"}, {Name: "forge-db", AppTown: true, Class: "infra"}}
	ts := tenantsOf(limits, memoryUse(map[string]int64{"apptownd-in-a-container": 400 << 20, "forge-db": 100 << 20}), nil, none, nil)
	var sum float64
	for _, tn := range ts {
		sum += tn.Used.Value
	}
	if f := tenantNamed(ts, "AppTown"); f == nil || f.Used.Value != 100<<20 || sum != 500<<20 {
		t.Errorf("apptownd counted twice or lost: %+v (sum %v)", ts, sum)
	}
}

func TestCapacityNamesADeploymentByItsDisplayLabel(t *testing.T) {
	id := ids.New(time.Unix(702, 0))
	owners := containerOwners{exact: map[string]partOf{"app": {deployment: id}}}
	deps := map[ids.ID]ProjectDeployment{id: {Deployment: Deployment{ID: id, Name: "main", DisplayLabel: "Production", Kind: Standing}, Project: "shop"}}
	kind, name, dep := tenantOf(docker.ContainerLimit{Name: "app"}, owners, deps)
	if kind != tenantDeployment || name != "shop/Production" || dep.Name != "main" {
		t.Fatalf("tenant identity changed or name is not displayed: %s %s %+v", kind, name, dep)
	}
}

func TestCapacityKeepsDeploymentsWithTheSameDisplayNameSeparate(t *testing.T) {
	a, b := ids.New(time.Unix(703, 0)), ids.New(time.Unix(704, 0))
	owners := containerOwners{exact: map[string]partOf{"a": {deployment: a}, "b": {deployment: b}}}
	deps := map[ids.ID]ProjectDeployment{
		a: {Deployment: Deployment{ID: a, Name: "east", DisplayLabel: "Office", Kind: Standing}, Project: "shop"},
		b: {Deployment: Deployment{ID: b, Name: "west", DisplayLabel: "Office", Kind: Standing}, Project: "shop"},
	}
	tenants := tenantsOf([]docker.ContainerLimit{{Name: "a"}, {Name: "b"}}, memoryUse(map[string]int64{"a": 1, "b": 2}), nil, owners, deps)
	if len(tenants) != 2 || tenants[0].Name != "shop/Office" || tenants[1].Name != "shop/Office" || tenants[0].Deployment == tenants[1].Deployment {
		t.Fatalf("display names merged identities: %+v", tenants)
	}
}

// memoryUse is a reading of containers' memory alone, each using no CPU.
func memoryUse(bytes map[string]int64) map[string]docker.Usage {
	out := make(map[string]docker.Usage, len(bytes))
	for name, b := range bytes {
		out[name] = docker.Usage{MemoryBytes: b}
	}
	return out
}

// A deployment's CPU use is its containers' together, read with their memory;
// when the memory reading is unknown so is the CPU, never zero.
func TestATenantsCPUUseIsItsContainersTogether(t *testing.T) {
	t.Parallel()
	limits := []docker.ContainerLimit{{Name: "web"}, {Name: "worker"}}
	ts := tenantsOf(limits, map[string]docker.Usage{"web": {MemoryBytes: 1, CPUCores: 0.25}, "worker": {MemoryBytes: 1, CPUCores: 0.5}}, nil, containerOwners{}, nil)
	if len(ts) != 1 || !ts[0].CPUUsed.Known || ts[0].CPUUsed.Value != 0.75 {
		t.Fatalf("tenants: %+v", ts)
	}
	ts = tenantsOf(limits, map[string]docker.Usage{"web": {MemoryBytes: 1, CPUCores: 0.25}}, nil, containerOwners{}, nil)
	if ts[0].CPUUsed.Known || ts[0].CPUUsed.Reason == "" {
		t.Fatalf("a container the daemon did not report left CPU known: %+v", ts[0].CPUUsed)
	}
}
