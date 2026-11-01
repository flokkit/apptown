package docker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/diskspace"
)

// needCatalog fulfils postgres as a service and llm as a key that runs
// nothing, as the stock catalogue does.
type needCatalog struct{}

func (needCatalog) Fulfil(_ context.Context, needs []string, _ map[string]map[string]string) ([]deploy.ServiceSpec, map[string]string, error) {
	var out []deploy.ServiceSpec
	for _, n := range needs {
		if n == "postgres" {
			out = append(out, deploy.ServiceSpec{Name: "postgres", Image: "postgres:16-alpine"})
		}
	}
	return out, map[string]string{}, nil
}
func (needCatalog) Data(context.Context, string) (string, error) {
	return "/var/lib/postgresql/data", nil
}

// A need's service is held to the size its contract declares, as a part
// is: the one place every Docker boot and the intranet stack resolve
// needs. A size declared for a need that runs no service is refused,
// never dropped.
func TestANeedsServiceIsHeldToItsDeclaredSize(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte(`
app:
  web: { image: "app:web", public: true }
needs:
  postgres: { resources: { memory: 256M, cpu: 0.5 } }
`))
	if err != nil {
		t.Fatal(err)
	}
	rs, err := NewHost(Dependencies{StaticServer: testStaticServer, DFImage: testDF, Needs: needCatalog{}}).ResolveStack(t.Context(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Needs) != 1 || rs.Needs[0].Resources != (appcontract.Resources{MemoryBytes: 256 << 20, CPUs: 0.5}) {
		t.Fatalf("the need's service carries %+v", rs.Needs)
	}
	if got := IntranetNeeds(rs.Needs); got[0].Resources != rs.Needs[0].Resources {
		t.Errorf("the intranet stack lost the need's size: %+v", got)
	}

	f := &fakeClient{}
	if err := runServiceContainers(t.Context(), Scope{Client: f}, "pvtest", "net", rs.Services); err != nil {
		t.Fatal(err)
	}
	if runs := standingCalls(f, "run"); len(runs) != 1 || !strings.Contains(runs[0], "--memory 268435456 --memory-swap 268435456 --cpus 0.5") {
		t.Errorf("the need's container was started without its size: %v", runs)
	}

	undeclared, _ := appcontract.Parse([]byte("app:\n  web: { image: \"app:web\", public: true }\nneeds:\n  postgres: {}\n"))
	rs, err = NewHost(Dependencies{StaticServer: testStaticServer, DFImage: testDF, Needs: needCatalog{}}).ResolveStack(t.Context(), undeclared, nil)
	if err != nil || rs.Needs[0].Resources.Declared() {
		t.Fatalf("an undeclared need has a limit: %+v, %v", rs.Needs, err)
	}

	key, _ := appcontract.Parse([]byte("app:\n  web: { image: \"app:web\", public: true }\nneeds:\n  llm: { resources: { memory: 1G } }\n"))
	if _, err := NewHost(Dependencies{StaticServer: testStaticServer, DFImage: testDF, Needs: needCatalog{}}).ResolveStack(t.Context(), key, nil); err == nil || !strings.Contains(err.Error(), "llm declares resources, but llm runs no service") {
		t.Fatalf("a size for a need that runs no service: %v", err)
	}
}

// standingDaemon answers as a daemon holding one standing Postgres with
// a data volume: its enforced limits, and what update, stop and rm do.
type standingDaemon struct {
	memory, nano int64
	failUpdate   bool
	failRun      bool // the daemon will not start the service again
	unreadable   bool // the daemon cannot say whether the service exists
	stopped      bool // the service exists but is not running
	started      bool // a start was asked for
}

func (d *standingDaemon) reply(req Command) ([]byte, error) {
	a := req.Args
	switch {
	case d.unreadable && len(a) == 2 && a[0] == "inspect":
		return []byte("Cannot connect to the Docker daemon"), errors.New("exit 1")
	case d.failRun && len(a) > 0 && a[0] == "run":
		return []byte("Error response from daemon: Conflict"), errors.New("exit 125")
	case len(a) > 0 && a[0] == "inspect" && slices.Contains(a, "--type"):
		return fmt.Appendf(nil, "%d %d\n", d.memory, d.nano), nil
	case len(a) > 0 && a[0] == "start":
		d.started, d.stopped = true, false
	case len(a) > 0 && a[0] == "inspect" && slices.Contains(a, "{{.State.Running}}"):
		return []byte(fmt.Sprint(!d.stopped)), nil
	case len(a) > 0 && a[0] == "inspect":
		for _, x := range a {
			if strings.Contains(x, "Config.Image") {
				return []byte("postgres:16-alpine\n"), nil
			}
			// Its data is on its named volume.
			if strings.Contains(x, ".Mounts") {
				return []byte("volume\tapptown-need-env-shop-main-postgres\t/x\t/var/lib/postgresql/data\n"), nil
			}
		}
		return nil, nil
	case len(a) > 0 && a[0] == "update":
		if d.failUpdate {
			return []byte("Error: cannot update"), errors.New("exit 1")
		}
		for i := 0; i+1 < len(a); i++ {
			switch a[i] {
			case "--memory":
				fmt.Sscan(a[i+1], &d.memory)
			case "--cpus":
				var c float64
				fmt.Sscan(a[i+1], &c)
				d.nano = int64(c * 1e9)
			}
		}
	}
	return nil, nil
}

func standingCalls(f *fakeClient, verb string) []string {
	var out []string
	for _, a := range f.args {
		if len(a) > 0 && a[0] == verb {
			out = append(out, strings.Join(a, " "))
		}
	}
	return out
}

// A standing need takes a new size without losing its data: a limit set
// or changed is applied to the container it has, in place; a limit taken
// away remakes the container on the same volume, never a new one; and a
// size that cannot be applied fails, so it is never reported as applied.
func TestAStandingNeedTakesItsNewSizeWithoutLosingItsData(t *testing.T) {
	t.Parallel()
	own := Ownership{Needs: map[string]string{"postgres": "/var/lib/postgresql/data"}}
	svc := func(r appcontract.Resources) []deploy.ServiceSpec {
		return []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16-alpine", Resources: r}}
	}

	// No limit yet; the contract now declares 512M and one CPU: updated in
	// place, nothing removed or remade, and the production data untouched.
	d := &standingDaemon{}
	f := &fakeClient{reply: d.reply}
	if _, err := EnsureStandingServices(t.Context(), Scope{Client: f}, "env-shop-main", svc(appcontract.Resources{MemoryBytes: 512 << 20, CPUs: 1}), "production", own); err != nil {
		t.Fatal(err)
	}
	if got := standingCalls(f, "update"); len(got) != 1 || !strings.Contains(got[0], "--memory 536870912 --memory-swap 536870912 --cpus 1 apptown-svc-env-shop-main-postgres") {
		t.Fatalf("the size was not applied in place: %v", got)
	}
	if len(standingCalls(f, "rm"))+len(standingCalls(f, "run"))+len(standingCalls(f, "volume")) > 0 {
		t.Fatalf("a size change touched the container or its volume: %v", f.args)
	}

	// The same size again: nothing to do.
	f = &fakeClient{reply: d.reply}
	if _, err := EnsureStandingServices(t.Context(), Scope{Client: f}, "env-shop-main", svc(appcontract.Resources{MemoryBytes: 512 << 20, CPUs: 1}), "production", own); err != nil {
		t.Fatal(err)
	}
	if got := standingCalls(f, "update"); len(got) != 0 {
		t.Fatalf("an unchanged size was applied again: %v", got)
	}

	// The limit taken away: stopped, removed and remade on the same volume;
	// the volume is never removed.
	f = &fakeClient{reply: d.reply}
	if _, err := EnsureStandingServices(t.Context(), Scope{Client: f}, "env-shop-main", svc(appcontract.Resources{}), "production", own); err != nil {
		t.Fatal(err)
	}
	runs := standingCalls(f, "run")
	if len(standingCalls(f, "stop")) != 1 || len(standingCalls(f, "rm")) != 1 || len(runs) != 1 {
		t.Fatalf("a lifted limit did not remake the container: %v", f.args)
	}
	if !strings.Contains(runs[0], "-v apptown-need-env-shop-main-postgres:/var/lib/postgresql/data") || strings.Contains(runs[0], "--memory") {
		t.Errorf("the remade container is not on its volume, or keeps a limit: %s", runs[0])
	}
	for _, a := range f.args {
		if len(a) > 1 && a[0] == "volume" && a[1] == "rm" {
			t.Fatalf("the need's volume was removed: %v", a)
		}
	}

	// A lifted limit whose remake fails fails the boot: the release cannot
	// settle on a service that is not there.
	d = &standingDaemon{memory: 512 << 20}
	d.failRun = true
	f = &fakeClient{reply: d.reply}
	if _, err := EnsureStandingServices(t.Context(), Scope{Client: f}, "env-shop-main", svc(appcontract.Resources{}), "production", own); err == nil || !strings.Contains(err.Error(), "starting the standing postgres service") {
		t.Fatalf("a failed remake was not an error: %v", err)
	}

	// A service the daemon cannot say exists is never made again under its
	// name, nor taken as sized: the boot fails.
	d = &standingDaemon{memory: 256 << 20, unreadable: true}
	f = &fakeClient{reply: d.reply}
	if _, err := EnsureStandingServices(t.Context(), Scope{Client: f}, "env-shop-main", svc(appcontract.Resources{MemoryBytes: 1 << 30}), "production", own); err == nil {
		t.Fatal("an unreadable service was taken as absent or sized")
	}
	if got := append(standingCalls(f, "run"), standingCalls(f, "update")...); len(got) != 0 {
		t.Fatalf("an unreadable service was acted on: %v", got)
	}

	// A size the daemon will not apply fails the boot.
	d = &standingDaemon{failUpdate: true}
	f = &fakeClient{reply: d.reply}
	if _, err := EnsureStandingServices(t.Context(), Scope{Client: f}, "env-shop-main", svc(appcontract.Resources{MemoryBytes: 1 << 30}), "production", own); err == nil || !strings.Contains(err.Error(), "applying its declared limits") {
		t.Fatalf("a failed update was not an error: %v", err)
	}
}

// What the daemon reports of a container being killed out of memory,
// exiting or being signalled is read as it reports it, with the exit's
// code; a line AppTown cannot read is an error, never skipped.
func TestAContainerEventIsReadAsDockerReportsIt(t *testing.T) {
	t.Parallel()
	out := []byte(`{"status":"oom","id":"a1","Type":"container","Action":"oom","Actor":{"ID":"a1","Attributes":{"name":"apptown-env-shop-main","image":"x"}},"time":1760000000,"timeNano":1760000000123456789}
{"status":"die","id":"a1","Type":"container","Action":"die","Actor":{"ID":"a1","Attributes":{"name":"apptown-env-shop-main","exitCode":"137","apptown.deployment":"01a1","apptown.generation":"4"}},"time":1760000000,"timeNano":1760000000200000000}
{"status":"kill","id":"b2","Type":"container","Action":"kill","Actor":{"ID":"b2","Attributes":{"name":"apptown-svc-env-shop-main-postgres","signal":"15"}},"time":1760000001,"timeNano":1760000001000000000}
`)
	events, err := parseContainerEvents(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Action != "oom" || events[0].Container != "apptown-env-shop-main" || events[0].At.UnixNano() != 1760000000123456789 {
		t.Fatalf("events %+v", events)
	}
	if events[1].ExitCode == nil || *events[1].ExitCode != 137 || events[0].ExitCode != nil {
		t.Errorf("only a die carries its exit code: %+v", events[:2])
	}
	// The container's AppTown identity comes with the event, so an exit is
	// attributed to the generation whose container it was.
	if events[1].Deployment != "01a1" || events[1].Generation != "4" || events[2].Deployment != "" || events[2].Generation != "" {
		t.Errorf("identity labels: %+v", events)
	}
	if _, err := parseContainerEvents([]byte("not json\n")); err == nil {
		t.Error("an unreadable line was skipped")
	}
	if _, err := parseContainerEvents([]byte(`{"Action":"oom","Actor":{"Attributes":{}},"timeNano":1}` + "\n")); err == nil {
		t.Error("an event naming no container was accepted")
	}
	if got, _ := parseContainerEvents(nil); len(got) != 0 {
		t.Errorf("no output read as events: %v", got)
	}
}

// A CPU ceiling may be set three ways, and the tightest holds; a cpuset
// AppTown cannot read is unread, never "no limit".
func TestEveryWayDockerCapsCPUsIsRead(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		line         string
		cpus         float64
		unread       bool
		mem          int64
		apptown      bool
		class, owner string
	}{
		{"/web\t536870912\t1500000000\t0\t0\t\tenv\tapptown-env-a-main", 1.5, false, 512 << 20, true, "env", "apptown-env-a-main"},
		{"/other\t0\t0\t50000\t100000\t\t<no value>\t<no value>", 0.5, false, 0, false, "", ""},
		{"/other\t0\t0\t50000\t0\t\t\t", 0.5, false, 0, false, "", ""},
		{"/pinned\t0\t0\t0\t0\t0-3,6\t\t", 5, false, 0, false, "", ""},
		{"/both\t0\t2000000000\t0\t0\t0\tenv\t", 1, false, 0, true, "env", ""},
		{"/odd\t0\t0\t0\t0\tlots\tstep\tstep-1", 0, true, 0, true, "step", "step-1"},
		{"/free\t0\t0\t0\t0\t\t\t", 0, false, 0, false, "", ""},
	} {
		l, err := parseContainerLimit(c.line)
		if err != nil {
			t.Fatalf("%q: %v", c.line, err)
		}
		if l.CPUs != c.cpus || l.CPUUnread != c.unread || l.MemoryBytes != c.mem || l.AppTown != c.apptown || l.Class != c.class || l.Owner != c.owner {
			t.Errorf("%q read as %+v", c.line, l)
		}
	}
	if _, err := parseContainerLimit("/x\tlots\t0\t0\t0\t\t\t"); err == nil {
		t.Error("an unreadable memory was accepted")
	}
	if _, err := parseContainerLimit("/x\t0\t0\t0\t0\t\tenv"); err == nil {
		t.Error("a line without the owner field was accepted")
	}
}

// A part or need larger than the daemon can give any container is refused
// for good, before anything runs; a host whose limits already add up to
// more than it has still admits one that fits (limits are caps, not
// reservations); and a daemon that cannot say what it has refuses for now.
func TestAPartOrNeedLargerThanTheHostIsRefused(t *testing.T) {
	t.Parallel()
	host := func(info string, err error) Scope {
		return Scope{Client: &fakeClient{reply: func(req Command) ([]byte, error) {
			if req.Args[0] == "info" {
				return []byte(info), err
			}
			return nil, nil
		}}}
	}
	eight := host("8589934592\t8\t/var/lib/docker\n", nil)
	parse := func(y string) *appcontract.Contract {
		c, err := appcontract.Parse([]byte(y))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	for _, c := range []struct{ name, yaml, want string }{
		{"a part over the host's memory", "app:\n  web: { image: x, public: true, resources: { memory: 16G } }\n", "part web declares 16Gi of memory; this Docker host has 8Gi"},
		{"a part over the host's CPUs", "app:\n  web: { image: x, public: true, resources: { cpu: 12 } }\n", "part web declares 12 CPUs; this Docker host has 8"},
		{"a need over the host's memory", "app:\n  web: { image: x, public: true }\nneeds:\n  postgres: { resources: { memory: 9G } }\n", "need postgres declares 9G"},
	} {
		err := AdmitResources(t.Context(), eight, parse(c.yaml))
		if err == nil || !strings.Contains(err.Error(), c.want) || !deploy.IsTerminal(err) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if err := AdmitResources(t.Context(), eight, parse("app:\n  web: { image: x, public: true, resources: { memory: 8G, cpu: 8 } }\n")); err != nil {
		t.Errorf("a part the host can just hold was refused: %v", err)
	}
	unreadable := host("", errors.New("cannot connect"))
	err := AdmitResources(t.Context(), unreadable, parse("app:\n  web: { image: x, public: true, resources: { memory: 1G } }\n"))
	if err == nil || deploy.IsTerminal(err) {
		t.Errorf("an unreadable host: %v (refused, and not for good)", err)
	}
	if err := AdmitResources(t.Context(), unreadable, parse("app:\n  web: { image: x, public: true }\n")); err != nil {
		t.Errorf("a contract that declares nothing asked the host: %v", err)
	}
}

// Every boot admits its contract against its daemon before anything
// starts: a release preview, a rehearsal or a landing that reaches Boot
// with a part larger than the host is refused in its resolve phase.
func TestABootRefusesAPartLargerThanItsHost(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("app:\n  web: { image: \"app:web\", public: true, resources: { memory: 16G } }\n"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeClient{reply: func(req Command) ([]byte, error) {
		if req.Args[0] == "info" {
			return []byte("8589934592\t8\t/var/lib/docker\n"), nil
		}
		return nil, nil
	}}
	_, err = NewHost(Dependencies{StaticServer: testStaticServer, DFImage: testDF, Needs: needCatalog{}}).Boot(t.Context(), BootRequest{Allowed: AuthorisedWhenRequested, Docker: Scope{Client: f}, Name: "apptown-preview-x", StackID: "pvx", Workspace: t.TempDir()}, c)
	if be := AsBootError(err); err == nil || be.Phase != PhaseResolve || !strings.Contains(err.Error(), "declares 16G") {
		t.Fatalf("an oversized boot: %v", err)
	}
	for _, a := range f.args {
		if a[0] != "info" && !f.disk.IsRead(a) {
			t.Fatalf("the boot did %v before refusing", a)
		}
	}
}

// Every boot reads its own daemon's data disk before anything is pulled,
// built or started: below the floor (5%, at least 1 GiB, at most 20 GiB), or with the disk
// unread, it is refused in its resolve phase with what is free; at the
// floor it goes on. What runs now is not touched.
//
// Kills: AdmitDisk removed from Boot; the floor's comparison inverted; an
// unread disk admitted.
func TestABootIsRefusedOnADaemonShortOfDisk(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("app:\n  web: { image: \"app:web\", public: true }\n"))
	if err != nil {
		t.Fatal(err)
	}
	const gib = int64(1) << 30
	df := func(total, free int64) func() ([]byte, error) {
		return func() ([]byte, error) {
			return []byte(fmt.Sprintf("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x %d %d %d 99%% /d\n", total>>10, (total-free)>>10, free>>10)), nil
		}
	}
	for _, tc := range []struct {
		name    string
		disk    func() ([]byte, error)
		refused string
	}{
		{"below the floor", df(100*gib, 5*gib-(100<<20)), "4.9 GiB free of 100.0 GiB"},
		{"unread", func() ([]byte, error) { return []byte("no space left on device"), errors.New("exit status 125") }, "could not be read"},
		{"at the floor", df(100*gib, 5*gib), ""},
	} {
		f := &fakeClient{reply: func(req Command) ([]byte, error) {

			if req.Args[0] == "port" {
				return []byte("0.0.0.0:43210\n"), nil
			}
			return nil, nil
		}}
		f.disk.Answer = func() (int, []byte, string) {
			out, err := tc.disk()
			if err != nil {
				return 125, nil, string(out)
			}
			return 0, out, ""
		}

		_, err := NewHost(Dependencies{StaticServer: testStaticServer, DFImage: testDF, Needs: reviewNoNeeds{}, Secrets: reviewNoSecrets{}, Probe: reviewUp{}}).Boot(t.Context(), BootRequest{Allowed: AuthorisedWhenRequested, Docker: Scope{Client: f}, Name: "apptown-preview-x", StackID: "pvx", Workspace: t.TempDir(), Probe: time.Second}, c)
		if tc.refused == "" {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		if be := AsBootError(err); !errors.Is(err, diskspace.ErrRefused) || be.Phase != PhaseResolve || !strings.Contains(err.Error(), "/var/lib/docker") {
			t.Errorf("%s: %v", tc.name, err)
		}
		if !strings.Contains(err.Error(), tc.refused) {
			t.Errorf("%s: the refusal does not say what is free: %v", tc.name, err)
		}
		for _, a := range f.args {
			if !f.disk.IsRead(a) {
				t.Errorf("%s: the boot did %v after refusing", tc.name, a)
			}
		}
	}
}

// unreadableData fulfils postgres, but cannot say where it keeps its data.
type unreadableData struct{ needCatalog }

func (unreadableData) Data(context.Context, string) (string, error) {
	return "", errors.New("reading the dependency kind postgres: connection reset")
}

// A need whose data folder cannot be read is never taken for one that
// keeps none: the boot stops while resolving, before a standing service is
// stopped or made again (where a remake without its folder would start an
// empty database), and preparation for a worker stops the same way.
func TestANeedWhoseDataFolderCannotBeReadStopsTheBoot(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("app:\n  web: { image: \"app:web\", public: true }\nneeds:\n  postgres: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewHost(Dependencies{StaticServer: testStaticServer, DFImage: testDF, Needs: unreadableData{}}).ResolveStack(t.Context(), c, nil); err == nil || !strings.Contains(err.Error(), "where postgres keeps its data") {
		t.Fatalf("resolving with an unreadable data folder: %v", err)
	}
	f := &fakeClient{}
	_, err = NewHost(Dependencies{StaticServer: testStaticServer, DFImage: testDF, Needs: unreadableData{}}).Boot(t.Context(), BootRequest{Allowed: AuthorisedWhenRequested, Docker: Scope{Client: f}, Name: "apptown-env-shop-main", StackID: "env-shop-main", Workspace: t.TempDir(), Persistent: true}, c)
	if err == nil {
		t.Fatal("a boot went on without knowing where its database keeps its data")
	}
	for _, a := range f.args {
		if !f.disk.IsRead(a) && (a[0] == "stop" || a[0] == "rm" || a[0] == "run" || a[0] == "update") {
			t.Fatalf("the boot did %v before it knew", a)
		}
	}
	if _, err := PrepareBoot(t.Context(), Dependencies{StaticServer: testStaticServer, DFImage: testDF, Needs: unreadableData{}}, nil, "shop", "preview", t.TempDir(), c); err == nil {
		t.Fatal("a preparation went on without knowing where its database keeps its data")
	}
}
