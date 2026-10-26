package digitalocean

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/digitalocean/dofake"
)

func TestAppPlatformRequiresDeclaredStatusThroughRecovery(t *testing.T) {
	for _, status := range []int{200, 201, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int32
			api := &dofake.API{}
			api.ServeFrontDoor(false)
			provider := api.Handler()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/apps/") {
					requests.Add(1)
					if strings.HasSuffix(r.URL.Path, "/redirected") {
						w.WriteHeader(200)
						return
					}
					if status == 302 {
						http.Redirect(w, r, r.URL.Path+"/redirected", http.StatusFound)
						return
					}
					w.WriteHeader(status)
					return
				}
				provider.ServeHTTP(w, r)
			}))
			defer server.Close()
			c, err := appcontract.Parse([]byte("app:\n  web:\n    image: web:1\n    public: true\n    health: {readiness: {http: /ready, expect: 200}}\n"))
			if err != nil {
				t.Fatal(err)
			}
			d := NewDriver(Dependencies{Slug: "status", Client: func(context.Context) (*Client, Config, error) {
				return NewClient("test", server.URL), Config{Region: "syd", Registry: "forge-test"}, nil
			}}, Options{PollInterval: time.Millisecond, PushImage: func(*Driver, context.Context, *Client, string, string, string, string, func(string, ...any)) (string, error) {
				return "sha256:" + strings.Repeat("a", 64), nil
			}})
			src := deploy.Source{Images: map[string]string{"web": "local-web"}, Contract: c, Revision: "selected"}
			p := deploy.Placement{Name: "cloud"}
			art, err := d.Render(t.Context(), p, src, nil, scopedRenderSecrets{}, func(string, ...any) {})
			if err != nil {
				t.Fatal(err)
			}
			art.Source = &src
			var checkpoint map[string]any
			observation, err := d.Reconcile(t.Context(), p, 1, art, "key", func(string, ...any) {}, func(v map[string]any) error { checkpoint = v; return nil })
			for _, spec := range api.Snapshot().Specs {
				if _, ok := spec["readiness"]; ok {
					t.Fatal("AppTown readiness metadata reached provider")
				}
				if _, ok := spec["spec"]; ok {
					t.Fatal("artifact envelope reached provider")
				}
			}
			if status != 200 && err == nil {
				t.Fatalf("declared200 accepted response%d as %s", status, observation.Health)
			}
			if status == 200 && (err != nil || observation.Health != "healthy") {
				t.Fatalf("exact response refused: %+v %v", observation, err)
			}
			saved, marshalErr := json.Marshal(checkpoint)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if err := json.Unmarshal(saved, &checkpoint); err != nil {
				t.Fatal(err)
			}
			observation, err = d.Resume(t.Context(), p, 1, checkpoint, func(string, ...any) {})
			if status != 200 && err == nil {
				t.Fatalf("recovery lost declared200 and accepted%d as %s", status, observation.Health)
			}
			if status == 200 && (err != nil || observation.Health != "healthy") {
				t.Fatalf("exact recovery refused: %+v %v", observation, err)
			}
			if status == 200 {
				before := requests.Load()
				lost := errors.New("checkpoint write failed")
				obs, err := d.Reconcile(t.Context(), p, 2, art, "second", func(string, ...any) {}, func(map[string]any) error { return lost })
				if !errors.Is(err, lost) || obs.Health == "healthy" || requests.Load() != before {
					t.Fatalf("checkpoint failure reported/probed readiness: %+v %v", obs, err)
				}
			}

		})
	}
}

func TestAppPlatformRedirectCompatibilitySurvivesResume(t *testing.T) {
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ready" {
			http.Redirect(w, r, "/canonical", http.StatusFound)
			return
		}
		w.WriteHeader(200)
	}))
	defer front.Close()
	provider := appAt(t, front.URL, time.Time{})
	d := NewDriver(Dependencies{Client: func(context.Context) (*Client, Config, error) { return NewClient("test", provider.URL), Config{}, nil }}, Options{PollInterval: time.Millisecond})
	for _, tc := range []struct {
		name          string
		question      any
		legacy, fails bool
	}{
		{"legacy checkpoint", nil, true, false},
		{"new omitted expectation", map[string]any{"version": 1, "path": "/ready", "expect": 0}, false, false},
		{"explicit expectation", map[string]any{"version": 1, "path": "/ready", "expect": 200}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noted := map[string]any{"appId": "app-1", "appName": "app", "deploymentId": "dep-1"}
			if tc.legacy {
				noted["readinessPath"] = "/ready"
			} else {
				noted["artifactKind"] = "do-app-spec-v2"
				noted["readiness"] = tc.question
			}
			obs, err := d.Resume(t.Context(), deploy.Placement{}, 1, noted, func(string, ...any) {})
			if (err != nil) != tc.fails {
				t.Fatalf("redirect compatibility: %+v %v", obs, err)
			}
			if !tc.fails && obs.Health != "healthy" {
				t.Fatalf("successful response not witnessed: %+v", obs)
			}
		})
	}
	if d.frontDoor.CheckRedirect != nil {
		t.Fatal("exact probe mutated shared redirect policy")
	}
}

func TestAppPlatformRefusesMalformedReadinessBeforeProviderAccess(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"version":1,"path":"/ready"}`, `{"version":2,"path":"/ready","expect":200}`, `{"version":1,"path":"/ready","expect":404}`, `{"version":1,"path":"/ready","expect":"200"}`, `{"version":1,"path":"/ready","expect":200,"extra":true}`} {
		t.Run(raw, func(t *testing.T) {
			d := NewDriver(Dependencies{Client: func(context.Context) (*Client, Config, error) {
				t.Fatal("malformed readiness reached provider")
				return nil, Config{}, nil
			}}, Options{})
			art := deploy.Artifact{Kind: "do-app-spec-v2", Body: []byte(`{"spec":{"name":"app"},"readiness":` + raw + `}`)}
			if _, err := d.Reconcile(t.Context(), deploy.Placement{}, 1, art, "key", func(string, ...any) {}, func(map[string]any) error { return nil }); err == nil {
				t.Fatal("malformed artifact accepted")
			}
			var question any
			if err := json.Unmarshal([]byte(raw), &question); err != nil {
				t.Fatal(err)
			}
			noted := map[string]any{"appId": "app-1", "appName": "app", "deploymentId": "dep-1", "artifactKind": "do-app-spec-v2", "readiness": question}
			if _, err := d.Resume(t.Context(), deploy.Placement{}, 1, noted, func(string, ...any) {}); err == nil {
				t.Fatal("malformed checkpoint accepted")
			}
		})
	}
}

func TestAppPlatformReadinessHashAndSelectionArePublic(t *testing.T) {
	d, _ := gatedDriver(t, nil)
	c := gatedContract(t)
	private := *c.PublicPart()
	private.Name = "first-private"
	private.Public = false
	intent := *private.Intent
	probe := *intent.Readiness
	probe.HTTP = "/private"
	intent.Readiness = &probe
	private.Intent = &intent
	c.Parts = append([]appcontract.Part{private}, c.Parts...)
	c.PublicPart().Intent.Readiness.Expect = 200
	src := deploy.Source{Images: map[string]string{"first-private": "app:1", "web": "app:1", "worker": "worker:1"}, Init: map[string]string{"migrate": "migrate:1"}, Contract: c}
	first, err := d.Render(t.Context(), deploy.Placement{}, src, nil, scopedRenderSecrets{}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Readiness struct {
			Version int
			Path    string
			Expect  int
		}
	}
	if err := json.Unmarshal(first.Body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Readiness.Path != "/healthz" || envelope.Readiness.Expect != 200 || envelope.Readiness.Version != 1 {
		t.Fatalf("wrong public predicate: %+v", envelope)
	}
	c.PublicPart().Intent.Readiness.Expect = 201
	second, err := d.Render(t.Context(), deploy.Placement{}, src, nil, scopedRenderSecrets{}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash == second.Hash {
		t.Fatal("expectation-only change kept artifact hash")
	}
}

func TestAppPlatformRefusesUnsupportedStatusBeforeEffects(t *testing.T) {
	for _, tc := range []struct {
		name    string
		private bool
		expect  int
	}{{"private", true, 200}, {"redirect", false, 302}, {"failure", false, 500}, {"invalid", false, -1}} {
		t.Run(tc.name, func(t *testing.T) {
			c := gatedContract(t)
			part := c.PublicPart()
			if tc.private {
				private := *part
				private.Name = "private"
				private.Public = false
				intent := *private.Intent
				probe := *intent.Readiness
				intent.Readiness = &probe
				private.Intent = &intent
				c.Parts = append(c.Parts, private)
				part = &c.Parts[len(c.Parts)-1]
			}
			part.Intent.Readiness.Expect = tc.expect
			d := NewDriver(Dependencies{Client: func(context.Context) (*Client, Config, error) {
				t.Fatal("unsupported predicate reached provider")
				return nil, Config{}, nil
			}}, Options{})
			src := deploy.Source{Contract: c, Images: map[string]string{"web": "app:1"}}
			if err := d.Preflight(t.Context(), deploy.Placement{}, src, scopedRenderSecrets{}, func(string, ...any) {}); err == nil || !deploy.IsTerminal(err) {
				t.Fatalf("preflight accepted: %v", err)
			}
			if _, err := d.Render(t.Context(), deploy.Placement{}, src, nil, scopedRenderSecrets{}, func(string, ...any) {}); err == nil || !deploy.IsTerminal(err) {
				t.Fatalf("renderer accepted: %v", err)
			}
		})
	}
}

func TestAppPlatformReadinessRejectsDeploymentReplacement(t *testing.T) {
	var probed atomic.Bool
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { probed.Store(true); w.WriteHeader(200) }))
	defer front.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/deployments/dep-1") {
			fmt.Fprint(w, `{"deployment":{"id":"dep-1","phase":"ACTIVE"}}`)
			return
		}
		id := "dep-1"
		if probed.Load() {
			id = "dep-2"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"id": "app-1", "live_url": front.URL, "active_deployment": map[string]any{"id": id, "phase": "ACTIVE"}}})
	}))
	defer provider.Close()
	d := NewDriver(Dependencies{Client: func(context.Context) (*Client, Config, error) { return NewClient("test", provider.URL), Config{}, nil }}, Options{PollInterval: time.Millisecond})
	obs, err := d.Resume(t.Context(), deploy.Placement{}, 1, map[string]any{"appId": "app-1", "deploymentId": "dep-1", "artifactKind": "do-app-spec-v2", "readiness": map[string]any{"version": 1, "path": "/ready", "expect": 200}}, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "changed during readiness") || obs.Health == "healthy" {
		t.Fatalf("replacement was reported healthy: %+v %v", obs, err)
	}
}

func TestAppPlatformLegacyArtifactsKeepTheirRecordedPredicate(t *testing.T) {
	d, _ := gatedDriver(t, nil)
	c := gatedContract(t)
	c.PublicPart().Intent.Readiness.Expect = 200
	art, err := d.Render(t.Context(), deploy.Placement{}, deploy.Source{Images: map[string]string{"web": "app:1", "worker": "worker:1"}, Init: map[string]string{"migrate": "migrate:1"}, Contract: c}, nil, scopedRenderSecrets{}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	legacy := deploy.Artifact{Kind: "do-app-spec", Body: renderedSpecBody(t, art)}
	calls := 0
	d.options.ProbeFrontDoor = func(_ context.Context, url string, expect int, _ time.Time, _ func(string, ...any)) error {
		calls++
		if expect != 0 || !strings.HasSuffix(url, "/healthz") {
			t.Fatalf("historical artifact invented exact predicate: %s %d", url, expect)
		}
		return nil
	}
	var saved map[string]any
	if _, err := d.Reconcile(t.Context(), deploy.Placement{}, 1, legacy, "legacy", func(string, ...any) {}, func(v map[string]any) error { saved = v; return nil }); err != nil {
		t.Fatal(err)
	}
	if saved["readinessPath"] != "/healthz" || saved["readiness"] != nil || calls != 1 {
		t.Fatalf("legacy checkpoint changed: %+v calls%d", saved, calls)
	}
}

func TestAppPlatformNewCheckpointCannotLoseItsPredicate(t *testing.T) {
	d := NewDriver(Dependencies{Client: func(context.Context) (*Client, Config, error) {
		t.Fatal("incomplete checkpoint reached provider")
		return nil, Config{}, nil
	}}, Options{})
	for _, kind := range []any{"do-app-spec-v2", "unknown", 17} {
		if _, err := d.Resume(t.Context(), deploy.Placement{}, 1, map[string]any{"appId": "app-1", "deploymentId": "dep-1", "artifactKind": kind}, func(string, ...any) {}); err == nil {
			t.Fatalf("missing predicate accepted for format%v", kind)
		}
	}
	if _, err := d.Resume(t.Context(), deploy.Placement{}, 1, map[string]any{"appId": "app-1", "deploymentId": "dep-1", "readiness": map[string]any{"version": 1, "path": "/ready", "expect": 200}}, func(string, ...any) {}); err == nil {
		t.Fatal("new predicate without format accepted")
	}
}
