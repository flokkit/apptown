package deployment

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/hostmem"
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
		shop: {Deployment: Deployment{ID: shop, Name: "main", Kind: Standing}, Project: "shop"},
		pv:   {Deployment: Deployment{ID: pv, Name: "preview-7", Kind: Preview}, Project: "shop"},
	}
	owners := containerOwners{
		exact:    map[string]partOf{"apptown-env-shop-main": {deployment: shop, part: "web"}, "apptown-preview-7": {deployment: pv, part: "web"}},
		services: map[string]ids.ID{docker.BeforeKey(docker.Current, "shop", "main").Service(""): shop},
	}
	limits := []docker.ContainerLimit{
		{Name: "apptown-env-shop-main", MemoryBytes: 256 << 20, CPUs: 1, AppTown: true, Class: "env"},
		{Name: docker.BeforeKey(docker.Current, "shop", "main").Service("postgres"), AppTown: true, Class: "env"},
		{Name: "apptown-preview-7", MemoryBytes: 128 << 20, AppTown: true, Class: "preview"},
		{Name: "apptown-svc-pv-redis", MemoryBytes: 64 << 20, AppTown: true, Class: "service"},
		{Name: "apptown-step-1", MemoryBytes: 2 << 30, CPUs: 2, AppTown: true, Class: "step"},
		{Name: "apptown-console-a", AppTown: true, Class: "console", CPUUnread: true},
		{Name: "forge-db", AppTown: true, Class: "infra"},
		{Name: "apptown-env-gone-main", MemoryBytes: 64 << 20, AppTown: true, Class: "env"},
		{Name: "traefik", MemoryBytes: 256 << 20},
	}
	used := map[string]int64{}
	for i, l := range limits {
		used[l.Name] = int64(i+1) << 20
	}
	ts := tenantsOf(limits, used, nil, owners, deps)

	want := map[string]struct {
		kind               string
		containers         int
		promised           float64
		unlimited          int
		used               float64
		cpus               float64
		cpuUnread, cpuNone int
	}{
		"shop/main":                        {TenantDeployment, 2, 256 << 20, 1, (1 + 2) << 20, 1, 0, 1},
		"previews":                         {TenantPreviews, 2, 192 << 20, 0, (3 + 4) << 20, 0, 0, 2},
		"agent work":                       {TenantWork, 2, 2 << 30, 1, (5 + 6) << 20, 2, 1, 0},
		"AppTown":                          {TenantAppTown, 1, 0, 1, 7 << 20, 0, 0, 1},
		"AppTown containers not on record": {TenantUnrecorded, 1, 64 << 20, 0, 8 << 20, 0, 0, 1},
		"other containers":                 {TenantOther, 1, 256 << 20, 0, 9 << 20, 0, 0, 1},
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
	ts := tenantsOf(limits, map[string]int64{"a": 1, "traefik": 1, "forge-db": 1}, nil, none, nil)
	if w := tenantNamed(ts, "agent work"); w.Used.Known || !strings.Contains(w.Used.Reason, "did not report b") {
		t.Errorf("agent work: %+v", w.Used)
	}
	if o := tenantNamed(ts, "other containers"); !o.Used.Known || o.Used.Value != 1 {
		t.Errorf("other: %+v", o.Used)
	}

	all := map[string]int64{"a": 1, "b": 1, "traefik": 1, "forge-db": 1, "late": 5}
	for _, tn := range tenantsOf(limits, all, nil, none, nil) {
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

// The host's own use is known only on the built-in host, and only when the
// daemon reads back the marker apptownd wrote and both see the same memory
// total. Equal totals without the marker (a same-sized daemon elsewhere),
// the marker with unequal totals (a virtualised view), an attached host and
// an unreadable /proc each leave it unknown with the reason.
//
// Kills: host use from equal totals alone; from the marker alone; the
// witness re-read on every view.
func TestTheHostsOwnUseIsKnownOnlyWhereTheDaemonReadsAppTownsMarker(t *testing.T) {
	t.Parallel()
	w := newDigestWorldWith(t, "", digestAppYAML, nil)
	dk, err := w.s.hosts.DockerScope(t.Context(), w.lab)
	if err != nil {
		t.Fatal(err)
	}
	answer := "mark" // what the daemon reads at the marker's path: the file, or not
	reads := 0
	w.hosts.daemon("lab").SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "run" && slices.Contains(args, "cat") {
			reads++
			for _, a := range args {
				if src, ok := strings.CutPrefix(a, "type=bind,src="); ok {
					path := strings.SplitN(src, ",", 2)[0]
					switch answer {
					case "mark":
						b, err := os.ReadFile(path)
						return b, err, true
					case "other":
						return []byte("someone else's"), nil, true
					}
					return []byte("invalid mount config: bind source path does not exist: " + path), errors.New("exit 125"), true
				}
			}
		}
		return nil, nil, false
	})
	w.s.capacity.host = func() (hostmem.Memory, error) {
		return hostmem.Memory{TotalBytes: 8 << 30, AvailableBytes: 5 << 30}, nil
	}
	fresh := func() {
		w.s.capacity.mu.Lock()
		w.s.capacity.witness = nil
		w.s.capacity.mu.Unlock()
	}
	w.s.capacity.markDir = t.TempDir()
	builtin := &Target{ID: w.lab.ID, Origin: OriginBuiltIn}

	if host := w.s.hostUse(t.Context(), builtin, dk, 8<<30); !host.Known || host.Value != 3<<30 {
		t.Errorf("witnessed: %+v", host)
	}
	for range 3 {
		w.s.hostUse(t.Context(), builtin, dk, 8<<30)
	}
	if reads != 1 {
		t.Errorf("the marker was read %d times", reads)
	}
	for _, a := range []string{"absent", "other"} {
		answer = a
		fresh()
		if host := w.s.hostUse(t.Context(), builtin, dk, 8<<30); host.Known || !strings.Contains(host.Reason, "does not run where this AppTown does") {
			t.Errorf("%s marker with equal totals: %+v", a, host)
		}
	}
	answer = "mark"
	fresh()
	if host := w.s.hostUse(t.Context(), builtin, dk, 16<<30); host.Known || !strings.Contains(host.Reason, "bytes of memory") {
		t.Errorf("witnessed, unequal totals: %+v", host)
	}
	if host := w.s.hostUse(t.Context(), &Target{ID: w.lab.ID, Origin: OriginAttached}, dk, 8<<30); host.Known {
		t.Errorf("attached host: %+v", host)
	}
	w.s.capacity.host = func() (hostmem.Memory, error) { return hostmem.Memory{}, errors.New("no /proc/meminfo") }
	if host := w.s.hostUse(t.Context(), builtin, dk, 8<<30); host.Known {
		t.Errorf("unreadable: %+v", host)
	}
}

// apptownd's own memory is counted once wherever it runs (CV-C01): no tenant
// adds it, so on the host it is part of the host's own use and in a
// container (one that shares its temp folder with the host, so the marker
// is read) it is that container's. AppTown's tenant is its own containers.
//
// Kills: apptownd's memory added to AppTown on top of the container that
// holds it.
func TestApptowndsMemoryIsCountedOnceWhereverItRuns(t *testing.T) {
	t.Parallel()
	none := containerOwners{exact: map[string]partOf{}, services: map[string]ids.ID{}}
	limits := []docker.ContainerLimit{{Name: "apptownd-in-a-container"}, {Name: "forge-db", AppTown: true, Class: "infra"}}
	ts := tenantsOf(limits, map[string]int64{"apptownd-in-a-container": 400 << 20, "forge-db": 100 << 20}, nil, none, nil)
	var sum float64
	for _, tn := range ts {
		sum += tn.Used.Value
	}
	if f := tenantNamed(ts, "AppTown"); f == nil || f.Used.Value != 100<<20 || sum != 500<<20 {
		t.Errorf("apptownd counted twice or lost: %+v (sum %v)", ts, sum)
	}
}
