package deployment

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/dockerlabel"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/poll"
)

// profiledAppYAML is the world's app with resources and a production
// profile: production gives the worker 1G and a greeting of its own.
const profiledAppYAML = `app:
  web:
    public: true
    static: site
    ports: ["8080"]
    resources: { memory: 128M }
  worker:
    build: worker
    environment:
      GREETING: hello
    resources: { memory: 256M, cpu: 0.5 }
  cache:
    image: %[1]s/upstream/cache:7
init:
  - name: migrate
    build: migrations
  - name: seed
    image: %[1]s/upstream/seed:1
profiles:
  production:
    worker:
      resources: { memory: 1G }
      environment: { GREETING: hi }
    web:
      resources: { memory: 512M }
`

// ranWith is the run of the container named, on target, since the
// commands numbered from.
func ranWith(t *testing.T, w *digestWorld, target, name string, from int) string {
	t.Helper()
	runs := w.hosts.ran(target)
	for i := len(runs) - 1; i >= from && i >= 0; i-- {
		if strings.Contains(runs[i], "--name "+name+" ") {
			return runs[i]
		}
	}
	t.Fatalf("%s never ran on %s:\n%s", name, target, strings.Join(runs, "\n"))
	return ""
}

func profiledWorld(t *testing.T) (*digestWorld, *secret.Service) {
	var secrets *secret.Service
	w := newDigestWorldWith(t, "", profiledAppYAML, func(_ *digestWorld, rt *testRuntime) { secrets = rt.Secrets })
	return w, secrets
}

// A release runs under the deployment's one profile: its containers hold
// the profile's limits and environment, and the generation records what it
// applied. A rebuild of that generation runs what it recorded, not a
// profile chosen since.
func TestADeploymentRunsItsProfileAndARebuildRunsWhatItsGenerationApplied(t *testing.T) {
	t.Parallel()
	w, secrets := profiledWorld(t)
	b := w.publish(t)
	if _, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "prod", w.lab.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Profiles.Set(t.Context(), w.project, "prod", "production"); err != nil {
		t.Fatal(err)
	}
	if rel, err := w.promote(t, b.ID, "prod", w.lab); err != nil || rel.State != Released {
		t.Fatalf("release: %+v %v\n%s", rel, err, strings.Join(w.hosts.commands("lab"), "\n"))
	}
	stack := docker.StackID("p", "prod")
	worker := ranWith(t, w, "lab", docker.ServiceName(stack, "worker"), 0)
	for _, want := range []string{"--memory 1073741824 --memory-swap 1073741824", "--cpus 0.5", "GREETING=hi"} {
		if !strings.Contains(worker, want) {
			t.Errorf("the worker ran without %q under production: %s", want, worker)
		}
	}
	if web := ranWith(t, w, "lab", docker.ContainerName("p", "prod"), 0); !strings.Contains(web, "--memory 536870912") {
		t.Errorf("the front door ran without production's 512M: %s", web)
	}
	dep, err := w.s.Deployments.ByName(t.Context(), w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	applied, err := w.s.SettledApplied(t.Context(), dep)
	if err != nil || applied.Profile != "production" || applied.How != appcontract.ProfileChosen ||
		applied.Parts["worker"].Resources != (appcontract.Resources{MemoryBytes: 1 << 30, CPUs: 0.5}) || !applied.Parts["web"].Public {
		t.Fatalf("the generation's record: %+v %v", applied, err)
	}

	// Another profile is chosen; the container goes; the rebuild runs the
	// generation as it ran.
	if err := secrets.Profiles.Set(t.Context(), w.project, "prod", "default"); err != nil {
		t.Fatal(err)
	}
	w.removeContainer(t, "lab", docker.ContainerName("p", "prod"))
	from := len(w.hosts.ran("lab"))
	if out, err := w.rebuild(t, dep); err != nil || out["skipped"] != nil {
		t.Fatalf("rebuild: %v %v", out, err)
	}
	if web := ranWith(t, w, "lab", docker.ContainerName("p", "prod"), from); !strings.Contains(web, "--memory 536870912") {
		t.Errorf("the rebuild ran the front door without its generation's 512M: %s", web)
	}
	if worker := ranWith(t, w, "lab", docker.ServiceName(stack, "worker"), from); !strings.Contains(worker, "--memory 1073741824") || !strings.Contains(worker, "GREETING=hi") {
		t.Errorf("the rebuild ran the worker without its generation's profile: %s", worker)
	}

	// The next release takes the profile chosen since.
	from = len(w.hosts.ran("lab"))
	if rel, err := w.promote(t, b.ID, "prod", w.lab); err != nil || rel.State != Released {
		t.Fatalf("second release: %+v %v", rel, err)
	}
	if worker := ranWith(t, w, "lab", docker.ServiceName(stack, "worker"), from); !strings.Contains(worker, "--memory 268435456") || !strings.Contains(worker, "GREETING=hello") {
		t.Errorf("the release after choosing default ran the worker as %s", worker)
	}
}

// An operator's override is a recorded act by someone who may deploy the
// deployment, with a reason, of a part its app has. It applies over the
// profile at the deploy, and a deploy whose committed profile declares
// the same clears it, recorded as the forge's act.
func TestAnOverrideIsARecordedActAndTheCommittedProfileClearsIt(t *testing.T) {
	t.Parallel()
	w, secrets := profiledWorld(t)
	b := w.publish(t)
	if _, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "prod", w.lab.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	set := func(part, memory, reason string) (OverrideResult, error) {
		m := int64(0)
		if memory != "" {
			var err error
			if m, err = appcontract.ParseMemory(memory); err != nil {
				t.Fatal(err)
			}
		}
		return w.s.SetOverride(ctx, OverrideChange{ProjectID: w.project, Deployment: "prod", Part: part, Resources: appcontract.Resources{MemoryBytes: m}, Reason: reason})
	}
	if _, err := set("worker", "2G", "  "); !errors.Is(err, ErrOverrideReason) {
		t.Errorf("an override without a reason: %v", err)
	}
	if _, err := set("api", "2G", "spike"); err == nil || !strings.Contains(err.Error(), `no part or need "api"`) {
		t.Errorf("an override of a part the app has not: %v", err)
	}
	auth := w.deps.Releases.Authority.(*requestAuthority)
	denied := errors.New("no deploy grant")
	auth.deny = denied
	if _, err := set("worker", "2G", "spike"); !errors.Is(err, denied) {
		t.Errorf("an override without the deploy grant: %v", err)
	}
	auth.deny = nil
	seen := len(auth.seen)

	// Nothing runs yet: recorded, applied at the deploy.
	out, err := set("worker", "2G", "the import job ran out of memory")
	if err != nil || out.Override == nil || out.Release != nil || out.NotYet == "" {
		t.Fatalf("an override before anything runs: %+v %v", out, err)
	}
	if got := auth.seen[seen]; got.Capability != access.CapDeploymentDeploy || !got.Context.Consequential {
		t.Errorf("the override asked %+v; want the deploy grant, as a consequential act", got)
	}
	// The production web's own 512M, set as an override: the committed
	// profile declares it, so the deploy clears it.
	if err := secrets.Profiles.Set(t.Context(), w.project, "prod", "production"); err != nil {
		t.Fatal(err)
	}
	if _, err := set("web", "512M", "same as production"); err != nil {
		t.Fatal(err)
	}
	if rel, err := w.promote(t, b.ID, "prod", w.lab); err != nil || rel.State != Released {
		t.Fatalf("release: %+v %v", rel, err)
	}
	worker := ranWith(t, w, "lab", docker.ServiceName(docker.StackID("p", "prod"), "worker"), 0)
	if !strings.Contains(worker, "--memory 2147483648") || !strings.Contains(worker, "--cpus 0.5") {
		t.Errorf("the worker ran without its override over the profile (2G, the profile's cpu): %s", worker)
	}
	overrides, err := w.s.Overrides(ctx, mustDeployment(t, w, "prod").ID)
	if err != nil {
		t.Fatal(err)
	}
	if o, ok := overrides["worker"]; !ok || o.SetBy != "alice" || o.Reason != "the import job ran out of memory" {
		t.Errorf("the worker's override: %+v", overrides)
	}
	if _, ok := overrides["web"]; ok {
		t.Errorf("the web's override stands though the committed profile declares the same")
	}
	// The read is the deployment read grant's.
	reader := New(context.Background(), w.db, func() Dependencies { d := w.deps; d.Authority = auth; return d }())
	res, err := reader.Resources(ctx, w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range res.Parts {
		if p.Part == "worker" && (p.Override == nil || !p.OverrideApplied || p.Memory != "2G") {
			t.Errorf("the worker's resources read %+v", p)
		}
	}
	kinds := map[string]int{}
	entries, err := audit.Recent(ctx, w.db, audit.Filter{ProjectID: w.project, KindPrefix: "deployment.resources", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		kinds[e.Kind+" by "+e.Actor]++
	}
	if kinds["deployment.resources-overridden by alice"] != 2 || kinds["deployment.resources-override-cleared by apptown-system"] != 1 {
		t.Errorf("the recorded acts: %v", kinds)
	}

	// Once it runs, setting an override redeploys the running bundle.
	out, err = set("worker", "3G", "still not enough")
	if err != nil || out.Release == nil || out.Release.Deployment != "prod" {
		t.Fatalf("an override while it runs: %+v %v", out, err)
	}
}

func mustDeployment(t *testing.T, w *digestWorld, name string) *Deployment {
	t.Helper()
	dep, err := w.s.Deployments.ByName(t.Context(), w.project, name)
	if err != nil {
		t.Fatal(err)
	}
	return dep
}

// Memory use is what the deployment's host reports for each part's own
// container; a part it does not report is unknown with the reason, never
// zero; a target that reports nothing says so.
func TestMemoryUseIsWhatTheHostReportsOrUnknown(t *testing.T) {
	t.Parallel()
	w, _ := profiledWorld(t)
	b := w.publish(t)
	if _, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "prod", w.lab.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	if rel, err := w.promote(t, b.ID, "prod", w.lab); err != nil || rel.State != Released {
		t.Fatalf("release: %+v %v", rel, err)
	}
	web, worker := docker.ContainerName("p", "prod"), docker.ServiceName(docker.StackID("p", "prod"), "worker")
	var mu sync.Mutex
	inspected := map[string]int{}
	replaced := "" // a container whose second inspection finds another container
	w.hosts.daemon("lab").SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "stats" {
			return []byte(web + "\t11.5MiB / 128MiB\n" + worker + "\t200MiB / 256MiB\n"), nil, true
		}
		if len(args) == 4 && args[0] == "inspect" && args[2] == docker.RuntimeFormat {
			mu.Lock()
			defer mu.Unlock()
			inspected[args[3]]++
			id := "id-" + args[3]
			if args[3] == replaced && inspected[args[3]] > 1 {
				id = "id-new-" + args[3]
			}
			limit := map[string]int64{web: 128 << 20, worker: 0}[args[3]]
			return []byte(fmt.Sprintf(`{"id":%q,"image":"i","running":true,"labels":{},"memoryLimit":%d}`, id, limit)), nil, true
		}
		return nil, nil, false
	})
	observe := func() map[string]PartMemory {
		got, err := w.s.ObservedMemory(t.Context(), mustDeployment(t, w, "prod"))
		if err != nil {
			t.Fatal(err)
		}
		use := map[string]PartMemory{}
		for _, m := range got {
			use[m.Part] = m
		}
		return use
	}
	use := observe()
	if m := use["web"]; !m.Known || m.UsedBytes != int64(11.5*(1<<20)) || m.LimitBytes != 128<<20 {
		t.Errorf("web, with the limit its container is held to: %+v", m)
	}
	if m := use["worker"]; !m.Known || m.UsedBytes != 200<<20 || m.LimitBytes != 0 {
		t.Errorf("worker, held to no limit: %+v", m)
	}
	// A container replaced while it was observed (a deploy, an override)
	// answers for nothing: its use is unknown, never another container's.
	mu.Lock()
	replaced, inspected = web, map[string]int{}
	mu.Unlock()
	if m := observe()["web"]; m.Known || !strings.Contains(m.Reason, "replaced while it was being observed") {
		t.Errorf("a replaced container: %+v", m)
	}
	mu.Lock()
	replaced = ""
	mu.Unlock()
	if m := use["cache"]; m.Known || m.UsedBytes != 0 || m.Reason == "" {
		t.Errorf("a part the host did not report: %+v", m)
	}
	// A deployment with no container of the forge's (App Platform) is
	// unknown, with the reason.
	platform := *mustDeployment(t, w, "prod")
	platform.Container = ""
	got, err := w.s.ObservedMemory(t.Context(), &platform)
	if err != nil || len(got) == 0 || got[0].Known || !strings.Contains(got[0].Reason, "does not report") {
		t.Errorf("a target that reports nothing: %+v %v", got, err)
	}
}

// On App Platform a part runs on the cheapest size that covers its
// declaration, recorded with the generation. A declaration no size covers,
// or a size list that cannot be read, fails the release before the forge
// writes anything at DigitalOcean: no database (the refused apps need a
// managed Postgres), no registry, no app.
func TestAppPlatformFitsEachPartBeforeAnyEffect(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		memory string
		fail   bool
		want   string // in the release's log; "" releases
	}{
		"a covered declaration":   {memory: "768M"},
		"no size covers it":       {memory: "64G", want: "no App Platform instance size has 64G"},
		"the list cannot be read": {memory: "1G", fail: true, want: "reading App Platform's instance sizes"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app := "app:\n  web:\n    public: true\n    static: site\n    ports: [\"8080\"]\n    resources: { memory: " + tc.memory + " }\n"
			if tc.want != "" {
				// A managed database is the first effect a release makes.
				app += "needs:\n  postgres:\n    version: \"16\"\n"
			}
			p := newPromotionTargetsWith(t, false, app)
			// main lands on the lab host first; it must hold what App Platform
			// is asked to round (a Docker host refuses a part larger than it).
			p.hosts.daemon("lab").SetHostInfo(128<<30, 64, "/var/lib/docker")
			b := p.land(t)
			if tc.fail {
				p.do.Fail("GET", "/v2/apps/tiers/instance_sizes", 503)
			}
			before := len(p.do.Snapshot().Requests)
			rel, err := p.promote(t, b.ID, "cloud", p.target(t, "do-syd"))
			events, _ := p.s.Releases.Events(context.Background(), rel.ID)
			var log []string
			for _, e := range events {
				log = append(log, e.Text)
			}
			if tc.want == "" {
				if err != nil || rel.State != Released {
					t.Fatalf("the release: %+v %v\n%s", rel, err, strings.Join(log, "\n"))
				}
				applied, err := p.s.SettledApplied(t.Context(), mustDeployment(t, p.digestWorld, "cloud"))
				if err != nil || applied.Parts["web"].RunsOn != "apps-s-1vcpu-1gb-fixed (1G, 1 CPU)" {
					t.Errorf("the generation's record of what web runs on: %+v %v", applied, err)
				}
				if spec := p.do.Snapshot().Specs; len(spec) != 1 || !strings.Contains(asJSON(spec[0]), `"instance_size_slug":"apps-s-1vcpu-1gb-fixed"`) {
					t.Errorf("the app spec: %v", spec)
				}
				// The page says what was declared and what it runs on; no
				// figure on a size is "no limit".
				reader := New(context.Background(), p.db, func() Dependencies { d := p.deps; d.Authority = &requestAuthority{}; return d }())
				res, err := reader.Resources(t.Context(), p.project, "cloud")
				if err != nil || len(res.Parts) != 1 {
					t.Fatalf("the resources read: %+v %v", res, err)
				}
				if web := res.Parts[0]; web.Memory != "768M declared" || web.CPU != "not declared" || web.RunsOn != "apps-s-1vcpu-1gb-fixed (1G, 1 CPU)" {
					t.Errorf("the web part reads %+v", web)
				}
				return
			}
			if rel == nil || rel.State == Released || !strings.Contains(strings.Join(log, "\n"), tc.want) {
				t.Fatalf("the release: %+v %v; want it failed saying %q\n%s", rel, err, tc.want, strings.Join(log, "\n"))
			}
			for _, r := range p.do.Snapshot().Requests[before:] {
				if r.Method != "GET" {
					t.Errorf("%s %s before the refusal", r.Method, r.Path)
				}
			}
		})
	}
}

// Two inputs are the same work only when every field is: a field added to
// Inputs that Same forgot would let a stored key admit other work.
func TestInputsAreTheSameOnlyWhenEveryFieldIs(t *testing.T) {
	t.Parallel()
	base := Inputs{Applied: AppliedProfile{Profile: "default", Parts: map[string]AppliedPart{"web": {}}}}
	v := reflect.ValueOf(&base).Elem()
	for i := 0; i < v.NumField(); i++ {
		other := base
		f := reflect.ValueOf(&other).Elem().Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("changed")
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Array: // ids.ID
			f.Index(0).SetUint(1)
		case reflect.Struct: // the applied record
			other.Applied = AppliedProfile{Profile: "production"}
		default:
			t.Fatalf("Inputs.%s is a %s this test cannot change: teach it, and Same", v.Type().Field(i).Name, f.Kind())
		}
		if base.Same(other) {
			t.Errorf("inputs differing in %s are called the same", v.Type().Field(i).Name)
		}
	}
	if !base.Same(base) {
		t.Errorf("inputs are not the same as themselves")
	}
}

// A preview boots under the preview profile when a file declares one: the
// prepared contract, which the boot runs, carries it.
func TestAPreviewBootsUnderThePreviewProfile(t *testing.T) {
	t.Parallel()
	w, _ := profiledWorld(t)
	c, err := appcontract.Parse([]byte("app:\n  web: {image: app:1, public: true, resources: {memory: 1G}}\nneeds: {mariadb: {}}\nprofiles:\n  preview:\n    mariadb: {size: small}\n    web: {resources: {memory: 256M}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := w.s.env.PrepareBoot(t.Context(), w.project, "p", "preview", t.TempDir(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Contract.Parts[0].Resources.MemoryBytes; got != 256<<20 {
		t.Errorf("the preview's prepared web memory is %d, want the preview profile's 256M", got)
	}
	if len(p.Needs) != 1 || p.Needs[0].Resources.MemoryBytes != 128<<20 || !strings.Contains(strings.Join(p.Needs[0].Command, " "), "--innodb-buffer-pool-size=32M") {
		t.Fatalf("preview did not carry resolved size to worker: %+v", p.Needs)
	}

}

// landAs is a landing of b on main by run, as the deploy step of that
// run's landing does.
func landAs(t *testing.T, w *digestWorld, b *ReleaseBundle, run ids.ID) {
	t.Helper()
	dep, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "main", w.lab.ID, Attributes{Feed: "landing", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	art, cleanup, err := docker.PrepareSource(context.Background(), w.repo, w.revision, "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	out, err := w.s.RunLanding(context.Background(), landingClaim{w.project, run}, LandingInput{
		Start:  Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: fmt.Sprintf("run-%d-deploy", run), Inputs: Inputs{ReleaseRun: run, Revision: w.revision, ArtifactHash: art.Hash, DockerSource: string(art.Body), BundleID: b.ID}},
		Source: *art.Source, DataClass: "synthetic", Bundle: b,
	})
	if err != nil || out.Boot.Err != nil || out.Execution.Outcome != Applied {
		t.Fatalf("landing by run %s: %+v %v %v", run, out.Execution, err, out.Boot.Err)
	}
}

// A landing, the ordinary way main is deployed, runs main's profile and
// its operator's override, and records them: a later landing keeps the
// override, and a rebuild runs what the landing applied.
func TestALandingRunsMainsProfileAndKeepsItsOverride(t *testing.T) {
	t.Parallel()
	w, secrets := profiledWorld(t)
	b := w.publish(t)
	if err := secrets.Profiles.Set(t.Context(), w.project, "main", "production"); err != nil {
		t.Fatal(err)
	}
	landAs(t, w, b, idstest.N(801))
	worker := docker.ServiceName(docker.StackID("p", "main"), "worker")
	if run := ranWith(t, w, "lab", worker, 0); !strings.Contains(run, "--memory 1073741824") || !strings.Contains(run, "GREETING=hi") {
		t.Errorf("the landing ran the worker without main's production profile: %s", run)
	}
	if _, err := w.s.SetOverride(context.Background(), OverrideChange{ProjectID: w.project, Deployment: "main", Part: "worker",
		Resources: appcontract.Resources{MemoryBytes: 3 << 30}, Reason: "an import ran out of memory"}); err != nil {
		t.Fatal(err)
	}
	from := len(w.hosts.ran("lab"))
	landAs(t, w, b, idstest.N(802))
	if run := ranWith(t, w, "lab", worker, from); !strings.Contains(run, "--memory 3221225472") {
		t.Errorf("the next landing dropped the override: %s", run)
	}
	main := mustDeployment(t, w, "main")
	if applied, err := w.s.SettledApplied(t.Context(), main); err != nil || applied.Profile != "production" || applied.Parts["worker"].Override.MemoryBytes != 3<<30 {
		t.Errorf("the landing's record: %+v %v", applied, err)
	}
	w.removeContainer(t, "lab", docker.ContainerName("p", "main"))
	from = len(w.hosts.ran("lab"))
	if out, err := w.rebuild(t, main); err != nil || out["skipped"] != nil {
		t.Fatalf("rebuild: %v %v", out, err)
	}
	if run := ranWith(t, w, "lab", worker, from); !strings.Contains(run, "--memory 3221225472") {
		t.Errorf("the rebuild of the landing's generation ran the worker as %s", run)
	}
}

// A release App Platform refuses for its resources changes nothing
// anywhere first: not the deployment's AI models key at OpenRouter, not a
// database or app at DigitalOcean.
func TestAnAppPlatformResourceRefusalComesBeforeTheAIModelsKey(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		memory string
		fail   bool
	}{"no size covers it": {memory: "64G"}, "the list cannot be read": {memory: "1G", fail: true}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			service, provider, _, pid, dep, bundle := doReleaseWorldNeeding(t, "  llm:\n    limit: 4\nprofiles:\n  big:\n    web: {resources: {memory: "+tc.memory+"}}\n")
			f := withLLM(t, service, pid, true)
			if err := service.env.secrets.Profiles.Set(t.Context(), pid, dep.Name, "big"); err != nil {
				t.Fatal(err)
			}
			if tc.fail {
				provider.Fail("GET", "/v2/apps/tiers/instance_sizes", 503)
			}
			accepted := doAcceptRelease(t, service, pid, dep, bundle)
			_ = service.ExecuteRelease(context.Background(), accepted.ID)
			rel, err := service.Releases.ByNumber(context.Background(), pid, accepted.Number)
			if err != nil || rel.State == Released {
				t.Fatalf("the release: %+v %v", rel, err)
			}
			for _, m := range []string{"POST", "PATCH", "DELETE"} {
				if n := f.CallsOf(m); n != 0 {
					t.Errorf("OpenRouter was sent %d %s before the refusal", n, m)
				}
			}
			for _, r := range provider.Snapshot().Requests {
				if r.Method != "GET" {
					t.Errorf("DigitalOcean was sent %s %s before the refusal", r.Method, r.Path)
				}
			}
		})
	}
}

// An override of the intranet deployment redeploys the bundle it runs
// through the intranet release, so Portainer is given the new limit now;
// clearing it gives the stack back its declared one. The redeploy is
// recorded like any intranet release.
func TestAnIntranetOverrideRedeploysTheRunningBundleThroughPortainer(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	b := p.land(t)
	if rel := p.releaseLAN(t); rel.State != Released {
		t.Fatalf("the intranet release: %+v", rel)
	}
	cfg, err := p.s.IntranetConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wait := func(out OverrideResult) *Release {
		t.Helper()
		if out.Release == nil {
			t.Fatalf("the override started no redeploy: %+v", out)
		}
		var rel *Release
		if !poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, func() bool {
			var err error
			if rel, err = p.s.Releases.ByNumber(context.Background(), p.project, out.Release.Number); err != nil {
				t.Fatal(err)
			}
			return rel.State != Releasing
		}) {
			t.Fatalf("release %d is still releasing", out.Release.Number)
		}
		return rel
	}
	lastStack := func() string {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.stacks[len(p.stacks)-1]
	}
	// worker is the worker service's lines: those after "  worker:" up to
	// the next service, indented less.
	worker := func(compose string) string {
		_, after, _ := strings.Cut(compose, "\n  worker:\n")
		var lines []string
		for _, line := range strings.Split(after, "\n") {
			if !strings.HasPrefix(line, "    ") {
				break
			}
			lines = append(lines, line)
		}
		return strings.Join(lines, "\n")
	}
	out, err := p.s.SetOverride(context.Background(), OverrideChange{ProjectID: p.project, Deployment: cfg.ReleaseDeployment, Part: "worker",
		Resources: appcontract.Resources{MemoryBytes: 512 << 20}, Reason: "the intranet's worker needs more"})
	if err != nil {
		t.Fatal(err)
	}
	if rel := wait(out); rel.State != Released || rel.BundleID != b.ID {
		t.Fatalf("the redeploy: %+v", rel)
	}
	if w := worker(lastStack()); !strings.Contains(w, "mem_limit: 536870912") || !strings.Contains(w, "memswap_limit: 536870912") {
		t.Errorf("Portainer was not given the override:\n%s", lastStack())
	}
	dep, err := p.s.intranetDeployment(t.Context(), p.project, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if applied, err := p.s.SettledApplied(t.Context(), dep); err != nil || applied.Parts["worker"].Override.MemoryBytes != 512<<20 {
		t.Errorf("the redeploy's record: %+v %v", applied, err)
	}
	out, err = p.s.SetOverride(context.Background(), OverrideChange{ProjectID: p.project, Deployment: cfg.ReleaseDeployment, Part: "worker", Reason: "back to declared"})
	if err != nil {
		t.Fatal(err)
	}
	if rel := wait(out); rel.State != Released {
		t.Fatalf("the redeploy after clearing: %+v", rel)
	}
	if w := worker(lastStack()); !strings.Contains(w, "mem_limit: 536870912") {
		t.Errorf("clearing didn't restore the fresh fallback cap:\n%s", w)
	}
	dep, err = p.s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a, err := p.s.SettledApplied(t.Context(), dep); err != nil || a.Parts["worker"].Override.Declared() || a.Parts["worker"].MemoryDefault == nil {
		t.Errorf("clearing retained override instead of default: %+v %v", a, err)
	}
}

// releaseDenied allows everything but the release grant.
type releaseDenied struct{}

func (releaseDenied) Require(_ context.Context, p access.Permission) (access.Caller, error) {
	if p.Capability == access.CapReleasePromote {
		return access.Caller{}, errReleaseDenied
	}
	return access.Caller{Principal: access.Principal{Name: "carol"}, Verified: true}, nil
}

var errReleaseDenied = errors.New("no release grant")

// An override of the intranet deployment needs the release grant, as an
// intranet release does. Someone with only the deploy grant is refused,
// and so is anyone when the forge cannot read which target the deployment
// is on: an authority input it cannot read never lowers the authority
// asked. A refusal changes nothing: the override stands as it was, and no
// act or release is recorded.
func TestAnIntranetOverrideNeedsTheReleaseGrantEvenWhenItsTargetCannotBeRead(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	p.land(t)
	if rel := p.releaseLAN(t); rel.State != Released {
		t.Fatalf("the intranet release: %+v", rel)
	}
	cfg, err := p.s.IntranetConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := p.s.SetOverride(ctx, OverrideChange{ProjectID: p.project, Deployment: cfg.ReleaseDeployment, Part: "worker", Resources: appcontract.Resources{MemoryBytes: 512 << 20}, Reason: "set by someone who may"}); err != nil {
		t.Fatal(err)
	}
	deps := p.deps
	deps.Releases.Authority = releaseDenied{}
	carol := New(context.Background(), p.db, deps)
	dep, err := p.s.intranetDeployment(t.Context(), p.project, cfg)
	if err != nil {
		t.Fatal(err)
	}
	acts := func() int {
		entries, err := audit.Recent(ctx, p.db, audit.Filter{ProjectID: p.project, KindPrefix: "deployment.resources", Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	last, err := p.s.Releases.Last(ctx, p.project, cfg.ReleaseDeployment)
	if err != nil {
		t.Fatal(err)
	}
	before := acts()
	for _, c := range []struct {
		name     string
		change   OverrideChange
		failRead bool
		want     error
	}{
		{"setting, without the release grant", OverrideChange{Part: "worker", Resources: appcontract.Resources{MemoryBytes: 2 << 30}, Reason: "more"}, false, errReleaseDenied},
		{"clearing, without the release grant", OverrideChange{Part: "worker", Reason: "less"}, false, errReleaseDenied},
		{"setting, its target unread", OverrideChange{Part: "worker", Resources: appcontract.Resources{MemoryBytes: 2 << 30}, Reason: "more"}, true, nil},
		{"clearing, its target unread", OverrideChange{Part: "worker", Reason: "less"}, true, nil},
	} {
		c.change.ProjectID, c.change.Deployment = p.project, cfg.ReleaseDeployment
		if c.failRead {
			p.db.FailNextStatement("FROM targets WHERE id")
		}
		_, err := carol.SetOverride(ctx, c.change)
		if c.want != nil && !errors.Is(err, c.want) || c.want == nil && err == nil {
			t.Errorf("%s: %v, want a refusal", c.name, err)
		}
		overrides, err := p.s.Overrides(ctx, dep.ID)
		if err != nil || overrides["worker"].Resources.MemoryBytes != 512<<20 {
			t.Errorf("%s changed the override: %+v %v", c.name, overrides, err)
		}
	}
	if acts() != before {
		t.Errorf("a refused override recorded an act")
	}
	if now, err := p.s.Releases.Last(ctx, p.project, cfg.ReleaseDeployment); err != nil || now.ID != last.ID {
		t.Errorf("a refused override started a release: %+v %v", now, err)
	}
}

// A deployment's memory is observed only while its public container is the
// generation the deployment recorded, before and after the sample: settled,
// it is that generation's commit and each part's use with its container's
// limit; a replacement already running (a deploy or an override not yet
// settled), or one that replaces it during the sample, answers for nothing.
func TestMemoryIsObservedOnlyWhileTheRecordedGenerationRuns(t *testing.T) {
	t.Parallel()
	w, _ := profiledWorld(t)
	b := w.publish(t)
	if _, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "prod", w.lab.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	if rel, err := w.promote(t, b.ID, "prod", w.lab); err != nil || rel.State != Released {
		t.Fatalf("release: %+v %v", rel, err)
	}
	dep := mustDeployment(t, w, "prod")
	web := docker.ContainerName("p", "prod")
	var mu sync.Mutex
	generation, swapAt, inspections, failRun := strconv.FormatInt(dep.ObservedGeneration, 10), 0, 0, false
	w.hosts.daemon("lab").SetReply(func(args []string) ([]byte, error, bool) {
		mu.Lock()
		fail := failRun
		mu.Unlock()
		if fail && len(args) > 0 && args[0] == "run" {
			return []byte("no room on the host"), errors.New("exit status 125"), true
		}
		if len(args) > 0 && args[0] == "stats" {
			return []byte(web + "\t100MiB / 128MiB\n"), nil, true
		}
		if len(args) == 4 && args[0] == "inspect" && args[2] == docker.RuntimeFormat {
			mu.Lock()
			defer mu.Unlock()
			id := "id-" + args[3]
			if args[3] == web {
				inspections++
				if swapAt > 0 && inspections >= swapAt {
					id = "id-replacement"
				}
			}
			return []byte(fmt.Sprintf(`{"id":%q,"image":"i","running":true,"labels":{%q:%q,%q:%q},"memoryLimit":%d}`,
				id, dockerlabel.Deployment, dep.ID.String(), dockerlabel.Generation, generation, 128<<20)), nil, true
		}
		return nil, nil, false
	})
	got, err := w.s.MemoryOfRecordedGeneration(t.Context(), dep)
	if err != nil || got.Commit != dep.ReleaseSHA || got.Reason != "" {
		t.Fatalf("the settled generation: %+v %v", got, err)
	}
	var web0 PartMemory
	for _, m := range got.Parts {
		if m.Part == "web" {
			web0 = m
		}
	}
	if !web0.Known || web0.UsedBytes != 100<<20 || web0.LimitBytes != 128<<20 {
		t.Errorf("its use and its container's limit: %+v", got.Parts)
	}
	// A replacement of another generation is already running.
	mu.Lock()
	generation = strconv.FormatInt(dep.ObservedGeneration+1, 10)
	mu.Unlock()
	if got, err = w.s.MemoryOfRecordedGeneration(t.Context(), dep); err != nil || got.Commit != "" || !strings.Contains(got.Reason, "not the generation") {
		t.Errorf("a replacement not yet settled: %+v %v", got, err)
	}
	// The container is replaced while the sample is taken: the last
	// inspection of the public container finds another one.
	mu.Lock()
	generation, inspections, swapAt = strconv.FormatInt(dep.ObservedGeneration, 10), 0, 4
	mu.Unlock()
	if got, err = w.s.MemoryOfRecordedGeneration(t.Context(), dep); err != nil || got.Commit != "" || !strings.Contains(got.Reason, "changed while it was being observed") {
		t.Errorf("replaced during the sample: %+v %v", got, err)
	}
	// A later rollout (the same bundle again) that failed before replacing the
	// public container: the old container still runs as its generation, but
	// the deployment has not settled on what was admitted since. The record
	// the review read first answers for nothing.
	mu.Lock()
	swapAt, failRun = 0, true
	mu.Unlock()
	if rel, err := w.promote(t, b.ID, "prod", w.lab); err == nil && rel.State == Released {
		t.Fatalf("the failing rollout released: %+v", rel)
	}
	if got, err = w.s.MemoryOfRecordedGeneration(t.Context(), dep); err != nil || got.Commit != "" || !(strings.Contains(got.Reason, "in progress") || strings.Contains(got.Reason, "not settled")) {
		t.Errorf("after an unsettled rollout: %+v %v", got, err)
	}
}

// A preview's boot refuses a contract's needs as a release does, before
// anything is prepared: the catalogue's refusal is the same everywhere.
func TestAPreviewBootRefusesTheNeedsAReleaseRefuses(t *testing.T) {
	t.Parallel()
	w, _ := profiledWorld(t)
	c, err := appcontract.Parse([]byte("app:\n  web: {image: app:1, public: true}\nneeds: {postgres: {password: sourdough}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.s.env.PrepareBoot(t.Context(), w.project, "p", "preview", t.TempDir(), c, nil)
	var boot *docker.BootError
	if !errors.As(err, &boot) || boot.Phase != docker.PhaseResolve || !strings.Contains(err.Error(), "AppTown sets the password of the postgres need") {
		t.Fatalf("a preview boot with a password parameter: %v", err)
	}
}
