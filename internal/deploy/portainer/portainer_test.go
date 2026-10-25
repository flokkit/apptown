package portainer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// The endpoint is found by the environment's name; an unknown name is a
// plain sentence; the platform is the endpoint's Docker's answer in OCI form,
// and an endpoint that names none has no platform.
func TestEndpointByName(t *testing.T) {
	var sawKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawKey = r.Header.Get("X-API-Key")
		switch r.URL.Path {
		case "/api/endpoints":
			_, _ = w.Write([]byte(`[{"Id":1,"Name":"other"},{"Id":3,"Name":"local"}]`))
		case "/api/endpoints/3/docker/version":
			_, _ = w.Write([]byte(`{"Os":"linux","Arch":"amd64"}`))
		case "/api/endpoints/4/docker/version":
			_, _ = w.Write([]byte(`{}`))
		case "/api/endpoints/5/docker/version":
			_, _ = w.Write([]byte(`{"Os":"linux","Arch":"s390x"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cfg := &Target{PortainerURL: server.URL, PortainerKey: "k", EnvName: "local"}
	id, err := EndpointID(context.Background(), cfg)
	if err != nil || id != 3 || sawKey != "k" {
		t.Fatalf("endpoint: %v %d key=%q", err, id, sawKey)
	}
	if got, err := EndpointPlatform(context.Background(), cfg, 3); err != nil || got != "linux/amd64" {
		t.Fatalf("platform: %q %v", got, err)
	}
	if got, err := EndpointPlatform(context.Background(), cfg, 9); err == nil || got != "" {
		t.Fatalf("an endpoint that does not answer has no platform: %q %v", got, err)
	}
	// P07: an empty answer, or one naming no known platform, is refused,
	// never taken for "any platform".
	for _, id := range []int64{4, 5} {
		if got, err := EndpointPlatform(context.Background(), cfg, id); err == nil || got != "" {
			t.Fatalf("endpoint %d's platform: %q %v", id, got, err)
		}
	}
	cfg.EnvName = "missing"
	if _, err := EndpointID(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), `no environment named "missing"`) {
		t.Fatalf("missing environment: %v", err)
	}
}

// Portainer says whether each stack's app container runs: one list of the
// environment's app containers answers every deployment on the target.
// Running is active; stopped or missing is absent (stopped); a handle with
// no stack is unknown unless nothing was released; a failed list is an
// error, never stopped. A stack AppTown did not record creating is its
// Portainer owner's to take down.
func TestObserveAsksPortainerWhetherEachStacksAppRuns(t *testing.T) {
	var lists int
	failing := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/endpoints":
			_, _ = w.Write([]byte(`[{"Id":3,"Name":"lan"}]`))
		case "/api/endpoints/3/docker/containers/json":
			lists++
			if failing {
				w.WriteHeader(502)
				return
			}
			if r.URL.Query().Get("all") != "1" || !strings.Contains(r.URL.Query().Get("filters"), "com.docker.compose.service=app") {
				t.Errorf("the list asked %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[
				{"State":"exited","Image":"reg/snake@sha256:old","Labels":{"com.docker.compose.project":"apptown-snake","com.docker.compose.service":"app"}},
				{"State":"running","Image":"reg/snake@sha256:new","Labels":{"com.docker.compose.project":"apptown-snake","com.docker.compose.service":"app"}},
				{"State":"exited","Image":"reg/racer@sha256:r","Labels":{"com.docker.compose.project":"apptown-racer","com.docker.compose.service":"app"}},
				{"State":"running","Image":"postgres:16","Labels":{"com.docker.compose.project":"apptown-racer","com.docker.compose.service":"postgres"}}]`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cfg := &Target{PortainerURL: server.URL, PortainerKey: "k", EnvName: "lan"}
	inv := Inventory{Config: func() (*Target, error) { return cfg, nil }}
	placed := func(stack string) deploy.Placement {
		return deploy.Placement{Generation: 4, URL: "https://" + stack, Handle: map[string]any{"stack": stack}}
	}
	ps := []deploy.Placement{placed("apptown-snake"), placed("apptown-racer"), placed("apptown-gone"),
		{Generation: 4, URL: "https://old", Handle: map[string]any{}}, {Handle: map[string]any{}}}
	got, err := inv.ObserveAll(context.Background(), ps)
	if err != nil || len(got) != len(ps) {
		t.Fatalf("observing: %+v %v", got, err)
	}
	for i, want := range []string{"active", "absent", "absent", "unknown", "absent"} {
		if got[i].Rollout != want {
			t.Errorf("placement %d: %s, want %s", i, got[i].Rollout, want)
		}
	}
	if got[0].Generation != 4 || got[0].URL != "https://apptown-snake" || got[4].Generation != 0 {
		t.Errorf("observations: %+v", got)
	}
	// What runs is the running app container's image; a stopped one still
	// names its image; a stack with no app container names none (gone).
	for i, want := range []string{"reg/snake@sha256:new", "reg/racer@sha256:r", ""} {
		if got[i].Running != want {
			t.Errorf("placement %d runs %q, want %q", i, got[i].Running, want)
		}
	}
	if lists != 1 {
		t.Errorf("Portainer was asked for its containers %d times for one target", lists)
	}
	d := &Driver{TargetID: idstest.N(2), Config: func(ids.ID) (*Target, error) { return cfg, nil }}
	if o, err := d.Observe(context.Background(), placed("apptown-snake")); err != nil || o.Rollout != "active" {
		t.Fatalf("one deployment: %+v %v", o, err)
	}
	failing = true
	if got, err := inv.ObserveAll(context.Background(), ps); err == nil {
		t.Fatalf("a failed list was read as %+v", got)
	}
	if _, err := d.Teardown(context.Background(), deploy.Placement{Generation: 4}, 4, deploy.TakeDown{}); err == nil || !strings.Contains(err.Error(), "did not record creating") {
		t.Fatalf("a stack AppTown did not make was taken down: %v", err)
	}
	if c := d.Controls(); !c.Liveness || !c.Rollout {
		t.Fatal("Portainer owns liveness and rollout")
	}
	if _, err := d.Resume(context.Background(), deploy.Placement{}, 4, nil, func(string, ...any) {}); err == nil {
		t.Fatal("a lost apply is not resolved by guessing")
	}
}

type stackBook struct{ made []deploy.Owned }

func (b *stackBook) Created(_ context.Context, o deploy.Owned) error {
	b.made = append(b.made, o)
	return nil
}

// A stack is recorded as AppTown's only when AppTown's own create made
// it; updating one that was there records nothing.
func TestOnlyAStackAppTownCreatedIsRecorded(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing %v", existing), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "GET" && r.URL.Path == "/api/stacks":
					if existing {
						fmt.Fprint(w, `[{"Id":7,"Name":"shop-lan","EndpointId":3}]`)
					} else {
						fmt.Fprint(w, `[]`)
					}
				case r.Method == "PUT" && r.URL.Path == "/api/stacks/7":
					fmt.Fprint(w, `{"Id":7}`)
				case r.Method == "POST" && r.URL.Path == "/api/stacks/create/standalone/string":
					fmt.Fprint(w, `{"Id":12}`)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL)
					http.Error(w, "unexpected", 500)
				}
			}))
			defer server.Close()
			book := &stackBook{}
			_, err := (Execution{Config: Target{PortainerURL: server.URL, EnvName: "lan"}, EndpointID: 3, Stack: "shop-lan", Compose: "services: {}", Owner: book, Log: func(string, ...any) {}}).
				Execute(context.Background(), 1, func(map[string]any) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			want := []deploy.Owned{{Kind: "stack", ID: "12", Name: "shop-lan"}}
			if existing {
				want = nil
			}
			if !reflect.DeepEqual(book.made, want) {
				t.Fatalf("recorded %+v want %+v", book.made, want)
			}
		})
	}
}

// Take-down deletes the stack AppTown made and keeps its volumes, each
// named for a person to delete; a stack Portainer would not delete fails
// the take-down.
func TestTakeDownDeletesAppTownsStackAndKeepsItsVolumes(t *testing.T) {
	for _, scenario := range []string{"deleted", "refused"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			var deleted []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/endpoints":
					fmt.Fprint(w, `[{"Id":3,"Name":"lan"}]`)
				case r.Method == "GET" && r.URL.Path == "/api/stacks/12/file":
					fmt.Fprint(w, `{"StackFileContent":"services: {}\nvolumes:\n  forge-data-shop: {}\n  named:\n    name: shop-uploads\n  shared:\n    external: true\n"}`)
				case r.Method == "DELETE" && r.URL.Path == "/api/stacks/12":
					if scenario == "refused" {
						http.Error(w, "no", 500)
						return
					}
					deleted = append(deleted, r.URL.String())
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL)
					http.Error(w, "unexpected", 500)
				}
			}))
			defer server.Close()
			d := &Driver{TargetID: idstest.N(2), Config: func(ids.ID) (*Target, error) { return &Target{PortainerURL: server.URL, EnvName: "lan"}, nil }}
			out, err := d.Teardown(context.Background(), deploy.Placement{Generation: 1}, 1, deploy.TakeDown{Owned: []deploy.Owned{{Kind: "stack", ID: "12", Name: "shop-lan"}}})
			if scenario == "refused" {
				if err == nil {
					t.Fatal("a stack Portainer kept was reported deleted")
				}
				return
			}
			if err != nil || len(deleted) != 1 || !strings.Contains(deleted[0], "endpointId=3") || !reflect.DeepEqual(out.Deleted, []string{"stack shop-lan"}) {
				t.Fatalf("teardown: %+v %v %v", out, deleted, err)
			}
			var left []string
			for _, l := range out.Left {
				if !l.Data || l.Why == "" {
					t.Errorf("volume not kept as data with a reason: %+v", l)
				}
				left = append(left, l.What)
			}
			if !reflect.DeepEqual(left, []string{"volume shop-lan_forge-data-shop", "volume shop-uploads"}) {
				t.Fatalf("kept volumes: %v", left)
			}
		})
	}
}

// Not parallel: the downgrade case installs only its test server's transport.
func TestPortainerCredentialsStayAtTheConfiguredEndpoint(t *testing.T) {
	cases := []struct {
		name            string
		status          int
		sameOrigin, tls bool
	}{
		{"success", http.StatusOK, false, false},
		{"provider-failure", http.StatusForbidden, false, false},
		{"same-origin-loop", http.StatusFound, true, false},
		{"https-downgrade", http.StatusFound, false, true},
	}
	for _, status := range []int{301, 302, 303, 307, 308} {
		cases = append(cases, struct {
			name            string
			status          int
			sameOrigin, tls bool
		}{fmt.Sprintf("cross-origin-%d", status), status, false, false})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var destinationCalls, sourceCalls atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				destinationCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer destination.Close()
			source := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sourceCalls.Add(1)
				if got := r.Header.Get("X-API-Key"); got != "fixture-secret" {
					t.Errorf("initial credential = %q", got)
				}
				if tc.status >= 300 && tc.status < 400 {
					target := destination.URL + "/redirected"
					if tc.sameOrigin {
						target = r.URL.Path
					}
					w.Header().Set("Location", target)
				}
				w.WriteHeader(tc.status)
			}))
			if tc.tls {
				source.StartTLS()
			} else {
				source.Start()
			}
			defer source.Close()
			previous := http.DefaultTransport
			if tc.tls {
				http.DefaultTransport = source.Client().Transport
				defer func() { http.DefaultTransport = previous }()
			}
			status, _, err := JSON(t.Context(), &Target{PortainerURL: source.URL, PortainerKey: "fixture-secret"}, http.MethodPost, "/api/stacks", map[string]any{"name": "fixture"})
			redirect := tc.status >= 300 && tc.status < 400
			if redirect {
				if err == nil {
					t.Error("redirect reported success")
				}
			} else {
				if err != nil || status != tc.status {
					t.Fatalf("provider response: status=%d err=%v", status, err)
				}
			}
			if got := destinationCalls.Load(); got != 0 {
				t.Errorf("redirect destination received %d requests", got)
			}
			if got := sourceCalls.Load(); got != 1 {
				t.Errorf("configured endpoint received %d requests; want one", got)
			}
		})
	}
}
