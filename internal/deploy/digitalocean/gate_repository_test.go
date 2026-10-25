package digitalocean

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/digitalocean/dofake"
	"github.com/flokkit/apptown/internal/oci"
)

// gateRepositoryDriver renders and deploys against api, recording the
// repository each image was copied into.
func gateRepositoryDriver(t *testing.T, api *dofake.API) (*Driver, map[string]string) {
	t.Helper()
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	into := map[string]string{}
	copyImage := func(_ *Driver, _ context.Context, _ *Client, _, repo string, from oci.Source, _ string, _ func(string, ...any)) (string, error) {
		into[from.Reference] = repo
		_, digest, _ := strings.Cut(from.Reference, "@")
		return digest, nil
	}
	d := NewDriver(Dependencies{Slug: "orders", Client: func(context.Context) (*Client, Config, error) {
		return &Client{Base: server.URL, HTTP: server.Client()}, Config{Region: "syd", Registry: "forge-test", InstanceSize: "apps-s-1vcpu-0.5gb"}, nil
	}}, Options{CopyImage: copyImage, PollInterval: time.Millisecond, ProbeFrontDoor: func(context.Context, string, time.Time, func(string, ...any)) error { return nil }})
	return d, into
}

func appCreates(api *dofake.API) int {
	n := 0
	for _, r := range api.Snapshot().Requests {
		if r.Method == "POST" && r.Path == "/v2/apps" {
			n++
		}
	}
	return n
}

// App Platform runs one version of a DOCR repository per app: a restored
// copy whose gate image was a second digest in the project's repository
// was refused at create (restore test RT-L3). The gate image goes into its
// own repository, so a gated app whose release image is a different digest
// is created.
func TestGatedAppNamesTheGateFromItsOwnRepository(t *testing.T) {
	t.Parallel()
	api := &dofake.API{}
	d, into := gateRepositoryDriver(t, api)
	web := "registry.example/demo/orders@sha256:" + strings.Repeat("2", 64)
	c, err := appcontract.Parse([]byte("app:\n  web: {image: app:1, public: true, health: {readiness: {http: /healthz}}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := deploy.Placement{Name: "copy-1", Gate: &deploy.Gate{Image: gateRef, Secret: strings.Repeat("ab", 32), AppTown: "https://forge.example", Marker: testMarker}}
	art, err := d.Render(context.Background(), p, deploy.Source{Images: map[string]string{"web": web}, Contract: c, Revision: "selected"}, deploy.NeedBindings{}, scopedRenderSecrets{}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if into[gateRef] != GateRepository || into[web] != "orders" {
		t.Fatalf("copied into %v; want the gate in %s and the release in orders", into, GateRepository)
	}
	var spec AppSpec
	if err := json.Unmarshal(art.Body, &spec); err != nil {
		t.Fatal(err)
	}
	repos := map[string]string{}
	for _, s := range spec.Services {
		repos[s.Name] = s.Image.Repository
	}
	if repos[GateComponent] != GateRepository || repos["web"] != "orders" {
		t.Fatalf("the spec names %v", repos)
	}
	if _, err := d.Reconcile(context.Background(), p, 1, art, "restored-copy-1", func(string, ...any) {}, func(map[string]any) error { return nil }); err != nil {
		t.Fatalf("the platform refused the gated app: %v", err)
	}
}

// A spec naming two versions of one DOCR repository is refused by the
// driver before it asks App Platform to create the app, saying how many
// images and the remedy AppTown supports (a Docker host target, not other
// repositories, which AppTown cannot use): the platform would accept it at propose and refuse it
// at create, after the app's databases were made. The fake platform
// refuses it as App Platform does, so the refusal is seen to be the
// driver's.
func TestTwoVersionsOfOneRepositoryAreRefusedBeforeCreate(t *testing.T) {
	t.Parallel()
	api := &dofake.API{}
	d, _ := gateRepositoryDriver(t, api)
	img := func(digest string) doImage {
		return doImage{RegistryType: "DOCR", Repository: "orders", Digest: "sha256:" + strings.Repeat(digest, 64)}
	}
	spec := AppSpec{Name: "orders-copy-1", Region: "syd", Services: []Component{
		{Name: "web", Image: img("2"), InstanceSizeSlug: "apps-s-1vcpu-0.5gb", HTTPPort: 8080},
		{Name: GateComponent, Image: img("1"), InstanceSizeSlug: "apps-s-1vcpu-0.5gb", HTTPPort: deploy.GatePort},
	}}
	body, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}

	// The platform's refusal, as the restore test saw it.
	c, _, _ := d.Client(context.Background())
	err = c.Call(context.Background(), "POST", "/v2/apps", map[string]any{"spec": json.RawMessage(body)}, nil)
	if !IsStatus(err, 400) || !strings.Contains(err.Error(), "not configured for this app") {
		t.Fatalf("the fake platform took two versions of one repository: %v", err)
	}
	before := appCreates(api)

	_, err = d.Reconcile(context.Background(), deploy.Placement{Name: "copy-1"}, 1, deploy.Artifact{Kind: "do-app-spec", Body: body}, "k", func(string, ...any) {}, func(map[string]any) error { return nil })
	if err == nil || !deploy.IsTerminal(err) || err.Error() != "App Platform runs one image per repository, and this release's parts and init steps use 2 different images in the project's one repository. Deploy it to a Docker host target instead." {
		t.Fatalf("not refused with the supported remedy: %v", err)
	}
	if appCreates(api) != before {
		t.Fatal("the driver asked the platform to create the app")
	}

	// The renderer refuses it too, before the spec reaches an attempt: a
	// release whose part and init step are different images.
	c2, err := appcontract.Parse([]byte("app:\n  web: {image: app:1, public: true}\ninit:\n  - {name: migrate, image: migrate:1}\n"))
	if err != nil {
		t.Fatal(err)
	}
	src := deploy.Source{Contract: c2, Revision: "r1",
		Images: map[string]string{"web": "registry.example/orders@sha256:" + strings.Repeat("2", 64)},
		Init:   map[string]string{"migrate": "registry.example/orders@sha256:" + strings.Repeat("3", 64)}}
	if _, err := d.Render(context.Background(), deploy.Placement{Name: "cloud"}, src, deploy.NeedBindings{}, scopedRenderSecrets{}, func(string, ...any) {}); err == nil || !deploy.IsTerminal(err) || err.Error() != "App Platform runs one image per repository, and this release's parts and init steps use 2 different images in the project's one repository. Deploy it to a Docker host target instead." {
		t.Fatalf("the renderer gave two versions of one repository: %v", err)
	}

	// One version of the repository, in every component, is created.
	spec.Services[1].Image = img("2")
	body, _ = json.Marshal(spec)
	if _, err := d.Reconcile(context.Background(), deploy.Placement{Name: "copy-1"}, 1, deploy.Artifact{Kind: "do-app-spec", Body: body}, "k", func(string, ...any) {}, func(map[string]any) error { return nil }); err != nil {
		t.Fatalf("one version was refused: %v", err)
	}
}

// The gate's repository counts against the registry's plan: a gated
// deployment on a registry with room for the project's repository but
// not also the gate's is refused before anything is made; an ungated one
// goes on, and so does a gated one once the gate's repository exists.
func TestPreflightCountsTheGatesRepository(t *testing.T) {
	t.Parallel()
	gated := deploy.Placement{Name: "copy-1", Gate: &deploy.Gate{Image: gateRef}}
	for _, tc := range []struct {
		name   string
		repos  []string
		p      deploy.Placement
		refuse bool
	}{
		{"gated, room for one", []string{"a", "b", "c", "d"}, gated, true},
		{"ungated, room for one", []string{"a", "b", "c", "d"}, deploy.Placement{Name: "cloud"}, false},
		{"gated, gate repository there", []string{"a", "b", "c", GateRepository}, gated, false},
		{"gated, room for two", []string{"a", "b", "c"}, gated, false},
	} {
		api := &dofake.API{}
		api.SeedRegistry("forge-test", "basic", 5, tc.repos...)
		d, _ := gateRepositoryDriver(t, api)
		err := d.Preflight(context.Background(), tc.p, deploy.Source{}, nil, func(string, ...any) {})
		if tc.refuse != (err != nil) {
			t.Fatalf("%s: preflight %v", tc.name, err)
		}
		if tc.refuse && (!deploy.IsTerminal(err) || !strings.Contains(err.Error(), GateRepository)) {
			t.Fatalf("%s: the refusal does not name the gate's repository: %v", tc.name, err)
		}
	}
}

func TestReleaseCopySelectsCredentialForItsActualReference(t *testing.T) {
	t.Parallel()
	ref := "old.example/team/shop@sha256:" + strings.Repeat("a", 64)
	failure := errors.New("credential unavailable")
	for _, fail := range []bool{false, true} {
		calls := 0
		d := NewDriver(Dependencies{ReleaseLogin: func(actual string) (oci.Credential, error) {
			if actual != ref {
				t.Fatalf("credential reference=%q want=%q", actual, ref)
			}
			if fail {
				return oci.Credential{}, failure
			}
			return oci.Credential{Password: "selected"}, nil
		}}, Options{CopyImage: func(_ *Driver, _ context.Context, c *Client, registry, repo string, from oci.Source, tag string, _ func(string, ...any)) (string, error) {
			calls++
			if from.Reference != ref || from.Credential.Password != "selected" || c.Token != "destination" || registry != "target" || repo != "shop" || tag != "release" {
				t.Fatal("copy arguments changed")
			}
			return "digest", nil
		}})
		_, err := d.copyImage(t.Context(), &Client{Token: "destination"}, "target", "shop", ref, "release", func(string, ...any) {})
		if fail && (!errors.Is(err, failure) || calls != 0) {
			t.Fatalf("failed selection: calls=%d err=%v", calls, err)
		}
		if !fail && (err != nil || calls != 1) {
			t.Fatalf("copy: calls=%d err=%v", calls, err)
		}
	}
}
