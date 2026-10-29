package digitalocean

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/digitalocean/dofake"
)

// AppTown owns what carries its installation's mark, not the account: two
// installations on one account, and a database a provisioning script made,
// are not each other's leftovers. An app or cluster without this
// installation's mark is reported only to confirm a record.
func TestInventoryCountsOnlyThisInstallationsMark(t *testing.T) {
	t.Parallel()
	api := &dofake.API{}
	api.SeedDatabase(map[string]any{"id": "db-ours", "name": "apptown-db-ours", "status": "online", "tags": []any{"apptown", InstallationTag("inst-a")}})
	api.SeedDatabase(map[string]any{"id": "db-theirs", "name": "apptown-db-theirs", "status": "online", "tags": []any{"apptown", InstallationTag("inst-b")}})
	api.SeedDatabase(map[string]any{"id": "db-script", "name": "forge-demo-pg", "status": "online"})
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	c := NewClient("dop_v1_test", server.URL)
	ctx := context.Background()
	for _, spec := range []map[string]any{
		{"name": "apptown-ours", "region": "syd", "envs": []any{map[string]any{"key": InstanceEnv, "value": "inst-a", "scope": "RUN_TIME"}}},
		{"name": "apptown-theirs", "region": "syd", "envs": []any{map[string]any{"key": InstanceEnv, "value": "inst-b", "scope": "RUN_TIME"}}},
		{"name": "apptown-before", "region": "syd"},
	} {
		if err := c.Call(ctx, "POST", "/v2/apps", map[string]any{"spec": spec}, nil); err != nil {
			t.Fatal(err)
		}
	}
	resources, err := Inventory{Installation: "inst-a", Client: func(context.Context) (*Client, Config, error) { return c, Config{}, nil }}.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ours := map[string]bool{}
	for _, r := range resources {
		if r.Kind == "app" || r.Kind == "database" {
			ours[r.Name] = !slices.Contains(r.Tags, deploy.TagConfirmsOnly)
		}
	}
	want := map[string]bool{"apptown-db-ours": true, "apptown-db-theirs": false, "forge-demo-pg": false, "apptown-ours": true, "apptown-theirs": false, "apptown-before": false}
	for name, mine := range want {
		got, listed := ours[name]
		if !listed || got != mine {
			t.Errorf("%s: listed %v, this installation's %v; want this installation's %v", name, listed, got, mine)
		}
	}
}

// What the driver makes carries the installation's mark: an app's spec
// its app-wide variable, a new cluster the installation's tag.
func TestMadeResourcesCarryTheInstallationsMark(t *testing.T) {
	c, err := appcontract.Parse([]byte("app:\n  web: {image: app:1, public: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	d, _ := gatedDriver(t, nil)
	d.Installation = "inst-a"
	art, err := d.Render(context.Background(), deploy.Placement{ID: mustID("01a15e84-c8f1-7000-b757-3b89d1da8075"), Name: "main"}, deploy.Source{Images: map[string]string{"web": "app:1"}, Contract: c},
		deploy.NeedBindings{}, scopedRenderSecrets{}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	var spec AppSpec
	if err := json.Unmarshal(renderedSpecBody(t, art), &spec); err != nil {
		t.Fatal(err)
	}
	if len(spec.Envs) != 1 || spec.Envs[0].Key != InstanceEnv || spec.Envs[0].Value != "inst-a" {
		t.Fatalf("the app spec lacks the installation's mark: %+v", spec.Envs)
	}
	if tags := d.madeTags("shop", "main"); !slices.Contains(tags, InstallationTag("inst-a")) || !slices.Contains(tags, "apptown-deployment-main") {
		t.Fatalf("cluster tags %v", tags)
	}
}
