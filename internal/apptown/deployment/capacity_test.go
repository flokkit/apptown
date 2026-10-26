package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/diskspace/diskspacetest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// sizedNeedYAML is the world's app with a Postgres it sizes, and a
// production profile that gives the database more.
const sizedNeedYAML = `app:
  web:
    public: true
    static: site
    ports: ["8080"]
needs:
  postgres:
    version: "16"
    resources: { memory: 256M }
`

func commandsOn(w *digestWorld, target, verb string) []string {
	var out []string
	for _, c := range w.hosts.commands(target) {
		if strings.HasPrefix(c, verb+" ") {
			out = append(out, c)
		}
	}
	return out
}

// A need is sized on its standing deployment as a part is: its service is
// started at the declared size, the deployment's page shows it as a
// service, and an operator's override reaches the database it already has
// in place, its data untouched.
func TestANeedIsSizedOnItsDeploymentAndChangedInPlace(t *testing.T) {
	t.Parallel()
	w := newDigestWorldWith(t, "", sizedNeedYAML, nil)
	w.s.authority = w.deps.Releases.Authority // the page's read is the deploy owner's, as the route gives it
	b := w.publish(t)
	if rel, err := w.promote(t, b.ID, "prod", w.lab); err != nil || rel.State != Released {
		t.Fatalf("release: %+v %v", rel, err)
	}
	db := w.names(t, "prod").Service("postgres")
	if run := ranWith(t, w, "lab", db, 0); !strings.Contains(run, "--memory 268435456 --memory-swap 268435456") {
		t.Fatalf("the database was started without its size: %s", run)
	}
	r, err := w.s.Resources(t.Context(), w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	var row *PartResource
	for i := range r.Parts {
		if r.Parts[i].Part == "postgres" {
			row = &r.Parts[i]
		}
	}
	if row == nil || !row.Need || row.Memory != "256Mi" {
		t.Fatalf("the page does not show the database's size: %+v", r.Parts)
	}
	// Its memory use is a need's row, so the memory check judges it as one.
	observed, err := w.s.ObservedMemory(t.Context(), mustDeployment(t, w, "prod"))
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(observed, func(m PartMemory) bool { return m.Part == "postgres" }); i < 0 || !observed[i].Need {
		t.Fatalf("the database's use is not marked as a need's: %+v", observed)
	}

	runsBefore := len(w.hosts.ran("lab"))
	out, err := w.s.SetOverride(t.Context(), OverrideChange{ProjectID: w.project, Deployment: "prod", Part: "postgres",
		Resources: appcontract.Resources{MemoryBytes: 1 << 30}, Reason: "the nightly import runs out of memory"})
	if err != nil || out.Release == nil {
		t.Fatalf("override: %+v %v", out, err)
	}
	// The redeploy it requested runs, as the job worker runs it.
	redeploy, err := w.s.Releases.ByNumber(t.Context(), w.project, out.Release.Number)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.ExecuteRelease(t.Context(), redeploy.ID); err != nil {
		t.Fatal(err)
	}
	if mem, _ := w.hosts.daemon("lab").Limits(db); mem != 1<<30 {
		t.Fatalf("the override did not reach the database: it is held to %d", mem)
	}
	updates := commandsOn(w, "lab", "update")
	if len(updates) != 1 || !strings.HasSuffix(updates[0], db) {
		t.Fatalf("the database was not resized in place: %v", updates)
	}
	for _, c := range w.hosts.ran("lab")[runsBefore:] {
		if strings.Contains(c, "--name "+db+" ") {
			t.Fatalf("the database was made again for a size change: %s", c)
		}
	}
	for _, c := range w.hosts.commands("lab") {
		if strings.HasPrefix(c, "volume rm") || c == "rm -f "+db {
			t.Fatalf("a size change removed the database or its data: %s", c)
		}
	}

	// A rebuild of that generation (its front door gone, as after a host
	// was wiped of it) runs the database at the size it recorded: nothing
	// to change, so the database is not touched.
	if _, err := w.hosts.daemon("lab").Run(t.Context(), "", docker.Command{Args: []string{"rm", w.names(t, "prod").App()}}); err != nil {
		t.Fatal(err)
	}
	seen, ranBefore := len(w.hosts.commands("lab")), len(w.hosts.ran("lab"))
	if _, err := w.rebuild(t, mustDeployment(t, w, "prod")); err != nil {
		t.Fatal(err)
	}
	if len(w.hosts.ran("lab")) == ranBefore {
		t.Fatal("the rebuild booted nothing")
	}
	for _, c := range w.hosts.commands("lab")[seen:] {
		if strings.HasSuffix(c, db) && (strings.HasPrefix(c, "stop") || strings.HasPrefix(c, "update") || strings.HasPrefix(c, "rm")) {
			t.Fatalf("a rebuild changed the database's size: %s", c)
		}
	}
	if mem, _ := w.hosts.daemon("lab").Limits(db); mem != 1<<30 {
		t.Fatalf("after the rebuild the database is held to %d", mem)
	}
}

// A release to a Docker host larger than the host is refused before
// anything is built, pulled or run there.
func TestAReleaseLargerThanItsHostIsRefusedBeforeAnyEffect(t *testing.T) {
	t.Parallel()
	w := newDigestWorldWith(t, "", "app:\n  web:\n    public: true\n    static: site\n    ports: [\"8080\"]\n  worker:\n    build: worker\n    resources: { memory: 64G }\n", nil)
	b := w.publish(t)
	before := len(w.hosts.commands("lab"))
	rel, err := w.promote(t, b.ID, "prod", w.lab)
	if err == nil && rel.State == Released {
		t.Fatal("a 64G part was released to an 8G host")
	}
	for _, c := range w.hosts.commands("lab")[before:] {
		if !strings.HasPrefix(c, "info") {
			t.Fatalf("the host was asked %q before the refusal", c)
		}
	}
}

// A size declared for a need that runs no service (a key AppTown
// registers) is refused before the release does anything on the host.
func TestASizeForANeedThatRunsNoServiceIsRefusedBeforeAnyEffect(t *testing.T) {
	t.Parallel()
	w := newDigestWorldWith(t, "", "app:\n  web:\n    public: true\n    static: site\n    ports: [\"8080\"]\nneeds:\n  llm:\n    resources: { memory: 1G }\n", nil)
	b := w.publish(t)
	rel, err := w.promote(t, b.ID, "prod", w.lab)
	if err == nil && rel.State == Released {
		t.Fatal("a size for a key was accepted")
	}
	events, _ := w.s.Releases.Events(context.Background(), rel.ID)
	said := ""
	for _, e := range events {
		said += e.Text + "\n"
	}
	if !strings.Contains(said+fmt.Sprint(err), "llm declares resources, but llm runs no service") {
		t.Errorf("the refusal does not name the need: %v\n%s", err, said)
	}
	if runs := w.hosts.ran("lab"); len(runs) > 0 {
		t.Errorf("the host was asked to run something: %v", runs)
	}
}

func eventLine(action, container string, at time.Time, exit int) string {
	attrs := fmt.Sprintf(`"name":%q`, container)
	if action == "die" {
		attrs += fmt.Sprintf(`,"exitCode":"%d"`, exit)
	}
	return fmt.Sprintf(`{"Type":"container","Action":%q,"Actor":{"Attributes":{%s}},"timeNano":%d}`, action, attrs, at.UnixNano()) + "\n"
}

// Limit events are recorded for every standing deployment on a host,
// one with a release still open among them, with the part or need each
// happened to; an exit a stop caused is not an exit; another host's
// containers are never attributed here; reading the same window again
// records nothing twice; and the health controller still leaves the open
// deployment alone.
func TestLimitEventsAreRecordedForEveryStandingDeployment(t *testing.T) {
	t.Parallel()
	w := newDigestWorldWith(t, "", digestAppYAML, nil)
	b := w.publish(t)
	for _, name := range []string{"prod", "stage"} {
		if rel, err := w.promote(t, b.ID, name, w.lab); err != nil || rel.State != Released {
			t.Fatalf("release %s: %+v %v", name, rel, err)
		}
	}
	if rel, err := w.promote(t, b.ID, "other", w.lab2); err != nil || rel.State != Released {
		t.Fatalf("release other: %+v %v", rel, err)
	}
	stage := mustDeployment(t, w, "stage")
	if _, created, err := w.s.Outcomes.Admit(t.Context(), deploymentStartFor(stage, "a release in flight", "flight")); err != nil || !created {
		t.Fatalf("opening stage's release: %v %v", created, err)
	}
	for _, watched := range w.s.hosts.Watched(t.Context(), w.s.Deployments) {
		if watched.Name == "stage" {
			t.Fatal("the health controller watches a deployment whose release is open")
		}
	}

	// The second-read frontier comes back from PostgreSQL at microsecond
	// precision. Keep the fixture at that precision so the exact overlap
	// assertion below tests the window rather than the clock's precision.
	t0 := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Microsecond)
	prodWeb, prodWorker := w.names(t, "prod").App(), w.names(t, "prod").Service("worker")
	stageWeb := w.names(t, "stage").App()
	lab := eventLine("oom", stageWeb, t0, 0) + // stage: killed while its release runs
		eventLine("die", stageWeb, t0.Add(time.Second), 137) + // that kill's exit
		eventLine("die", prodWorker, t0.Add(2*time.Minute), 1) + // prod's worker exits on its own
		eventLine("kill", prodWeb, t0.Add(3*time.Minute), 0) + // a release replaces prod's front door
		eventLine("die", prodWeb, t0.Add(3*time.Minute+time.Second), 143) +
		eventLine("die", "apptown-preview-01a1", t0.Add(4*time.Minute), 1) // a preview: not a deployment's
	// The daemon answers as Docker does: the events at or after --since.
	var since []string
	w.hosts.daemon("lab").SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "events" {
			since = append(since, args[2])
			var sec, nsec int64
			fmt.Sscanf(args[2], "%d.%d", &sec, &nsec)
			from := time.Unix(sec, nsec)
			var out strings.Builder
			for _, line := range strings.SplitAfter(lab, "\n") {
				var e struct{ TimeNano int64 }
				if line != "" && json.Unmarshal([]byte(line), &e) == nil && !time.Unix(0, e.TimeNano).Before(from) {
					out.WriteString(line)
				}
			}
			return []byte(out.String()), nil, true
		}
		return nil, nil, false
	})
	w.hosts.daemon("lab2").SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "events" {
			return []byte(eventLine("die", prodWorker, t0.Add(5*time.Minute), 2)), nil, true // same name, another host
		}
		return nil, nil, false
	})
	// The health controller's sweep reads them, as the daemon runs it.
	w.s.HealthController().Sweep(t.Context())
	// The next read covers the same window again, and one new exit: the
	// old ones are recorded once, and the new one is not lost to them.
	lab += eventLine("die", prodWorker, t0.Add(6*time.Minute), 3)
	w.s.RecordLimitEvents(t.Context())

	read := func(name string) []LimitEvent {
		got, err := w.s.LimitEvents(t.Context(), mustDeployment(t, w, name), t0.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := read("stage"); len(got) != 1 || got[0].Kind != LimitOutOfMemory || got[0].Part != "web" {
		t.Errorf("stage: %+v (its out-of-memory kill, once, and not its exit)", got)
	}
	if got := read("prod"); len(got) != 2 || got[0].Kind != LimitExited || got[0].Part != "worker" || got[0].ExitCode == nil || *got[0].ExitCode != 1 || *got[1].ExitCode != 3 {
		t.Errorf("prod: %+v (its worker's two exits, each once, and not the replaced front door)", got)
	}
	if got := read("other"); len(got) != 0 {
		t.Errorf("another host's container was attributed: %+v", got)
	}
	if len(since) != 2 {
		t.Fatalf("lab's events were read %d times", len(since))
	}
	var sec, nsec int64
	fmt.Sscanf(since[0], "%d.%d", &sec, &nsec)
	first := time.Unix(sec, nsec)
	if reach := time.Since(first); reach < 59*time.Minute || reach > 61*time.Minute {
		t.Errorf("the first read reached %s back; an hour is the most", reach)
	}
	fmt.Sscanf(since[1], "%d.%d", &sec, &nsec)
	if again, want := time.Unix(sec, nsec), t0.Add(2*time.Minute-stopPairing); !again.Equal(want) {
		t.Errorf("the second read began at %s, not ten seconds before the newest event recorded (%s)", again.UTC(), want)
	}

	summary, err := w.s.LimitSummary(t.Context(), mustDeployment(t, w, "prod"), 24*time.Hour)
	if err != nil || len(summary) != 1 || summary[0].Count != 2 || summary[0].Kind != LimitExited {
		t.Errorf("prod's summary: %+v %v", summary, err)
	}
	if known, words := w.s.LimitEventsSource(t.Context(), mustDeployment(t, w, "prod")); !known || !strings.Contains(words, "a gap is possible") {
		t.Errorf("where prod's events come from: %v %q", known, words)
	}
}

// Events older than thirty days are pruned; newer ones stay.
func TestLimitEventsAreKeptThirtyDays(t *testing.T) {
	t.Parallel()
	w := newDigestWorldWith(t, "", digestAppYAML, nil)
	b := w.publish(t)
	if rel, err := w.promote(t, b.ID, "prod", w.lab); err != nil || rel.State != Released {
		t.Fatalf("release: %+v %v", rel, err)
	}
	dep := mustDeployment(t, w, "prod")
	now := time.Now().UTC()
	for _, at := range []time.Time{now.Add(-31 * 24 * time.Hour), now.Add(-29 * 24 * time.Hour)} {
		if err := w.s.recordLimitEvent(t.Context(), dep.ID, LimitEvent{Part: "web", Kind: LimitExited, Container: "c", At: at}); err != nil {
			t.Fatal(err)
		}
	}
	w.s.pruneLimitEvents(t.Context())
	got, err := w.s.LimitEvents(t.Context(), dep, now.Add(-40*24*time.Hour))
	if err != nil || len(got) != 1 || got[0].At.Before(now.Add(-30*24*time.Hour)) {
		t.Fatalf("after pruning: %+v %v", got, err)
	}
	// The memory check's window is the last day: an event a day and an
	// hour old is not in it, one an hour old is.
	for _, at := range []time.Time{now.Add(-25 * time.Hour), now.Add(-time.Hour)} {
		if err := w.s.recordLimitEvent(t.Context(), dep.ID, LimitEvent{Part: "web", Kind: LimitOutOfMemory, Container: "c", At: at}); err != nil {
			t.Fatal(err)
		}
	}
	day, err := w.s.LimitSummary(t.Context(), dep, 24*time.Hour)
	if err != nil || len(day) != 1 || day[0].Kind != LimitOutOfMemory || day[0].Count != 1 {
		t.Fatalf("the last day's events: %+v %v", day, err)
	}
}

// A service container is its stack's: the longest stack prefix wins, so
// main never takes main-eu's; a container of no standing deployment is
// no one's.
func TestAContainerBelongsToTheLongestStackItsNameBegins(t *testing.T) {
	t.Parallel()
	main, eu := idstest.N(1), idstest.N(2)
	o := containerOwners{exact: map[string]partOf{"apptown-env-shop-main": {deployment: main, part: "web"}},
		services: map[string]ids.ID{docker.Current.Service("env-shop-main", ""): main, docker.Current.Service("env-shop-main-eu", ""): eu}}
	for name, want := range map[string]partOf{
		"apptown-env-shop-main":                 {deployment: main, part: "web"},
		"apptown-svc-env-shop-main-postgres":    {deployment: main, part: "postgres"},
		"apptown-svc-env-shop-main-eu-postgres": {deployment: eu, part: "postgres"},
		"apptown-svc-env-shop-main-eu-worker-2": {deployment: eu, part: "worker-2"},
	} {
		if got, ok := o.of(name); !ok || got != want {
			t.Errorf("%s: %+v %v, want %+v", name, got, ok, want)
		}
	}
	for _, name := range []string{"apptown-preview-01a1", "apptown-svc-env-shop-main-", "traefik"} {
		if got, ok := o.of(name); ok {
			t.Errorf("%s was attributed to %+v", name, got)
		}
	}
}

// A host's capacity is what its daemon reports: what it can give, the
// limits it enforces (AppTown's and the rest, unlimited counted, a
// limit it cannot read making the sum partial), what the containers use,
// and its disk (which folder, when, and whether new work is refused); a
// reading it cannot make is unknown, never zero.
func TestAHostsCapacityIsWhatItsDaemonReports(t *testing.T) {
	t.Parallel()
	w := newDigestWorldWith(t, "", digestAppYAML, nil)
	d := w.hosts.daemon("lab")
	d.SetHostInfo(8<<30, 8, "/var/lib/docker")
	statsFail := false
	dfRuns := 0
	d.SetDisk(1000000*1024, 50000*1024)
	d.SetReply(func(args []string) ([]byte, error, bool) {
		switch {
		case len(args) > 1 && args[0] == "ps" && args[1] == "-q":
			return []byte("a\nb\nc\nd\n"), nil, true
		case len(args) > 4 && args[0] == "inspect" && strings.Contains(args[4], "CpuQuota"):
			return []byte("/apptown-env-p-prod\t536870912\t1000000000\t0\t0\t\tenv\t\n" +
				"/apptown-svc-env-p-prod-postgres\t0\t0\t0\t0\t\tenv\t\n" +
				"/traefik\t268435456\t0\t50000\t100000\t\t<no value>\t<no value>\n" +
				"/odd\t0\t0\t0\t0\tlots\t\t\n"), nil, true
		case len(args) > 0 && args[0] == "stats":
			if statsFail {
				return []byte("cannot connect"), fmt.Errorf("exit 1"), true
			}
			return []byte("apptown-env-p-prod\t100MiB / 512MiB\napptown-svc-env-p-prod-postgres\t30MiB / 8GiB\ntraefik\t20MiB / 256MiB\nodd\t1MiB / 8GiB\n"), nil, true
		case diskspacetest.IsCreate(args):
			dfRuns++
			return nil, nil, false
		}
		return nil, nil, false
	})
	c, err := w.s.Capacity(t.Context(), "lab")
	if err != nil {
		t.Fatal(err)
	}
	m, cpu := c.Memory, c.CPU
	if c.Elastic || !m.Total.Known || m.Total.Value != 8<<30 || cpu.Total.Value != 8 {
		t.Fatalf("totals: %+v %+v", m.Total, cpu.Total)
	}
	if m.Promised.Sum.AppTown.Value != 512<<20 || m.Promised.Sum.Other.Value != 256<<20 || m.Promised.Unlimited != (Count{AppTown: 1, Other: 1}) {
		t.Errorf("memory promised: %+v", m.Promised)
	}
	if cpu.Promised.Sum.AppTown.Value != 1 || cpu.Promised.Sum.Other.Value != 0.5 || cpu.Promised.Unread != (Count{Other: 1}) || cpu.Promised.Unlimited != (Count{AppTown: 1}) {
		t.Errorf("CPUs promised: %+v", cpu.Promised)
	}
	if m.InUse == nil || m.InUse.AppTown.Value != 130<<20 || m.InUse.Other.Value != 21<<20 {
		t.Errorf("in use: %+v", m.InUse)
	}
	if m.Overcommitted || cpu.Overcommitted {
		t.Errorf("a host with room is overcommitted: %v %v", m.Overcommitted, cpu.Overcommitted)
	}
	if c.Disk == nil || c.Disk.Total.Value != 1000000<<10 || c.Disk.Free.Value != 50000<<10 || !c.Disk.Low {
		t.Errorf("disk: %+v", c.Disk)
	}
	// It says which folder's filesystem it read and when, and that new
	// releases are refused there: 49 MiB free is under the 1 GiB floor.
	if c.Disk.Path != "/var/lib/docker" || c.Disk.ReadAt.IsZero() || !c.Disk.Refuses || c.Disk.Floor != 1<<30 || c.Disk.Warning != 1<<30 {
		t.Errorf("the disk's folder, time and floor: %+v", c.Disk)
	}
	// Who holds it, from the same readings: the two env containers no
	// record here names, and the two AppTown did not start; an attached
	// host's own use is not read.
	if o, u := tenantNamed(c.Tenants, "other containers"), tenantNamed(c.Tenants, "AppTown containers not on record"); o == nil || u == nil ||
		o.Containers != 2 || o.Promised.Value != 256<<20 || o.Unlimited != 1 || o.Used.Value != 21<<20 ||
		u.Containers != 2 || u.Promised.Value != 512<<20 || u.Used.Value != 130<<20 {
		t.Errorf("tenants: %+v", c.Tenants)
	}
	if m.HostUsed == nil || m.HostUsed.Known || m.HostUsed.Reason == "" {
		t.Errorf("an attached host's own use: %+v", m.HostUsed)
	}

	// The disk read runs a container: at most once in five minutes.
	labScope, err := w.s.hosts.DockerScope(t.Context(), w.lab)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if got := w.s.disk(t.Context(), w.lab.ID, labScope, "/var/lib/docker"); !got.Free.Known {
			t.Fatalf("disk: %+v", got)
		}
	}
	if dfRuns != 1 {
		t.Errorf("the disk was read %d times", dfRuns)
	}

	// A host whose limits add up to more than it has is overcommitted;
	// use it cannot read is unknown with the reason, never zero.
	d.SetHostInfo(512<<20, 1, "/var/lib/docker")
	statsFail = true
	w.s.capacity.mu.Lock()
	w.s.capacity.read = nil
	w.s.capacity.mu.Unlock()
	c, err = w.s.Capacity(t.Context(), "lab")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Memory.Overcommitted || !c.CPU.Overcommitted {
		t.Errorf("limits above the total are not overcommitted: %+v %+v", c.Memory, c.CPU)
	}
	if c.Memory.InUse.AppTown.Known || c.Memory.InUse.AppTown.Reason == "" {
		t.Errorf("unread use: %+v", c.Memory.InUse)
	}

	// Use read while the owners could not be: the inspection fails, or a
	// container appears between the readings. Neither is split by a guess.
	statsFail = false
	inspectFails, newcomer := true, false
	d.SetReply(func(args []string) ([]byte, error, bool) {
		switch {
		case len(args) > 1 && args[0] == "ps" && args[1] == "-q":
			return []byte("a\n"), nil, true
		case len(args) > 4 && args[0] == "inspect" && strings.Contains(args[4], "CpuQuota"):
			if inspectFails {
				return []byte("Error: No such object: a"), fmt.Errorf("exit 1"), true
			}
			return []byte("/apptown-env-p-prod\t0\t0\t0\t0\t\tenv\t\n"), nil, true
		case len(args) > 0 && args[0] == "stats":
			out := "apptown-env-p-prod\t100MiB / 8GiB\n"
			if newcomer {
				out += "apptown-preview-01a1\t50MiB / 8GiB\n"
			}
			return []byte(out), nil, true
		}
		return nil, nil, false
	})
	for _, c := range []struct {
		name              string
		inspect, newcomer bool
		why               string
	}{{"inspection failed", true, false, "whose containers they are cannot be read"}, {"a container appeared", false, true, "apptown-preview-01a1 started after the containers were read"}} {
		inspectFails, newcomer = c.inspect, c.newcomer
		w.s.capacity.mu.Lock()
		w.s.capacity.read = nil
		w.s.capacity.mu.Unlock()
		got, err := w.s.Capacity(t.Context(), "lab")
		if err != nil {
			t.Fatal(err)
		}
		if got.Memory.InUse.AppTown.Known || got.Memory.InUse.Other.Known || !strings.Contains(got.Memory.InUse.AppTown.Reason, c.why) {
			t.Errorf("%s: use split by a guess, or not said why: %+v", c.name, got.Memory.InUse)
		}
	}
}

// On App Platform the target grows: its capacity is what each part runs
// on and that size's listed price, summed as an estimate that says it is
// one and leaves the managed database out.
func TestAnElasticTargetShowsSizesAndAnEstimate(t *testing.T) {
	t.Parallel()
	p := newPromotionTargetsWith(t, false, "app:\n  web:\n    public: true\n    static: site\n    ports: [\"8080\"]\n    resources: { memory: 768M }\n")
	b := p.land(t)
	if rel, err := p.promote(t, b.ID, "cloud", p.target(t, "do-syd")); err != nil || rel.State != Released {
		t.Fatalf("release: %+v %v", rel, err)
	}
	c, err := p.s.Capacity(t.Context(), "do-syd")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Elastic || c.Memory != nil || len(c.Parts) != 1 {
		t.Fatalf("capacity: %+v", c)
	}
	part := c.Parts[0]
	if !strings.HasPrefix(part.RunsOn, "apps-s-1vcpu-1gb-fixed") || !part.USDPerMonth.Known || part.USDPerMonth.Value != 10 {
		t.Errorf("part: %+v", part)
	}
	if c.Estimate == nil || c.Estimate.Value != 10 || !strings.Contains(c.Estimate.Reason, "estimate") || !strings.Contains(c.Estimate.Reason, "managed databases are not included") {
		t.Errorf("estimate: %+v", c.Estimate)
	}
}

// A deployment's page says what it holds of its fixed host: its declared
// memory against the host's, and its use.
func TestADeploymentShowsItsShareOfItsHost(t *testing.T) {
	t.Parallel()
	w, _ := profiledWorld(t)
	w.s.authority = w.deps.Releases.Authority
	w.hosts.daemon("lab").SetHostInfo(4<<30, 4, "/var/lib/docker")
	b := w.publish(t)
	if rel, err := w.promote(t, b.ID, "prod", w.lab); err != nil || rel.State != Released {
		t.Fatalf("release: %+v %v", rel, err)
	}
	r, err := w.s.Resources(t.Context(), w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if r.Share == nil || r.Share.Target != "lab" || r.Share.HostMemory.Value != 4<<30 {
		t.Fatalf("share: %+v", r.Share)
	}
	// The formerly undeclared cache is capped by the fresh generation's
	// recorded fallback, and contributes to the promise rather than disappearing.
	if !r.Share.Declared.Known || r.Share.Declared.Value != float64((128+256+512)<<20) {
		t.Errorf("declared: %+v", r.Share.Declared)
	}
	if !r.LimitsKnown || r.Limits == nil {
		t.Errorf("limit events: %v %+v", r.LimitsKnown, r.Limits)
	}
}

// Through Portainer AppTown reads what the endpoint has, and the limits
// its own deployments there declare; the other stacks are unknown, not
// none. A deployment released before limits were recorded makes the
// AppTown's sum unknown rather than leaving it out.
func TestAPortainerTargetsCapacityIsWhatItHasAndWhatAppTownDeclares(t *testing.T) {
	t.Parallel()
	p := newPromotionTargetsWith(t, false, "app:\n  web:\n    public: true\n    static: site\n    ports: [\"8080\"]\n    resources: { memory: 256M }\n")
	c, err := p.s.Capacity(t.Context(), IntranetTargetName)
	if err != nil {
		t.Fatal(err)
	}
	if c.Elastic || !c.Memory.Total.Known || c.Memory.Total.Value != 16<<30 || c.CPU.Total.Value != 4 {
		t.Fatalf("what the endpoint has: %+v %+v", c.Memory.Total, c.CPU.Total)
	}
	if !c.Memory.Promised.Sum.AppTown.Known || c.Memory.Promised.Sum.Other.Known || !strings.Contains(c.Words, "declare") {
		t.Errorf("promised through Portainer: %+v (%s)", c.Memory.Promised, c.Words)
	}
	if c.Memory.InUse == nil || c.Memory.InUse.AppTown.Known || c.Disk.Free.Known {
		t.Errorf("use and disk through Portainer are read: %+v %+v", c.Memory.InUse, c.Disk)
	}

	// What AppTown's running deployments declare is summed.
	b := p.land(t)
	if rel, err := p.promote(t, b.ID, "prod", p.lab); err != nil || rel.State != Released {
		t.Fatalf("release: %+v %v", rel, err)
	}
	declared, err := p.s.declaredOn(t.Context(), p.lab.ID)
	if err != nil || !declared[0].Sum.AppTown.Known || declared[0].Sum.AppTown.Value < 256<<20 {
		t.Fatalf("declared with every record: %+v %v", declared[0], err)
	}
	// A generation from before applied records were kept makes the
	// AppTown's sum unknown, never the others' sum alone.
	recorded := AppliedProfile{Profile: "default", Parts: map[string]AppliedPart{"web": {Resources: appcontract.Resources{MemoryBytes: 256 << 20}}}}
	if got := sumDeclared([]AppliedProfile{recorded}); !got[0].Sum.AppTown.Known || got[0].Sum.AppTown.Value != 256<<20 {
		t.Fatalf("one recorded generation: %+v", got[0])
	}
	got := sumDeclared([]AppliedProfile{recorded, {}})
	if got[0].Sum.AppTown.Known || got[1].Sum.AppTown.Known || !strings.Contains(got[0].Sum.AppTown.Reason, "before limits were recorded") {
		t.Fatalf("with an unrecorded generation: %+v %+v", got[0], got[1])
	}
}

// A first release that has not settled is read too: its boot is when a
// newly limited container is likeliest to be killed, and nothing has
// recorded its container yet. The health controller still leaves it alone.
func TestAFirstReleasesKillIsRecordedBeforeAnythingSettles(t *testing.T) {
	t.Parallel()
	w := newDigestWorldWith(t, "", sizedNeedYAML, nil)
	if _, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "first", w.lab2.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	first := mustDeployment(t, w, "first")
	if _, created, err := w.s.Outcomes.Admit(t.Context(), deploymentStartFor(first, "its first release", "first")); err != nil || !created {
		t.Fatalf("opening the first release: %v %v", created, err)
	}
	at := time.Now().Add(-time.Minute).UTC()
	db := w.names(t, "first").Service("postgres")
	w.hosts.daemon("lab2").SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "events" {
			return []byte(eventLine("oom", w.names(t, "first").App(), at, 0) + eventLine("oom", db, at.Add(time.Second), 0)), nil, true
		}
		return nil, nil, false
	})
	w.s.HealthController().Sweep(t.Context())
	got, err := w.s.LimitEvents(t.Context(), first, at.Add(-time.Hour))
	if err != nil || len(got) != 2 || got[0].Kind != LimitOutOfMemory || got[1].Part != "postgres" {
		t.Fatalf("the first release's kills: %+v %v", got, err)
	}
	for _, c := range w.hosts.commands("lab2") {
		if strings.HasPrefix(c, "restart") || strings.HasPrefix(c, "stop") {
			t.Fatalf("the health controller acted on an open release: %s", c)
		}
	}
}

// What was read before the frontier is context, never judged again: a
// stop's exit stays a stop's though the read that next covers it no longer
// sees the stop, and a new exit after a stop in that context is still a
// stop's, while one with no stop is recorded.
func TestAStoppedContainersExitIsNeverRecordedAsItsOwn(t *testing.T) {
	t.Parallel()
	w := newDigestWorldWith(t, "", digestAppYAML, nil)
	b := w.publish(t)
	if rel, err := w.promote(t, b.ID, "prod", w.lab); err != nil || rel.State != Released {
		t.Fatalf("release: %+v %v", rel, err)
	}
	stack := w.names(t, "prod").Stack()
	a, bb, cc := docker.Current.Service(stack, "a"), docker.Current.Service(stack, "b"), docker.Current.Service(stack, "c")
	t0 := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Second)
	lab := eventLine("kill", a, t0, 0) + // A is stopped
		eventLine("die", a, t0.Add(8*time.Second), 143) + // and exits, slowly
		eventLine("die", bb, t0.Add(15*time.Second), 1) // B exits on its own
	w.hosts.daemon("lab").SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "events" {
			var sec, nsec int64
			fmt.Sscanf(args[2], "%d.%d", &sec, &nsec)
			from := time.Unix(sec, nsec)
			var out strings.Builder
			for _, line := range strings.SplitAfter(lab, "\n") {
				var e struct{ TimeNano int64 }
				if line != "" && json.Unmarshal([]byte(line), &e) == nil && !time.Unix(0, e.TimeNano).Before(from) {
					out.WriteString(line)
				}
			}
			return []byte(out.String()), nil, true
		}
		return nil, nil, false
	})
	w.s.RecordLimitEvents(t.Context())
	// C is stopped just before B's exit and exits just after; D is new.
	lab += eventLine("kill", cc, t0.Add(14*time.Second), 0) + eventLine("die", cc, t0.Add(16*time.Second), 143) +
		eventLine("die", docker.Current.Service(stack, "d"), t0.Add(20*time.Second), 2)
	w.s.RecordLimitEvents(t.Context())
	got, err := w.s.LimitEvents(t.Context(), mustDeployment(t, w, "prod"), t0.Add(-time.Hour))
	if err != nil || len(got) != 2 || got[0].Part != "b" || got[1].Part != "d" {
		t.Fatalf("recorded %+v %v: only B and D exited on their own", got, err)
	}
}

// An open first release on a target AppTown does not inspect may have
// applied its limits: AppTown's declared sum there is unknown, not zero.
func TestAnOpenFirstReleaseMakesTheDeclaredSumUnknown(t *testing.T) {
	t.Parallel()
	p := newPromotionTargetsWith(t, false, "app:\n  web:\n    public: true\n    static: site\n    ports: [\"8080\"]\n    resources: { memory: 256M }\n")
	intranet := p.target(t, IntranetTargetName)
	if _, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, "office", intranet.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	c, err := p.s.Capacity(t.Context(), IntranetTargetName)
	if err != nil || !c.Memory.Promised.Sum.AppTown.Known || c.Memory.Promised.Sum.AppTown.Value != 0 {
		t.Fatalf("nothing released or open: %+v %v", c.Memory.Promised, err)
	}
	office := mustDeployment(t, p.digestWorld, "office")
	if _, created, err := p.s.Outcomes.Admit(t.Context(), deploymentStartFor(office, "a first apply, its answer lost", "office")); err != nil || !created {
		t.Fatalf("opening the release: %v %v", created, err)
	}
	p.s.capacity.mu.Lock()
	p.s.capacity.read = nil
	p.s.capacity.mu.Unlock()
	c, err = p.s.Capacity(t.Context(), IntranetTargetName)
	if err != nil || c.Memory.Promised.Sum.AppTown.Known || c.CPU.Promised.Sum.AppTown.Known {
		t.Fatalf("with an open first release: %+v %v", c.Memory.Promised, err)
	}
}
