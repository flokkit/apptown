package digitalocean

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/digitalocean/dofake"
	"github.com/flokkit/apptown/internal/trust"
	"github.com/flokkit/apptown/internal/trust/trusttest"
)

// Every container App Platform runs for the app (its services, its workers,
// its pre-deploy jobs) and the gate in front of a restored copy are given
// the extra certificates as APPTOWN_TRUST: the company's, never this
// machine's local authority, in a value App Platform delivers as written.
// Not parallel: it installs the process's trust.
func TestAppPlatformGivesEveryContainerTheExtraCertificates(t *testing.T) {
	company, local := trusttest.New(t, "Company CA"), trusttest.New(t, "Local CA")
	trusttest.Install(t, company.Bundle(t), local.Bundle(t))
	d, _ := gateRepositoryDriver(t, &dofake.API{})
	img := "registry.example/demo/orders@sha256:" + strings.Repeat("2", 64)
	c, err := appcontract.Parse([]byte("app:\n  web: {image: app:1, public: true, health: {readiness: {http: /healthz}}}\n  jobs: {image: app:1}\ninit:\n  - {name: migrate, image: app:1}\n"))
	if err != nil {
		t.Fatal(err)
	}
	src := deploy.Source{Images: map[string]string{"web": img, "jobs": img}, Init: map[string]string{"migrate": img}, Contract: c, Revision: "selected"}
	want := trust.Current().Script(false)
	if want == "" || strings.Contains(want, strings.TrimSpace(string(local.PEM()))) || strings.Contains(want, "${") {
		t.Fatalf("the script for App Platform:\n%s", want)
	}
	given := map[string]bool{}
	// A deployment, and a restored copy behind its gate (which runs no
	// workers).
	for _, p := range []deploy.Placement{{Name: "cloud"}, {Name: "copy-1", Gate: &deploy.Gate{Image: gateRef, Secret: strings.Repeat("ab", 32), AppTown: "https://forge.example", Marker: testMarker}}} {
		art, err := d.Render(context.Background(), p, src, deploy.NeedBindings{}, scopedRenderSecrets{}, func(string, ...any) {})
		if err != nil {
			t.Fatal(err)
		}
		var spec AppSpec
		if err := json.Unmarshal(art.Body, &spec); err != nil {
			t.Fatal(err)
		}
		for _, comps := range [][]Component{spec.Services, spec.Workers, spec.Jobs} {
			for _, comp := range comps {
				for _, e := range comp.Envs {
					if e.Key == trust.EnvVar {
						if e.Value != want || e.Type != "GENERAL" {
							t.Fatalf("%s is given %s=%q (%s)", comp.Name, e.Key, e.Value, e.Type)
						}
						given[comp.Name] = true
					}
				}
			}
		}
	}
	for _, name := range []string{"web", "jobs", "migrate", GateComponent} {
		if !given[name] {
			t.Fatalf("%s is not given the extra certificates (given: %v)", name, given)
		}
	}
}
