package digitalocean

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// Collision refusal precedes client/registry/image effects and names only the
// consumer and variable, even for equal values or a later consumer.
func TestAppPlatformRefusesNeedSecretCollisionsBeforeProviderAccess(t *testing.T) {
	for _, consumer := range []string{"web", "worker", "init-migrate"} {
		for _, value := range []string{"manual-secret-sentinel", "need-secret-sentinel", ""} {
			for _, secretBinding := range []bool{false, true} {
				t.Run(consumer+"/"+value+map[bool]string{false: "/env", true: "/secret"}[secretBinding], func(t *testing.T) {
					c := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true, Image: "image", Port: 8080}, {Name: "worker", Image: "image"}}, Init: []appcontract.InitContainer{{Name: "migrate", Image: "image"}}}
					calls := 0
					d := NewDriver(Dependencies{Slug: "app", Client: func(context.Context) (*Client, Config, error) {
						calls++
						return nil, Config{}, errors.New("provider reached")
					}}, Options{})
					binding := deploy.NeedBinding{Env: map[string]string{"DATABASE_URL": "need-secret-sentinel"}}
					if secretBinding {
						binding.Secret = binding.Env
						binding.Env = nil
					}
					_, err := d.Render(t.Context(), deploy.Placement{}, deploy.Source{Contract: c, Images: map[string]string{"web": "image", "worker": "image"}}, deploy.NeedBindings{"postgres": binding}, scopedRenderSecrets{consumer: {"DATABASE_URL": value}}, func(string, ...any) {})
					if err == nil || calls != 0 || !strings.Contains(err.Error(), consumer) || !strings.Contains(err.Error(), "DATABASE_URL") || strings.Contains(err.Error(), "sentinel") {
						t.Fatalf("ambiguous secret reached provider or leaked: %v calls%d", err, calls)
					}
				})
			}
		}
	}
}

func TestGatedAppCollisionGuardKeepsInitAndOmitsWorker(t *testing.T) {
	d, _ := gatedDriver(t, nil)
	c := gatedContract(t)
	place := deploy.Placement{Name: "copy-3", Gate: &deploy.Gate{Image: gateRef, Secret: strings.Repeat("ab", 32), AppTown: "https://apptown.example", Marker: testMarker}}
	src := deploy.Source{Images: map[string]string{"web": "app:1", "worker": "worker:1"}, Init: map[string]string{"migrate": "migrate:1"}, Contract: c, Revision: "selected"}
	bindings := deploy.NeedBindings{"postgres": {Env: map[string]string{"DATABASE_URL": "need-value"}}}
	secrets := scopedRenderSecrets{"worker": {"DATABASE_URL": "omitted-secret"}, "web": {"SESSION_KEY": "kept"}}
	if _, err := d.Render(t.Context(), place, src, bindings, secrets, func(string, ...any) {}); err != nil {
		t.Fatalf("omitted worker blocked valid gated render: %v", err)
	}
	secrets["init-migrate"] = map[string]string{"DATABASE_URL": "init-secret"}
	if _, err := d.Render(t.Context(), place, src, bindings, secrets, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "init-migrate") {
		t.Fatalf("executed init collision ignored: %v", err)
	}
}
