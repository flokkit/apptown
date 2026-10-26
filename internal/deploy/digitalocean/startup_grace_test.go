package digitalocean

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
)

func TestAppPlatformPreservesDeclaredStartupGrace(t *testing.T) {
	for _, tc := range []struct {
		name, grace string
		seconds     int
	}{
		{"omitted", "", 5}, {"zero", "0s", 5}, {"below default", "4s", 5},
		{"declared", "90s", 90}, {"fractional", "5.001s", 6}, {"maximum", "3600s", 3600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, gated := range []bool{false, true} {
				d, _ := gatedDriver(t, nil)
				c := gatedContract(t)
				c.PublicPart().Intent.Readiness.StartupGrace = tc.grace
				p := deploy.Placement{Name: "main"}
				if gated {
					p.Gate = &deploy.Gate{Image: gateRef, Secret: strings.Repeat("ab", 32), AppTown: "https://forge.example", Marker: testMarker}
				}
				art, err := d.Render(t.Context(), p, deploy.Source{Images: map[string]string{"web": "app:1", "worker": "worker:1"}, Init: map[string]string{"migrate": "migrate:1"}, Contract: c}, nil, scopedRenderSecrets{}, func(string, ...any) {})
				if err != nil {
					t.Fatal(err)
				}
				var spec AppSpec
				if err := json.Unmarshal(renderedSpecBody(t, art), &spec); err != nil {
					t.Fatal(err)
				}
				for _, s := range spec.Services {
					if s.Name == "web" && (s.HealthCheck == nil || s.HealthCheck.InitialDelaySeconds != tc.seconds) {
						t.Fatalf("gated=%v: declared grace %q rendered as %+v, want %d seconds", gated, tc.grace, s.HealthCheck, tc.seconds)
					}
					if s.Name == GateComponent && (s.HealthCheck == nil || s.HealthCheck.HTTPPath != "" || s.HealthCheck.Port != deploy.GatePort || s.HealthCheck.InitialDelaySeconds != 2) {
						t.Fatalf("gate probe changed: %+v", s.HealthCheck)
					}
				}
			}
		})
	}
}

func TestAppPlatformRefusesUnsupportedGraceBeforeProviderAccess(t *testing.T) {
	for _, grace := range []string{"3600.001s", "999999h", "invalid", "-1s"} {
		t.Run(grace, func(t *testing.T) {
			c := gatedContract(t)
			// The later service must be checked before even the first image copy.
			late := *c.PublicPart()
			late.Name = "later"
			late.Public = false
			intent := *late.Intent
			readiness := *intent.Readiness
			intent.Readiness = &readiness
			late.Intent = &intent
			c.Parts = append(c.Parts, late)
			c.Parts[len(c.Parts)-1].Intent.Readiness.StartupGrace = grace
			calls := 0
			d := NewDriver(Dependencies{Client: func(context.Context) (*Client, Config, error) {
				calls++
				t.Fatal("provider accessed before grace refusal")
				return nil, Config{}, nil
			}}, Options{})
			_, err := d.Render(t.Context(), deploy.Placement{}, deploy.Source{Images: map[string]string{"web": "app:1", "worker": "worker:1", "later": "app:1"}, Contract: c}, nil, scopedRenderSecrets{}, func(string, ...any) {})
			if err == nil || calls != 0 || !strings.Contains(err.Error(), "later") {
				t.Fatalf("unsupported grace accepted: err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestAppPlatformPreflightRefusesGraceBeforeProvisioning(t *testing.T) {
	for _, gated := range []bool{false, true} {
		t.Run(map[bool]string{false: "later service", true: "gated public service"}[gated], func(t *testing.T) {
			c := gatedContract(t)
			p := deploy.Placement{}
			if gated {
				p.Gate = &deploy.Gate{}
				c.PublicPart().Intent.Readiness.StartupGrace = "3601s"
			} else {
				late := *c.PublicPart()
				late.Name = "later"
				late.Public = false
				intent := *late.Intent
				readiness := *intent.Readiness
				intent.Readiness = &readiness
				late.Intent = &intent
				late.Intent.Readiness.StartupGrace = "3601s"
				c.Parts = append(c.Parts, late)
			}
			d := NewDriver(Dependencies{Client: func(context.Context) (*Client, Config, error) {
				t.Fatal("provider accessed before preflight refusal")
				return nil, Config{}, nil
			}}, Options{})
			err := d.Preflight(t.Context(), p, deploy.Source{Contract: c}, scopedRenderSecrets{}, func(string, ...any) {})
			part := "later"
			if gated {
				part = "web"
			}
			if err == nil || !deploy.IsTerminal(err) || !strings.Contains(err.Error(), "part "+part) {
				t.Fatalf("want terminal part-specific refusal, got %v", err)
			}
		})
	}
}
