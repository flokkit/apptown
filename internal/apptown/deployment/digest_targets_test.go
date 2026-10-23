package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/digitalocean"
	"github.com/flokkit/apptown/internal/deploy/digitalocean/dofake"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/poll"
)

// App Platform and Portainer by promotion (design/subsystems/deployment.md,
// "Release bundles"). A published digest bundle is received by App Platform (the
// DigitalOcean fake API, with an in-memory registry standing for DOCR) and
// Portainer (a fake API) by promotion, every image by its digest, and
// nothing runs on AppTown's own Docker.

const digestDOToken = "dop_v1_test"

// promotionTargets is the digest world with App Platform and Portainer
// attached: DOCR answers only the DigitalOcean token, and Portainer's
// endpoint answers platform (its docker/version body).
type promotionTargets struct {
	*digestWorld
	do       *dofake.API
	docr     *digestRegistry
	platform atomic.Pointer[string]
	mu       sync.Mutex
	stacks   []string // the compose files Portainer was given
	// volumes are the Docker volumes the environment holds: a stack's
	// create makes those its file declares.
	volumes map[string]bool
}

func asJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func newPromotionTargets(t *testing.T, noLocalDocker bool) *promotionTargets {
	t.Helper()
	return newPromotionTargetsWith(t, noLocalDocker, digestAppYAML)
}

// newPromotionTargetsWith is newPromotionTargets whose app is appYAML.
func newPromotionTargetsWith(t *testing.T, noLocalDocker bool, appYAML string) *promotionTargets {
	t.Helper()
	p := &promotionTargets{do: &dofake.API{}, docr: newDigestRegistry(t, "apptown", digestDOToken), volumes: map[string]bool{}}
	p.do.ServeFrontDoor(false)
	amd64 := `{"Os":"linux","Arch":"amd64"}`
	p.platform.Store(&amd64)
	api := httptest.NewServer(p.do.Handler())
	t.Cleanup(api.Close)
	portainer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/endpoints":
			fmt.Fprint(w, `[{"Id":3,"Name":"local"},{"Id":3,"Name":"office"}]`)
		case "/api/endpoints/3/docker/version":
			fmt.Fprint(w, *p.platform.Load())
		case "/api/endpoints/3/docker/info":
			fmt.Fprint(w, `{"MemTotal":17179869184,"NCPU":4}`)
		case "/api/stacks":
			fmt.Fprint(w, `[]`)
		case "/api/stacks/create/standalone/string":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			compose, _ := body["stackFileContent"].(string)
			name, _ := body["name"].(string)
			vols, err := portainer.ComposeVolumes(name, strings.TrimPrefix(name, "forge-"), compose)
			if err != nil {
				t.Error(err)
			}
			p.mu.Lock()
			p.stacks = append(p.stacks, compose)
			for _, v := range vols {
				p.volumes[v.Name] = true
			}
			p.mu.Unlock()
			fmt.Fprint(w, `{"Id":1}`)
		default:
			if name, ok := strings.CutPrefix(r.URL.Path, "/api/endpoints/3/docker/volumes/"); ok && r.Method == http.MethodGet {
				p.mu.Lock()
				there := p.volumes[name]
				p.mu.Unlock()
				if there {
					fmt.Fprintf(w, `{"Name":%q}`, name)
					return
				}
			}
			http.Error(w, "not faked", http.StatusNotFound)
		}
	}))
	t.Cleanup(portainer.Close)
	secrets := fmt.Sprintf("DIGITALOCEAN_ACCESS_TOKEN=%s\nPORTAINER_URL=%s\nPORTAINER_API_KEY=key\nAPPTOWN_APPS_DOMAIN=apps.example\n", digestDOToken, portainer.URL)
	p.digestWorld = newDigestWorldWith(t, secrets, appYAML, func(w *digestWorld, deps *testRuntime) {
		deps.DigitalOcean = digitalocean.Options{APIBase: api.URL, RegistryHost: p.docr.host, PollInterval: 10 * time.Millisecond, DBPoll: 10 * time.Millisecond,
			ProbeFrontDoor: func(context.Context, string, time.Time, func(string, ...any)) error { return nil }}
		deps.OfferedByConstruction = []string{IntranetTargetName}
		if noLocalDocker {
			// As an AppTown started with -no-local-docker: its own daemon refuses.
			deps.NoLocalDocker = true
			deps.Scope = docker.Scope{Client: docker.Refusing{Attached: w.hosts}}
		}
	})
	if _, err := p.s.env.integrations.Bindings.Save(context.Background(), "do-syd", digitalocean.CapCloudAccount, "digitalocean", asJSON(map[string]any{"tokenFrom": "DIGITALOCEAN_ACCESS_TOKEN", "region": "syd", "registry": "forge-test"}), "fixture"); err != nil {
		t.Fatal(err)
	}
	p.s.SeedCloudTargets(t.Context(), "fixture")
	// The intranet target, as AppTown records it at startup from its
	// Portainer configuration.
	cfg, err := p.s.env.integrations.PortainerTarget(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.s.Targets.Put(context.Background(), &Target{Name: IntranetTargetName, Kind: KindPortainer, Provider: "portainer-deploy", Origin: OriginAttached,
		Address:      map[string]any{"url": cfg.PortainerURL, AddressEnvironment: DefaultPortainerEnvironment, "tier": cfg.ReleaseDeployment},
		Capabilities: TargetCapabilities{PersistentVolumes: true, OwnLiveness: true, Rollout: []string{"replace"}},
		Ingress:      map[string]any{"binding": "traefik", "domain": cfg.AppsDomain},
		NeedsMode:    "emulators", SecretsDelivery: "inject", Status: TargetReady}); err != nil {
		t.Fatal(err)
	}
	return p
}

// land publishes the app's bundle, as a landing on lab does, and deploys
// it to main there.
func (p *promotionTargets) land(t *testing.T) *ReleaseBundle {
	t.Helper()
	b := p.publish(t)
	p.mainOf(t, b)
	return b
}

func (p *promotionTargets) target(t *testing.T, name string) *Target {
	t.Helper()
	target, err := p.s.Targets.ByName(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

// releaseLAN starts an intranet release and waits for it to end.
func (p *promotionTargets) releaseLAN(t *testing.T) *Release {
	t.Helper()
	proj, err := p.projects.ByID(context.Background(), p.project)
	if err != nil {
		t.Fatal(err)
	}
	out, code, err := p.s.ReleaseLAN(t.Context(), &proj, "boss", LANReleaseRequest{})
	if err != nil || code != 202 {
		t.Fatalf("an intranet release: %d %v", code, err)
	}
	var rel *Release
	if !poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, func() bool {
		var err error
		if rel, err = p.s.Releases.ByNumber(context.Background(), p.project, out.Number); err != nil {
			t.Fatal(err)
		}
		return rel.State != Releasing
	}) {
		t.Fatalf("release %d is still releasing", rel.Number)
	}
	return rel
}

// expectAppTownDockerIdle says AppTown's own Docker built, pushed and
// tagged nothing.
func (p *promotionTargets) expectAppTownDockerIdle(t *testing.T) {
	t.Helper()
	for _, c := range p.hosts.commands("") {
		for _, verb := range []string{"build", "buildx", "push", "tag", "login"} {
			if strings.HasPrefix(c, verb+" ") {
				t.Errorf("AppTown's own Docker ran %q", c)
			}
		}
	}
}

// componentImages are the digests an App Platform spec names, by
// component.
func componentImages(t *testing.T, spec map[string]any) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, kind := range []string{"services", "workers", "jobs"} {
		list, _ := spec[kind].([]any)
		for _, c := range list {
			comp := c.(map[string]any)
			img := comp["image"].(map[string]any)
			if img["registry_type"] != "DOCR" || img["repository"] != "p" {
				t.Errorf("%s's image: %v", comp["name"], img)
			}
			out[comp["name"].(string)] = img["digest"].(string)
		}
	}
	return out
}

// A promotion to App Platform copies the bundle's image into DOCR by
// digest; the app spec names the digest DOCR answers, which is the
// bundle's own; AppTown's Docker is not used.
//
// Kills: pushing from AppTown's Docker (the 8a path); a spec naming a
// digest DOCR does not hold, or another than the bundle's; a copy that
// rewrites the manifest (a new digest).
func TestAPromotionToAppPlatformNamesTheBundlesDigestsInDOCR(t *testing.T) {
	t.Parallel()
	p := newPromotionTargetsWith(t, false, digestOneImageYAML)
	b := p.land(t)
	rel, err := p.promote(t, b.ID, "cloud", p.target(t, "do-syd"))
	if err != nil || rel.State != Released {
		events, _ := p.s.Releases.Events(context.Background(), rel.ID)
		t.Fatalf("the release: %+v %v\n%+v", rel, err, events)
	}
	state := p.do.Snapshot()
	if len(state.Specs) != 1 {
		t.Fatalf("%d app specs", len(state.Specs))
	}
	got := componentImages(t, state.Specs[0])
	_, digest, _ := strings.Cut(b.Parts["web"], "@")
	if len(got) != 1 || got["web"] != digest {
		t.Errorf("the spec runs %v, the bundle's web is %s", got, digest)
	}
	if answer := p.docr.digestOf(t, p.docr.host+"/forge-test/p@"+digest); answer != digest {
		t.Errorf("DOCR answers %q for %s", answer, digest)
	}
	p.expectAppTownDockerIdle(t)
}

// App Platform runs one version of a DOCR repository per app, and the
// project's images share its one repository: a bundle whose parts and init
// steps are different images is refused for App Platform when it is asked
// for, before any call to DigitalOcean or copy into DOCR (restore test
// RT-L3: the platform refuses such an app only at create). The refusal says
// how many images and the remedy AppTown supports: a Docker host target.
//
// Kills: the refusal left to the renderer or the platform, after effects;
// a refusal advising what AppTown cannot do (other repositories).
func TestAMultipartBundleOfDistinctImagesIsRefusedBeforeTheAppPlatformApp(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	b := p.land(t)
	if _, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, "cloud", p.target(t, "do-syd").ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	before := len(p.do.Snapshot().Requests)
	_, err := p.s.RequestRelease(context.Background(), DeploySelection{ProjectID: p.project, DeploymentName: "cloud", BundleID: b.ID})
	if !errors.Is(err, ErrReleaseRefused) || !strings.HasSuffix(err.Error(), "App Platform runs one image per repository, and this release's parts and init steps use 5 different images in the project's one repository. Deploy it to a Docker host target instead.") {
		t.Fatalf("the release: %v", err)
	}
	if after := p.do.Snapshot().Requests[before:]; len(after) != 0 {
		t.Fatalf("the refusal called DigitalOcean: %v", after)
	}
	_, digest, _ := strings.Cut(b.Parts["web"], "@")
	if p.docr.digestOf(t, p.docr.host+"/forge-test/p@"+digest) != "" {
		t.Fatalf("the refused release copied %s into DOCR", digest)
	}
}

// A static site on App Platform runs its image's own command: the spec
// gives it none, only the release identity its file server answers (the
// part and the revision), and the release is released when the front door
// answers this release's,
// which the log says. A front door serving the file server's stock
// configuration (Caddy's default page) answers no
// identity: the release fails saying so, never "released".
//
// Kills: a renderer that adds the file server's command again; a readiness
// that takes "the front door answers" for "it serves this release".
func TestAStaticSiteOnAppPlatformIsWitnessedServingItsRelease(t *testing.T) {
	t.Parallel()
	p := newPromotionTargetsWith(t, false, digestOneImageYAML)
	b := p.land(t)
	rel, err := p.promote(t, b.ID, "cloud", p.target(t, "do-syd"))
	events, _ := p.s.Releases.Events(context.Background(), rel.ID)
	if err != nil || rel.State != Released || !strings.Contains(asJSON(events), "the front door serves this release: ") {
		t.Fatalf("the release: %+v %v\n%+v", rel, err, events)
	}
	spec := p.do.Snapshot().Specs[0]
	var web map[string]any
	for _, s := range spec["services"].([]any) {
		if s.(map[string]any)["name"] == "web" {
			web = s.(map[string]any)
		}
	}
	if web == nil || web["run_command"] != nil || !strings.Contains(asJSON(web["envs"]), `{"key":"APPTOWN_RELEASE","scope":"RUN_TIME","type":"GENERAL","value":"web@`+rel.CommitSHA+`"}`) {
		t.Fatalf("the static service: %s", asJSON(web))
	}

	p.do.ServeFrontDoor(true)
	rel, err = p.promote(t, b.ID, "cloud", p.target(t, "do-syd"))
	events, _ = p.s.Releases.Events(context.Background(), rel.ID)
	if err == nil || rel.State != ReleaseFailed || !strings.Contains(asJSON(events), "the front door does not serve this release") {
		t.Fatalf("a front door serving the stock page: %+v %v\n%+v", rel, err, events)
	}
}

// A DOCR copy that fails fails the release before the app changes; the
// same bundle, released again once DOCR answers, is released.
//
// Kills: a copy failure taken for success, or left releasing; a failure
// that leaves the bundle unreleasable.
func TestADOCRCopyFailureFailsTheReleaseAndReleasingAgainSucceeds(t *testing.T) {
	t.Parallel()
	p := newPromotionTargetsWith(t, false, digestOneImageYAML)
	b := p.land(t)
	cloud := p.target(t, "do-syd")
	p.docr.refusing.Store(true)
	rel, err := p.promote(t, b.ID, "cloud", cloud)
	if err == nil || rel.State != ReleaseFailed {
		t.Fatalf("a release DOCR refused: %+v %v", rel, err)
	}
	if state := p.do.Snapshot(); len(state.Specs) != 0 || len(state.Apps) != 0 {
		t.Fatalf("a failed copy changed the app: %d specs, %d apps", len(state.Specs), len(state.Apps))
	}
	p.docr.refusing.Store(false)
	rel, err = p.promote(t, b.ID, "cloud", cloud)
	if err != nil || rel.State != Released {
		t.Fatalf("releasing the same bundle again: %+v %v", rel, err)
	}
	if state := p.do.Snapshot(); len(state.Specs) != 1 {
		t.Fatalf("%d app specs after the second release", len(state.Specs))
	}
}

// DOCR answers every push 401 while it collects garbage, as it does a
// wrong credential. A push refused at DOCR while a collection runs keeps
// the refusal and adds that a collection was running, and that a refusal
// that persists after it ends is the credential's; it never says the
// credential is not at fault. A refusal with no collection, an unreachable
// DOCR, and a refused read of the release's own registry are said as they
// are, with no collection named — a source layer refused while the push
// streams it included: who refused is read from the answer, not from the
// step the copy was in.
//
// Kills: the refusal replaced by a busy diagnosis; the credential cleared;
// a source refusal (up front or mid-push) or an outage blamed on the
// collection.
func TestACopyRefusedDuringGarbageCollectionSaysBothFacts(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		collecting, unavailable, source, layer, noted bool
		want                                          string
	}{
		// The source answers its manifest and config, then refuses a layer
		// the write reads from it as it pushes.
		"source layer refused mid-copy while collecting": {collecting: true, layer: true, want: "copying to "},
		"refused while collecting":                       {collecting: true, noted: true, want: "refused the credential"},
		"refused, nothing collecting":                    {want: "refused the credential"},
		"unavailable while collecting":                   {collecting: true, unavailable: true, want: "unavailable"},
		"source refused while collecting":                {collecting: true, source: true, want: "refused the credential"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newPromotionTargetsWith(t, false, digestOneImageYAML)
			b := p.land(t)
			cloud := p.target(t, "do-syd")
			if tc.collecting {
				p.do.SeedCollection("waiting for write JWTs to expire")
			}
			switch {
			case tc.layer:
				var blobs atomic.Int32
				refuseLayers := func(r *http.Request) {
					if strings.Contains(r.URL.Path, "/blobs/") && r.Method == http.MethodGet && blobs.Add(1) > 1 {
						p.registry.refusing.Store(true)
					}
				}
				p.registry.onRequest.Store(&refuseLayers)
			case tc.source:
				p.registry.refusing.Store(true)
			case tc.unavailable:
				p.docr.unavailable.Store(true)
			default:
				p.docr.refusing.Store(true)
			}
			rel, err := p.promote(t, b.ID, "cloud", cloud)
			if err == nil || rel.State != ReleaseFailed {
				t.Fatalf("a refused copy: %+v %v", rel, err)
			}
			msg := err.Error()
			noted := strings.Contains(msg, "was collecting garbage (started 2026-10-08T04:45:54Z, waiting for write JWTs to expire) when it refused") && strings.Contains(msg, "If the refusal persists after it ends, the credential is at fault")
			if noted != tc.noted || !strings.Contains(msg, tc.want) || strings.Contains(msg, "not at fault") {
				t.Fatalf("%v", err)
			}
			if tc.layer && !strings.Contains(msg, p.registry.host) {
				t.Fatalf("the source layer's refusal: %v", err)
			}
			if tc.source && !strings.Contains(msg, "reading ") {
				t.Fatalf("the source's refusal: %v", err)
			}
		})
	}
}

// An intranet release ships the latest bundle: the compose names every
// part and init step by the bundle's digest, a multipart app included, with
// its runtime configuration (the parts' environment) kept, and the static
// site run by its image's own command; nothing is built or pushed on
// AppTown.
//
// The release adds its readable tag to the same digests.
//
// Kills: building and pushing the branch head (the old path); refusing a
// part built separately from the public one; a stack that overrides the
// static site's own command; an intranet release never tagged.
func TestAnIntranetReleaseNamesEveryImageByDigest(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	b := p.land(t)
	rel := p.releaseLAN(t)
	if rel.State != Released || rel.BundleID != b.ID {
		events, _ := p.s.Releases.Events(context.Background(), rel.ID)
		t.Fatalf("the intranet release: %+v\n%+v", rel, events)
	}
	_, web, _ := strings.Cut(b.Parts["web"], "@")
	// The tag is written once the release is live, after its state is
	// recorded (a name for people, never evidence), so it is awaited.
	ref := fmt.Sprintf("%s/team/p:release-%d-part-web", p.registry.host, rel.Number)
	if !poll.Until(context.Background(), 10*time.Second, 10*time.Millisecond, func() bool { return p.registry.digestOf(t, ref) == web }) {
		t.Fatalf("the intranet release's web image is tagged %q, want %s", p.registry.digestOf(t, ref), web)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.stacks) != 1 {
		t.Fatalf("%d stacks deployed", len(p.stacks))
	}
	compose := p.stacks[0]
	for service, ref := range map[string]string{"app": b.Parts["web"], "worker": b.Parts["worker"], "cache": b.Parts["cache"], "init-migrate": b.Init["migrate"], "init-seed": b.Init["seed"]} {
		if !strings.Contains(compose, "  "+service+":\n    image: "+ref+"\n") {
			t.Errorf("%s does not run %s:\n%s", service, ref, compose)
		}
	}
	if !strings.Contains(compose, `GREETING: "hello"`) {
		t.Errorf("the compose lacks the worker's environment:\n%s", compose)
	}
	// The static site's image carries the command serving it
	// (docker.StaticSiteDockerfile); the stack adds none.
	if strings.Contains(compose, "caddy run") {
		t.Errorf("the compose gives the static site a command:\n%s", compose)
	}
	// It is told the release it serves, which it answers at
	// /_apptown/release, as on App Platform.
	if want := fmt.Sprintf("      %s: %q\n", appcontract.ReleaseEnvVar, appcontract.ReleaseIdentity("web", b.ContractRevision)); !strings.Contains(compose, want) {
		t.Errorf("the compose does not tell the static site its release %q:\n%s", want, compose)
	}
	p.expectAppTownDockerIdle(t)
}

// An intranet release refuses an endpoint that names no platform, or
// another than the bundle's, before Portainer is given anything.
//
// Kills: an empty answer taken for "any platform" (P07); shipping an
// image built for another platform.
func TestAnIntranetReleaseRefusesAnUnknownOrOtherPlatform(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	p.land(t)
	for answer, words := range map[string]string{`{}`: "names no known platform", `{"Os":"linux","Arch":"arm64"}`: "built for linux/amd64"} {
		p.platform.Store(&answer)
		rel := p.releaseLAN(t)
		events, _ := p.s.Releases.Events(context.Background(), rel.ID)
		if rel.State != ReleaseFailed || !strings.Contains(fmt.Sprint(events), words) {
			t.Errorf("endpoint %s: %+v %+v", answer, rel, events)
		}
	}
	if len(p.stacks) != 0 {
		t.Fatalf("a refused release reached Portainer: %v", p.stacks)
	}
}

// In an AppTown with no local Docker a digest bundle goes by promotion to App
// Platform and to Portainer, and the built-in host (of an AppTown that once had local Docker) is
// still refused.
//
// Kills: 8a's refusal of every Portainer and App Platform release kept for
// digest bundles; the built-in host admitted because the bundle is in a
// registry.
func TestInTheNoLocalDockerModeADigestBundleGoesToAppPlatformAndPortainer(t *testing.T) {
	t.Parallel()
	p := newPromotionTargetsWith(t, true, digestOneImageYAML)
	b := p.land(t)
	if rel, err := p.promote(t, b.ID, "cloud", p.target(t, "do-syd")); err != nil || rel.State != Released {
		t.Fatalf("App Platform: %+v %v", rel, err)
	}
	if rel := p.releaseLAN(t); rel.State != Released {
		t.Fatalf("Portainer: %+v", rel)
	}
	builtIn, err := p.s.Targets.Put(context.Background(), &Target{Name: "local", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := p.s.Deployments.EnsureStanding(t.Context(), p.project, "old", builtIn.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.s.RequestRelease(context.Background(), DeploySelection{ProjectID: p.project, DeploymentName: dep.Name, BundleID: b.ID}); !errors.Is(err, ErrNoLocalDocker) {
		t.Fatalf("the built-in host: %v", err)
	}
}

// The intranet target's domain is the administrator's setting, and an
// intranet release answers on it: after an edit and a restart (which syncs
// the target from a configuration naming another domain) the stack's hosts
// and the release's address use the target's domain; with the domain
// cleared, a release is refused before Portainer is given anything.
//
// Kills: a release reading the configured domain while the target keeps
// another (TS-02); a release with no domain shipping hosts like "p.".
func TestAnIntranetReleaseAnswersOnTheTargetsDomain(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	p.land(t)
	ctx := context.Background()
	edit := func(domain string) {
		t.Helper()
		intranet := p.target(t, IntranetTargetName)
		if _, err := p.s.Targets.EditSettings(ctx, IntranetTargetName, TargetSettingsEdit{ExpectedRevision: &intranet.SettingsRevision, Domain: &domain}, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	edit("lan.example.org")
	p.s.SeedTargets(ctx, nil) // a restart: configuration still names apps.example
	if d := p.target(t, IntranetTargetName).Ingress["domain"]; d != "lan.example.org" {
		t.Fatalf("a restart changed the intranet domain to %v", d)
	}
	if rel := p.releaseLAN(t); rel.State != Released {
		events, _ := p.s.Releases.Events(ctx, rel.ID)
		t.Fatalf("the intranet release: %+v\n%+v", rel, events)
	}
	p.mu.Lock()
	compose := p.stacks[0]
	p.mu.Unlock()
	if !strings.Contains(compose, "p.lan.example.org") || strings.Contains(compose, "p.apps.example") {
		t.Fatalf("the stack does not answer on the target's domain:\n%s", compose)
	}
	if env, err := p.s.Deployments.ByName(ctx, p.project, "lan"); err != nil || env.URL != "https://p.lan.example.org" {
		t.Fatalf("the intranet deployment's address: %+v %v", env, err)
	}

	edit("")
	proj, err := p.projects.ByID(ctx, p.project)
	if err != nil {
		t.Fatal(err)
	}
	if _, code, err := p.s.ReleaseLAN(ctx, &proj, "boss", LANReleaseRequest{}); code != 422 || err == nil || !strings.Contains(err.Error(), "names no domain") {
		t.Fatalf("a release with no domain: %d %v", code, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.stacks) != 1 {
		t.Fatalf("a refused release reached Portainer: %d stacks", len(p.stacks))
	}
}

// An intranet release ships only a release the landing published to the
// project's registry. With no bundle, or a latest bundle of image IDs, it
// is refused before a number is made, with the same next action as a
// release to Portainer by name, and AppTown builds, pushes and logs in to
// nothing: there is one build path, the landing's.
//
// Kills: the intranet release building and pushing the branch head itself
// when the latest landing published nothing.
func TestAnIntranetReleaseOfAnUnpublishedLandingIsRefusedWithoutABuild(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	proj, err := p.projects.ByID(context.Background(), p.project)
	if err != nil {
		t.Fatal(err)
	}
	refused := func(what string) {
		t.Helper()
		before, err := p.s.Releases.ForProject(context.Background(), p.project, 50)
		if err != nil {
			t.Fatal(err)
		}
		if _, code, err := p.s.ReleaseLAN(t.Context(), &proj, "boss", LANReleaseRequest{}); code != 422 || !errors.Is(err, ErrReleaseRefused) ||
			!strings.Contains(err.Error(), "intranet pulls every image from the project's container registry, and this release was not published there. Attach a container registry to the project, then land a change") {
			t.Fatalf("%s: %d %v", what, code, err)
		}
		if after, err := p.s.Releases.ForProject(context.Background(), p.project, 50); err != nil || len(after) != len(before) {
			t.Fatalf("%s: a refused release made one: %d before, %d after (%v)", what, len(before), len(after), err)
		}
	}
	refused("no bundle")
	cutBundleFixture(t, p.db, p.project, p.revision, BundleOrigin{})
	refused("a bundle of image IDs")
	p.expectAppTownDockerIdle(t)
	if _, n := p.lastStack(t); n != 0 {
		t.Fatal("Portainer was given a stack")
	}
}
