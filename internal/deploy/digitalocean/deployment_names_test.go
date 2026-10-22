package digitalocean

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/s3bucket"
)

// Two deployments whose project slug and deployment name join alike
// (shop/eu-main and shop-eu/main) get different App Platform apps,
// clusters, Spaces keys and buckets: each is named by its own deployment's
// key, never by the joined slug and name.
//
// Kills: any of these names built from slug-deployment again, where the
// second deployment's release would find and overwrite the first's app.
func TestDeploymentsWhoseNamesJoinAlikeGetDifferentResources(t *testing.T) {
	c, err := appcontract.Parse([]byte("app:\n  web: {image: app:1, public: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	type made struct{ app, cluster, maker, key, bucket string }
	of := func(slug string, p deploy.Placement) made {
		d, _ := gatedDriver(t, nil)
		d.Slug = slug
		art, err := d.Render(context.Background(), p, deploy.Source{Images: map[string]string{"web": "app:1"}, Contract: c},
			deploy.NeedBindings{}, scopedRenderSecrets{}, func(string, ...any) {})
		if err != nil {
			t.Fatal(err)
		}
		var spec AppSpec
		if err := json.Unmarshal(art.Body, &spec); err != nil {
			t.Fatal(err)
		}
		key := deploy.HostKey(p.ID)
		return made{app: spec.Name, cluster: clusterName(key), maker: makerKeyName(key), key: appKeyName(key), bucket: s3bucket.BucketName(key, "abcd1234")}
	}
	one := of("shop", deploy.Placement{ID: mustID("01a15e84-c8f1-7000-b757-3b89d1da8075"), Name: "eu-main"})
	two := of("shop-eu", deploy.Placement{ID: mustID("01a15e85-0000-7000-8a3f-5e0c2b7d9a14"), Name: "main"})
	if one.app != "apptown-3b89d1da8075" || len(one.app) > 32 {
		t.Fatalf("the app is named %q", one.app)
	}
	if one.app == two.app || one.cluster == two.cluster || one.maker == two.maker || one.key == two.key || one.bucket == two.bucket {
		t.Fatalf("two deployments share a name:\n%+v\n%+v", one, two)
	}
}
