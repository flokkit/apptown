package deployment

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

func TestALandingRefusesUnclaimedWorkWithoutSideEffects(t *testing.T) {
	t.Parallel()
	s := New(context.Background(), nil, Dependencies{})
	for _, claim := range []ClaimedExecution{nil, (*workflow.Execution)(nil), &workflow.Execution{}} {
		if _, err := s.RunLanding(context.Background(), claim, LandingInput{}); err == nil {
			t.Fatal("landing accepted unclaimed work")
		}
	}
}

func TestDockerScopeRejectsMissingTargetBeforeUsingLocalDaemon(t *testing.T) {
	t.Parallel()
	calls := 0
	s := New(context.Background(), nil, assembled(nil, testRuntime{Scope: docker.Scope{Client: sshDockerFake(func(context.Context, string, docker.Command) ([]byte, error) { calls++; return nil, nil })}}, Dependencies{}))
	for _, target := range []*Target{nil, {Kind: KindPortainer}, {Kind: KindDockerHost, Origin: OriginAttached, Name: "remote"}} {
		if _, err := s.hosts.DockerScope(context.Background(), target); err == nil {
			t.Fatal("invalid target accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid target reached a daemon")
	}
}

// A Docker deployment's address is chosen in one place for the driver, the
// landing and reads: this AppTown's routes on its own host, the host's own
// front door on an attached one, never this AppTown's address for an app
// that runs elsewhere.
func TestADockerDeploymentsAddressFollowsItsTarget(t *testing.T) {
	routes := Routing{PublicHost: "apptown.example", MainScheme: "https", MainDomain: "apps.example"}
	builtIn := &Target{Kind: KindDockerHost, Origin: OriginBuiltIn, Name: "local"}
	attached := &Target{Kind: KindDockerHost, Origin: OriginAttached, Name: "box",
		Address: map[string]any{"host": "ssh://me@box.example:22", "keyFrom": "box-key"}, Ingress: map[string]any{"domain": "box.example"}}
	for name, tc := range map[string]struct {
		target   *Target
		deploy   string
		want     string
		hostOnly bool
	}{
		"main here, at its domain":     {builtIn, "main", "https://shop.apps.example", false},
		"another deployment here":      {builtIn, "staging", "http://apptown.example:8080", true},
		"main on the attached host":    {attached, "main", "https://shop.box.example", false},
		"staging on the attached host": {attached, "staging", "http://box.example:8080", false},
	} {
		reach, err := dockerReach(routes, tc.target, "shop", tc.deploy, 8080)
		if err != nil || reach.URL != tc.want || reach.HostOnly != tc.hostOnly {
			t.Errorf("%s: %+v %v, want %s", name, reach, err, tc.want)
		}
	}
	if _, err := dockerReach(routes, &Target{Kind: KindDockerHost, Origin: OriginAttached, Name: "bad"}, "shop", "main", 8080); err == nil {
		t.Error("an attached target with no address was given this AppTown's")
	}
}

// Published app ports replace AppTown's port for both deployments and previews.
func TestPublishedAddressesReplaceThePublicHostPort(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ host, want string }{
		{"localhost:7777", "http://localhost:43210"},
		{"forge.example:8443", "http://forge.example:43210"},
		{"forge.example", "http://forge.example:43210"},
		{"", "http://localhost:43210"},
		{"[::1]:7777", "http://[::1]:43210"},
	} {
		t.Run(tc.host, func(t *testing.T) {
			r := Routing{PublicHost: tc.host}
			for _, got := range []string{r.DeploymentURL("shop", 43210), r.PreviewURL("change-1", 43210)} {
				if got != tc.want {
					t.Errorf("address %q, want %q", got, tc.want)
				}
				if _, err := docker.ExactOrigin(got); err != nil {
					t.Errorf("invalid app origin: %v", err)
				}
			}
			r.MainScheme, r.MainDomain = "https", "apps.example"
			r.PreviewScheme, r.PreviewDomain = "https", "preview.example"
			if got := r.DeploymentURL("shop", 43210); got != "https://shop.apps.example" {
				t.Errorf("main domain lost: %s", got)
			}
			if got := r.PreviewURL("change-1", 43210); got != "https://change-1.preview.example" {
				t.Errorf("preview domain lost: %s", got)
			}
		})
	}
}
