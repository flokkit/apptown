package verification

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// The runtime "page" probe asks the app's root through its front door, the
// address a visitor uses, so an app whose readiness path passes while every
// real page is refused (Rails' /up skips host authorization) is a finding.
func TestTheRuntimePageProbeAsksTheFrontDoorsRoot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		root int
		want check.Outcome
	}{{http.StatusForbidden, check.DoesNotConform}, {http.StatusOK, check.Conforms}} {
		t.Run(http.StatusText(tc.root), func(t *testing.T) {
			front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/up" {
					w.WriteHeader(http.StatusOK)
					return
				}
				w.WriteHeader(tc.root)
			}))
			t.Cleanup(front.Close)
			if got := (Runtime{}).Probe(context.Background(), guidanceTarget{FrontDoor: front.URL, Naked: "http://127.0.0.1:1"}, "page"); got.Outcome != tc.want {
				t.Fatalf("root answering %d judged %+v", tc.root, got)
			}
		})
	}
}

// Both kinds of target receive all configured namespaces, independent of the
// single exact APPTOWN_ORIGINS a preview was started with.
func TestRuntimeOwnAddressUsesConfiguredNamespaces(t *testing.T) {
	t.Parallel()
	environment := deployment.NewEnvironment(deployment.EnvironmentDependencies{Routes: deployment.Routing{
		PreviewDomain: "preview.test", PreviewScheme: "https", MainDomain: "main.test", MainScheme: "https", EvalDomain: "eval.test", EvalScheme: "https",
	}})
	runtime := NewRuntime(RuntimeDependencies{Environment: environment})
	declared := sourcedNamed(t, "lang/selection/wordpress-bedrock", "deployment-home")
	if declared.Check.Evaluator != "runtime" || declared.Check.Probe != "page-own-address" || declared.Check.Severity != "high" {
		t.Fatalf("wrong address check: %+v", declared)
	}
	for _, host := range []string{"old.preview.test", "app.main.test", "copy.eval.test", "cdn.example"} {
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `<script src="https://%s/app.js"></script>`, host)
			}))
			t.Cleanup(front.Close)
			target := guidanceTarget{FrontDoor: front.URL, Naked: "http://127.0.0.1:1"}
			v := runtime.Probe(t.Context(), target, declared.Check.Probe)
			want := check.DoesNotConform
			if host == "cdn.example" {
				want = check.Conforms
			}
			if v.Outcome != want || (want == check.DoesNotConform && !strings.Contains(v.Detail, host)) {
				t.Fatalf("%+v", v)
			}
		})
	}
}

// Standing reviews use the same configured namespaces as preview checks, and
// ask only the front door even when the naked address cannot serve the page.
func TestStandingOwnAddressUsesConfiguredNamespaces(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<link rel="stylesheet" href="https://old.preview.test/app.css">`)
	}))
	t.Cleanup(front.Close)
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	targets := deployment.NewTargets(f.DB, nil)
	target, err := targets.Put(t.Context(), &deployment.Target{Name: "built-in", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	deployments := deployment.NewDeployments(f.DB)
	if _, err := deployments.EnsureStanding(t.Context(), f.ProjectID, "main", target.ID, (deployment.Attributes{}).WithDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := deployments.ReserveHostPort(t.Context(), f.ProjectID, "main", "own-address-main", port); err != nil {
		t.Fatal(err)
	}
	scope := docker.Scope{Client: f.Daemon}
	if _, err := f.Daemon.Run(t.Context(), "", docker.Command{Args: []string{"run", "-d", "--name", "own-address-main", "fixture"}}); err != nil {
		t.Fatal(err)
	}
	hosts := deployment.NewHosts(targets, nil, deployment.HostSettings{Scope: scope})
	environment := deployment.NewEnvironment(deployment.EnvironmentDependencies{Projects: project.New(f.DB, nil), Routes: deployment.Routing{PublicHost: "127.0.0.1", PreviewScheme: "https", PreviewDomain: "preview.test"}})
	service := deployment.New(t.Context(), f.DB, deployment.Dependencies{Targets: targets, Deployments: deployments, Hosts: hosts, Environment: environment})
	runtime := NewRuntime(RuntimeDependencies{DB: f.DB, Deployment: service, Hosts: hosts, Environment: environment})
	state, detail := runtime.observe(t.Context(), f.ProjectID, "", "page-own-address")
	if state != conformance.DoesNotConform || !strings.Contains(detail, "https://old.preview.test/app.css") {
		t.Fatalf("%s: %s", state, detail)
	}
}
