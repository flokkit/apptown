package digitalocean

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/digitalocean/dofake"
)

// Observe says which deployment the platform runs, from the same answer it
// reads the rollout from: after a restore that is how AppTown knows whether
// the app still runs what it recorded. A gone app names none and reads
// absent; one the platform cannot be asked about is an error, never gone.
func TestObserveNamesTheActiveDeployment(t *testing.T) {
	for _, scenario := range []string{"active", "gone", "unreadable"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			api := &dofake.API{}
			switch scenario {
			case "active":
				api.SeedApp("app-1", "dep-7", "")
				if !api.AdvanceDeployment("app-1", "dep-7") {
					t.Fatal("fixture did not select active deployment")
				}
			case "unreadable":
				api.SeedApp("app-1", "dep-7", "")
				api.Fail("GET", "/v2/apps/app-1", 503)
			}
			server := httptest.NewServer(api.Handler())
			defer server.Close()
			client := NewClient("test", server.URL)
			d := NewDriver(Dependencies{Client: func(context.Context) (*Client, Config, error) { return client, Config{}, nil }}, Options{})
			o, err := d.Observe(context.Background(), deploy.Placement{Generation: 1, Handle: map[string]any{"appId": "app-1", "deploymentId": "dep-1"}})
			switch scenario {
			case "active":
				if err != nil || o.Running != "dep-7" {
					t.Fatalf("observed %+v, %v; want dep-7 running", o, err)
				}
				if o.Rollout != "active" {
					t.Fatalf("fixture lost ACTIVE state: %+v", o)
				}
				if o.Health != "unknown" || !o.HealthObservedAt.IsZero() {
					t.Fatalf("display inventory promoted ACTIVE to current health: %+v", o)
				}
			case "gone":
				if err != nil || o.Running != "" || o.Rollout != "absent" {
					t.Fatalf("observed %+v, %v; want absent, nothing running", o, err)
				}
			case "unreadable":
				if err == nil {
					t.Fatalf("an unreadable app was observed as %+v", o)
				}
			}
		})
	}
}
