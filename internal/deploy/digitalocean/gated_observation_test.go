package digitalocean

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy"
)

// platformWithDeployment answers one app whose deployment ends in phase,
// with the failing component's deploy log holding row.
func platformWithDeployment(t *testing.T, phase, row string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v2/apps/app-1/deployments/dep-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"deployment": map[string]any{"id": "dep-1", "phase": phase,
				"progress": map[string]any{"steps": []map[string]any{{"name": "deploy", "status": "ERROR", "steps": []map[string]any{{"name": "components", "status": "ERROR",
					"steps": []map[string]any{{"name": "web", "status": "ERROR", "reason": map[string]any{"message": "Your deploy failed because your container exited with a non-zero exit code."}}}}}}}}}})
		case r.URL.Path == "/v2/apps/app-1/deployments/dep-1/components/web/logs":
			_ = json.NewEncoder(w).Encode(map[string]any{"historic_urls": []string{srv.URL + "/log"}})
		case r.URL.Path == "/log":
			fmt.Fprintf(w, "starting\nduplicate key value violates unique constraint: %s\n", row)
		case r.URL.Path == "/v2/apps/app-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"id": "app-1", "default_ingress": "https://copy.example",
				"active_deployment": map[string]any{"id": "dep-1", "phase": "ACTIVE"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func resumeOn(t *testing.T, srv *httptest.Server, p deploy.Placement, probed *[]string) (string, error) {
	t.Helper()
	d := NewDriver(Dependencies{Slug: "orders", Client: func(context.Context) (*Client, Config, error) {
		return &Client{Base: srv.URL, HTTP: srv.Client()}, Config{Region: "syd"}, nil
	}}, Options{PollInterval: time.Millisecond, ProbeFrontDoor: func(_ context.Context, url string, _ time.Time, _ func(string, ...any)) error {
		*probed = append(*probed, url)
		return nil
	}})
	var log []string
	_, err := d.Resume(context.Background(), p, 1, map[string]any{"appId": "app-1", "appName": "orders-copy-3", "deploymentId": "dep-1", "readinessPath": "/healthz"},
		func(f string, a ...any) { log = append(log, fmt.Sprintf(f, a...)) })
	return strings.Join(log, "\n"), err
}

// A gated app runs on production data, so when it fails to deploy its
// component's log, which can hold a row (a duplicate key names its
// value), is not copied into the forge's log; the platform's own sentence
// for the failing step is kept. An ungated app's log tail still is, which
// shows the test can see one.
func TestGatedDeploymentFailureCopiesNoComponentLog(t *testing.T) {
	t.Parallel()
	const row = "alice@example.com"
	srv := platformWithDeployment(t, "ERROR", row)
	var probed []string
	log, err := resumeOn(t, srv, deploy.Placement{Name: "copy-3", Gate: &deploy.Gate{}}, &probed)
	if err == nil || !strings.Contains(err.Error(), "non-zero exit code") {
		t.Fatalf("the failure is not the platform's sentence: %v", err)
	}
	if strings.Contains(log, row) || strings.Contains(err.Error(), row) {
		t.Fatalf("a production row reached AppTown's log:\n%s", log)
	}
	if !strings.Contains(log, "web failed; its log is not copied here") {
		t.Fatalf("the log does not say why no tail is shown:\n%s", log)
	}
	log, _ = resumeOn(t, srv, deploy.Placement{Name: "cloud"}, &probed)
	if !strings.Contains(log, row) {
		t.Fatalf("an ungated failure's tail was not shown, so this test sees nothing:\n%s", log)
	}
}

// The gate answers anyone without a credential 404, so a gated app is
// never asked at its front door anonymously when it goes active: the
// forge's own checks ask it through the gate. An ungated one still is.
func TestGatedDeploymentIsNotProbedAnonymously(t *testing.T) {
	t.Parallel()
	srv := platformWithDeployment(t, "ACTIVE", "")
	var probed []string
	log, err := resumeOn(t, srv, deploy.Placement{Name: "copy-3", Gate: &deploy.Gate{}}, &probed)
	if err != nil {
		t.Fatal(err)
	}
	if len(probed) != 0 {
		t.Fatalf("the gated front door was asked anonymously: %v", probed)
	}
	if !strings.Contains(log, "the front door is AppTown's gate") {
		t.Fatalf("log:\n%s", log)
	}
	if _, err := resumeOn(t, srv, deploy.Placement{Name: "cloud"}, &probed); err != nil {
		t.Fatal(err)
	}
	if len(probed) != 1 {
		t.Fatalf("an ungated front door was not asked, so this test sees nothing: %v", probed)
	}
}

// An app found by its name is a gated deployment's only when it carries
// that deployment's own mark: two projects' names can join into the same
// app name, and an app without the mark is reported, never taken as ours.
func TestFindMadeTakesOnlyAnAppCarryingTheMark(t *testing.T) {
	t.Parallel()
	app := func(marker string) map[string]any {
		return map[string]any{"id": "app-7", "spec": map[string]any{"name": "orders-copy-3", "services": []map[string]any{
			{"name": GateComponent, "envs": []map[string]any{{"key": deploy.GateEnvMarker, "value": marker}}}}}}
	}
	for _, tc := range []struct {
		name   string
		app    map[string]any
		marker string
		ours   bool
	}{
		{"its mark", app("mine-0123456789abcdef0123456789ab"), "mine-0123456789abcdef0123456789ab", true},
		{"another mark", app("theirs-123456789abcdef0123456789"), "mine-0123456789abcdef0123456789ab", false},
		{"no mark at all", map[string]any{"id": "app-7", "spec": map[string]any{"name": "orders-copy-3"}}, "mine-0123456789abcdef0123456789ab", false},
		{"no mark recorded", app(""), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v2/apps":
					_ = json.NewEncoder(w).Encode(map[string]any{"apps": []map[string]any{tc.app}, "links": map[string]any{}, "meta": map[string]any{"total": 1}})
				case "/v2/databases":
					_ = json.NewEncoder(w).Encode(map[string]any{"databases": []map[string]any{}, "links": map[string]any{}, "meta": map[string]any{"total": 0}})
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			d := NewDriver(Dependencies{Slug: "orders", Client: func(context.Context) (*Client, Config, error) {
				return &Client{Base: srv.URL, HTTP: srv.Client()}, Config{Region: "syd"}, nil
			}}, Options{})
			made, err := d.FindMade(context.Background(), deploy.Placement{Name: "copy-3", Gate: &deploy.Gate{Marker: tc.marker}})
			if err != nil {
				t.Fatal(err)
			}
			if (made.AppID == "app-7") != tc.ours || (len(made.NotOurs) == 1) == tc.ours {
				t.Fatalf("found %+v", made)
			}
		})
	}
}
