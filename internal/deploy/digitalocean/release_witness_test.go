package digitalocean

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/digitalocean/dofake"
)

// The identity the front door must answer comes from the selected source:
// the public part's name and the release's revision, when that part is a
// static site. Another service's declared APPTOWN_RELEASE, or a private static
// part behind a dynamic public one, never decides it: a service declaring
// the previous release's value must not pass as "the front door serves this
// release".
func TestTheReleaseWitnessIsTheSelectedPublicStaticPart(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, contract, want string
		witnessed            bool
	}{
		{"another service declares a release", "app:\n  api:\n    image: api:1\n    ports: [\"9000\"]\n  web:\n    static: site\n    public: true\n", "web@selected-new", true},
		{"a private static part behind a dynamic public one", "app:\n  assets:\n    static: site\n    ports: [\"8081\"]\n  web:\n    image: web:1\n    public: true\n", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			contract, err := appcontract.Parse([]byte(c.contract))
			if err != nil {
				t.Fatal(err)
			}
			// The contract refuses APPTOWN_RELEASE in a part's environment;
			// a stored contract from before that refusal may still carry it,
			// and it must not decide the witness.
			for i := range contract.Parts {
				if contract.Parts[i].Name == "api" {
					contract.Parts[i].Env = map[string]string{"APPTOWN_RELEASE": "previous-release"}
				}
			}
			images := map[string]string{}
			for _, p := range contract.Parts {
				images[p.Name] = "local-" + p.Name
			}
			src := deploy.Source{Images: images, Contract: contract, Revision: "selected-new"}
			if got := expectedRelease(&src); got != c.want {
				t.Fatalf("expected release %q, want %q", got, c.want)
			}
			api := &dofake.API{}
			api.ServeFrontDoor(false)
			server := httptest.NewServer(api.Handler())
			defer server.Close()
			push := func(_ *Driver, _ context.Context, _ *Client, _, _, _, _ string, _ func(string, ...any)) (string, error) {
				return "sha256:" + strings.Repeat("a", 64), nil
			}
			d := NewDriver(Dependencies{Slug: "shop", Client: func(context.Context) (*Client, Config, error) {
				return NewClient("test", server.URL), Config{Region: "syd", Registry: "forge-test"}, nil
			}}, Options{PushImage: push, PollInterval: time.Millisecond, ProbeFrontDoor: func(context.Context, string, time.Time, func(string, ...any)) error { return nil }})
			var log []string
			logf := func(f string, a ...any) { log = append(log, fmt.Sprintf(f, a...)) }
			art, err := d.Render(context.Background(), deploy.Placement{Name: "cloud"}, src, nil, scopedRenderSecrets{}, logf)
			if err != nil {
				t.Fatal(err)
			}
			art.Source = &src
			var noted map[string]any
			if _, err := d.Reconcile(context.Background(), deploy.Placement{Name: "cloud"}, 1, art, "k", logf, func(ids map[string]any) error { noted = ids; return nil }); err != nil {
				t.Fatalf("reconcile: %v\n%s", err, strings.Join(log, "\n"))
			}
			joined := strings.Join(log, "\n")
			if c.witnessed != strings.Contains(joined, "the front door serves this release: ") || c.witnessed == strings.Contains(joined, "not witnessed") {
				t.Fatalf("witnessed %v:\n%s", c.witnessed, joined)
			}
			if got, _ := noted["release"].(string); got != c.want {
				t.Fatalf("the checkpoint keeps %q for recovery, want %q", got, c.want)
			}
		})
	}
	// A front door answering the other service's declared value is not this
	// release.
	stale := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "previous-release") }))
	defer stale.Close()
	if err := witnessRelease(context.Background(), stale.Client(), stale.URL+appcontract.ReleasePath, "web@selected-new", time.Time{}, func(string, ...any) {}, time.Millisecond, time.Now); err == nil {
		t.Fatal("a front door answering another release was witnessed as this one")
	}
}

// App Platform's front door is asked only when the contract gives AppTown
// something to ask: an HTTP readiness path, or a static site's release
// identity. A dynamic app without either settles on the platform's own
// active deployment and probe, with no request from AppTown, and the
// release log says the release it serves is not witnessed.
//
// Kills: a guide or result that claims AppTown asked the app.
func TestAppPlatformAsksTheFrontDoorOnlyWhenTheContractSaysWhat(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, contract string
		asked          int
	}{
		{"no readiness path", "app:\n  web:\n    image: web:1\n    ports: [\"8080\"]\n    public: true\n", 0},
		{"an HTTP readiness path", "app:\n  web:\n    image: web:1\n    ports: [\"8080\"]\n    public: true\n    health:\n      readiness: { http: /ready }\n", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			contract, err := appcontract.Parse([]byte(c.contract))
			if err != nil {
				t.Fatal(err)
			}
			// The contract refuses APPTOWN_RELEASE in a part's environment;
			// a stored contract from before that refusal may still carry it,
			// and it must not decide the witness.
			for i := range contract.Parts {
				if contract.Parts[i].Name == "api" {
					contract.Parts[i].Env = map[string]string{"APPTOWN_RELEASE": "previous-release"}
				}
			}
			src := deploy.Source{Images: map[string]string{"web": "local-web"}, Contract: contract, Revision: "r1"}
			api := &dofake.API{}
			api.ServeFrontDoor(false)
			server := httptest.NewServer(api.Handler())
			defer server.Close()
			push := func(_ *Driver, _ context.Context, _ *Client, _, _, _, _ string, _ func(string, ...any)) (string, error) {
				return "sha256:" + strings.Repeat("a", 64), nil
			}
			asked := 0
			d := NewDriver(Dependencies{Slug: "shop", Client: func(context.Context) (*Client, Config, error) {
				return NewClient("test", server.URL), Config{Region: "syd", Registry: "forge-test"}, nil
			}}, Options{PushImage: push, PollInterval: time.Millisecond, ProbeFrontDoor: func(context.Context, string, time.Time, func(string, ...any)) error { asked++; return nil }})
			var log []string
			logf := func(f string, a ...any) { log = append(log, fmt.Sprintf(f, a...)) }
			art, err := d.Render(context.Background(), deploy.Placement{Name: "cloud"}, src, nil, scopedRenderSecrets{}, logf)
			if err != nil {
				t.Fatal(err)
			}
			art.Source = &src
			obs, err := d.Reconcile(context.Background(), deploy.Placement{Name: "cloud"}, 1, art, "k", logf, func(map[string]any) error { return nil })
			if err != nil {
				t.Fatalf("reconcile: %v\n%s", err, strings.Join(log, "\n"))
			}
			if asked != c.asked || obs.Rollout != "active" || !strings.Contains(strings.Join(log, "\n"), "not witnessed") {
				t.Fatalf("asked the front door %d times (want %d), %+v\n%s", asked, c.asked, obs, strings.Join(log, "\n"))
			}
		})
	}
}
