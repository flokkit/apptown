package digitalocean

import (
	"context"
	"encoding/json"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
)

// Parts and init steps keep their own names as App Platform components,
// and the contract keeps those names apart (appcontract refuses an init
// step named like a part or a need), so every component of an app has a
// name of its own.
func TestEveryComponentKeepsItsOwnName(t *testing.T) {
	c, err := appcontract.Parse([]byte(`app:
  web: {image: app:1, public: true}
  worker: {image: app:1}
init:
  - {name: migrate, image: migrate:1}
`))
	if err != nil {
		t.Fatal(err)
	}
	d, _ := gatedDriver(t, nil)
	art, err := d.Render(context.Background(), deploy.Placement{Name: "main"}, deploy.Source{
		Images: map[string]string{"web": "app:1", "worker": "app:1"}, Init: map[string]string{"migrate": "migrate:1"}, Contract: c,
	}, deploy.NeedBindings{}, scopedRenderSecrets{}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	var spec AppSpec
	if err := json.Unmarshal(art.Body, &spec); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, components := range [][]Component{spec.Services, spec.Workers, spec.Jobs} {
		for _, component := range components {
			if have[component.Name] {
				t.Fatalf("two components are named %q", component.Name)
			}
			have[component.Name] = true
		}
	}
	for _, name := range []string{"web", "worker", "migrate"} {
		if !have[name] {
			t.Errorf("no component %q in %v", name, have)
		}
	}
}
