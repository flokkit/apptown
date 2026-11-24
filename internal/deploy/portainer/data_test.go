package portainer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// environment is a fake Portainer with one environment's Docker volumes.
type environment struct {
	mu      sync.Mutex
	volumes map[string]bool
	deleted []string
	// onDeploy runs when the stack is created: the volumes compose makes.
	onDeploy     func()
	background   bool
	status       int
	update       bool
	reject       string
	rejectCode   int
	reads        int
	checkpointID int64
}

func (e *environment) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		volume, isVolume := strings.CutPrefix(r.URL.Path, "/api/endpoints/3/docker/volumes/")
		switch {
		case r.URL.Path == "/api/endpoints":
			fmt.Fprint(w, `[{"Id":3,"Name":"lan","Type":2}]`)
		case r.Method == "GET" && r.URL.Path == "/api/stacks":
			if e.update {
				fmt.Fprint(w, `[{"Id":12,"Name":"forge-shop","EndpointId":3}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		case r.Method == "GET" && r.URL.Path == "/api/stacks/12":
			e.reads++
			if e.checkpointID != 12 {
				t.Error("status read before stack ID checkpoint")
			}
			if e.onDeploy != nil {
				e.onDeploy()
			}
			fmt.Fprintf(w, `{"Id":12,"Status":%d,"DeploymentStatus":[{"Status":3,"Message":"starting"},{"Status":%d,"Message":"manifest unknown"}]}`, e.status, e.status)
		case r.Method == "PUT" && r.URL.Path == "/api/stacks/12", r.Method == "POST" && r.URL.Path == "/api/stacks/create/standalone/string":
			if e.rejectCode > 0 {
				w.WriteHeader(e.rejectCode)
				fmt.Fprint(w, e.reject)
				return
			}
			if e.background {
				fmt.Fprint(w, `{"Id":12,"Status":3}`)
				return
			}
			if e.onDeploy != nil {
				e.onDeploy()
			}
			fmt.Fprint(w, `{"Id":12,"Status":1}`)
		case r.Method == "GET" && r.URL.Path == "/api/stacks/12/file":
			_ = json.NewEncoder(w).Encode(map[string]string{"StackFileContent": scopeFixtureCompose("services: {}\nvolumes:\n  forge-data-shop: {}\n  forge-need-shop-postgres: {}\n")})
		case r.Method == "DELETE" && r.URL.Path == "/api/stacks/12":
			w.WriteHeader(204)
		case r.Method == "GET" && r.URL.Path == "/api/endpoints/3/docker/containers/json": // no readiness readers
			fmt.Fprint(w, `[]`)
		case isVolume && r.Method == "GET":
			if !e.volumes[volume] {
				http.Error(w, `{"message":"no such volume"}`, 404)
				return
			}
			fmt.Fprintf(w, `{"Name":%q}`, volume)
		case isVolume && r.Method == "DELETE":
			delete(e.volumes, volume)
			e.deleted = append(e.deleted, volume)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", 500)
		}
	})
}

const shopCompose = "services:\n  app:\n    volumes: [\"forge-data-shop:/data\"]\n  postgres:\n    volumes: [\"forge-need-shop-postgres:/var/lib/postgresql/data\"]\nvolumes:\n  forge-data-shop: {}\n  forge-need-shop-postgres: {}\n"

// A deploy records on the deployment's list each volume the stack made:
// one the environment did not hold before and holds after. A volume that
// was there before (an earlier take-down kept it, compose reuses it) is not
// recorded, so it is never the deployment's to delete.
func TestADeployRecordsTheVolumesItMade(t *testing.T) {
	t.Parallel()
	e := &environment{volumes: map[string]bool{"forge-shop_forge-data-shop": true}}
	e.onDeploy = func() { e.volumes["forge-shop_forge-need-shop-postgres"] = true }
	server := httptest.NewServer(e.handler(t))
	defer server.Close()
	book := &stackBook{}
	_, err := (execution{deploymentID: idstest.N(7), config: Target{Scope: scopeFixtureScope(server.URL), PortainerURL: server.URL, EnvName: "lan"}, endpointID: 3, stack: "forge-shop", prefix: "forge-", compose: scopeFixtureCompose(shopCompose), owner: book, log: func(string, ...any) {}}).
		execute(context.Background(), 1, func(map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []deploy.Owned{{Kind: "stack", ID: "12", Name: "forge-shop"}, {Kind: "volume", ID: "forge-shop_forge-need-shop-postgres", Name: "forge-shop_forge-need-shop-postgres", Data: true, Need: "postgres"}}
	if !reflect.DeepEqual(book.made, want) {
		t.Fatalf("recorded %+v, want %+v", book.made, want)
	}
}

// Take-down deletes a listed volume only when a complete final backup
// covers it, or its data is synthetic; a volume the stack declares that is
// not on the list is kept, whatever the backup.
func TestTakeDownDeletesOnlyTheListedVolumesItMayDelete(t *testing.T) {
	for _, td := range []deploy.TakeDown{{BackedUp: true}, {Synthetic: true}, {}} {
		t.Run(fmt.Sprintf("backed up %v, synthetic %v", td.BackedUp, td.Synthetic), func(t *testing.T) {
			t.Parallel()
			e := &environment{volumes: map[string]bool{"forge-shop_forge-data-shop": true, "forge-shop_forge-need-shop-postgres": true}}
			server := httptest.NewServer(e.handler(t))
			defer server.Close()
			d := &Driver{TargetID: idstest.N(2), Config: func(context.Context, ids.ID) (*Target, error) {
				return &Target{Scope: scopeFixtureScope(server.URL), PortainerURL: server.URL, EnvName: "lan"}, nil
			}}
			td.Owned = []deploy.Owned{{Kind: "stack", ID: "12", Name: "forge-shop"}, {Kind: "volume", ID: "forge-shop_forge-need-shop-postgres", Data: true, Need: "postgres"}}
			out, err := d.Teardown(context.Background(), deploy.Placement{ID: idstest.N(7), Generation: 1}, 1, td)
			if err != nil {
				t.Fatal(err)
			}
			var left []string
			for _, l := range out.Left {
				left = append(left, l.What)
			}
			mayGo := td.BackedUp || td.Synthetic
			if mayGo && (!slices.Equal(e.deleted, []string{"forge-shop_forge-need-shop-postgres"}) || !slices.Equal(left, []string{"volume forge-shop_forge-data-shop"})) {
				t.Fatalf("deleted %v, kept %v", e.deleted, left)
			}
			if !mayGo && (len(e.deleted) != 0 || len(left) != 2) {
				t.Fatalf("with no final backup: deleted %v, kept %v", e.deleted, left)
			}
		})
	}
}

// AppTown copies only in an environment whose Docker it can reach
// through Portainer: an edge agent in async mode, a Kubernetes environment
// or a Docker that does not answer are refused by name.
func TestOnlyAReachableDockerEnvironmentIsBackedUp(t *testing.T) {
	for _, tc := range []struct {
		name, endpoints string
		version         int
		why             string
	}{
		{"an agent", `[{"Id":3,"Name":"lan","Type":2}]`, 200, ""},
		{"a standard edge agent", `[{"Id":3,"Name":"lan","Type":4,"Edge":{"AsyncMode":false}}]`, 200, ""},
		{"an async edge agent", `[{"Id":3,"Name":"lan","Type":4,"Edge":{"AsyncMode":true}}]`, 200, "async mode"},
		{"Kubernetes", `[{"Id":3,"Name":"lan","Type":6}]`, 200, "not a Docker environment"},
		{"a Docker that does not answer", `[{"Id":3,"Name":"lan","Type":2}]`, 502, "does not answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/endpoints":
					fmt.Fprint(w, tc.endpoints)
				case "/api/endpoints/3/docker/version":
					w.WriteHeader(tc.version)
				default:
					t.Errorf("unexpected %s", r.URL)
				}
			}))
			defer server.Close()
			_, err := DataReach(context.Background(), &Target{PortainerURL: server.URL, EnvName: "lan"})
			if tc.why == "" && err != nil || tc.why != "" && (!errors.Is(err, ErrCannotBackUp) || !strings.Contains(err.Error(), tc.why) || !strings.Contains(err.Error(), `"lan"`)) {
				t.Fatalf("%s: %v", tc.name, err)
			}
		})
	}
}

// The Docker proxy forwards each request on its socket to the
// environment's Docker API through Portainer, with the key the target's
// configuration holds at that moment; the key never reaches the socket's
// side.
func TestTheDockerProxyForwardsWithTheCurrentKey(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("X-API-Key")+" "+r.URL.Path)
		mu.Unlock()
		fmt.Fprint(w, `{"Version":"29"}`)
	}))
	defer server.Close()
	key := "first"
	// A socket's path is short (about 100 bytes): a test's temporary
	// folder can be longer.
	dir, err := os.MkdirTemp("/tmp", "fp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	p, err := StartDockerProxy(dir, deploymentEnvironment{ID: 3, Name: "lan"}, func(context.Context) (*Target, error) {
		return &Target{PortainerURL: server.URL + "/portainer", PortainerKey: key}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p.Socket)
	}}}
	for _, k := range []string{"first", "second"} {
		key = k
		resp, err := client.Get("http://docker/v1.47/version")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.Header.Get("X-API-Key") != "" || strings.Contains(string(body), k) {
			t.Fatal("the key came back through the socket")
		}
	}
	if want := []string{"first /portainer/api/endpoints/3/docker/v1.47/version", "second /portainer/api/endpoints/3/docker/v1.47/version"}; !slices.Equal(seen, want) {
		t.Fatalf("forwarded %v, want %v", seen, want)
	}
}

// Portainer (2.45 and later) passes an empty request body on to an agent
// with no length, and Docker refuses a start whose body is of unknown
// length. So the proxy gives the CLI's body-less start an empty JSON object
// of known length, with or without the API version in the path; other
// body-less requests (a wait) go as they came.
func TestTheDockerProxyGivesAStartABodyOfKnownLength(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	seen := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen[r.URL.Path] = fmt.Sprintf("%d %q", r.ContentLength, body)
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer server.Close()
	client := proxyClient(t, server.URL)
	for _, path := range []string{"/v1.55/containers/job-1/start", "/containers/job-2/start", "/v1.55/containers/job-1/wait"} {
		resp, err := client.Post("http://docker"+path, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	want := map[string]string{
		"/api/endpoints/3/docker/v1.55/containers/job-1/start": `2 "{}"`,
		"/api/endpoints/3/docker/containers/job-2/start":       `2 "{}"`,
		"/api/endpoints/3/docker/v1.55/containers/job-1/wait":  `0 ""`,
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("forwarded %v, want %v", seen, want)
	}
}

// Portainer refuses a request with a message and its reason in details
// ({"message":"Proxy failure","details":"Forbidden to use bind mounts"});
// the Docker CLI prints only the message, so the proxy puts the reason in
// it, keeping the status. An answer without a separate reason is unchanged.
func TestTheDockerProxyCarriesPortainersReason(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/create") {
			w.WriteHeader(502)
			fmt.Fprint(w, `{"message":"Proxy failure","details":"Forbidden to use bind mounts"}`)
			return
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"message":"No such container: job-1"}`)
	}))
	defer server.Close()
	client := proxyClient(t, server.URL)
	for path, want := range map[string]string{
		"/v1.55/containers/create":      `502 {"details":"Forbidden to use bind mounts","message":"Proxy failure: Forbidden to use bind mounts"}`,
		"/v1.55/containers/job-1/start": `404 {"message":"No such container: job-1"}`,
	} {
		resp, err := client.Post("http://docker"+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if got := fmt.Sprintf("%d %s", resp.StatusCode, body); got != want {
			t.Errorf("%s answered %s, want %s", path, got, want)
		}
	}
}

// proxyClient is an HTTP client of a DockerProxy to environment 3 of the
// Portainer at url.
func proxyClient(t *testing.T, url string) *http.Client {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "fp") // a socket's path is short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	p, err := StartDockerProxy(dir, deploymentEnvironment{ID: 3, Name: "lan"}, func(context.Context) (*Target, error) {
		return &Target{PortainerURL: url, PortainerKey: "key"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p.Socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

// noSecrets delivers no secrets to any consumer.
type noSecrets struct{}

func (noSecrets) EnvFor(string) map[string]string { return nil }

// A data folder with $ in its path is found where the app was told its
// data is: the stack file writes the $ as $$ for Compose, and the layout
// data protection reads is the path Compose mounts.
func TestTheLayoutFindsADataFolderAtThePathTheAppWasGiven(t *testing.T) {
	t.Parallel()
	compose := deploy.IntranetCompose(deploy.IntranetStack{Secrets: noSecrets{}, Prefix: "forge-", Base: "app", Hosts: []string{"app.apps.example.com"},
		Domain: "apps.example.com", Image: "reg/app@sha256:x", Port: 8080, PublicPart: "web", DataDir: "/data/$tenant",
		Needs: []deploy.IntranetNeed{{Name: "postgres", Image: "postgres:16", Data: "/pg/$$x"}}})
	layout, err := ReadLayout("forge-app", "app", compose)
	if err != nil {
		t.Fatal(err)
	}
	for volume, want := range map[string]mount{
		"forge-app_forge-data-app":          {Service: "app", Path: "/data/$tenant"},
		"forge-app_forge-need-app-postgres": {Service: "postgres", Path: "/pg/$$x"},
	} {
		if got, ok := layout.MountOf(volume); !ok || got != want {
			t.Errorf("%s is mounted at %+v (found: %v); want %+v\n%s", volume, got, ok, want, compose)
		}
	}
}

// Compose makes volumes only when the background operation completes.
func TestABackgroundStackCreateRecordsTheVolumesComposeMade(t *testing.T) {
	t.Parallel()
	e := &environment{background: true, status: 1, volumes: map[string]bool{}}
	e.onDeploy = func() { e.volumes["forge-shop_forge-need-shop-postgres"] = true }
	book, _, err := runBackgroundStack(t, e)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(book.made, func(v deploy.Owned) bool { return v.Kind == "volume" && v.Need == "postgres" }) {
		t.Fatalf("compose volume missing: %+v", book.made)
	}
}

func TestAStackThatSettlesFailedFailsTheRelease(t *testing.T) {
	t.Parallel()
	assertFailedStack(t, false)
}
func TestAnUpdateThatSettlesFailedFailsTheRelease(t *testing.T) {
	t.Parallel()
	assertFailedStack(t, true)
}
func assertFailedStack(t *testing.T, update bool) {
	t.Helper()
	e := &environment{background: true, status: 4, update: update, volumes: map[string]bool{}}
	e.onDeploy = func() { e.volumes["forge-shop_forge-need-shop-postgres"] = true }
	book, noted, err := runBackgroundStack(t, e)
	if err == nil || !deploy.IsTerminal(err) || !strings.Contains(err.Error(), "manifest unknown") || noted["stackApplied"] == true {
		t.Fatalf("failed compose: %v noted%v", err, noted)
	}
	if !update && !slices.ContainsFunc(book.made, func(v deploy.Owned) bool { return v.Kind == "stack" && v.ID == "12" }) {
		t.Fatalf("failed create lost ownership: %+v", book.made)
	}
	if !slices.ContainsFunc(book.made, func(v deploy.Owned) bool { return v.Need == "postgres" }) {
		t.Fatalf("partial compose volume lost ownership: %+v", book.made)
	}
}

func TestAStackStillDeployingAtTheDeadlineIsUnknown(t *testing.T) {
	t.Parallel()
	_, noted, err := runBackgroundStack(t, &environment{background: true, status: 3, volumes: map[string]bool{}})
	if err == nil || deploy.IsTerminal(err) || noted["stackApplied"] == true || noted["stackId"] != int64(12) {
		t.Fatalf("unconfirmed apply: %v noted%v", err, noted)
	}
}

func TestASynchronousComposeFailureIsTerminal(t *testing.T) {
	for _, update := range []bool{false, true} {
		for _, confirmed := range []bool{false, true} {
			t.Run(fmt.Sprintf("update=%v/confirmed=%v", update, confirmed), func(t *testing.T) {
				t.Parallel()
				message := "upstream unavailable"
				if confirmed {
					message = "failed to deploy a stack: compose up operation failed: manifest unknown"
				}
				raw, _ := json.Marshal(map[string]string{"message": message})
				_, _, err := runBackgroundStack(t, &environment{update: update, rejectCode: 500, reject: string(raw), volumes: map[string]bool{}})
				if err == nil || deploy.IsTerminal(err) != confirmed || !strings.Contains(err.Error(), message) {
					t.Fatalf("synchronous failure: %v", err)
				}
			})
		}
	}
}

func TestTakeDownAfterABackgroundCreateDeletesTheNeedVolumes(t *testing.T) {
	t.Parallel()
	e := &environment{background: true, status: 1, volumes: map[string]bool{}}
	e.onDeploy = func() { e.volumes["forge-shop_forge-need-shop-postgres"] = true }
	book, _, err := runBackgroundStack(t, e)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(e.handler(t))
	t.Cleanup(server.Close)
	d := Driver{Config: func(context.Context, ids.ID) (*Target, error) {
		return &Target{Scope: scopeFixtureScope(server.URL), PortainerURL: server.URL}, nil
	}}
	if _, err := d.Teardown(t.Context(), deploy.Placement{ID: idstest.N(7), Generation: 1}, 1, deploy.TakeDown{Owned: book.made, BackedUp: true}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.deleted, []string{"forge-shop_forge-need-shop-postgres"}) {
		t.Fatalf("need volume retained: %v", e.deleted)
	}
}

func runBackgroundStack(t *testing.T, e *environment) (*stackBook, map[string]any, error) {
	t.Helper()
	server := httptest.NewServer(e.handler(t))
	t.Cleanup(server.Close)
	book := &stackBook{}
	noted := map[string]any{}
	wait := 5 * time.Second
	if e.status == 3 {
		wait = 20 * time.Millisecond
	}
	_, err := (execution{settleTimeout: wait, deploymentID: idstest.N(7), config: Target{Scope: scopeFixtureScope(server.URL), PortainerURL: server.URL}, endpointID: 3, stack: "forge-shop", prefix: "forge-", compose: scopeFixtureCompose(shopCompose), owner: book, log: func(string, ...any) {}}).execute(t.Context(), 1, func(values map[string]any) error {
		e.mu.Lock()
		defer e.mu.Unlock()
		for k, v := range values {
			noted[k] = v
		}
		if id, ok := values["stackId"].(int64); ok {
			e.checkpointID = id
		}
		if values["stackApplied"] == true && e.background && e.reads == 0 {
			t.Error("applied checkpoint before status read")
		}
		return nil
	})
	return book, noted, err
}
