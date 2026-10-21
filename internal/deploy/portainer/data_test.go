package portainer

import (
	"context"
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

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// environment is a fake Portainer with one environment's Docker volumes.
type environment struct {
	mu      sync.Mutex
	volumes map[string]bool
	deleted []string
	// onDeploy runs when the stack is created: the volumes compose makes.
	onDeploy func()
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
			fmt.Fprint(w, `[]`)
		case r.Method == "POST" && r.URL.Path == "/api/stacks/create/standalone/string":
			if e.onDeploy != nil {
				e.onDeploy()
			}
			fmt.Fprint(w, `{"Id":12}`)
		case r.Method == "GET" && r.URL.Path == "/api/stacks/12/file":
			fmt.Fprint(w, `{"StackFileContent":"services: {}\nvolumes:\n  forge-data-shop: {}\n  forge-need-shop-postgres: {}\n"}`)
		case r.Method == "DELETE" && r.URL.Path == "/api/stacks/12":
			w.WriteHeader(204)
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
	_, err := (Execution{Config: Target{PortainerURL: server.URL, EnvName: "lan"}, EndpointID: 3, Stack: "forge-shop", Prefix: "forge-", Compose: shopCompose, Owner: book, Log: func(string, ...any) {}}).
		Execute(context.Background(), 1, func(map[string]any) error { return nil })
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
			d := &Driver{TargetID: idstest.N(2), Config: func(ids.ID) (*Target, error) { return &Target{PortainerURL: server.URL, EnvName: "lan"}, nil }}
			td.Owned = []deploy.Owned{{Kind: "stack", ID: "12", Name: "forge-shop"}, {Kind: "volume", ID: "forge-shop_forge-need-shop-postgres", Data: true, Need: "postgres"}}
			out, err := d.Teardown(context.Background(), deploy.Placement{Generation: 1}, 1, td)
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
	p, err := StartDockerProxy(dir, Environment{ID: 3, Name: "lan"}, func(context.Context) (*Target, error) {
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

// noSecrets delivers no secrets to any consumer.
type noSecrets struct{}

func (noSecrets) EnvFor(string) map[string]string { return nil }

// A data folder with $ in its path is found where the app was told its
// data is: the stack file writes the $ as $$ for Compose, and the layout
// data protection reads is the path Compose mounts.
func TestTheLayoutFindsADataFolderAtThePathTheAppWasGiven(t *testing.T) {
	t.Parallel()
	compose := deploy.IntranetCompose(deploy.IntranetStack{Secrets: noSecrets{}, Prefix: "forge-", Slug: "app", Hosts: []string{"app.apps.example.com"},
		Domain: "apps.example.com", Image: "reg/app@sha256:x", Port: 8080, PublicPart: "web", DataDir: "/data/$tenant",
		Needs: []deploy.IntranetNeed{{Name: "postgres", Image: "postgres:16", Data: "/pg/$$x"}}})
	layout, err := ReadLayout("forge-app", "app", compose)
	if err != nil {
		t.Fatal(err)
	}
	for volume, want := range map[string]Mount{
		"forge-app_forge-data-app":          {Service: "app", Path: "/data/$tenant"},
		"forge-app_forge-need-app-postgres": {Service: "postgres", Path: "/pg/$$x"},
	} {
		if got, ok := layout.MountOf(volume); !ok || got != want {
			t.Errorf("%s is mounted at %+v (found: %v); want %+v\n%s", volume, got, ok, want, compose)
		}
	}
}
